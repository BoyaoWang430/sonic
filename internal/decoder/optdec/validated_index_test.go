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
	"math"
	"reflect"
	"testing"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

type idxTestChild struct {
	Label string  `json:"label"`
	F32   float32 `json:"f32"`
	F64   float64 `json:"f64"`
}

type idxTestStruct struct {
	ID      int64           `json:"id"`
	Count   int             `json:"count"`
	SmallI8 int8            `json:"i8"`
	U32     uint32          `json:"u32"`
	Flag    bool            `json:"flag"`
	Title   string          `json:"title"`
	PInt    *int64          `json:"pint"`
	PStr    *string         `json:"pstr"`
	Child   idxTestChild    `json:"child"`
	PChild  *idxTestChild   `json:"pchild"`
	Nums    []int64         `json:"nums"`
	Floats  []float32       `json:"floats"`
	Items   []idxTestChild  `json:"items"`
	PItems  []*idxTestChild `json:"pitems"`
}

func TestValidatedIndexAdmissionBarrierAndSemantics(t *testing.T) {
	var probe idxTestStruct
	_, _, _, proj, err := unpackAndCompileRoot(&probe)
	if err != nil || proj == nil || proj.idxPlan == nil {
		t.Fatalf("expected idxTestStruct to compile idxPlan: proj=%+v err=%v", proj, err)
	}

	// 1. SYN-PREMUT barrier: syntax error inside top-level value MUST leave prefilled
	// struct, pointee alias, and shared slice backing 100% unmutated!
	aliasedInt := int64(777)
	aliasedStr := "keep-me"
	backing := []int64{10, 20, 30}
	prefilled := idxTestStruct{
		ID:    99,
		Title: "before",
		PInt:  &aliasedInt,
		PStr:  &aliasedStr,
		Nums:  backing[:2],
	}
	malformed := `{"id": 123, "pint": 456, "pstr": "mutated", "nums": [1, 2], "broken": [1, }`
	if _, _, err := DecodeOne(malformed, 1<<_F_validate_string, &prefilled); err == nil {
		t.Fatal("expected syntax error on malformed input")
	}
	if prefilled.ID != 99 || prefilled.Title != "before" || aliasedInt != 777 || aliasedStr != "keep-me" || backing[0] != 10 || backing[1] != 20 {
		t.Fatalf("SYN-PREMUT violated! prefilled=%+v aliasedInt=%d aliasedStr=%q backing=%v",
			prefilled, aliasedInt, aliasedStr, backing)
	}

	// 2. Duplicate keys, unknown fields, prefilled pointee mutation in-place, -0,
	// escaped strings/keys, and single-step float32 midpoint rounding (1.0000000596046448)!
	validJSON := `{
		"unknown_obj": {"nested": [1, true, "esc\nstr", 1e400]},
		"id": 1,
		"iD": 2,
		"count": -0,
		"pint": 888,
		"pstr": "updated\nstr",
		"t\u0069tle": "hello\"world",
		"child": {"label": "c1", "f32": 1.0000000596046448, "f64": -0.0},
		"pchild": {"label": "pc", "f32": -0.0, "f64": 1.0000000596046448},
		"nums": [-9223372036854775808, 0, -0, 9223372036854775807],
		"floats": [1.0000000596046448, -0.0, 0.125],
		"items": [{"label": "i0", "f32": 2.5, "f64": 3.5}],
		"pitems": [null, {"label": "pi1", "f32": 4.5, "f64": 5.5}]
	}`

	aliasedInt2 := int64(111)
	dstIdx := idxTestStruct{PInt: &aliasedInt2}
	aliasedIntStd := int64(111)
	dstStd := idxTestStruct{PInt: &aliasedIntStd}

	if err := json.Unmarshal([]byte(validJSON), &dstStd); err != nil {
		t.Fatalf("stdlib unmarshal failed: %v", err)
	}
	if _, _, err := DecodeOne(validJSON, 1<<_F_copy_string|1<<_F_validate_string, &dstIdx); err != nil {
		t.Fatalf("DecodeOne failed: %v", err)
	}
	if aliasedInt2 != 888 || dstIdx.PInt != &aliasedInt2 {
		t.Fatalf("expected in-place pointee mutation on PInt: got %v (%d)", dstIdx.PInt, aliasedInt2)
	}
	if !reflect.DeepEqual(dstIdx, dstStd) {
		t.Fatalf("mismatch vs stdlib:\n got=%+v\nwant=%+v", dstIdx, dstStd)
	}
	// Verify negative zero sign bits on float64 and float32!
	if !math.Signbit(dstIdx.Child.F64) || !math.Signbit(float64(dstIdx.PChild.F32)) || !math.Signbit(float64(dstIdx.Floats[1])) {
		t.Fatalf("negative zero sign bit lost: child.f64=%v pchild.f32=%v floats[1]=%v",
			math.Float64bits(dstIdx.Child.F64), math.Float32bits(dstIdx.PChild.F32), math.Float32bits(dstIdx.Floats[1]))
	}
	// Verify exact single-step float32 rounding on 1.0000000596046448!
	wantF32Bits := math.Float32bits(1.0) + 1
	if math.Float32bits(dstIdx.Child.F32) != wantF32Bits || math.Float32bits(dstIdx.Floats[0]) != wantF32Bits {
		t.Fatalf("float32 single-step rounding mismatch: got %08x / %08x, want %08x",
			math.Float32bits(dstIdx.Child.F32), math.Float32bits(dstIdx.Floats[0]), wantF32Bits)
	}

	// 3. First-error continuation + failed-slot pointer allocation parity vs OptionCaseSensitive (FromDom path)!
	mismatchJSON := `{"pint": "bad_int", "i8": 999, "title": "still_applied"}`
	var gotIdx, gotDom idxTestStruct
	_, _, errIdx := DecodeOne(mismatchJSON, 0, &gotIdx)
	// OptionCaseSensitive forces the existing Node Tape + FromDom path for exact differential comparison:
	_, _, errDom := DecodeOne(mismatchJSON, uint64(consts.OptionCaseSensitive), &gotDom)
	if (errIdx == nil) != (errDom == nil) || errIdx.Error() != errDom.Error() {
		t.Fatalf("mismatch error divergence:\n idx=%v\n dom=%v", errIdx, errDom)
	}
	if !reflect.DeepEqual(gotIdx, gotDom) {
		t.Fatalf("failed-slot / continuation state divergence:\n idx=%+v\n dom=%+v", gotIdx, gotDom)
	}
}

type idxUnmarshalerBox struct {
	Called *int
	Raw    string
}

func (b *idxUnmarshalerBox) UnmarshalJSON(data []byte) error {
	if b.Called != nil {
		*b.Called++
	}
	b.Raw = string(data)
	return nil
}

type idxDynamicAnyStruct struct {
	Dyn   interface{}   `json:"dyn"`
	Slice []interface{} `json:"slice"`
	Tail  string        `json:"tail"`
}

type idxRecNode struct {
	Val  int         `json:"val"`
	Next *idxRecNode `json:"next"`
}

func TestValidatedIndexDynamicAnyAndGrowth(t *testing.T) {
	// 1. Pre-populated any with Unmarshaler MUST NOT invoke callback on late syntax error!
	called := 0
	box := &idxUnmarshalerBox{Called: &called}
	badDst := idxDynamicAnyStruct{Dyn: box, Tail: "untouched"}
	if _, _, err := DecodeOne(`{"dyn": {"a": 1}, "tail": "x", "bad": [}`, 0, &badDst); err == nil {
		t.Fatal("expected syntax error")
	}
	if called != 0 || box.Raw != "" || badDst.Tail != "untouched" {
		t.Fatalf("pre-populated any callback or sibling mutated before syntax error: called=%d raw=%q tail=%q",
			called, box.Raw, badDst.Tail)
	}

	// 2. Valid pre-populated any, duplicate key resetting any -> nil -> map, double-pointer null, and pre-populated []any!
	innerVal := 42
	innerPtr := &innerVal
	child := &idxTestChild{Label: "init", F32: 9.5}
	elemChild := &idxTestChild{Label: "elem0"}

	cases := []struct {
		name string
		json string
		make func() idxDynamicAnyStruct
	}{
		{
			name: "nil_any_nested_and_slice",
			json: `{"dyn":{"k":"v\n1","arr":[1,-2,3.5,true,false,null,{}]},"slice":[10,"abc",null,{"x":1}],"tail":"ok"}`,
			make: func() idxDynamicAnyStruct { return idxDynamicAnyStruct{} },
		},
		{
			name: "prefilled_struct_ptr_in_any",
			json: `{"dyn":{"label":"updated","f32":1.0000000596046448},"slice":[],"tail":"done"}`,
			make: func() idxDynamicAnyStruct {
				c := *child
				return idxDynamicAnyStruct{Dyn: &c}
			},
		},
		{
			name: "duplicate_key_resets_prefilled_any",
			json: `{"dyn":{"label":"step1"},"dyn":null,"dyn":["now","slice",42],"tail":"dup"}`,
			make: func() idxDynamicAnyStruct {
				c := *child
				return idxDynamicAnyStruct{Dyn: &c}
			},
		},
		{
			name: "double_ptr_in_any_null",
			json: `{"dyn":null,"slice":null,"tail":"dptr"}`,
			make: func() idxDynamicAnyStruct {
				v := *innerPtr
				p := &v
				return idxDynamicAnyStruct{Dyn: &p, Slice: []interface{}{1, 2}}
			},
		},
		{
			name: "prefilled_slice_eface_backing",
			json: `{"dyn":123,"slice":[{"label":"into_child"},"plain"],"tail":"backing"}`,
			make: func() idxDynamicAnyStruct {
				ec := *elemChild
				backing := []interface{}{&ec, "old"}
				return idxDynamicAnyStruct{Slice: backing[:0]}
			},
		},
		{
			name: "any_float_overflow_error_parity",
			json: `{"dyn":1e400,"slice":[1e400],"tail":"after_err"}`,
			make: func() idxDynamicAnyStruct { return idxDynamicAnyStruct{} },
		},
	}

	for _, tc := range cases {
		gotIdx := tc.make()
		gotDom := tc.make()
		_, _, errIdx := DecodeOne(tc.json, 1<<_F_copy_string|1<<_F_validate_string, &gotIdx)
		_, _, errDom := DecodeOne(tc.json, uint64(consts.OptionCaseSensitive)|1<<_F_copy_string|1<<_F_validate_string, &gotDom)
		if (errIdx == nil) != (errDom == nil) {
			t.Fatalf("%s: err mismatch idx=%v dom=%v", tc.name, errIdx, errDom)
		}
		if errIdx != nil && errIdx.Error() != errDom.Error() {
			t.Fatalf("%s: err text mismatch:\n idx=%v\n dom=%v", tc.name, errIdx, errDom)
		}
		if !reflect.DeepEqual(gotIdx, gotDom) {
			t.Fatalf("%s: value mismatch:\n idx=%#v\n dom=%#v", tc.name, gotIdx, gotDom)
		}
	}

	// 3. Self-recursive *Struct + >4096 index entry growth (growIdxEntries)!
	var recGot, recWant idxRecNode
	recJSON := `{"val":1,"next":{"val":2,"next":{"val":3,"next":null}}}`
	if _, _, err := DecodeOne(recJSON, 0, &recGot); err != nil {
		t.Fatalf("recursive DecodeOne failed: %v", err)
	}
	if err := json.Unmarshal([]byte(recJSON), &recWant); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recGot, recWant) {
		t.Fatalf("recursive struct mismatch: got=%+v want=%+v", recGot, recWant)
	}

	bigSlice := make([]int64, 6000)
	for i := range bigSlice {
		bigSlice[i] = int64(i*3 - 5000)
	}
	bigBytes, err := json.Marshal(bigSlice)
	if err != nil {
		t.Fatal(err)
	}
	var gotBig []int64
	if _, _, err := DecodeOne(string(bigBytes), 0, &gotBig); err != nil {
		t.Fatalf("big slice DecodeOne failed: %v", err)
	}
	if !reflect.DeepEqual(gotBig, bigSlice) {
		t.Fatalf("big slice mismatch: len=%d", len(gotBig))
	}
}
