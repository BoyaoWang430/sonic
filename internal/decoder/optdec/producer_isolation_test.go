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
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic/internal/ident"
	"github.com/bytedance/sonic/internal/jsonscan"
)

// expectedDispatchedFamilyFromIdent independently derives the expected outer
// lexical implementation family from ident.Current() rather than calling
// jsonscan.ExpectedDispatchedLexFamily, avoiding circular self-validation (§6.2).
func expectedDispatchedFamilyFromIdent(forceScalar bool) jsonscan.LexProducer {
	if forceScalar {
		return jsonscan.LexProducerGoScalarForced
	}
	cur := ident.Current()
	if cur.SemanticBackend != "sonic" {
		return jsonscan.LexProducerGoScalarCompat
	}
	if cur.EffectiveDispatch == "scalar" {
		if cur.BuildVariant == "sonic_scalar" || cur.BuildVariant == "future_go_scalar" || cur.BuildVariant == "race" {
			return jsonscan.LexProducerGoScalarBuild
		}
		return jsonscan.LexProducerGoScalarGlobal
	}
	switch cur.GOARCH {
	case "amd64":
		switch cur.EffectiveDispatch {
		case "avx2":
			return jsonscan.LexProducerAsmAMD64AVX2
		case "ssse3":
			return jsonscan.LexProducerAsmAMD64SSSE3
		default:
			return jsonscan.LexProducerAsmAMD64SSE2
		}
	case "arm64":
		return jsonscan.LexProducerAsmARM64NEON
	case "riscv64":
		if strings.Contains(cur.ArchitectureLevel, "rva23") {
			return jsonscan.LexProducerAsmRISCV64RVV
		}
		return jsonscan.LexProducerAsmRISCV64Zbb
	default:
		return jsonscan.LexProducerNone
	}
}

func requireWitnessIfEnvSet(tb testing.TB) {
	tb.Helper()
	if os.Getenv("SONIC_REQUIRE_WITNESS") == "1" && !jsonscan.WitnessEnabled {
		tb.Fatalf("SONIC_REQUIRE_WITNESS=1 set, but binary compiled without producer witness (jsonscan.WitnessEnabled=false; use -tags=sonic_witness or -tags=sonic_diag)")
	}
}

// TestF3_PinnedParserPolicyIsolationAndActualProducers deterministically verifies
// request-level forceScalar isolation on the EXACT SAME *Parser instance (zero
// sync.Pool luck) across BOTH default and witness builds (§4.6):
//   - true -> false -> true -> true -> false sequences on Index & Tape
//   - cross-block escaped strings + cold numbers + multiple R2-G capacity resumes
//   - sidecar & subtree bridge propagation
//   - concurrent goroutines with distinct Parsers never cross-contaminating (§7.4)
//   - In !sonic_witness && !sonic_diag builds, asserts producer witness is truly
//     DISABLED (0 / LexProducerDisabled) while running 100% of functional steps.
//   - In sonic_witness || sonic_diag builds, asserts observed outer families match
//     the independent expectation derived from ident.Current().
func TestF3_PinnedParserPolicyIsolationAndActualProducers(t *testing.T) {
	requireWitnessIfEnvSet(t)

	var wantNat, wantSca jsonscan.LexProducerMask
	if jsonscan.WitnessEnabled {
		expNat := expectedDispatchedFamilyFromIdent(false)
		expSca := expectedDispatchedFamilyFromIdent(true)
		if got := jsonscan.ExpectedDispatchedLexFamily(false); got != expNat {
			t.Fatalf("ExpectedDispatchedLexFamily(false)=%v disagrees with independent ident derivation %v (ident=%+v)",
				got, expNat, ident.Current())
		}
		if got := jsonscan.ExpectedDispatchedLexFamily(true); got != expSca {
			t.Fatalf("ExpectedDispatchedLexFamily(true)=%v disagrees with independent ident derivation %v",
				got, expSca)
		}
		wantNat = jsonscan.LexProducerBit(expNat)
		wantSca = jsonscan.LexProducerBit(expSca)
		if wantNat == 0 || wantSca == 0 {
			t.Fatalf("invalid expected family bits under witness build: nat=%v sca=%v", expNat, expSca)
		}
	} else {
		if got := jsonscan.ExpectedDispatchedLexFamily(false); got != jsonscan.LexProducerDisabled {
			t.Fatalf("default build ExpectedDispatchedLexFamily(false)=%v want %v", got, jsonscan.LexProducerDisabled)
		}
		if got := jsonscan.ExpectedDispatchedLexFamily(true); got != jsonscan.LexProducerDisabled {
			t.Fatalf("default build ExpectedDispatchedLexFamily(true)=%v want %v", got, jsonscan.LexProducerDisabled)
		}
	}

	type f3DynItem struct {
		ID   int64   `json:"id"`
		Text string  `json:"text"`
		Val  float64 `json:"val"`
		Nums []int   `json:"nums"`
		Dyn  any     `json:"dyn"`
	}

	var sb strings.Builder
	sb.WriteString(`{"id":42,"text":"`)
	sb.WriteString(strings.Repeat(`hello\n`, 32)) // >192 bytes escaped string crossing 64B blocks
	sb.WriteString(`","val":1.2345678901234567890123456789e+150,"nums":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("9")
	}
	sb.WriteString(`],"dyn":{"nested":"`)
	sb.WriteString(strings.Repeat(`sub\t`, 24))
	sb.WriteString(`","k":7}}`)
	src := sb.String()

	// Allocate one explicit fixed Parser shared across EVERY step (never via sync.Pool).
	fixedP := &Parser{
		Document: Document{
			nodes: make([]node, defaultNodesCount, defaultNodesCount),
		},
	}

	// 1. Deterministic same-Parser true -> false -> true -> true -> false across Index (with R2-G resume + sidecar + subtree) & Tape.
	type stepSpec struct {
		rep         decodeRepChoice
		forceScalar bool
		initCap     int
	}
	steps := []stepSpec{
		{repForceIndex, true, 64},
		{repForceIndex, false, 64},
		{repForceIndex, true, 64},
		{repForceIndex, true, 64},
		{repForceIndex, false, 64},
		{repForceTape, true, 0},
		{repForceIndex, false, 64},
		{repForceIndex, true, 64},
		{repForceTape, false, 0},
	}

	for i, st := range steps {
		preMap := map[string]any{"pre": 1}
		dst := f3DynItem{Dyn: &preMap} // pointer-typed Dyn forces sidecar + subtree bridge on Index route!
		var w routeWitness
		_, endPos, err := decodeOneWithControl(src, diffStdOpts, &dst, &decodeTestControl{
			forceRep:      st.rep,
			forceScalar:   st.forceScalar,
			initialIdxCap: st.initCap,
			pinnedParser:  fixedP,
			witness:       &w,
		})
		if err != nil || endPos != len(src) || dst.ID != 42 || len(dst.Nums) != 200 {
			t.Fatalf("step %d (%+v) decode failed: err=%v endPos=%d/%d id=%d nums=%d",
				i, st, err, endPos, len(src), dst.ID, len(dst.Nums))
		}
		if w.RequestedForceScalar != st.forceScalar {
			t.Fatalf("step %d requested forceScalar witness mismatch: got=%v want=%v", i, w.RequestedForceScalar, st.forceScalar)
		}
		wantMask := wantNat
		if st.forceScalar {
			wantMask = wantSca
		}
		if jsonscan.WitnessEnabled {
			if w.ObservedProducers != wantMask {
				t.Fatalf("step %d (%+v) main family mismatch: got=%v want=%v",
					i, st, w.ObservedProducers.Names(), wantMask.Names())
			}
		} else if w.ObservedProducers != 0 {
			t.Fatalf("step %d (%+v) default build unexpectedly observed producer mask=%v",
				i, st, w.ObservedProducers.Names())
		}
		if st.rep == repForceIndex {
			if w.IndexBinderEntered != 1 || w.IndexCapacityGrows == 0 || w.PositionBuilds != 1 || w.IndexSubtreeFallback != 1 {
				t.Fatalf("step %d expected index+resume+sidecar+subtree, got %+v", i, w)
			}
			if jsonscan.WitnessEnabled {
				if w.PositionProducers != wantMask {
					t.Fatalf("step %d sidecar family mismatch: got=%v want=%v", i, w.PositionProducers.Names(), wantMask.Names())
				}
				if w.SubtreeProducers != wantMask {
					t.Fatalf("step %d subtree family mismatch: got=%v want=%v", i, w.SubtreeProducers.Names(), wantMask.Names())
				}
			} else if w.PositionProducers != 0 || w.SubtreeProducers != 0 {
				t.Fatalf("step %d default build unexpectedly observed sidecar/subtree mask: pos=%v sub=%v",
					i, w.PositionProducers.Names(), w.SubtreeProducers.Names())
			}
		} else {
			if w.TapeRouteSelected != 1 || w.IndexScanStarted != 0 {
				t.Fatalf("step %d expected pure Tape route, got %+v", i, w)
			}
		}
	}

	// 2. Multi-goroutine isolation: concurrent goroutines alternating forceScalar=true/false
	// on distinct pooled Parsers must NEVER cross-contaminate.
	const workers = 8
	const iters = 40
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for k := 0; k < iters; k++ {
				reqSca := ((id + k) & 1) == 1
				wantMask := wantNat
				if reqSca {
					wantMask = wantSca
				}
				preMap := map[string]any{}
				dst := f3DynItem{Dyn: &preMap}
				var w routeWitness
				if _, _, err := decodeOneWithControl(src, diffStdOpts, &dst, &decodeTestControl{
					forceRep:      repForceIndex,
					forceScalar:   reqSca,
					initialIdxCap: 64,
					witness:       &w,
				}); err != nil {
					errCh <- err
					return
				}
				if jsonscan.WitnessEnabled {
					if w.ObservedProducers != wantMask || w.PositionProducers != wantMask || w.SubtreeProducers != wantMask {
						t.Errorf("goroutine %d iter %d (reqSca=%v) family leak: main=%v pos=%v sub=%v want=%v",
							id, k, reqSca, w.ObservedProducers.Names(), w.PositionProducers.Names(), w.SubtreeProducers.Names(), wantMask.Names())
						return
					}
				} else if w.ObservedProducers != 0 || w.PositionProducers != 0 || w.SubtreeProducers != 0 {
					t.Errorf("goroutine %d iter %d default build unexpectedly observed masks: main=%v pos=%v sub=%v",
						id, k, w.ObservedProducers.Names(), w.PositionProducers.Names(), w.SubtreeProducers.Names())
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent isolation decode error: %v", err)
	}
}
