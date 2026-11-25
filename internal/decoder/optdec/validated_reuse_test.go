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
	"reflect"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

type customPlainInt int

// TestValidatedIndexReuseForcedIndexVsForcedTape verifies strict parity between
// forced Validated Index and forced Tape decoding across reused any / []any slots:
//   - error type & Pos, old slot preservation on overflow/nested error, subsequent field updates
//   - cap(cur) >= cnt direct reuse vs cap(cur) < cnt fallback
//   - stale pointer hidden in cur[len(cur):cap(cur)] triggering safe fallback before mutation
//   - pointer identity, **int null clearing, custom named non-pointer types, and UnmarshalJSON callbacks
//   - null, empty array, duplicate keys, and CopyString=false/true string ownership
func TestValidatedIndexReuseForcedIndexVsForcedTape(t *testing.T) {
	type reuseTarget struct {
		Meta  any    `json:"meta"`
		Slice []any  `json:"slice"`
		CB    any    `json:"cb"`
		After int    `json:"after"`
		Str   string `json:"str"`
	}

	assertErrEqual := func(t *testing.T, label string, eIdx, eTape error) {
		t.Helper()
		if (eIdx == nil) != (eTape == nil) {
			t.Fatalf("%s error presence mismatch: idx=%v tape=%v", label, eIdx, eTape)
		}
		if eIdx == nil {
			return
		}
		m1, ok1 := eIdx.(*MismatchTypeError)
		m2, ok2 := eTape.(*MismatchTypeError)
		if !ok1 || !ok2 || m1.Pos != m2.Pos || m1.Type != m2.Type {
			t.Fatalf("%s MismatchTypeError mismatch: idx=%#v tape=%#v", label, eIdx, eTape)
		}
	}

	for _, copyStr := range []bool{false, true} {
		var opts uint64
		if copyStr {
			opts = 1 << consts.F_copy_string
		}

		// 1. Direct plain reuse: zero position builds, zero subtree fallbacks, exact parity on success.
		t.Run("PlainReuseDirectSuccess", func(t *testing.T) {
			src := `{"meta":{"k":"v","n":1.5,"arr":[1,true,null]},"slice":["a",2.5,{"x":3},false],"after":42,"str":"ok"}`
			initTarget := func() reuseTarget {
				return reuseTarget{
					Meta:  map[string]any{"old": "val"},
					Slice: []any{"s0", 1.0, "s2", true, "extra"},
					After: -1,
				}
			}
			gIdx, gTape := initTarget(), initTarget()
			var w routeWitness
			_, _, eIdx := decodeOneWithControl(src, opts, &gIdx, &decodeTestControl{forceRep: repForceIndex, witness: &w})
			_, _, eTape := decodeOneWithControl(src, opts, &gTape, &decodeTestControl{forceRep: repForceTape})
			assertErrEqual(t, "plain_success", eIdx, eTape)
			if !reflect.DeepEqual(gIdx, gTape) {
				t.Fatalf("copyStr=%v mismatch:\n idx = %#v\ntape = %#v", copyStr, gIdx, gTape)
			}
			if w.IndexBinderEntered != 1 || w.PositionBuilds != 0 || w.IndexSubtreeFallback != 0 {
				t.Fatalf("copyStr=%v expected direct index bind (0 pos builds, 0 subtree fallbacks), got %+v", copyStr, w)
			}
			// Verify CopyString ownership on plain string inside reused []any.
			buf := []byte(src)
			sBuf := unsafe.String(unsafe.SliceData(buf), len(buf))
			gOwn := initTarget()
			if _, _, err := decodeOneWithControl(sBuf, opts, &gOwn, &decodeTestControl{forceRep: repForceIndex}); err != nil {
				t.Fatal(err)
			}
			gotElem := gOwn.Slice[0].(string)
			base := uintptr(unsafe.Pointer(unsafe.SliceData(buf)))
			ptr := uintptr(unsafe.Pointer(unsafe.StringData(gotElem)))
			aliased := ptr >= base && ptr < base+uintptr(len(buf))
			if copyStr && aliased {
				t.Fatalf("CopyString=true reused []any string unexpectedly aliased input buffer")
			}
			if !copyStr && !aliased {
				t.Fatalf("CopyString=false reused []any string unexpectedly copied")
			}
		})

		// 2. Old slot preserved on scalar overflow (1e400) and nested container overflow in both Meta and Slice,
		//    while subsequent elements/fields and callbacks still run identically to Tape.
		t.Run("ErrorPreservesOldSlotsAndUpdatesFollowing", func(t *testing.T) {
			cases := []struct {
				name string
				src  string
			}{
				{"MetaScalarOverflow", `{"meta":1e400,"slice":["ok0","ok1"],"cb":{"v":7},"after":11}`},
				{"MetaNestedMapOverflow", `{"meta":{"ok":1,"bad":1e400},"slice":["ok0","ok1"],"cb":{"v":8},"after":12}`},
				{"MetaNestedSliceOverflow", `{"meta":[1,[1e400],2],"slice":["ok0","ok1"],"cb":{"v":9},"after":13}`},
				{"SliceElemScalarOverflow", `{"meta":"new_meta","slice":[1e400,"updated1",{"z":1e400},"updated3"],"cb":{"v":10},"after":14}`},
				{"SliceElemNestedOverflow", `{"meta":"new_meta","slice":[{"ok":1,"bad":[1e400]},"updated1"],"cb":{"v":11},"after":15}`},
				{"DuplicateKeyValidThenOverflow", `{"meta":"first","meta":1e400,"slice":["x","y"],"after":16}`},
				{"DuplicateKeyOverflowThenValid", `{"meta":1e400,"meta":"second","slice":["x","y"],"after":17}`},
				{"NullAndEmptyArray", `{"meta":null,"slice":[],"after":18}`},
				{"SliceNull", `{"meta":"ok","slice":null,"after":19}`},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					cbIdx, cbTape := &r2CallbackVal{}, &r2CallbackVal{}
					makeT := func(cb *r2CallbackVal) reuseTarget {
						s := make([]any, 2, 6)
						s[0] = "old_s0"
						s[1] = "old_s1"
						full := s[:6]
						full[2] = "hidden_plain2"
						full[3] = "hidden_plain3"
						return reuseTarget{
							Meta:  "old_meta",
							Slice: s,
							CB:    cb,
							After: -1,
						}
					}
					gIdx, gTape := makeT(cbIdx), makeT(cbTape)
					_, _, eIdx := decodeOneWithControl(tc.src, opts, &gIdx, &decodeTestControl{forceRep: repForceIndex})
					_, _, eTape := decodeOneWithControl(tc.src, opts, &gTape, &decodeTestControl{forceRep: repForceTape})
					assertErrEqual(t, tc.name, eIdx, eTape)
					if !reflect.DeepEqual(gIdx, gTape) {
						t.Fatalf("%s copyStr=%v mismatch:\n idx = %#v\ntape = %#v", tc.name, copyStr, gIdx, gTape)
					}
					if cbIdx.Calls != cbTape.Calls || cbIdx.V != cbTape.V {
						t.Fatalf("%s callback mismatch: idx=%+v tape=%+v", tc.name, cbIdx, cbTape)
					}
				})
			}
		})

		// 3. Capacity insufficient (0 < cap(cur) < cnt) falls back cleanly and matches Tape.
		t.Run("SliceCapInsufficientFallback", func(t *testing.T) {
			src := `{"slice":["a","b","c","d"],"after":55}`
			makeT := func() reuseTarget {
				return reuseTarget{Slice: []any{"old0", "old1"}}
			}
			gIdx, gTape := makeT(), makeT()
			var w routeWitness
			_, _, eIdx := decodeOneWithControl(src, opts, &gIdx, &decodeTestControl{forceRep: repForceIndex, witness: &w})
			_, _, eTape := decodeOneWithControl(src, opts, &gTape, &decodeTestControl{forceRep: repForceTape})
			assertErrEqual(t, "cap_insufficient", eIdx, eTape)
			if !reflect.DeepEqual(gIdx, gTape) {
				t.Fatalf("mismatch:\n idx = %#v\ntape = %#v", gIdx, gTape)
			}
			if w.IndexSubtreeFallback != 1 {
				t.Fatalf("expected 1 subtree fallback when cap(cur) < cnt, got %+v", w)
			}
		})

		// 4. Hidden pointer in cur[len(cur):cap(cur)] triggers fallback before ANY element mutation.
		t.Run("HiddenPointerBeyondLenWithinCap", func(t *testing.T) {
			src := `{"slice":["first",{"v":77},"third"],"after":66}`
			cbIdx, cbTape := &r2CallbackVal{V: 1}, &r2CallbackVal{V: 1}
			makeT := func(cb *r2CallbackVal) reuseTarget {
				backing := []any{"old0", cb, "old2", "old3"}
				return reuseTarget{Slice: backing[:1]} // len=1, cap=4, cb hidden at index 1!
			}
			gIdx, gTape := makeT(cbIdx), makeT(cbTape)
			var w routeWitness
			_, _, eIdx := decodeOneWithControl(src, opts, &gIdx, &decodeTestControl{forceRep: repForceIndex, witness: &w})
			_, _, eTape := decodeOneWithControl(src, opts, &gTape, &decodeTestControl{forceRep: repForceTape})
			assertErrEqual(t, "hidden_ptr", eIdx, eTape)
			if !reflect.DeepEqual(gIdx, gTape) {
				t.Fatalf("mismatch:\n idx = %#v\ntape = %#v", gIdx, gTape)
			}
			if cbIdx.Calls != 1 || cbIdx.V != 77 || gIdx.Slice[1] != cbIdx {
				t.Fatalf("hidden pointer not decoded in-place: cb=%+v slice[1]=%#v", cbIdx, gIdx.Slice[1])
			}
			if w.IndexSubtreeFallback != 1 {
				t.Fatalf("expected fallback on hidden pointer, got %+v", w)
			}
		})

		// 5. **int null clearing and custom named non-pointer type fallback parity.
		t.Run("PtrToPtrNullAndCustomNamedType", func(t *testing.T) {
			// **int with JSON null must nil inner pointer while keeping outer **int in Meta!
			x1, x2 := 10, 10
			px1, px2 := &x1, &x2
			gIdx := reuseTarget{Meta: &px1, Slice: []any{customPlainInt(5)}}
			gTape := reuseTarget{Meta: &px2, Slice: []any{customPlainInt(5)}}
			src := `{"meta":null,"slice":[123],"after":88}`
			var w routeWitness
			_, _, eIdx := decodeOneWithControl(src, opts, &gIdx, &decodeTestControl{forceRep: repForceIndex, witness: &w})
			_, _, eTape := decodeOneWithControl(src, opts, &gTape, &decodeTestControl{forceRep: repForceTape})
			assertErrEqual(t, "ptr_to_ptr_null", eIdx, eTape)
			if px1 != nil || px2 != nil || gIdx.Meta != &px1 || !reflect.DeepEqual(gIdx, gTape) {
				t.Fatalf("ptr-to-ptr null mismatch: px1=%v meta=%#v vs tape=%#v", px1, gIdx, gTape)
			}
			if w.IndexSubtreeFallback != 2 {
				t.Fatalf("expected 2 fallbacks (**int null + custom named type in slice), got %+v", w)
			}
		})

		// 6. Plain json.Number stored in reused any/[]any overwrites directly with 0 fallbacks.
		t.Run("PlainJSONNumberOverwritesDirectly", func(t *testing.T) {
			gIdx := reuseTarget{Meta: json.Number("99"), Slice: []any{json.Number("1"), json.Number("2")}}
			gTape := reuseTarget{Meta: json.Number("99"), Slice: []any{json.Number("1"), json.Number("2")}}
			src := `{"meta":42.5,"slice":[3.5,"ok"],"after":9}`
			var w routeWitness
			_, _, eIdx := decodeOneWithControl(src, opts, &gIdx, &decodeTestControl{forceRep: repForceIndex, witness: &w})
			_, _, eTape := decodeOneWithControl(src, opts, &gTape, &decodeTestControl{forceRep: repForceTape})
			assertErrEqual(t, "json_number_reuse", eIdx, eTape)
			if !reflect.DeepEqual(gIdx, gTape) {
				t.Fatalf("mismatch:\n idx = %#v\ntape = %#v", gIdx, gTape)
			}
			if w.PositionBuilds != 0 || w.IndexSubtreeFallback != 0 {
				t.Fatalf("expected direct bind for json.Number reuse, got %+v", w)
			}
		})
	}
}
