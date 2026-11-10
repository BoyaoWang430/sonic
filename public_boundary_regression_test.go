package sonic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bytedance/sonic/ast"
)

type customStringIntMap map[string]int
type customIntSlice []int

type nilCheckHolder struct {
	M    customStringIntMap `json:"m"`
	MO   customStringIntMap `json:"mo,omitempty"`
	S    customIntSlice     `json:"s"`
	SO   customIntSlice     `json:"so,omitempty"`
	I    fmt.Stringer       `json:"i"`
	IO   fmt.Stringer       `json:"io,omitempty"`
	U    uintptr            `json:"u,omitempty"`
	UNon uintptr            `json:"unon,omitempty"`
}

func TestGenericMapAndNilChecks(t *testing.T) {
	m := customStringIntMap{"a": 1, "b": 2}
	got, err := ConfigStd.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal custom map failed: %v", err)
	}
	want, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal custom map = %s, want %s", got, want)
	}

	var nilMap customStringIntMap
	got, err = Marshal(nilMap)
	if err != nil || string(got) != "null" {
		t.Fatalf("Marshal(nilMap) = %s, %v; want null", got, err)
	}
	got, err = Config{NoNullSliceOrMap: true}.Froze().Marshal(nilMap)
	if err != nil || string(got) != "{}" {
		t.Fatalf("Marshal(nilMap) with NoNullSliceOrMap = %s, %v; want {}", got, err)
	}

	h := nilCheckHolder{UNon: 42}
	got, err = Marshal(h)
	if err != nil {
		t.Fatalf("Marshal(nilCheckHolder) failed: %v", err)
	}
	want, err = json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal(nilCheckHolder) = %s, want %s", got, want)
	}
}

var retainedBoxPtrs []*boxEscapeField

type boxEscapeField struct {
	V int `json:"v"`
}

func (f *boxEscapeField) IsZero() bool {
	retainedBoxPtrs = append(retainedBoxPtrs, f)
	return f.V == 0
}

type boxEscapeOuter struct {
	F boxEscapeField `json:"f,omitzero"`
}

func TestEncoderTempBoxEscapeIsZero(t *testing.T) {
	retainedBoxPtrs = nil

	out1, err := Marshal(boxEscapeOuter{F: boxEscapeField{V: 7}})
	if err != nil {
		t.Fatalf("first Marshal failed: %v", err)
	}
	if string(out1) != `{"f":{"v":7}}` {
		t.Fatalf("first Marshal = %s", out1)
	}
	if len(retainedBoxPtrs) != 1 || retainedBoxPtrs[0] == nil || retainedBoxPtrs[0].V != 7 {
		t.Fatalf("after first Marshal, retained pointer = %+v (want V=7)", retainedBoxPtrs)
	}
	firstPtr := retainedBoxPtrs[0]

	out2, err := Marshal(boxEscapeOuter{F: boxEscapeField{V: 9}})
	if err != nil {
		t.Fatalf("second Marshal failed: %v", err)
	}
	if string(out2) != `{"f":{"v":9}}` {
		t.Fatalf("second Marshal = %s", out2)
	}
	if len(retainedBoxPtrs) != 2 || retainedBoxPtrs[1] == nil || retainedBoxPtrs[1].V != 9 {
		t.Fatalf("after second Marshal, retained pointer = %+v (want V=9)", retainedBoxPtrs)
	}
	if firstPtr == retainedBoxPtrs[1] || firstPtr.V != 7 {
		t.Fatalf("first retained pointer was mutated or aliased: firstPtr.V=%d, same=%v", firstPtr.V, firstPtr == retainedBoxPtrs[1])
	}

	// Addressable root struct should pass the real field address.
	retainedBoxPtrs = nil
	addrOuter := &boxEscapeOuter{F: boxEscapeField{V: 42}}
	if _, err := Marshal(addrOuter); err != nil {
		t.Fatalf("Marshal(addrOuter) failed: %v", err)
	}
	if len(retainedBoxPtrs) != 1 || retainedBoxPtrs[0] != &addrOuter.F {
		t.Fatalf("addressable struct did not pass real field address: got %p, want %p", retainedBoxPtrs[0], &addrOuter.F)
	}

	// Unordered and ordered map values with omitzero field retaining receiver pointer.
	for _, cfg := range []API{ConfigDefault, ConfigStd} {
		retainedBoxPtrs = nil
		mp := map[string]boxEscapeOuter{
			"a": {F: boxEscapeField{V: 11}},
			"b": {F: boxEscapeField{V: 22}},
			"c": {F: boxEscapeField{V: 33}},
		}
		if _, err := cfg.Marshal(mp); err != nil {
			t.Fatalf("Marshal(mp) failed: %v", err)
		}
		if len(retainedBoxPtrs) != 3 {
			t.Fatalf("expected 3 retained pointers from map encode, got %d", len(retainedBoxPtrs))
		}
		vals := []int{retainedBoxPtrs[0].V, retainedBoxPtrs[1].V, retainedBoxPtrs[2].V}
		sort.Ints(vals)
		if !reflect.DeepEqual(vals, []int{11, 22, 33}) {
			t.Fatalf("map value temporary slots overwrote retained pointers: got %v, want [11 22 33]", vals)
		}
	}
}

type selfPtrMapKey struct {
	self *selfPtrMapKey
	k    string
}

func (k *selfPtrMapKey) UnmarshalText(b []byte) error {
	k.self = k
	k.k = string(b)
	return nil
}

var retainedDecodeVals []*selfPtrMapVal

type selfPtrMapVal struct {
	V string
}

func (v *selfPtrMapVal) UnmarshalJSON(b []byte) error {
	retainedDecodeVals = append(retainedDecodeVals, v)
	return json.Unmarshal(b, &v.V)
}

type nestedSelfPtrMapVal struct {
	Inner selfPtrMapVal `json:"inner"`
}

func TestDecoderMapSlotReuseCallbacks(t *testing.T) {
	// 1. Key whose UnmarshalText embeds receiver pointer into key struct itself.
	var stdKeyMap map[selfPtrMapKey]int
	var sonicKeyMap map[selfPtrMapKey]int
	src := []byte(`{"a":1,"b":2,"c":3}`)
	if err := json.Unmarshal(src, &stdKeyMap); err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal(src, &sonicKeyMap); err != nil {
		t.Fatalf("Sonic Unmarshal selfPtrMapKey failed: %v", err)
	}
	if len(sonicKeyMap) != len(stdKeyMap) || len(sonicKeyMap) != 3 {
		t.Fatalf("map entry count = %d, want %d (entries collapsed due to key slot reuse)", len(sonicKeyMap), len(stdKeyMap))
	}
	gotKV := make(map[string]int, len(sonicKeyMap))
	for k, v := range sonicKeyMap {
		if k.self == nil || k.self.k != k.k {
			t.Fatalf("corrupted self pointer in key %+v (self=%+v)", k, k.self)
		}
		gotKV[k.k] = v
	}
	if !reflect.DeepEqual(gotKV, map[string]int{"a": 1, "b": 2, "c": 3}) {
		t.Fatalf("decoded key/values = %v", gotKV)
	}

	// 2. Direct map value with UnmarshalJSON retaining receiver pointer.
	retainedDecodeVals = nil
	var valMap map[string]selfPtrMapVal
	if err := Unmarshal([]byte(`{"x":"one","y":"two","z":"three"}`), &valMap); err != nil {
		t.Fatalf("Unmarshal valMap failed: %v", err)
	}
	if len(retainedDecodeVals) != 3 {
		t.Fatalf("retainedDecodeVals len = %d, want 3", len(retainedDecodeVals))
	}
	gotVals := []string{retainedDecodeVals[0].V, retainedDecodeVals[1].V, retainedDecodeVals[2].V}
	sort.Strings(gotVals)
	if !reflect.DeepEqual(gotVals, []string{"one", "three", "two"}) {
		t.Fatalf("retained map value receivers were overwritten: got %v", gotVals)
	}

	// 3. Nested struct field inside map value with UnmarshalJSON retaining receiver pointer.
	retainedDecodeVals = nil
	var nestedMap map[int]nestedSelfPtrMapVal
	if err := Unmarshal([]byte(`{"1":{"inner":"alpha"},"2":{"inner":"beta"}}`), &nestedMap); err != nil {
		t.Fatalf("Unmarshal nestedMap failed: %v", err)
	}
	if len(retainedDecodeVals) != 2 {
		t.Fatalf("retainedDecodeVals len = %d, want 2", len(retainedDecodeVals))
	}
	gotNested := []string{retainedDecodeVals[0].V, retainedDecodeVals[1].V}
	sort.Strings(gotNested)
	if !reflect.DeepEqual(gotNested, []string{"alpha", "beta"}) {
		t.Fatalf("retained nested map value receivers were overwritten: got %v", gotNested)
	}
}

type emptySliceFieldsHolder struct {
	Ints    []int     `json:"ints"`
	Strs    []string  `json:"strs"`
	Floats  []float64 `json:"floats"`
	Bools   []bool    `json:"bools"`
	Anys    []any     `json:"anys"`
	Structs []struct {
		X int `json:"x"`
	} `json:"structs"`
}

func TestEmptyArrayDecodeNoSliceAliasing(t *testing.T) {
	// 1. Root slice
	stdBacking := []int{1, 2, 3}
	stdSlice := stdBacking[:2]
	if err := json.Unmarshal([]byte("[]"), &stdSlice); err != nil {
		t.Fatal(err)
	}
	stdSlice = append(stdSlice, 9)

	sonicBacking := []int{1, 2, 3}
	sonicSlice := sonicBacking[:2]
	if err := Unmarshal([]byte("[]"), &sonicSlice); err != nil {
		t.Fatalf("Unmarshal empty slice failed: %v", err)
	}
	if sonicSlice == nil || len(sonicSlice) != 0 || cap(sonicSlice) != 0 {
		t.Fatalf("after Unmarshal([]), len=%d cap=%d nil=%v; want len=0 cap=0 non-nil", len(sonicSlice), cap(sonicSlice), sonicSlice == nil)
	}
	sonicSlice = append(sonicSlice, 9)
	if !reflect.DeepEqual(sonicBacking, stdBacking) {
		t.Fatalf("sonicBacking = %v, want %v (empty array decode retained old backing)", sonicBacking, stdBacking)
	}

	// 2. Struct fields (fused primitive slices, []any, and generic struct slice)
	intBack := []int{10, 20}
	strBack := []string{"a", "b"}
	fltBack := []float64{1.5, 2.5}
	boolBack := []bool{true, true}
	anyBack := []any{"old1", "old2"}
	structBack := []struct {
		X int `json:"x"`
	}{{X: 1}, {X: 2}}

	h := emptySliceFieldsHolder{
		Ints:    intBack[:1],
		Strs:    strBack[:1],
		Floats:  fltBack[:1],
		Bools:   boolBack[:1],
		Anys:    anyBack[:1],
		Structs: structBack[:1],
	}
	input := `{"ints":[],"strs":[],"floats":[],"bools":[],"anys":[],"structs":[]}`
	if err := UnmarshalString(input, &h); err != nil {
		t.Fatalf("UnmarshalString emptySliceFieldsHolder failed: %v", err)
	}
	if h.Ints == nil || cap(h.Ints) != 0 ||
		h.Strs == nil || cap(h.Strs) != 0 ||
		h.Floats == nil || cap(h.Floats) != 0 ||
		h.Bools == nil || cap(h.Bools) != 0 ||
		h.Anys == nil || cap(h.Anys) != 0 ||
		h.Structs == nil || cap(h.Structs) != 0 {
		t.Fatalf("expected non-nil zero-cap slices, got caps: ints=%d strs=%d floats=%d bools=%d anys=%d structs=%d",
			cap(h.Ints), cap(h.Strs), cap(h.Floats), cap(h.Bools), cap(h.Anys), cap(h.Structs))
	}
	h.Ints = append(h.Ints, 99)
	h.Strs = append(h.Strs, "z")
	h.Floats = append(h.Floats, 9.9)
	h.Bools = append(h.Bools, false)
	h.Anys = append(h.Anys, "new")
	h.Structs = append(h.Structs, struct {
		X int `json:"x"`
	}{X: 99})
	if intBack[0] != 10 || strBack[0] != "a" || fltBack[0] != 1.5 || !boolBack[0] || anyBack[0] != "old1" || structBack[0].X != 1 {
		t.Fatalf("old backing arrays were mutated after append: int=%v str=%v flt=%v bool=%v any=%v struct=%v",
			intBack, strBack, fltBack, boolBack, anyBack, structBack)
	}

	// 3. Streaming decoder path
	intBackStream := []int{10, 20}
	hStream := emptySliceFieldsHolder{Ints: intBackStream[:1]}
	if err := ConfigDefault.NewDecoder(strings.NewReader(`{"ints":[]}`)).Decode(&hStream); err != nil {
		t.Fatalf("Stream Decode empty slice failed: %v", err)
	}
	if hStream.Ints == nil || cap(hStream.Ints) != 0 {
		t.Fatalf("stream empty slice: nil=%v cap=%d", hStream.Ints == nil, cap(hStream.Ints))
	}
	hStream.Ints = append(hStream.Ints, 77)
	if intBackStream[0] != 10 {
		t.Fatalf("stream empty slice mutated old backing: %v", intBackStream)
	}
}

func TestASTNodeUnmarshalJSONCopiesInput(t *testing.T) {
	raw := []byte(`{"greeting":"hello"}`)
	var n ast.Node
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("json.Unmarshal into ast.Node failed: %v", err)
	}
	copy(raw, `{"greeting":"world"}`)
	got, err := n.Get("greeting").String()
	if err != nil || got != "hello" {
		t.Fatalf("n.Get(\"greeting\").String() = %q, %v; want \"hello\"", got, err)
	}
}
