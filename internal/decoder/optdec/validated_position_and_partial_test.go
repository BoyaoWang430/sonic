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
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/jsonscan"
)

type r2CallbackVal struct {
	V        int
	Calls    int
	FailWith error
	Reenter  string
	InnerOut int
}

func (c *r2CallbackVal) UnmarshalJSON(b []byte) error {
	c.Calls++
	if c.FailWith != nil {
		return c.FailWith
	}
	if c.Reenter != "" {
		var tmp struct {
			X int `json:"x"`
		}
		if _, _, err := DecodeOne(c.Reenter, 0, &tmp); err != nil {
			return err
		}
		c.InnerOut = tmp.X
	}
	var payload struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return err
	}
	c.V = payload.V
	return nil
}

// TestR2_LinearPositionSidecarAndErrorDecoupling covers R2-T1 through R2-T12 (§5.9).
func TestR2_LinearPositionSidecarAndErrorDecoupling(t *testing.T) {
	// R2-T1 & R2-T10: []Item with prefilled pointer any on N {"A":null} objects ->
	// PositionBuilds == 1, SubtreeBytes == 4*N, linear scaling across N=100,200,400,800.
	// Plain built-in non-pointer any (e.g. int) binds directly with zero sidecar builds.
	t.Run("R2-T1_T10_PrefilledAnyLinearScaling", func(t *testing.T) {
		type item struct {
			A any
			B int
			C int
		}
		measure := func(n int) (routeWitness, jsonscan.DiagSnapshot, uint64) {
			dst := make([]item, n)
			for i := range dst {
				v := 1
				dst[i].A = &v
			}
			var sb strings.Builder
			sb.WriteByte('[')
			for i := 0; i < n; i++ {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(`{"A":null}`)
			}
			sb.WriteByte(']')
			src := sb.String()

			var w routeWitness
			jsonscan.ResetDiag()
			var ms0, ms1 runtime.MemStats
			runtime.ReadMemStats(&ms0)
			_, _, err := decodeOneWithControl(src, 0, &dst, &decodeTestControl{witness: &w})
			runtime.ReadMemStats(&ms1)
			if err != nil {
				t.Fatalf("unexpected error for n=%d: %v", n, err)
			}
			for i := range dst {
				if dst[i].A != nil {
					t.Fatalf("dst[%d].A=%v want nil", i, dst[i].A)
				}
			}
			if w.IndexBinderEntered != 1 || w.PositionBuilds != 1 || w.IndexSubtreeFallback != uint32(n) || w.IndexSubtreeBytes != uint64(4*n) {
				t.Fatalf("n=%d unexpected witness=%+v", n, w)
			}
			return w, jsonscan.ReadDiag(), ms1.TotalAlloc - ms0.TotalAlloc
		}

		// Plain built-in non-pointer prefill (int -> null) binds directly with zero sidecar builds.
		plainDst := make([]item, 100)
		var plainSB strings.Builder
		plainSB.WriteByte('[')
		for i := range plainDst {
			plainDst[i].A = 1
			if i > 0 {
				plainSB.WriteByte(',')
			}
			plainSB.WriteString(`{"A":null}`)
		}
		plainSB.WriteByte(']')
		var wPlain routeWitness
		if _, _, err := decodeOneWithControl(plainSB.String(), 0, &plainDst, &decodeTestControl{witness: &wPlain}); err != nil {
			t.Fatalf("plain prefill null failed: %v", err)
		}
		if wPlain.IndexBinderEntered != 1 || wPlain.PositionBuilds != 0 || wPlain.IndexSubtreeFallback != 0 {
			t.Fatalf("expected direct index bind for plain prefilled int -> null, got %+v", wPlain)
		}
		for i := range plainDst {
			if plainDst[i].A != nil {
				t.Fatalf("plainDst[%d].A=%v want nil", i, plainDst[i].A)
			}
		}

		// Warmup pools & compiler
		_, _, _ = measure(16)
		_, s100, a100 := measure(100)
		_, s200, a200 := measure(200)
		_, s400, a400 := measure(400)
		_, s800, a800 := measure(800)

		// 8x input scale (100 -> 800) must stay near ~8x (linear), far below 64x (quadratic)!
		allocRatio8x := float64(a800) / float64(a100+1)
		t.Logf("R2-T10 allocs N=100:%d N=200:%d N=400:%d N=800:%d (8x ratio=%.2fx)",
			a100, a200, a400, a800, allocRatio8x)
		if allocRatio8x > 16.0 {
			t.Fatalf("superlinear allocation growth across 8x N: ratio=%.2fx", allocRatio8x)
		}
		if jsonscan.DiagEnabled {
			if s200.PositionBuildCount != 1 || s400.PositionBuildCount != 1 || s800.PositionBuildCount != 1 {
				t.Fatalf("expected PositionBuildCount==1 across all scales, got 200:%d 400:%d 800:%d",
					s200.PositionBuildCount, s400.PositionBuildCount, s800.PositionBuildCount)
			}
			// F4 strict per-role attribution check on s800 (len=8801, 800 x 4-byte "null" subtrees = 3200):
			expectedS800Len := uint64(2 + 800*10 + 799) // 8801
			if s800.ScanCompletes != 1 || s800.ScanSourceBytes != expectedS800Len ||
				s800.BytesLexed == 0 || s800.BytesLexed > expectedS800Len || s800.BytesLexed != s800.PositionScanBytes {
				t.Fatalf("F4 s800 main scan attribution drifted: completes=%d source=%d lexed=%d posLex=%d wantLen=%d snap=%+v",
					s800.ScanCompletes, s800.ScanSourceBytes, s800.BytesLexed, s800.PositionScanBytes, expectedS800Len, s800)
			}
			if s800.PositionSourceBytes != expectedS800Len || s800.PositionBatchCalls == 0 {
				t.Fatalf("F4 s800 position sidecar attribution drifted: snap=%+v", s800)
			}
			if s800.SubtreeScanCount != 800 || s800.SubtreeBatchCalls != 800 || s800.SubtreeScanBytes != 3200 || s800.SubtreeSourceBytes != 3200 {
				t.Fatalf("F4 s800 subtree attribution drifted (want count=800 calls=800 lex=3200 src=3200): snap=%+v", s800)
			}
			lexRatio8x := float64(s800.BytesLexed+s800.PositionScanBytes+s800.SubtreeScanBytes) /
				float64(s100.BytesLexed+s100.PositionScanBytes+s100.SubtreeScanBytes)
			t.Logf("R2-T10 diag scan bytes 8x ratio=%.2fx (s100=%+v s800=%+v)", lexRatio8x, s100, s800)
			if lexRatio8x > 11.0 {
				t.Fatalf("superlinear scan byte growth across 8x N: ratio=%.2fx", lexRatio8x)
			}
		}
	})

	// R2-T2: Duplicate keys turning nil any into plain map[string]any bind directly
	// with zero sidecar builds; duplicate keys targeting a prefilled pointer still
	// build the sidecar once and invoke callbacks on both occurrences.
	t.Run("R2-T2_DuplicateKeyNilToNonNilAny", func(t *testing.T) {
		type dupItem struct {
			A any `json:"a"`
			B int `json:"b"`
			C int `json:"c"`
		}
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < 300; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`{"a":{"k":1},"a":{"m":2},"b":3}`)
		}
		sb.WriteByte(']')
		src := sb.String()

		var w routeWitness
		var gotIdx, gotTape []dupItem
		_, _, errIdx := decodeOneWithControl(src, 0, &gotIdx, &decodeTestControl{witness: &w})
		_, _, errTape := decodeOneWithControl(src, 0, &gotTape, &decodeTestControl{forceRep: repForceTape})
		if errIdx != nil || errTape != nil || !reflect.DeepEqual(gotIdx, gotTape) {
			t.Fatalf("mismatch: errIdx=%v errTape=%v", errIdx, errTape)
		}
		if w.IndexBinderEntered != 1 || w.PositionBuilds != 0 || w.IndexSubtreeFallback != 0 {
			t.Fatalf("expected direct index bind for duplicate plain maps (0 position builds, 0 subtree fallbacks), got %+v", w)
		}

		// Duplicate keys targeting a prefilled pointer still fall back cleanly with single sidecar build.
		cbIdx, cbTape := &r2CallbackVal{}, &r2CallbackVal{}
		pIdx := []dupItem{{A: cbIdx}}
		pTape := []dupItem{{A: cbTape}}
		ptrSrc := `[{"a":{"v":1},"a":{"v":2},"b":3}]`
		var wPtr routeWitness
		_, _, ePtrIdx := decodeOneWithControl(ptrSrc, 0, &pIdx, &decodeTestControl{witness: &wPtr})
		_, _, ePtrTape := decodeOneWithControl(ptrSrc, 0, &pTape, &decodeTestControl{forceRep: repForceTape})
		if ePtrIdx != nil || ePtrTape != nil || !reflect.DeepEqual(pIdx, pTape) || cbIdx.Calls != 2 || cbIdx.V != 2 {
			t.Fatalf("pointer dup key mismatch: eIdx=%v eTape=%v cbIdx=%+v cbTape=%+v", ePtrIdx, ePtrTape, cbIdx, cbTape)
		}
		if wPtr.IndexBinderEntered != 1 || wPtr.PositionBuilds != 1 || wPtr.IndexSubtreeFallback != 2 {
			t.Fatalf("expected 1 position build and 2 subtree fallbacks for pointer dup keys, got %+v", wPtr)
		}
	})

	// R2-T3 & R2-T8: Multiple inline-int->string mismatches + subsequent valid fields & callbacks.
	t.Run("R2-T3_T8_MultipleMismatchesPreserveSubsequentSemanticsAndCallbacks", func(t *testing.T) {
		type errItem struct {
			A   string  `json:"a"`
			Ok  int     `json:"ok"`
			B   []int   `json:"b"`
			Dyn any     `json:"dyn"`
			Ptr *string `json:"ptr"`
		}
		const n = 400
		makeDst := func() ([]errItem, []*r2CallbackVal) {
			items := make([]errItem, n)
			spies := make([]*r2CallbackVal, n)
			for i := range items {
				sp := &r2CallbackVal{}
				spies[i] = sp
				items[i].Dyn = sp
			}
			return items, spies
		}
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`{"a":0,"ok":77,"b":[9],"dyn":{"v":5},"ptr":123}`)
		}
		sb.WriteByte(']')
		src := sb.String()

		gotIdx, spiesIdx := makeDst()
		gotTape, spiesTape := makeDst()
		var w routeWitness
		_, _, errIdx := decodeOneWithControl(src, 0, &gotIdx, &decodeTestControl{witness: &w})
		_, _, errTape := decodeOneWithControl(src, 0, &gotTape, &decodeTestControl{forceRep: repForceTape})
		mIdx, ok1 := errIdx.(*MismatchTypeError)
		mTape, ok2 := errTape.(*MismatchTypeError)
		if !ok1 || !ok2 || mIdx.Pos != mTape.Pos || mIdx.Type != mTape.Type {
			t.Fatalf("first error mismatch: idx=%#v tape=%#v", errIdx, errTape)
		}
		if w.IndexBinderEntered != 1 || w.PositionBuilds != 1 {
			t.Fatalf("expected single sidecar build across %d mismatches, got %+v", n, w)
		}
		for i := 0; i < n; i++ {
			if gotIdx[i].Ok != 77 || !reflect.DeepEqual(gotIdx[i].B, []int{9}) {
				t.Fatalf("item %d subsequent fields not updated: %+v", i, gotIdx[i])
			}
			if (gotIdx[i].Ptr == nil) != (gotTape[i].Ptr == nil) {
				t.Fatalf("item %d nil pointer allocation mismatch vs Tape", i)
			}
			if spiesIdx[i].Calls != 1 || spiesIdx[i].V != 5 || spiesTape[i].Calls != 1 {
				t.Fatalf("item %d callback not invoked after first error: idx=%+v tape=%+v", i, spiesIdx[i], spiesTape[i])
			}
		}

		// Also verify callback error BEFORE mismatch wins as first error.
		sentinelErr := errors.New("custom callback failure")
		cbFirstIdx := []errItem{{Dyn: &r2CallbackVal{FailWith: sentinelErr}}}
		cbFirstTape := []errItem{{Dyn: &r2CallbackVal{FailWith: sentinelErr}}}
		srcCbFirst := `[{"dyn":{"v":1},"a":0,"ok":99}]`
		_, _, errCbIdx := decodeOneWithControl(srcCbFirst, 0, &cbFirstIdx, nil)
		_, _, errCbTape := decodeOneWithControl(srcCbFirst, 0, &cbFirstTape, &decodeTestControl{forceRep: repForceTape})
		if !errors.Is(errCbIdx, sentinelErr) || !errors.Is(errCbTape, sentinelErr) || cbFirstIdx[0].Ok != 99 {
			t.Fatalf("callback-first error priority mismatch: idx=%v tape=%v ok=%d", errCbIdx, errCbTape, cbFirstIdx[0].Ok)
		}
	})

	// R2-T4, R2-T5, R2-T6, R2-T7, R2-T9, R2-T12: Reused []any, skipped unknown subtrees,
	// escaped/decimal/container/null positions, non-zero session + UTF-8 repair, and callback reentrancy.
	t.Run("R2-T4_T5_T6_T7_T9_T12_PositionsAliasesSessionAndReentrancy", func(t *testing.T) {
		// R2-T4: Reused []any backing with prefilled pointer & RawMessage
		type reusedWrap struct {
			Arr []any `json:"arr"`
			B   int   `json:"b"`
			C   int   `json:"c"`
		}
		p0, p1 := 10, 10
		var raw0, raw1 json.RawMessage
		backingIdx := []any{&p0, &raw0, "old"}
		backingTape := []any{&p1, &raw1, "old"}
		dIdx := reusedWrap{Arr: backingIdx[:2]}
		dTape := reusedWrap{Arr: backingTape[:2]}
		srcReused := `{"unknown":{"skip":[1,2,{"z":"e\n"}]},"arr":[42,{"k":true},"tail"],"b":7}`
		var wReused routeWitness
		_, _, e1 := decodeOneWithControl(srcReused, 0, &dIdx, &decodeTestControl{witness: &wReused})
		_, _, e2 := decodeOneWithControl(srcReused, 0, &dTape, &decodeTestControl{forceRep: repForceTape})
		if e1 != nil || e2 != nil || p0 != 42 || p1 != 42 || string(raw0) != `{"k":true}` || !reflect.DeepEqual(dIdx, dTape) {
			t.Fatalf("R2-T4/T5 reused []any mismatch: e1=%v e2=%v p0=%d raw0=%s", e1, e2, p0, string(raw0))
		}
		if wReused.IndexBinderEntered != 1 || wReused.PositionBuilds != 1 {
			t.Fatalf("expected index binder + 1 position build, got %+v", wReused)
		}

		// R2-T6: Mismatch Pos parity across plain str, escaped str, exact decimal, container, inline int, true/false
		type posTarget struct {
			I int    `json:"i"`
			S string `json:"s"`
			B int    `json:"b"`
		}
		cases := []string{
			`{"skip":[1,2],"i":"plain"}`,
			`{"skip":[1,2],"i":"e\u0073c"}`,
			`{"skip":[1,2],"i":{"nested":1}}`,
			`{"skip":[1,2],"i":[1,2]}`,
			`{"skip":[1,2],"i":12.345}`,
			`{"skip":[1,2],"s":123}`,
			`{"skip":[1,2],"s":true}`,
		}
		for _, tc := range cases {
			var g1, g2 posTarget
			_, _, er1 := decodeOneWithControl(tc, 0, &g1, &decodeTestControl{forceRep: repForceIndex})
			_, _, er2 := decodeOneWithControl(tc, 0, &g2, &decodeTestControl{forceRep: repForceTape})
			m1, ok1 := er1.(*MismatchTypeError)
			m2, ok2 := er2.(*MismatchTypeError)
			if !ok1 || !ok2 || m1.Pos != m2.Pos || m1.Type != m2.Type {
				t.Fatalf("R2-T6 Pos mismatch on %s: idx=%#v tape=%#v", tc, er1, er2)
			}
		}

		// R2-T7: Non-zero session start + invalid UTF-8 repair before type mismatch -> exact original Pos
		prefix := `{"ok":1} `
		badUTF := "{\"pad\":\"" + "\xff\xff" + "\",\"s\":456,\"b\":1}"
		sIdx := prefix + badUTF
		sTape := sIdx
		offIdx, offTape := len(prefix), len(prefix)
		var ptIdx, ptTape posTarget
		var wSess routeWitness
		errS1 := decodeSessionWithControl(&sIdx, &offIdx, 1<<consts.F_validate_string, &ptIdx, nil, &decodeTestControl{witness: &wSess})
		errS2 := decodeSessionWithControl(&sTape, &offTape, 1<<consts.F_validate_string, &ptTape, nil, &decodeTestControl{forceRep: repForceTape})
		ms1, ok1 := errS1.(*MismatchTypeError)
		ms2, ok2 := errS2.(*MismatchTypeError)
		if !ok1 || !ok2 || ms1.Pos != ms2.Pos || offIdx != offTape || wSess.IndexBinderEntered != 1 {
			t.Fatalf("R2-T7 session repaired mismatch Pos: idx=%#v tape=%#v witness=%+v", errS1, errS2, wSess)
		}

		// R2-T12: Re-entrant decoder inside dynamic callback isolates request state
		type reentRoot struct {
			First any `json:"first"`
			Sec   any `json:"sec"`
			Pad   int `json:"pad"`
		}
		cb1 := &r2CallbackVal{Reenter: `{"x":314}`}
		cb2 := &r2CallbackVal{Reenter: `{"x":271}`}
		rr := reentRoot{First: cb1, Sec: cb2}
		var wReent routeWitness
		_, _, errRe := decodeOneWithControl(`{"first":{"v":1},"sec":{"v":2},"pad":9}`, 0, &rr, &decodeTestControl{witness: &wReent})
		if errRe != nil || cb1.InnerOut != 314 || cb2.InnerOut != 271 || cb1.V != 1 || cb2.V != 2 || rr.Pad != 9 {
			t.Fatalf("R2-T12 reentrant failure: err=%v cb1=%+v cb2=%+v pad=%d", errRe, cb1, cb2, rr.Pad)
		}
		if wReent.PositionBuilds != 1 || wReent.IndexSubtreeFallback != 2 {
			t.Fatalf("expected outer request to keep isolated single sidecar build, got %+v", wReent)
		}
	})

	// R2-T11: End-to-end DecodeOne resumable index growth with 64 KiB prefix + 3000 items.
	t.Run("R2-T11_EndToEndResumableIndexGrowthZeroRestart", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString(`[{"prefix":"`)
		sb.WriteString(strings.Repeat("p", 64<<10))
		sb.WriteString(`","nums":[`)
		for i := 0; i < 3000; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("7")
		}
		sb.WriteString(`],"tail":99}]`)
		src := sb.String()

		type docItem struct {
			Prefix string `json:"prefix"`
			Nums   []int  `json:"nums"`
			Tail   int    `json:"tail"`
		}
		var dst []docItem
		var w routeWitness
		jsonscan.ResetDiag()
		_, _, err := decodeOneWithControl(src, 0, &dst, &decodeTestControl{initialIdxCap: 64, witness: &w})
		if err != nil || len(dst) != 1 || len(dst[0].Nums) != 3000 || dst[0].Tail != 99 || len(dst[0].Prefix) != 64<<10 {
			t.Fatalf("R2-T11 failed: err=%v items=%d", err, len(dst))
		}
		if w.IndexCapacityGrows == 0 || w.IndexBinderEntered != 1 {
			t.Fatalf("expected index capacity growth + bind, got %+v", w)
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.IndexResumeCount == 0 || snap.ScanCompletes != 1 || snap.ScanSourceBytes != uint64(len(src)) {
				t.Fatalf("expected >0 resumes, 1 completion, and ScanSourceBytes==%d, got %+v", len(src), snap)
			}
			if snap.BytesLexed > uint64(len(src)+1024) {
				t.Fatalf("prefix re-lexing detected: BytesLexed=%d len(src)=%d", snap.BytesLexed, len(src))
			}
		}
	})
}

// TestR3_PartialResultConsumptionMatrix covers all 7 consumption points in §6.2 & §6.3.
func TestR3_PartialResultConsumptionMatrix(t *testing.T) {
	type r3Target struct {
		S       []any    `json:"S"`
		AnyNil  any      `json:"AnyNil"`
		AnyPtr  any      `json:"AnyPtr"`
		Nums    []int32  `json:"Nums"`
		Strs    []string `json:"Strs"`
		AfterOk int      `json:"AfterOk"`
	}

	inputs := []struct {
		name      string
		json      string
		opts      uint64
		expectErr bool
	}{
		{"NestedFreshSliceOverflow", `{"S":[[1e400]],"AfterOk":5}`, 0, true},
		{"NestedMapInSliceOverflow", `{"S":[{"x":[1e400],"ok":1}, "tail"],"AfterOk":6}`, 0, true},
		{"DuplicateKeyInMapOverwriteAfterErr", `{"S":[{"x":[1e400],"x":[2]}],"AfterOk":7}`, 0, true},
		{"DuplicateKeyInMapErrAfterValid", `{"S":[{"x":[2],"x":[1e400]}],"AfterOk":8}`, 0, true},
		{"OuterNilAnyMapNotWrittenOnErr", `{"AnyNil":{"x":[1e400],"ok":1},"AfterOk":9}`, 0, true},
		{"OuterNilAnySliceNotWrittenOnErr", `{"AnyNil":[1,[1e400],2],"AfterOk":10}`, 0, true},
		{"MultipleNestedErrorsAndEmptyContainers", `{"S":[[], {}, [1e400, {"a":1e400,"b":"keep"}], null],"Nums":[1,99999999999,3],"AfterOk":11}`, 0, true},
		{"CopyStringAndUTF8RepairPartial", "{\"S\":[{\"bad\":[1e400],\"str\":\"a\xffb\"}],\"Strs\":[\"c\xffd\"],\"AfterOk\":12}", (1 << consts.F_copy_string) | (1 << consts.F_validate_string), true},
		// T5: Explicit AnyPtr prefilled-external-pointer hits (valid, type mismatch, null, duplicate keys):
		{"T5_AnyPtrValidOverwriteExternal", `{"AnyPtr":123,"AfterOk":13}`, 0, false},
		{"T5_AnyPtrValidWithNestedSliceErr", `{"AnyPtr":124,"S":[[1e400]],"AfterOk":14}`, 0, true},
		{"T5_AnyPtrTypeMismatchOnPrefilled", `{"AnyPtr":"not_an_int","S":[[1e400]],"AfterOk":15}`, 0, true},
		{"T5_AnyPtrNullClearsIfacePreservesExt", `{"AnyPtr":null,"S":[[1e400]],"AfterOk":16}`, 0, true},
		{"T5_AnyPtrDupKeyValidThenMismatch", `{"AnyPtr":11,"AnyPtr":"bad","S":[[1e400]],"AfterOk":17}`, 0, true},
		{"T5_AnyPtrDupKeyMismatchThenValid", `{"AnyPtr":"bad","AnyPtr":22,"S":[[1e400]],"AfterOk":18}`, 0, true},
		{"T5_AnyPtrDupKeyValidThenNull", `{"AnyPtr":33,"AnyPtr":null,"AfterOk":19}`, 0, false},
	}

	extMutatedCount := 0
	anyPtrNilledCount := 0
	anyPtrMismatchCount := 0

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			for _, prefill := range []bool{false, true} {
				newTarget := func() (*r3Target, *int) {
					ext := 88
					d := &r3Target{}
					if prefill {
						d.AnyPtr = &ext
						d.S = make([]any, 1, 4)
						d.S[0] = "reused0"
						d.Nums = []int32{100, 200, 300}
					}
					return d, &ext
				}

				gotIdx, extIdx := newTarget()
				gotTape, extTape := newTarget()
				var w routeWitness
				_, _, errIdx := decodeOneWithControl(tc.json, tc.opts, gotIdx, &decodeTestControl{forceRep: repForceIndex, witness: &w})
				_, _, errTape := decodeOneWithControl(tc.json, tc.opts, gotTape, &decodeTestControl{forceRep: repForceTape})

				if w.IndexBinderEntered != 1 {
					t.Fatalf("prefill=%v expected IndexBinderEntered==1, got %+v", prefill, w)
				}
				if tc.expectErr {
					mIdx, ok1 := errIdx.(*MismatchTypeError)
					mTape, ok2 := errTape.(*MismatchTypeError)
					if !ok1 || !ok2 || mIdx.Pos != mTape.Pos || mIdx.Type != mTape.Type {
						t.Fatalf("prefill=%v error mismatch: idx=%#v tape=%#v", prefill, errIdx, errTape)
					}
					if prefill && strings.Contains(tc.name, "AnyPtr") && mIdx.Type == reflect.TypeOf(int(0)) {
						anyPtrMismatchCount++
					}
				} else {
					if errIdx != nil || errTape != nil {
						t.Fatalf("prefill=%v unexpected err: idx=%v tape=%v", prefill, errIdx, errTape)
					}
				}
				if !reflect.DeepEqual(gotIdx, gotTape) || *extIdx != *extTape {
					t.Fatalf("prefill=%v partial result mismatch:\n idx = %#v (ext=%d)\ntape = %#v (ext=%d)",
						prefill, gotIdx, *extIdx, gotTape, *extTape)
				}
				if prefill {
					if *extIdx != 88 {
						extMutatedCount++
					}
					if gotIdx.AnyPtr == nil {
						anyPtrNilledCount++
					}
				}
			}
		})
	}
	if extMutatedCount == 0 || anyPtrNilledCount == 0 || anyPtrMismatchCount == 0 {
		t.Fatalf("T5 invariant failed: AnyPtr prefill paths were not all exercised (extMutated=%d nilled=%d mismatch=%d)",
			extMutatedCount, anyPtrNilledCount, anyPtrMismatchCount)
	}
}
