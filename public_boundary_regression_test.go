package sonic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bytedance/sonic/ast"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/option"
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
	if APIKind == UseSonicJSON {
		got, err = Config{NoNullSliceOrMap: true}.Froze().Marshal(nilMap)
		if err != nil || string(got) != "{}" {
			t.Fatalf("Marshal(nilMap) with NoNullSliceOrMap = %s, %v; want {}", got, err)
		}
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
	if APIKind != UseSonicJSON {
		t.Skip("omitzero and native encoder temporary box test only applies to UseSonicJSON")
	}
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
	if APIKind != UseSonicJSON {
		t.Skip("map callback temporary slot isolation applies to native UseSonicJSON backend")
	}
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

type ptrMarshalerInt int

func (n *ptrMarshalerInt) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"custom:%d"`, *n)), nil
}

type valMarshalerInt int

func (n valMarshalerInt) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"val:%d"`, int(n))), nil
}

type ptrTextMarshalerInt int

func (n *ptrTextMarshalerInt) MarshalText() ([]byte, error) {
	return []byte(fmt.Sprintf("txt:%d", *n)), nil
}

type ptrAppenderInt int

func (n *ptrAppenderInt) AppendJSON(dst []byte) ([]byte, error) {
	return append(dst, fmt.Sprintf(`"app:%d"`, *n)...), nil
}

type embeddedStringTagInner struct {
	PtrJSON ptrMarshalerInt     `json:"ptr_json,string"`
	ValJSON valMarshalerInt     `json:"val_json,string"`
	PtrText ptrTextMarshalerInt `json:"ptr_text,string"`
	Plain   int                 `json:"plain,string"`
}

type embeddedStringTagOuter struct {
	*embeddedStringTagInner
	DirectVal ptrMarshalerInt  `json:"direct_val,string"`
	DirectPtr *ptrMarshalerInt `json:"direct_ptr,string"`
}

type embeddedAppenderInner struct {
	App ptrAppenderInt `json:"app,string"`
}

type embeddedAppenderOuter struct {
	*embeddedAppenderInner
}

func TestEmbeddedPointerStringTagMarshaler(t *testing.T) {
	dp := ptrMarshalerInt(9)
	cases := []struct {
		name string
		val  any
	}{
		{
			name: "OuterValue_WithNonNilEmbeddedPtr",
			val: embeddedStringTagOuter{
				embeddedStringTagInner: &embeddedStringTagInner{
					PtrJSON: 7,
					ValJSON: 8,
					PtrText: 6,
					Plain:   5,
				},
				DirectVal: 4,
				DirectPtr: &dp,
			},
		},
		{
			name: "OuterPointer_WithNonNilEmbeddedPtr",
			val: &embeddedStringTagOuter{
				embeddedStringTagInner: &embeddedStringTagInner{
					PtrJSON: 7,
					ValJSON: 8,
					PtrText: 6,
					Plain:   5,
				},
				DirectVal: 4,
				DirectPtr: &dp,
			},
		},
		{
			name: "OuterValue_WithNilEmbeddedPtr",
			val: embeddedStringTagOuter{
				embeddedStringTagInner: nil,
				DirectVal:              4,
				DirectPtr:              nil,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.val)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			for _, cfg := range []API{ConfigDefault, ConfigStd} {
				got, err := cfg.Marshal(tc.val)
				if err != nil {
					t.Fatalf("sonic Marshal failed: %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("sonic Marshal = %s, want %s", got, want)
				}
			}
		})
	}

	if APIKind == UseSonicJSON {
		v := embeddedAppenderOuter{
			embeddedAppenderInner: &embeddedAppenderInner{App: 13},
		}
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("Marshal(embeddedAppenderOuter) failed: %v", err)
		}
		if string(got) != `{"app":"app:13"}` {
			t.Fatalf("Marshal(embeddedAppenderOuter) = %s, want {\"app\":\"app:13\"}", got)
		}
	}
}

type arrayBoxEscapeHolder struct {
	Arr [2]boxEscapeOuter `json:"arr"`
}

type failingMapVal struct {
	V string
}

func (v *failingMapVal) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "boom" {
		return fmt.Errorf("callback boom")
	}
	v.V = s
	return nil
}

func TestBoundaryCombinationScenarios(t *testing.T) {
	// 1. Stream Tape capacity boundary around 511 / 512 / 513 scalar elements in root array.
	for _, n := range []int{510, 511, 512, 513, 514} {
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, "%d", i)
		}
		sb.WriteString("] {\"tail\":true}")
		dec := ConfigDefault.NewDecoder(strings.NewReader(sb.String()))
		var arr []int
		if err := dec.Decode(&arr); err != nil {
			t.Fatalf("stream Decode(%d ints) failed: %v", n, err)
		}
		if len(arr) != n || arr[0] != 0 || arr[n-1] != n-1 {
			t.Fatalf("stream Decode(%d ints) len=%d, last=%d", n, len(arr), arr[len(arr)-1])
		}
		var tail map[string]bool
		if err := dec.Decode(&tail); err != nil || !tail["tail"] {
			t.Fatalf("stream Decode tail after %d ints failed: %v, %v", n, tail, err)
		}
	}

	// 2. Parser growth (>16 nodes) combined with >64-byte whitespace runs and Unicode unescaping.
	ws := strings.Repeat(" ", 96)
	var uBuilder strings.Builder
	uBuilder.WriteByte('[')
	for i := 0; i < 40; i++ {
		if i > 0 {
			uBuilder.WriteByte(',')
		}
		uBuilder.WriteString(ws)
		fmt.Fprintf(&uBuilder, `"item-\u0041-%d"`, i)
	}
	uBuilder.WriteByte(']')
	var uOut []string
	if err := UnmarshalString(uBuilder.String(), &uOut); err != nil {
		t.Fatalf("UnmarshalString with parser growth + whitespace + unicode failed: %v", err)
	}
	if len(uOut) != 40 || uOut[0] != "item-A-0" || uOut[39] != "item-A-39" {
		t.Fatalf("unexpected uOut: len=%d, first=%q, last=%q", len(uOut), uOut[0], uOut[len(uOut)-1])
	}

	// 3. Pre-populated map + duplicate keys + callback error preserves prior valid entries.
	preMap := map[string]failingMapVal{
		"keep": {V: "initial"},
	}
	err := Unmarshal([]byte(`{"dup":"first","dup":"second","bad":"boom"}`), &preMap)
	if err == nil {
		t.Fatalf("expected callback error on boom")
	}
	if preMap["keep"].V != "initial" || preMap["dup"].V != "second" {
		t.Fatalf("unexpected preMap state after callback error: %+v", preMap)
	}

	if APIKind != UseSonicJSON {
		return
	}

	// 4. Native-only: inline array elements with pointer-receiver IsZero and map value inline array callbacks.
	retainedBoxPtrs = nil
	arrHolder := arrayBoxEscapeHolder{
		Arr: [2]boxEscapeOuter{
			{F: boxEscapeField{V: 101}},
			{F: boxEscapeField{V: 202}},
		},
	}
	if _, err := Marshal(arrHolder); err != nil {
		t.Fatalf("Marshal(arrHolder) failed: %v", err)
	}
	if len(retainedBoxPtrs) != 2 || retainedBoxPtrs[0].V != 101 || retainedBoxPtrs[1].V != 202 {
		t.Fatalf("inline array temporary box overwrote retained pointers: %+v", retainedBoxPtrs)
	}

	retainedDecodeVals = nil
	var arrValMap map[string][2]selfPtrMapVal
	if err := Unmarshal([]byte(`{"a":["v1","v2"],"b":["v3","v4"]}`), &arrValMap); err != nil {
		t.Fatalf("Unmarshal(arrValMap) failed: %v", err)
	}
	if len(retainedDecodeVals) != 4 {
		t.Fatalf("retainedDecodeVals len = %d, want 4", len(retainedDecodeVals))
	}
	gotArrVals := []string{retainedDecodeVals[0].V, retainedDecodeVals[1].V, retainedDecodeVals[2].V, retainedDecodeVals[3].V}
	sort.Strings(gotArrVals)
	if !reflect.DeepEqual(gotArrVals, []string{"v1", "v2", "v3", "v4"}) {
		t.Fatalf("inline array map value callbacks overwrote receivers: %v", gotArrVals)
	}
}

func TestEfaceSmallUintStaticTableSemantics(t *testing.T) {
	payload := `{"zero":0,"one":1,"edgeLo":4095,"edgeHi":4096,"negZero":-0,"negZeroFrac":-0.0,"negOne":-1,"exp":1e2,"e1":[],"e2":[],"arr":[0,7,4095,4096,-0.0,[],18446744073709551615]}`

	// 1. Default float64 mode must match encoding/json dynamic types and values.
	var wantAny, gotAny any
	if err := json.Unmarshal([]byte(payload), &wantAny); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if err := ConfigDefault.Unmarshal([]byte(payload), &gotAny); err != nil {
		t.Fatalf("ConfigDefault.Unmarshal failed: %v", err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Fatalf("ConfigDefault.Unmarshal mismatch:\ngot:  %#v\nwant: %#v", gotAny, wantAny)
	}
	gotMap := gotAny.(map[string]any)
	e1 := gotMap["e1"].([]any)
	e2 := gotMap["e2"].([]any)
	if e1 == nil || e2 == nil || cap(e1) != 0 || cap(e2) != 0 {
		t.Fatalf("expected empty eface slices to be non-nil with cap=0: len/cap=(%d,%d) and (%d,%d)", len(e1), cap(e1), len(e2), cap(e2))
	}
	e1 = append(e1, "mutated")
	if len(gotMap["e2"].([]any)) != 0 {
		t.Fatalf("appending to empty eface slice unexpectedly mutated sibling empty slice")
	}
	if !math.Signbit(gotMap["negZeroFrac"].(float64)) {
		t.Fatalf("expected -0.0 to preserve negative signbit")
	}
	if math.Signbit(gotMap["zero"].(float64)) {
		t.Fatalf("expected 0 to have positive signbit")
	}

	// Also verify direct map[string]any, []any, and scalar root entrypoints.
	var gotDirectMap map[string]any
	if err := ConfigDefault.Unmarshal([]byte(`{"a":0,"b":42,"c":4095,"d":4096}`), &gotDirectMap); err != nil {
		t.Fatalf("direct map[string]any Unmarshal failed: %v", err)
	}
	if gotDirectMap["a"] != float64(0) || gotDirectMap["b"] != float64(42) || gotDirectMap["c"] != float64(4095) || gotDirectMap["d"] != float64(4096) {
		t.Fatalf("unexpected direct map values: %#v", gotDirectMap)
	}
	var gotScalar any
	if err := ConfigDefault.Unmarshal([]byte(`512`), &gotScalar); err != nil || gotScalar != float64(512) {
		t.Fatalf("scalar Unmarshal(512) = %#v (%T), err=%v", gotScalar, gotScalar, err)
	}

	// 2. UseNumber mode must still return json.Number across all integer magnitudes.
	cfgNum := Config{UseNumber: true}.Froze()
	var gotNum any
	if err := cfgNum.Unmarshal([]byte(payload), &gotNum); err != nil {
		t.Fatalf("UseNumber Unmarshal failed: %v", err)
	}
	numMap := gotNum.(map[string]any)
	if numMap["one"] != json.Number("1") || numMap["edgeLo"] != json.Number("4095") || numMap["edgeHi"] != json.Number("4096") {
		t.Fatalf("UseNumber did not preserve json.Number: %#v", numMap)
	}

	// 3. UseInt64 mode (native Sonic feature) must return int64 for representable ints and float64 for >MaxInt64.
	if APIKind == UseSonicJSON {
		cfgI64 := Config{UseInt64: true}.Froze()
		var gotI64 any
		if err := cfgI64.Unmarshal([]byte(payload), &gotI64); err != nil {
			t.Fatalf("UseInt64 Unmarshal failed: %v", err)
		}
		i64Map := gotI64.(map[string]any)
		if i64Map["zero"] != int64(0) || i64Map["one"] != int64(1) || i64Map["edgeLo"] != int64(4095) || i64Map["edgeHi"] != int64(4096) || i64Map["negOne"] != int64(-1) {
			t.Fatalf("UseInt64 integer mismatch: %#v", i64Map)
		}
		i64Arr := i64Map["arr"].([]any)
		if i64Arr[0] != int64(0) || i64Arr[1] != int64(7) || i64Arr[2] != int64(4095) || i64Arr[3] != int64(4096) {
			t.Fatalf("UseInt64 array integer mismatch: %#v", i64Arr)
		}
		if _, ok := i64Arr[6].(float64); !ok {
			t.Fatalf("expected MaxUint64 under UseInt64 to fall back to float64, got %T", i64Arr[6])
		}
	}
}

func TestSortedStringAnyMapNestedAndErrorCleanup(t *testing.T) {
	// Build nested map[string]any forcing KVBuf/KVOrder reallocation mid-recursion (outer 12 keys -> inner 120 keys).
	inner := make(map[string]any, 120)
	for i := 0; i < 120; i++ {
		inner[fmt.Sprintf("profile_field_%03d", 119-i)] = i
	}
	outer := map[string]any{
		"z_last":  "end",
		"a_first": map[string]any{"only_one": true},
		"m_empty": map[string]any{},
		"b_two":   map[string]any{"b": 2, "a": 1},
		"k_inner": inner,
	}
	for i := 0; i < 10; i++ {
		outer[fmt.Sprintf("p_prefix_%02d", 9-i)] = []any{i, fmt.Sprintf("v%d", i)}
	}

	want, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	got, err := ConfigStd.Marshal(outer)
	if err != nil {
		t.Fatalf("ConfigStd.Marshal failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ConfigStd.Marshal sorted map mismatch:\ngot:  %s\nwant: %s", got, want)
	}

	// Verify mid-iteration error inside nested sorted map cleanly unwinds without corrupting subsequent calls.
	bad := map[string]any{
		"a": map[string]any{"k1": "ok", "k2": math.NaN()},
		"b": "after",
	}
	if _, err := ConfigStd.Marshal(bad); err == nil {
		t.Fatalf("expected error for NaN inside sorted map[string]any")
	}
	got2, err := ConfigStd.Marshal(outer)
	if err != nil || !bytes.Equal(got2, want) {
		t.Fatalf("post-error ConfigStd.Marshal mismatch: err=%v", err)
	}
}

type namedStringSlice []string
type namedIntSlice []int
type namedInt64Slice []int64
type namedAnySlice []any
type namedByteSlice []byte
type namedFloat64Slice []float64
type namedZeroStructSlice []struct{}

type namedSliceHolder struct {
	SS   namedStringSlice     `json:"ss"`
	SSO  namedStringSlice     `json:"sso,omitempty"`
	IS   namedIntSlice        `json:"is"`
	ISO  namedIntSlice        `json:"iso,omitempty"`
	I64S namedInt64Slice      `json:"i64s"`
	AS   namedAnySlice        `json:"as"`
	BS   namedByteSlice       `json:"bs"`
	BSO  namedByteSlice       `json:"bso,omitempty"`
	FS   namedFloat64Slice    `json:"fs"`
	FSO  namedFloat64Slice    `json:"fso,omitempty"`
	ZS   namedZeroStructSlice `json:"zs"`
}

type rootPtrOmitNullDemo struct {
	S string `json:"s,omitempty"`
	P *int   `json:"p,omitempty"`
}

func TestNamedSliceExactTypesAndRootEncCache(t *testing.T) {
	samples := []namedSliceHolder{
		{},
		{
			SS:   namedStringSlice{},
			SSO:  namedStringSlice{},
			IS:   namedIntSlice{},
			ISO:  namedIntSlice{},
			I64S: namedInt64Slice{},
			AS:   namedAnySlice{},
			BS:   namedByteSlice{},
			BSO:  namedByteSlice{},
			FS:   namedFloat64Slice{},
			FSO:  namedFloat64Slice{},
			ZS:   namedZeroStructSlice{},
		},
		{
			SS:   namedStringSlice{"a", "b<c"},
			SSO:  namedStringSlice{"x"},
			IS:   namedIntSlice{1, 2, 3},
			ISO:  namedIntSlice{9},
			I64S: namedInt64Slice{-5, 0, 7},
			AS:   namedAnySlice{"ok", 12, true, nil},
			BS:   namedByteSlice("hello"),
			BSO:  namedByteSlice("world"),
			FS:   namedFloat64Slice{1.5, 2.5},
			FSO:  namedFloat64Slice{3.25},
			ZS:   make(namedZeroStructSlice, 2),
		},
	}

	for idx, h := range samples {
		for _, val := range []any{h, &h} {
			want, err := json.Marshal(val)
			if err != nil {
				t.Fatalf("case %d json.Marshal failed: %v", idx, err)
			}
			got, err := ConfigStd.Marshal(val)
			if err != nil {
				t.Fatalf("case %d ConfigStd.Marshal failed: %v", idx, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("case %d (%T) mismatch:\ngot:  %s\nwant: %s", idx, val, got, want)
			}
		}
	}

	// Nil root struct pointer must encode as "null".
	var nilPtr *namedSliceHolder
	if got, err := Marshal(nilPtr); err != nil || string(got) != "null" {
		t.Fatalf("Marshal(nilPtr) = %s, err=%v; want null", got, err)
	}

	if APIKind != UseSonicJSON {
		return
	}

	// Verify NoNullSliceOrMap on nil named slices inside struct and at top-level.
	cfgNoNull := Config{NoNullSliceOrMap: true}.Froze()
	if got, err := cfgNoNull.Marshal(namedStringSlice(nil)); err != nil || string(got) != "[]" {
		t.Fatalf("NoNullSliceOrMap Marshal(namedStringSlice(nil)) = %s, err=%v; want []", got, err)
	}
	if got, err := cfgNoNull.Marshal(namedByteSlice(nil)); err != nil || string(got) != "[]" {
		t.Fatalf("NoNullSliceOrMap Marshal(namedByteSlice(nil)) = %s, err=%v; want []", got, err)
	}
	if got, err := cfgNoNull.Marshal(namedFloat64Slice(nil)); err != nil || string(got) != "[]" {
		t.Fatalf("NoNullSliceOrMap Marshal(namedFloat64Slice(nil)) = %s, err=%v; want []", got, err)
	}

	// Verify rootEncCache honors Pretouch(*T, WithCompileEncOnlyOmitNull(true)) and ResetProgramCache().
	vars.ResetProgramCache()
	defer vars.ResetProgramCache()
	if err := Pretouch(reflect.TypeOf((*rootPtrOmitNullDemo)(nil)), option.WithCompileEncOnlyOmitNull(true)); err != nil {
		t.Fatalf("Pretouch(*rootPtrOmitNullDemo) failed: %v", err)
	}
	demo := &rootPtrOmitNullDemo{S: "", P: nil}
	got, err := Marshal(demo)
	if err != nil {
		t.Fatalf("Marshal(demo) failed: %v", err)
	}
	if string(got) != `{"s":""}` {
		t.Fatalf("rootEncCache did not honor Pretouch EncOnlyOmitNull: got %s, want {\"s\":\"\"}", got)
	}
}

type pureLeafSpecItem struct {
	Opt string `json:"opt,omitempty"`
	ID  int64  `json:"id"`
}

type pureNestedMeta struct {
	Code string `json:"code,omitempty"`
	Flag bool   `json:"flag"`
}

type pureChildSpecItem struct {
	Title string             `json:"title,omitempty"`
	Meta  pureNestedMeta     `json:"meta"`
	Leafs []pureLeafSpecItem `json:"leafs"`
	Empty []pureLeafSpecItem `json:"empty,omitempty"`
	Score float64            `json:"score,omitempty"`
}

type namedPureChildSlice []pureChildSpecItem

type ptrMarshalerLeaf struct {
	N int
}

func (m *ptrMarshalerLeaf) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"ptr:%d"`, m.N)), nil
}

type sliceElemWithPtrMarshaler struct {
	Name string           `json:"name"`
	Leaf ptrMarshalerLeaf `json:"leaf"`
}

type customMarshalerStructSlice []pureLeafSpecItem

func (s *customMarshalerStructSlice) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"custom_len:%d"`, len(*s))), nil
}

type pureStructSliceRoot struct {
	FirstOmit []pureChildSpecItem         `json:"first_omit,omitempty"`
	Named     namedPureChildSlice         `json:"named"`
	ZeroItems []struct{}                  `json:"zero_items"`
	AddrItems []sliceElemWithPtrMarshaler `json:"addr_items"`
	CustomPtr customMarshalerStructSlice  `json:"custom_ptr"`
	I64       int64                       `json:"i64"`
	I32       int32                       `json:"medium_int32_field_key"`
	I16       int16                       `json:"i16"`
	I8        int8                        `json:"i8"`
	U64       uint64                      `json:"u64"`
	U32       uint32                      `json:"medium_uint32_field_key"`
	U16       uint16                      `json:"u16"`
	U8        uint8                       `json:"u8"`
}

func TestPureStructSliceSpecAndFusedIntKeys(t *testing.T) {
	samples := []pureStructSliceRoot{
		{},
		{
			FirstOmit: []pureChildSpecItem{},
			Named:     namedPureChildSlice{},
			ZeroItems: make([]struct{}, 3),
			I64:       math.MinInt64,
			I32:       math.MinInt32,
			I16:       -999,
			I8:        -9,
			U64:       math.MaxUint64,
			U32:       math.MaxUint32,
			U16:       9999,
			U8:        99,
		},
		{
			FirstOmit: []pureChildSpecItem{
				{
					Meta:  pureNestedMeta{Flag: false},
					Leafs: []pureLeafSpecItem{{ID: 0}, {Opt: "a<b&c", ID: 42}},
				},
				{
					Title: "second \"item\"",
					Meta:  pureNestedMeta{Code: "X", Flag: true},
					Leafs: nil,
					Empty: []pureLeafSpecItem{{Opt: "keep", ID: -10000}},
					Score: 1.5,
				},
			},
			Named: namedPureChildSlice{
				{Title: "named", Meta: pureNestedMeta{Code: "Y", Flag: true}, Leafs: []pureLeafSpecItem{}},
			},
			ZeroItems: []struct{}{},
			AddrItems: []sliceElemWithPtrMarshaler{
				{Name: "elem0", Leaf: ptrMarshalerLeaf{N: 7}},
				{Name: "elem1", Leaf: ptrMarshalerLeaf{N: 8}},
			},
			CustomPtr: customMarshalerStructSlice{{ID: 1}, {ID: 2}},
			I64:       math.MaxInt64,
			I32:       123456,
			I16:       99,
			I8:        0,
			U64:       10000,
			U32:       999,
			U16:       9,
			U8:        0,
		},
	}

	for idx, s := range samples {
		// Test both value root (pv=false, yet slice elements remain addressable!) and pointer root (pv=true).
		for _, val := range []any{s, &s} {
			wantStd, err := json.Marshal(val)
			if err != nil {
				t.Fatalf("sample %d (%T) json.Marshal failed: %v", idx, val, err)
			}
			gotStd, err := ConfigStd.Marshal(val)
			if err != nil {
				t.Fatalf("sample %d (%T) ConfigStd.Marshal failed: %v", idx, val, err)
			}
			if !bytes.Equal(gotStd, wantStd) {
				t.Fatalf("sample %d (%T) ConfigStd mismatch:\ngot:  %s\nwant: %s", idx, val, gotStd, wantStd)
			}

			gotDef, err := ConfigDefault.Marshal(val)
			if err != nil {
				t.Fatalf("sample %d (%T) ConfigDefault.Marshal failed: %v", idx, val, err)
			}
			var roundTrip map[string]any
			if err := json.Unmarshal(gotDef, &roundTrip); err != nil {
				t.Fatalf("sample %d (%T) ConfigDefault invalid JSON: %v\n%s", idx, val, err, gotDef)
			}
		}
	}

	if APIKind != UseSonicJSON {
		return
	}

	// Verify NoNullSliceOrMap and EncodeNullForInfOrNan inside OP_slice_struct.
	cfgNoNull := Config{NoNullSliceOrMap: true, EncodeNullForInfOrNan: true}.Froze()
	nanSample := pureStructSliceRoot{
		FirstOmit: []pureChildSpecItem{{Title: "nan", Score: math.NaN()}},
	}
	if _, err := ConfigDefault.Marshal(&nanSample); err == nil {
		t.Fatalf("expected error on NaN inside OP_slice_struct under ConfigDefault")
	}
	gotNoNull, err := cfgNoNull.Marshal(&nanSample)
	if err != nil {
		t.Fatalf("cfgNoNull.Marshal(&nanSample) failed: %v", err)
	}
	if !bytes.Contains(gotNoNull, []byte(`"leafs":[]`)) || !bytes.Contains(gotNoNull, []byte(`"score":null`)) {
		t.Fatalf("unexpected NoNullSliceOrMap / EncodeNullForInfOrNan output: %s", gotNoNull)
	}
}
