//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package optdec

import (
	stdctx "context"
	"os"
	"os/exec"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/jsonscan"
)

type r1CallbackSpy struct {
	calls int
}

func (s *r1CallbackSpy) UnmarshalJSON(b []byte) error {
	s.calls++
	return nil
}

// TestR1_PostRepairAdmissionAndTermination covers R1-T1 through R1-T7 (§4.5).
func TestR1_PostRepairAdmissionAndTermination(t *testing.T) {
	const optVal = uint64(1 << consts.F_validate_string)

	// R1-T1: Both pre-repair and post-repair within threshold -> index hits.
	t.Run("R1-T1_WithinThresholdHitsIndex", func(t *testing.T) {
		raw := "[\"" + strings.Repeat("\xff", 100) + "\"]" // raw=104, repaired=304 <= 1024
		var w routeWitness
		ctrl := &decodeTestControl{maxIdxDocBytes: 1024, witness: &w}
		var dst []string
		_, endPos, err := decodeOneWithControl(raw, optVal, &dst, ctrl)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if endPos != len(raw) {
			t.Fatalf("endPos=%d want %d", endPos, len(raw))
		}
		want := strings.Repeat("\ufffd", 100)
		if len(dst) != 1 || dst[0] != want {
			t.Fatalf("unexpected repaired output len=%d", len(dst))
		}
		if w.IndexRouteSelected != 1 || w.IndexScanStarted != 1 || w.IndexBarrierPassed != 1 || w.IndexBinderEntered != 1 || w.TapeRouteSelected != 0 {
			t.Fatalf("expected clean index hit, got witness=%+v", w)
		}
	})

	// R1-T2: Pre-repair <= 1024 (504B), post-repair > 1024 (1504B) -> 0 index retries, Tape succeeds.
	t.Run("R1-T2_PostRepairExceedsThresholdRoutesToTape", func(t *testing.T) {
		raw := "[\"" + strings.Repeat("\xff", 500) + "\"]"
		var w routeWitness
		ctrl := &decodeTestControl{maxIdxDocBytes: 1024, witness: &w}
		var dstIdx, dstTape []string
		_, endPos, err := decodeOneWithControl(raw, optVal, &dstIdx, ctrl)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, wantEnd, errTape := decodeOneWithControl(raw, optVal, &dstTape, &decodeTestControl{forceRep: repForceTape})
		if errTape != nil || endPos != wantEnd || !reflect.DeepEqual(dstIdx, dstTape) {
			t.Fatalf("mismatch vs Tape: end=%d/%d err=%v/%v", endPos, wantEnd, err, errTape)
		}
		if w.IndexRouteSelected != 0 || w.IndexScanStarted != 0 || w.IndexCapacityGrows != 0 || w.TapeRouteSelected != 1 {
			t.Fatalf("expected immediate post-repair rejection to Tape with 0 retries, got %+v", w)
		}
	})

	// R1-T3: Exact threshold vs threshold+1 boundary classification.
	t.Run("R1-T3_ExactThresholdBoundary", func(t *testing.T) {
		// [" + 100*0xff (300B repaired) + "] -> repairedLen = 304.
		raw := "[\"" + strings.Repeat("\xff", 100) + "\"]"
		var wEq, wPlus1 routeWitness
		var dstEq, dstPlus1 []string
		_, _, errEq := decodeOneWithControl(raw, optVal, &dstEq, &decodeTestControl{maxIdxDocBytes: 304, witness: &wEq})
		_, _, errPlus := decodeOneWithControl(raw, optVal, &dstPlus1, &decodeTestControl{maxIdxDocBytes: 303, witness: &wPlus1})
		if errEq != nil || errPlus != nil || !reflect.DeepEqual(dstEq, dstPlus1) {
			t.Fatalf("boundary error: eq=%v plus1=%v", errEq, errPlus)
		}
		if wEq.IndexBinderEntered != 1 || wEq.TapeRouteSelected != 0 {
			t.Fatalf("expected exact threshold (304) to bind via index, got %+v", wEq)
		}
		if wPlus1.IndexRouteSelected != 0 || wPlus1.TapeRouteSelected != 1 {
			t.Fatalf("expected threshold-1 (303 < 304) to route to Tape, got %+v", wPlus1)
		}
	})

	// R1-T4: Small initial output cap with applicable input -> bounded growth succeeds.
	t.Run("R1-T4_SmallInitialCapBoundedGrowthSucceeds", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < 300; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`"ok"`)
		}
		sb.WriteByte(']')
		src := sb.String()
		var w routeWitness
		ctrl := &decodeTestControl{initialIdxCap: 16, witness: &w}
		var dst []string
		_, _, err := decodeOneWithControl(src, 0, &dst, ctrl)
		if err != nil || len(dst) != 300 {
			t.Fatalf("expected 300 elements, got len=%d err=%v", len(dst), err)
		}
		if w.IndexCapacityGrows == 0 || w.IndexCapacityGrows > 4 || w.IndexBinderEntered != 1 || w.TapeRouteSelected != 0 {
			t.Fatalf("expected bounded index capacity growth, got %+v", w)
		}
	})

	// R1-T5: Non-growing / tight hard-bound & direct jsonscan NotApplicable status never loop.
	t.Run("R1-T5_NonGrowingAndNotApplicableTerminateDeterministically", func(t *testing.T) {
		// 1. Direct jsonscan.ScanValidatedDocBounded returns ValidatedScanNotApplicable (never StopNeedOutput).
		var m jsonscan.Machine
		m.ResetValidatedDoc(true, false, 0)
		var doc jsonscan.ValidatedDoc
		dst := make([]uint64, 128)
		src := `["` + strings.Repeat("a", 256) + `"]`
		out := m.ScanValidatedDocBounded(src, dst, 128, &doc)
		if out.Status != jsonscan.ValidatedScanNotApplicable {
			t.Fatalf("expected ValidatedScanNotApplicable, got status=%d res=%+v", out.Status, out.Result)
		}

		// 2. Simulated non-growing / capped entry bound falls back to Tape in 1 pass without looping.
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < 200; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("1")
		}
		sb.WriteByte(']')
		arrSrc := sb.String()

		var wNoGrow, wTightBound routeWitness
		var out1, out2 []int
		_, _, err1 := decodeOneWithControl(arrSrc, 0, &out1, &decodeTestControl{
			initialIdxCap:  16,
			simulateNoGrow: true,
			witness:        &wNoGrow,
		})
		_, _, err2 := decodeOneWithControl(arrSrc, 0, &out2, &decodeTestControl{
			initialIdxCap:    64,
			maxIdxEntryBound: 140,
			witness:          &wTightBound,
		})
		if err1 != nil || err2 != nil || len(out1) != 200 || len(out2) != 200 {
			t.Fatalf("fallback decode failed: err1=%v err2=%v len1=%d len2=%d", err1, err2, len(out1), len(out2))
		}
		if wNoGrow.IndexFallbackToTape != 1 || wNoGrow.TapeRouteSelected != 1 || wNoGrow.IndexBinderEntered != 0 {
			t.Fatalf("expected deterministic fallback on simulateNoGrow, got %+v", wNoGrow)
		}
		if wTightBound.IndexFallbackToTape != 1 || wTightBound.TapeRouteSelected != 1 || wTightBound.IndexCapacityGrows > 2 {
			t.Fatalf("expected bounded fallback on tight maxIdxEntryBound, got %+v", wTightBound)
		}
	})

	// R1-T6: DecodeWithSession non-zero start offset, post-repair exceeds threshold + trailing 2nd value + error offset parity.
	t.Run("R1-T6_DecodeWithSessionNonZeroStartAndSubsequentValue", func(t *testing.T) {
		prefix := `{"a":1} `
		val1 := "[\"" + strings.Repeat("\xff", 400) + "\"]" // raw=404 <= 1024, repaired=1204 > 1024
		val2 := ` ["second", "ok"]`
		streamIdx := prefix + val1 + val2
		streamTape := streamIdx
		posIdx := len(prefix)
		posTape := len(prefix)
		var sessIdx, sessTape *InputSession

		var w1, w2 routeWitness
		var got1Idx, got1Tape, got2Idx, got2Tape []string
		err1 := decodeSessionWithControl(&streamIdx, &posIdx, optVal, &got1Idx, &sessIdx, &decodeTestControl{maxIdxDocBytes: 1024, witness: &w1})
		err1T := decodeSessionWithControl(&streamTape, &posTape, optVal, &got1Tape, &sessTape, &decodeTestControl{forceRep: repForceTape})
		if err1 != nil || err1T != nil || posIdx != posTape || streamIdx != streamTape || !reflect.DeepEqual(got1Idx, got1Tape) {
			t.Fatalf("session step 1 mismatch: err=%v/%v pos=%d/%d", err1, err1T, posIdx, posTape)
		}
		if w1.IndexRouteSelected != 0 || w1.TapeRouteSelected != 1 {
			t.Fatalf("expected session step 1 to reject index post-repair, got %+v", w1)
		}

		err2 := decodeSessionWithControl(&streamIdx, &posIdx, optVal, &got2Idx, &sessIdx, &decodeTestControl{maxIdxDocBytes: 1024, witness: &w2})
		err2T := decodeSessionWithControl(&streamTape, &posTape, optVal, &got2Tape, &sessTape, &decodeTestControl{forceRep: repForceTape})
		if err2 != nil || err2T != nil || posIdx != posTape || !reflect.DeepEqual(got2Idx, got2Tape) {
			t.Fatalf("session step 2 mismatch: err=%v/%v pos=%d/%d got=%v/%v", err2, err2T, posIdx, posTape, got2Idx, got2Tape)
		}
		if w2.IndexBinderEntered != 1 {
			t.Fatalf("expected session step 2 small value to hit index, got %+v", w2)
		}

		// Also verify original error offset mapping on malformed post-repair-expanded session value (no double offset mapping!).
		badStreamIdx := prefix + "[\"" + strings.Repeat("\xff", 400) + "\", ]"
		badStreamTape := badStreamIdx
		pBadIdx := len(prefix)
		pBadTape := len(prefix)
		var badDstIdx, badDstTape []string
		errBadIdx := decodeSessionWithControl(&badStreamIdx, &pBadIdx, optVal, &badDstIdx, nil, &decodeTestControl{maxIdxDocBytes: 1024})
		errBadTape := decodeSessionWithControl(&badStreamTape, &pBadTape, optVal, &badDstTape, nil, &decodeTestControl{forceRep: repForceTape})
		seIdx, ok1 := errBadIdx.(SyntaxError)
		seTape, ok2 := errBadTape.(SyntaxError)
		if !ok1 || !ok2 || seIdx.Pos != seTape.Pos {
			t.Fatalf("expected identical original SyntaxError.Pos, got idx=%#v tape=%#v", errBadIdx, errBadTape)
		}
	})

	// R1-T7: Post-repair over-limit input with trailing syntax error preserves SYN-PREMUT & zero callbacks.
	t.Run("R1-T7_OverLimitMalformedPreservesSynPremut", func(t *testing.T) {
		type r1Target struct {
			Text string  `json:"text"`
			P    *int    `json:"p"`
			Nums []int64 `json:"nums"`
			Dyn  any     `json:"dyn"`
		}
		extInt := 42
		backing := []int64{7, 8, 9}
		spy := &r1CallbackSpy{}
		dst := r1Target{
			Text: "untouched",
			P:    &extInt,
			Nums: backing[:2],
			Dyn:  spy,
		}
		malformed := "{\"text\":\"" + strings.Repeat("\xff", 400) + "\",\"p\":99,\"nums\":[1],\"dyn\":{\"x\":1},\"bad\":}"
		var w routeWitness
		_, _, err := decodeOneWithControl(malformed, optVal, &dst, &decodeTestControl{
			maxIdxDocBytes: 512,
			witness:        &w,
		})
		if err == nil {
			t.Fatal("expected syntax error")
		}
		if dst.Text != "untouched" || *dst.P != 42 || backing[0] != 7 || backing[1] != 8 || spy.calls != 0 {
			t.Fatalf("SYN-PREMUT violated: dst=%+v extInt=%d backing=%v spyCalls=%d", dst, extInt, backing, spy.calls)
		}
	})
}

func r1IsRaceBuild() bool {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "-race" && s.Value == "true" {
				return true
			}
		}
	}
	return false
}

// TestR1_T8_Real128MiBUTF8RepairExpansionBoundary verifies the exact 128 MiB
// review trigger (44,739,242 raw 0xff bytes -> 44,739,246 raw bytes <= 128 MiB,
// repaired to 134,217,730 bytes > MaxValidatedDocBytes = 134,217,728) completes
// via Tape with 0 index retries under BOTH a hard subprocess watchdog timeout
// (SIGKILL on non-termination) and a bounded wall-clock budget (T4 / §10.1).
func TestR1_T8_Real128MiBUTF8RepairExpansionBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 128 MiB UTF-8 expansion boundary test in -short mode")
	}

	watchdog := 60 * time.Second
	budget := 45 * time.Second
	if r1IsRaceBuild() {
		watchdog = 90 * time.Second
		budget = 75 * time.Second
	}

	if os.Getenv("SONIC_R1_T8_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatalf("T4 os.Executable failed: %v", err)
		}
		ctx, cancel := stdctx.WithTimeout(stdctx.Background(), watchdog)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.v", "-test.run=^TestR1_T8_Real128MiBUTF8RepairExpansionBoundary$")
		cmd.Env = append(os.Environ(), "SONIC_R1_T8_CHILD=1")
		parentStart := time.Now()
		out, runErr := cmd.CombinedOutput()
		parentElapsed := time.Since(parentStart)
		if ctx.Err() == stdctx.DeadlineExceeded {
			t.Fatalf("T4 watchdog killed hung 128 MiB boundary child after %s (limit=%s):\n%s",
				parentElapsed, watchdog, string(out))
		}
		if runErr != nil {
			t.Fatalf("T4 128 MiB boundary child failed in %s: %v\n%s", parentElapsed, runErr, string(out))
		}
		if !strings.Contains(string(out), "R1_T8_CHILD_OK") {
			t.Fatalf("T4 child exited 0 without R1_T8_CHILD_OK confirmation:\n%s", string(out))
		}
		t.Logf("T4 parent watchdog passed in %s (watchdog=%s budget=%s)", parentElapsed, watchdog, budget)
		return
	}

	const rawFFCount = 44739242
	buf := make([]byte, rawFFCount+4)
	buf[0] = '['
	buf[1] = '"'
	for i := 2; i < 2+rawFFCount; i++ {
		buf[i] = 0xff
	}
	buf[len(buf)-2] = '"'
	buf[len(buf)-1] = ']'
	raw := string(buf)
	if len(raw) != 44739246 || len(raw) > jsonscan.MaxValidatedDocBytes {
		t.Fatalf("unexpected raw length %d", len(raw))
	}

	var w routeWitness
	var dst []string
	start := time.Now()
	_, endPos, err := decodeOneWithControl(raw, 1<<consts.F_validate_string, &dst, &decodeTestControl{witness: &w})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("128 MiB repair boundary failed: %v", err)
	}
	if endPos != len(raw) || len(dst) != 1 || len(dst[0]) != rawFFCount*3 {
		t.Fatalf("unexpected result: endPos=%d len(dst)=%d len(dst[0])=%d", endPos, len(dst), len(dst[0]))
	}
	if w.IndexRouteSelected != 0 || w.IndexScanStarted != 0 || w.IndexCapacityGrows != 0 || w.TapeRouteSelected != 1 {
		t.Fatalf("expected 0 index retries and direct post-repair Tape route, got %+v", w)
	}
	if elapsed > budget {
		t.Fatalf("T4 exceeded wall-clock budget: elapsed=%s > budget=%s (witness=%+v)", elapsed, budget, w)
	}
	t.Logf("R1_T8_CHILD_OK elapsed=%s budget=%s retries=%d witness=%+v", elapsed, budget, w.IndexCapacityGrows, w)
}
