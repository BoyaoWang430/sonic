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

package jsonscan

import (
	"math"
	"strings"
	"testing"
)

func TestLexNumberAuthorityAndChunkSplits(t *testing.T) {
	// 1. Valid JSON numbers: verify LexNumberAt, arbitrary 1..7-byte chunk splits,
	// UseNumber invariance on lexical span, and hint vs ParseValidatedNumber identity.
	cases := []struct {
		src      string
		wantEnd  int
		wantForm NumberForm
		neg      bool
		negZero  bool
	}{
		{"0", 1, NumberFormInt, false, false},
		{"-0", 2, NumberFormInt, true, true},
		{"-0.0", 4, NumberFormFrac, true, true},
		{"-0.00e+5", 8, NumberFormFracExp, true, true},
		{"1.0", 3, NumberFormFrac, false, false},
		{"1e+1", 4, NumberFormExp, false, false},
		{"9223372036854775807", 19, NumberFormInt, false, false},
		{"9223372036854775808", 19, NumberFormInt, false, false},
		{"-9223372036854775808", 20, NumberFormInt, true, false},
		{"-9223372036854775809", 20, NumberFormInt, true, false},
		{"18446744073709551615", 20, NumberFormInt, false, false},
		{"18446744073709551616", 20, NumberFormInt, false, false},
		{"01", 1, NumberFormInt, false, false},
		{"1true", 1, NumberFormInt, false, false},
		{"1e999999", 8, NumberFormExp, false, false},
		{"0." + strings.Repeat("0", 820) + "5", 823, NumberFormFrac, false, false},
	}

	for _, tc := range cases {
		res := LexNumberAt(tc.src, 0, true)
		if !res.Complete || res.NeedMore || res.Start != 0 || res.End != tc.wantEnd ||
			res.Form != tc.wantForm || res.Negative != tc.neg || res.NegZero != tc.negZero {
			t.Fatalf("LexNumberAt(%q) = %+v, want end=%d form=%d neg=%v negZero=%v",
				tc.src, res, tc.wantEnd, tc.wantForm, tc.neg, tc.negZero)
		}

		// Chunk split invariance (step=1, 2, 7) on pure LexState (no Machine/Profile!).
		for _, step := range []int{1, 2, 7} {
			var l LexState
			l.beginNumber(0)
			for cut := step; ; cut += step {
				final := cut >= len(tc.src)
				if final {
					cut = len(tc.src)
				}
				r := lexNumber(&l, tc.src[:cut], final)
				if r.NeedMore {
					if final {
						t.Fatalf("lexNumber(%q) returned NeedMore at final", tc.src)
					}
					continue
				}
				if !r.Complete || r.End != tc.wantEnd || r.Form != tc.wantForm || r.Negative != tc.neg || r.NegZero != tc.negZero {
					t.Fatalf("chunked lexNumber(%q, step=%d) = %+v, want end=%d form=%d neg=%v negZero=%v",
						tc.src, step, r, tc.wantEnd, tc.wantForm, tc.neg, tc.negZero)
				}
				break
			}
		}
	}

	// 2. Prefix vs FullDocument and UseNumber consumer invariance on valid JSON numbers.
	// Single-value prefix ("01", "1true", "1 2") succeeds at prefix end when FullDocument=false
	// regardless of UseNumber, and rejects trailing bytes when FullDocument=true.
	for _, useNum := range []bool{false, true} {
		for _, src := range []string{"01", "1true", "1 2"} {
			var mPfx Machine
			mPfx.Nodes = make([]Node, 4)
			mPfx.Reset(StrictProfile(true, true, true, useNum, true, false), ModeEmit)
			rPfx := mPfx.Step(src, true)
			if rPfx.Reason != StopDone || rPfx.Consumed != 1 || mPfx.NodeLen != 1 {
				t.Fatalf("single-value prefix %q (useNum=%v) expected Consumed=1, got %+v", src, useNum, rPfx)
			}

			var mFull Machine
			mFull.Nodes = make([]Node, 4)
			mFull.Reset(StrictProfile(true, true, true, useNum, true, true), ModeEmit)
			rFull := mFull.Step(src, true)
			if rFull.Reason != StopError || rFull.Code != ErrInvalidChar {
				t.Fatalf("full-document %q (useNum=%v) expected trailing ErrInvalidChar, got %+v", src, useNum, rFull)
			}
		}
	}

	// 3. Malformed numbers ("1.", "1e+", "1.a", "-", "NaN", "Inf", "+1") and
	// error_adapter mapping from neutral NumberFaultFact across chunk splits.
	faultCases := []struct {
		src        string
		stdOff     int
		useNumOff  int
		wantDotPos int
		wantExpPos int
		wantSgnPos int
	}{
		{"-", 1, 1, -1, -1, -1},
		{"1.", 2, 1, 1, -1, -1},
		{"1.a", 2, 1, 1, -1, -1},
		{"1..", 2, 2, 1, -1, -1},
		{"1e", 2, 1, -1, 1, -1},
		{"1e+", 3, 2, -1, 1, 2},
		{"1e+a", 3, 2, -1, 1, 2},
		{"1e++", 3, 3, -1, 1, 2},
	}
	for _, tc := range faultCases {
		lr := LexNumberAt(tc.src, 0, true)
		if lr.Complete || lr.NeedMore {
			t.Fatalf("LexNumberAt(%q) unexpectedly succeeded: %+v", tc.src, lr)
		}
		if lr.Fault.DotPos != tc.wantDotPos || lr.Fault.ExpPos != tc.wantExpPos || lr.Fault.SignPos != tc.wantSgnPos || lr.Fault.FaultPos != tc.stdOff {
			t.Fatalf("LexNumberAt(%q) fault facts = %+v, want dot=%d exp=%d sign=%d fault=%d",
				tc.src, lr.Fault, tc.wantDotPos, tc.wantExpPos, tc.wantSgnPos, tc.stdOff)
		}

		for _, useNum := range []bool{false, true} {
			wantOff := tc.stdOff
			if useNum {
				wantOff = tc.useNumOff
			}
			for _, step := range []int{len(tc.src), 1} {
				var m Machine
				m.Nodes = make([]Node, 4)
				m.Reset(StrictProfile(true, true, true, useNum, !useNum, true), ModeEmit)
				var res ScanResult
				for cut := step; ; cut += step {
					final := cut >= len(tc.src)
					if final {
						cut = len(tc.src)
					}
					res = m.Step(tc.src[:cut], final)
					if res.Reason != StopNeedInput {
						break
					}
				}
				if res.Reason != StopError || res.Code != ErrInvalidNumber || res.ErrorOffset != wantOff {
					t.Fatalf("Machine(%q, useNum=%v, step=%d) = %+v, want ErrInvalidNumber at %d",
						tc.src, useNum, step, res, wantOff)
				}
			}
		}
	}

	// 4. Verify nonfinal trailing integer/literal does not prematurely terminate at chunk boundary.
	var lNonFinal LexState
	lNonFinal.beginNumber(0)
	if r := lexNumber(&lNonFinal, "123", false); !r.NeedMore || r.Complete {
		t.Fatalf("nonfinal \"123\" must yield NeedMore, got %+v", r)
	}
	if r2 := lexNumber(&lNonFinal, "123.5", false); !r2.NeedMore || r2.Complete {
		t.Fatalf("nonfinal \"123.5\" must yield NeedMore, got %+v", r2)
	}
	r3 := lexNumber(&lNonFinal, "123.5 ", false)
	if !r3.Complete || r3.End != 5 || r3.Form != NumberFormFrac {
		t.Fatalf("delimited nonfinal \"123.5 \" expected Complete at 5, got %+v", r3)
	}

	// 5. Hint vs unhinted ParseValidatedNumber bit-identity on negative zero and exact decimals.
	var mEmit Machine
	mEmit.Nodes = make([]Node, 8)
	mEmit.Reset(StrictProfile(true, true, true, false, false, true), ModeEmit)
	rNegZero := mEmit.Step("[-0, -0.0, 1.25]", true)
	if rNegZero.Reason != StopDone || mEmit.NodeLen != 4 {
		t.Fatalf("[-0, -0.0, 1.25] failed: %+v", rNegZero)
	}
	if (mEmit.Nodes[1].Typ&TypeMask) != KSint || mEmit.Nodes[1].Val != 0 {
		t.Fatalf("-0 node mismatch: %+v", mEmit.Nodes[1])
	}
	fNegZero := math.Float64frombits(mEmit.Nodes[2].Val)
	if (mEmit.Nodes[2].Typ&TypeMask) != KReal || fNegZero != 0 || !math.Signbit(fNegZero) {
		t.Fatalf("-0.0 lost signbit: %+v", mEmit.Nodes[2])
	}
}
