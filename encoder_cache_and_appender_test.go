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

package sonic

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"

	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/internal/decoder/optdec"
	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/prim"
	encvars "github.com/bytedance/sonic/internal/encoder/vars"
	encvm "github.com/bytedance/sonic/internal/encoder/vm"
	"github.com/bytedance/sonic/internal/resolver"
	"github.com/bytedance/sonic/option"
)

type stage3PtrMarshalerA struct {
	V int
}

func (m *stage3PtrMarshalerA) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"ptrA:%d"`, m.V)), nil
}

type stage3PtrMarshalerB struct {
	V int
}

func (m *stage3PtrMarshalerB) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"ptrB:%d"`, m.V)), nil
}

type stage3OuterValueFirst struct {
	Child stage3PtrMarshalerA
}

type stage3OuterPtrFirst struct {
	Child stage3PtrMarshalerB
}

func TestEncoderPointerValueCacheSeparation(t *testing.T) {
	// Case 1: Compile non-addressable value (pv=false) first, then addressable pointer (pv=true).
	v1 := stage3OuterValueFirst{Child: stage3PtrMarshalerA{V: 42}}
	gotVal1, err := ConfigStd.Marshal(v1)
	if err != nil {
		t.Fatalf("Marshal(v1) error: %v", err)
	}
	wantVal1, _ := json.Marshal(v1)
	if !bytes.Equal(gotVal1, wantVal1) {
		t.Fatalf("Marshal(v1) = %s, want %s", gotVal1, wantVal1)
	}

	gotPtr1, err := ConfigStd.Marshal(&v1)
	if err != nil {
		t.Fatalf("Marshal(&v1) error: %v", err)
	}
	wantPtr1, _ := json.Marshal(&v1)
	if !bytes.Equal(gotPtr1, wantPtr1) {
		t.Fatalf("Marshal(&v1) = %s, want %s", gotPtr1, wantPtr1)
	}

	// Case 2: Compile addressable pointer (pv=true) first, then non-addressable value (pv=false).
	v2 := stage3OuterPtrFirst{Child: stage3PtrMarshalerB{V: 99}}
	gotPtr2, err := ConfigStd.Marshal(&v2)
	if err != nil {
		t.Fatalf("Marshal(&v2) error: %v", err)
	}
	wantPtr2, _ := json.Marshal(&v2)
	if !bytes.Equal(gotPtr2, wantPtr2) {
		t.Fatalf("Marshal(&v2) = %s, want %s", gotPtr2, wantPtr2)
	}

	gotVal2, err := ConfigStd.Marshal(v2)
	if err != nil {
		t.Fatalf("Marshal(v2) error: %v", err)
	}
	wantVal2, _ := json.Marshal(v2)
	if !bytes.Equal(gotVal2, wantVal2) {
		t.Fatalf("Marshal(v2) = %s, want %s", gotVal2, wantVal2)
	}
}

type stage3CustomJSON []byte

func (c stage3CustomJSON) MarshalJSON() ([]byte, error) {
	return []byte(c), nil
}

type stage3CustomText []byte

func (c stage3CustomText) MarshalText() ([]byte, error) {
	return []byte(c), nil
}

type stage3FragmentEnvelope struct {
	ID   int              `json:"id"`
	Raw  json.RawMessage  `json:"raw"`
	Cust stage3CustomJSON `json:"cust"`
	Text stage3CustomText `json:"text"`
}

func TestCustomFragmentSanitizeAndCompactParity(t *testing.T) {
	cases := []stage3FragmentEnvelope{
		{
			ID:   1,
			Raw:  json.RawMessage(" \n\t { \"a\" : [ 1 , true , \"x < y & z > w\" ] , \"esc\" : \"quo\\\"te\\\\end\" } \r\n "),
			Cust: stage3CustomJSON("  [ \"line\u2028sep\u2029ok\" , { \"nested\" : 12345 } ]  "),
			Text: stage3CustomText("hello <world> & \u2028\u2029"),
		},
		{
			ID:   2,
			Raw:  json.RawMessage("   123456   "),
			Cust: stage3CustomJSON("  \"scalar <tag> \\\"quoted\\\"\"  "),
			Text: stage3CustomText("plain_ascii_text_1234567890"),
		},
	}

	for i, tc := range cases {
		gotStd, err := ConfigStd.Marshal(tc)
		if err != nil {
			t.Fatalf("case %d ConfigStd.Marshal error: %v", i, err)
		}
		wantStd, err := json.Marshal(tc)
		if err != nil {
			t.Fatalf("case %d json.Marshal error: %v", i, err)
		}
		if !bytes.Equal(gotStd, wantStd) {
			t.Fatalf("case %d ConfigStd mismatch:\ngot:  %s\nwant: %s", i, gotStd, wantStd)
		}

		gotDef, err := ConfigDefault.Marshal(tc)
		if err != nil {
			t.Fatalf("case %d ConfigDefault.Marshal error: %v", i, err)
		}
		if !json.Valid(gotDef) {
			t.Fatalf("case %d ConfigDefault produced invalid JSON: %s", i, gotDef)
		}
	}

	// Invalid UTF-8 inside RawMessage/Marshaler/TextMarshaler:
	// ConfigStd (ValidateString=true) replaces each invalid UTF-8 byte with \ufffd.
	badUTF8 := stage3FragmentEnvelope{
		ID:   3,
		Raw:  json.RawMessage("{\"bad_utf8\":\"" + string([]byte{'a', 0xff, 0xfe, '<', 'b'}) + "\"}"),
		Cust: stage3CustomJSON("\"" + string([]byte{0xed, 0xa0, 0x80, '&'}) + "\""),
		Text: stage3CustomText([]byte{'t', 0x80, '>'}),
	}
	gotBadUTF8, err := ConfigStd.Marshal(badUTF8)
	if err != nil {
		t.Fatalf("badUTF8 ConfigStd.Marshal error: %v", err)
	}
	const wantBadUTF8 = `{"id":3,"raw":{"bad_utf8":"a\ufffd\ufffd\u003cb"},"cust":"\ufffd\ufffd\ufffd\u0026","text":"t\ufffd\u003e"}`
	if string(gotBadUTF8) != wantBadUTF8 {
		t.Fatalf("badUTF8 ConfigStd mismatch:\ngot:  %s\nwant: %s", gotBadUTF8, wantBadUTF8)
	}

	// With ValidateString=false, RawMessage/Marshaler preserve raw invalid UTF-8 bytes like encoding/json.
	noValidateStd := Config{EscapeHTML: true, SortMapKeys: true, CompactMarshaler: true}.Froze()
	badUTF8RawOnly := stage3FragmentEnvelope{
		ID:   4,
		Raw:  badUTF8.Raw,
		Cust: badUTF8.Cust,
		Text: stage3CustomText("ok>"),
	}
	gotNoVal, err := noValidateStd.Marshal(badUTF8RawOnly)
	if err != nil {
		t.Fatalf("noValidateStd.Marshal error: %v", err)
	}
	wantNoVal, _ := json.Marshal(badUTF8RawOnly)
	if !bytes.Equal(gotNoVal, wantNoVal) {
		t.Fatalf("noValidateStd mismatch:\ngot:  %q\nwant: %q", gotNoVal, wantNoVal)
	}

	// Invalid JSON in RawMessage must still fail under both ConfigDefault and ConfigStd.
	bad := stage3FragmentEnvelope{
		ID:   9,
		Raw:  json.RawMessage(`{"unclosed": [1, 2`),
		Cust: stage3CustomJSON(`{}`),
	}
	if _, err := ConfigDefault.Marshal(bad); err == nil {
		t.Fatal("expected error from ConfigDefault.Marshal on invalid RawMessage")
	}
	if _, err := ConfigStd.Marshal(bad); err == nil {
		t.Fatal("expected error from ConfigStd.Marshal on invalid RawMessage")
	}

	// NoQuoteTextMarshaler with HTML + UTF-8 sanitization.
	rawText := stage3CustomText(append([]byte(`"prequoted<`), 0xff, '"'))
	gotNoQuote, err := encoder.Encode(rawText, encoder.NoQuoteTextMarshaler|encoder.EscapeHTML|encoder.ValidateString)
	if err != nil {
		t.Fatalf("NoQuoteTextMarshaler error: %v", err)
	}
	if string(gotNoQuote) != `"prequoted\u003c\ufffd"` {
		t.Fatalf("NoQuoteTextMarshaler got %s", gotNoQuote)
	}
}

type countingWriter struct {
	buf    bytes.Buffer
	writes int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}

func TestStreamEncoderSingleWriteAndIndent(t *testing.T) {
	cw := &countingWriter{}
	enc := ConfigStd.NewEncoder(cw)
	item := map[string]int{"alpha": 1, "beta": 2}
	for i := 0; i < 5; i++ {
		if err := enc.Encode(item); err != nil {
			t.Fatalf("Encode error: %v", err)
		}
	}
	if cw.writes != 5 {
		t.Fatalf("expected 1 Write call per Encode (5 total), got %d", cw.writes)
	}

	var sonicBuf, stdBuf bytes.Buffer
	sEnc := ConfigStd.NewEncoder(&sonicBuf)
	sEnc.SetIndent(">", "  ")
	jEnc := json.NewEncoder(&stdBuf)
	jEnc.SetIndent(">", "  ")
	for i := 0; i < 3; i++ {
		if err := sEnc.Encode(item); err != nil {
			t.Fatalf("indented Encode error: %v", err)
		}
		_ = jEnc.Encode(item)
	}
	if !bytes.Equal(sonicBuf.Bytes(), stdBuf.Bytes()) {
		t.Fatalf("indented StreamEncoder mismatch:\ngot:\n%s\nwant:\n%s", sonicBuf.String(), stdBuf.String())
	}
}

func TestRawBufferOverlapSafety(t *testing.T) {
	backing := make([]byte, 0, 128)
	backing = append(backing, `  { "expr" : "a < b & c > d" }  `...)
	srcAlias := backing[:len(backing)]
	dstAlias := backing[:0] // shares exact same backing array and spare capacity!

	if err := encoder.EncodeInto(&dstAlias, json.RawMessage(srcAlias), encoder.EscapeHTML|encoder.CompactMarshaler|encoder.ValidateString); err != nil {
		t.Fatalf("EncodeInto overlapping RawMessage error: %v", err)
	}
	const want = `{"expr":"a \u003c b \u0026 c \u003e d"}`
	if string(dstAlias) != want {
		t.Fatalf("overlapping EncodeInto corrupted output: got %q, want %q", string(dstAlias), want)
	}

	// Non-compact overlapping EncodeInto with expansion.
	backing2 := make([]byte, 0, 128)
	backing2 = append(backing2, `{"k":"<>&"}`...)
	src2 := backing2[:len(backing2)]
	dst2 := backing2[:0]
	if err := encoder.EncodeInto(&dst2, json.RawMessage(src2), encoder.EscapeHTML|encoder.ValidateString); err != nil {
		t.Fatalf("EncodeInto non-compact overlapping error: %v", err)
	}
	if string(dst2) != `{"k":"\u003c\u003e\u0026"}` {
		t.Fatalf("non-compact overlapping EncodeInto corrupted output: got %q", string(dst2))
	}
}

var stage3DynamicTypesOnce sync.Once
var stage3DynamicVals []interface{}

func initStage3DynamicVals() {
	stage3DynamicTypesOnce.Do(func() {
		const nTypes = 64
		stage3DynamicVals = make([]interface{}, nTypes)
		for i := 0; i < nTypes; i++ {
			fields := make([]reflect.StructField, 8)
			for f := 0; f < 8; f++ {
				fields[f] = reflect.StructField{
					Name: fmt.Sprintf("F_%d_%d", i, f),
					Type: reflect.TypeOf(int64(0)),
					Tag:  reflect.StructTag(fmt.Sprintf(`json:"field_%d_%d"`, i, f)),
				}
			}
			st := reflect.StructOf(fields)
			stage3DynamicVals[i] = reflect.New(st).Interface()
		}
	})
}

func TestSinglePassCompactedJSONParity(t *testing.T) {
	valids := []string{
		" \n\t { \n \"a\" : [ 1 , -2.5e+3 , true , false , null , \"<>&\\u2028\\u2029\" , \"\\u0041\\n\\t\\\"\\\\\\/\" ] , \"empty_arr\" : [ \n ] , \"empty_obj\" : { \t } } \r\n ",
		`[ { "k1" : "v1" } , { "k2" : [ true , false , null , 0 , 1234567890 ] } ]`,
		`"standalone <string> & value"`,
		`12345.6789e-10`,
		`true`,
		`false`,
		`null`,
	}
	for _, raw := range valids {
		w := stage3FragmentEnvelope{ID: 1, Raw: json.RawMessage(raw), Cust: stage3CustomJSON(raw), Text: stage3CustomText("t")}
		gotStd, errStd := ConfigStd.Marshal(w)
		wantStd, errWant := json.Marshal(w)
		if (errStd != nil) != (errWant != nil) || !bytes.Equal(gotStd, wantStd) {
			t.Fatalf("E03b ConfigStd parity mismatch for %q:\ngot  (%v): %s\nwant (%v): %s", raw, errStd, gotStd, errWant, wantStd)
		}

		var dst []byte
		dst = append(dst, "prefix:"...)
		dst, err := alg.AppendCompactedJSON(dst, []byte(raw), 0)
		if err != nil {
			t.Fatalf("E03b AppendCompactedJSON error on valid %q: %v", raw, err)
		}
		var stdBuf bytes.Buffer
		stdBuf.WriteString("prefix:")
		if err := json.Compact(&stdBuf, []byte(raw)); err != nil {
			t.Fatalf("stdlib Compact failed on %q: %v", raw, err)
		}
		if !bytes.Equal(dst, stdBuf.Bytes()) {
			t.Fatalf("E03b AppendCompactedJSON mismatch for %q:\ngot:  %s\nwant: %s", raw, dst, stdBuf.Bytes())
		}
	}

	invalids := []string{
		``,
		`   `,
		`{"a":1} {"b":2}`,
		`{"a":1,}`,
		`{"a":}`,
		`{"a" 1}`,
		`[1, 2,]`,
		`{"a": "\x20"}`,
		`{"a": "\u123g"}`,
		"{\"a\": \"raw\x01ctrl\"}",
		`{"a": 01}`,
		`{"a": 1.}`,
		`{"a": 1e}`,
		`{"a": tru}`,
		`{"a": [1, 2}`,
	}
	for _, bad := range invalids {
		dst := make([]byte, 6, 128)
		copy(dst, "keepme")
		got, err := alg.AppendCompactedJSON(dst, []byte(bad), (1<<alg.BitEscapeHTML)|(1<<alg.BitValidateString))
		if err == nil {
			t.Fatalf("E03b AppendCompactedJSON expected error for %q, got nil", bad)
		}
		if string(got) != "keepme" {
			t.Fatalf("E03b AppendCompactedJSON mutated dst on error for %q: %q", bad, got)
		}
	}

	// [P2] Overlapping dst := src[:0] with EncodeInto + EscapeHTML / CompactMarshaler / TextMarshaler.
	{
		src := make([]byte, 3, 64)
		copy(src, `"<"`)
		dst := src[:0]
		if err := encoder.EncodeInto(&dst, json.RawMessage(src), encoder.EscapeHTML); err != nil {
			t.Fatalf("overlapping EncodeInto RawMessage failed: %v", err)
		}
		if string(dst) != `"\u003c"` {
			t.Fatalf("overlapping EncodeInto RawMessage corrupted output: got %q, want %q", dst, `"\u003c"`)
		}
	}
	{
		src := make([]byte, 7, 64)
		copy(src, ` { "a" `)
		// make valid spaced raw JSON in src
		raw := ` { "a" : "<" } `
		buf := make([]byte, len(raw), 64)
		copy(buf, raw)
		dst := buf[:0]
		if err := encoder.EncodeInto(&dst, json.RawMessage(buf), encoder.EscapeHTML|encoder.CompactMarshaler); err != nil {
			t.Fatalf("overlapping compacted EncodeInto RawMessage failed: %v", err)
		}
		if string(dst) != `{"a":"\u003c"}` {
			t.Fatalf("overlapping compacted EncodeInto RawMessage mismatch: got %q", dst)
		}
	}
	{
		buf := make([]byte, 5, 64)
		copy(buf, `a<b>c`)
		dst := buf[:0]
		if err := encoder.EncodeInto(&dst, stage3CustomText(buf), encoder.EscapeHTML); err != nil {
			t.Fatalf("overlapping EncodeInto TextMarshaler failed: %v", err)
		}
		if string(dst) != `"a\u003cb\u003ec"` {
			t.Fatalf("overlapping EncodeInto TextMarshaler mismatch: got %q", dst)
		}
	}
}

func TestBatchPretouchDeduplicationAndFailurePolicy(t *testing.T) {
	initStage3DynamicVals()
	types32 := make([]reflect.Type, 32)
	for i := 0; i < 32; i++ {
		types32[i] = reflect.TypeOf(stage3DynamicVals[i]).Elem()
	}
	dups := append([]reflect.Type{}, types32...)
	dups = append(dups, types32[:16]...)

	encvars.ResetProgramCache()
	optdec.ResetProgramCache()
	if err := PretouchMany(dups, option.WithCompileRecursiveDepth(2)); err != nil {
		t.Fatalf("PretouchMany failed: %v", err)
	}
	for i := 0; i < 32; i++ {
		if encvars.GetProgram(types32[i], false) == nil {
			t.Fatalf("expected type %d in encoder cache after PretouchMany", i)
		}
	}
	out, err := Marshal(stage3DynamicVals[7])
	if err != nil || len(out) == 0 {
		t.Fatalf("warm Marshal after PretouchMany failed: %v", err)
	}
	dst := reflect.New(types32[7]).Interface()
	if err := Unmarshal(out, dst); err != nil {
		t.Fatalf("warm Unmarshal after PretouchMany failed: %v", err)
	}

	type badMapHolder struct {
		Bad map[struct{ K int }]int
	}
	type badParent struct {
		OK   int
		Next *badMapHolder
	}
	encvars.ResetProgramCache()
	if err := encoder.Pretouch(types32[1]); err != nil {
		t.Fatal(err)
	}
	if err := encoder.PretouchMany([]reflect.Type{types32[0], reflect.TypeOf(badParent{})}, option.WithCompileRecursiveDepth(2)); err == nil {
		t.Fatal("expected error for unsupported nested map key in PretouchMany")
	}
	if encvars.GetProgram(types32[1], false) == nil {
		t.Fatal("expected pre-existing warm cache entry to remain intact")
	}
	if encvars.GetProgram(types32[0], false) != nil {
		t.Fatal("expected failed batch not to partially mutate cache snapshot")
	}
}

func TestEncoderKeyBlobAndSlabContracts(t *testing.T) {
	type NestedChild struct {
		ChildA int64  `json:"child_a"`
		ChildB string `json:"child_b"`
	}
	type SampleStruct struct {
		ID      int64       `json:"id"`
		Name    string      `json:"name"`
		EscKey  string      `json:"a<b&c>d"`
		Nested  NestedChild `json:"nested"`
		OmitVal int         `json:"omit_val,omitempty"`
	}

	resolver.ResetFieldCacheForTest()
	fms := resolver.ResolveStruct(reflect.TypeOf(SampleStruct{}))
	if len(fms) != 5 {
		t.Fatalf("expected 5 fields, got %d", len(fms))
	}

	got, err := ConfigStd.Marshal(SampleStruct{
		ID:     42,
		Name:   "ok",
		EscKey: "v",
		Nested: NestedChild{ChildA: 7, ChildB: "sub"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":42,"name":"ok","a\u003cb\u0026c\u003ed":"v","nested":{"child_a":7,"child_b":"sub"}}`
	if string(got) != want {
		t.Fatalf("SampleStruct ConfigStd.Marshal got %s, want %s", got, want)
	}
}

type stage3AppenderRecord struct {
	ID   int64
	Code string
	Expr string
	Fail bool
	Raw  string
}

func (r stage3AppenderRecord) AppendSonicJSON(dst []byte) ([]byte, error) {
	if r.Fail {
		dst = append(dst, `{"partial":`...)
		return dst, errors.New("intentional AppendJSON error")
	}
	if r.Raw != "" {
		dst = append(dst, r.Raw...)
		return dst, nil
	}
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, r.ID, 10)
	dst = append(dst, `,"code":"`...)
	dst = append(dst, r.Code...)
	dst = append(dst, `","expr":"`...)
	dst = append(dst, r.Expr...)
	dst = append(dst, `"}`...)
	return dst, nil
}

func (r stage3AppenderRecord) MarshalJSON() ([]byte, error) {
	return r.AppendSonicJSON(nil)
}

func TestJSONAppenderContracts(t *testing.T) {
	type Envelope struct {
		Prefix string                 `json:"prefix"`
		Items  []stage3AppenderRecord `json:"items"`
		Ptr    *stage3AppenderRecord  `json:"ptr"`
		Suffix int                    `json:"suffix"`
	}

	env := Envelope{
		Prefix: "head",
		Items: []stage3AppenderRecord{
			{ID: 1, Code: "A01", Expr: "a < b & c > d"},
			{ID: 2, Code: "B02", Expr: "normal_ascii"},
			{ID: 3, Code: "C03", Expr: "   ", Raw: " \n { \"spaced\" : [ 1 , 2 , \"x < y\" ] } \t "},
		},
		Ptr:    &stage3AppenderRecord{ID: 4, Code: "D04", Expr: "line\u2028sep"},
		Suffix: 99,
	}

	gotStd, err := ConfigStd.Marshal(env)
	if err != nil {
		t.Fatalf("ConfigStd.Marshal(env) error: %v", err)
	}
	wantStd, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error: %v", err)
	}
	if !bytes.Equal(gotStd, wantStd) {
		t.Fatalf("ConfigStd parity mismatch:\ngot:  %s\nwant: %s", gotStd, wantStd)
	}

	gotDef, err := ConfigDefault.Marshal(env)
	if err != nil {
		t.Fatalf("ConfigDefault.Marshal(env) error: %v", err)
	}
	if !json.Valid(gotDef) {
		t.Fatalf("ConfigDefault produced invalid JSON: %s", gotDef)
	}

	// Error rollback safety: partial writes into parent buffer must not leak on error.
	badEnv := Envelope{
		Prefix: "head",
		Items:  []stage3AppenderRecord{{ID: 1, Fail: true}},
	}
	if _, err := ConfigDefault.Marshal(badEnv); err == nil {
		t.Fatal("expected error from failing AppendJSON")
	}
	badRaw := Envelope{
		Prefix: "head",
		Items:  []stage3AppenderRecord{{ID: 1, Raw: `{"unclosed": [1, 2`}},
	}
	if _, err := ConfigDefault.Marshal(badRaw); err == nil {
		t.Fatal("expected validation error from malformed AppendJSON output")
	}
	if _, err := ConfigStd.Marshal(badRaw); err == nil {
		t.Fatal("expected compaction error from malformed AppendJSON output")
	}
}

func TestReview_PretouchMergedAddressabilityPlans(t *testing.T) {
	type Child struct {
		X int `json:"x,omitempty"`
	}
	type Root struct {
		Items [1]Child
	}

	const wantOmitNull = `{"Items":[{"x":0}]}`
	const wantDefault = `{"Items":[{}]}`
	cfgOmitNull := Config{OnlyOmitNull: true}.Froze()
	for iter := 0; iter < 20; iter++ {
		encvars.ResetProgramCache()
		optdec.ResetProgramCache()
		// Pretouch with WithCompileEncOnlyOmitNull(true)
		// prewarms the omitNull=true caches without implicitly flipping default Marshal.
		if err := Pretouch(
			reflect.TypeOf(Root{}),
			option.WithCompileMaxInlineDepth(2),
			option.WithCompileRecursiveDepth(1),
			option.WithCompileEncOnlyOmitNull(true),
		); err != nil {
			t.Fatalf("iter %d Pretouch error: %v", iter, err)
		}
		if encvars.GetProgramMode(reflect.TypeOf(Child{}), false, true) == nil {
			t.Fatalf("iter %d missing (Child, pv=false, omitNull=true) program after Pretouch", iter)
		}
		if encvars.GetProgramMode(reflect.TypeOf(Child{}), true, true) == nil {
			t.Fatalf("iter %d missing (Child, pv=true, omitNull=true) program after Pretouch", iter)
		}

		defVal, err := Marshal(Root{})
		if err != nil || string(defVal) != wantDefault {
			t.Fatalf("iter %d default Marshal(Root{}) = (%s, %v), want %s", iter, defVal, err, wantDefault)
		}

		gotVal, err := cfgOmitNull.Marshal(Root{})
		if err != nil {
			t.Fatalf("iter %d cfgOmitNull.Marshal(Root{}) error: %v", iter, err)
		}
		if string(gotVal) != wantOmitNull {
			t.Fatalf("iter %d cfgOmitNull.Marshal(Root{}) = %s, want %s", iter, gotVal, wantOmitNull)
		}

		gotPtr, err := cfgOmitNull.Marshal(&Root{})
		if err != nil {
			t.Fatalf("iter %d cfgOmitNull.Marshal(&Root{}) error: %v", iter, err)
		}
		if string(gotPtr) != wantOmitNull {
			t.Fatalf("iter %d cfgOmitNull.Marshal(&Root{}) = %s, want %s", iter, gotPtr, wantOmitNull)
		}
	}

	// Single Root containing both non-addressable [1]Child and addressable *Child at max depth.
	type MixedRoot struct {
		Arr [1]Child
		Ptr *Child
	}
	for iter := 0; iter < 10; iter++ {
		encvars.ResetProgramCache()
		if err := encoder.Pretouch(
			reflect.TypeOf(MixedRoot{}),
			option.WithCompileMaxInlineDepth(2),
			option.WithCompileRecursiveDepth(1),
			option.WithCompileEncOnlyOmitNull(true),
		); err != nil {
			t.Fatalf("iter %d MixedRoot Pretouch error: %v", iter, err)
		}
		if encvars.GetProgramMode(reflect.TypeOf(Child{}), false, true) == nil || encvars.GetProgramMode(reflect.TypeOf(Child{}), true, true) == nil {
			t.Fatalf("iter %d MixedRoot missing one of (Child, pv=false/true, omitNull=true) plans", iter)
		}
		if encvars.GetProgramMode(reflect.TypeOf(Child{}), false, false) != nil || encvars.GetProgramMode(reflect.TypeOf(Child{}), true, false) != nil {
			t.Fatalf("iter %d MixedRoot EncOnlyOmitNull Pretouch polluted default omitNull=false cache for shared Child", iter)
		}
	}
}

func TestOnlyOmitNullExplicitConfigAndNoPretouchCrossPollution(t *testing.T) {
	type sharedChild struct {
		ZeroInt int    `json:"zero_int,omitempty"`
		NilPtr  *int   `json:"nil_ptr,omitempty"`
		Tag     string `json:"tag"`
	}
	type serviceARoot struct {
		Child sharedChild `json:"child"`
	}
	type serviceBRoot struct {
		Child sharedChild `json:"child"`
	}

	encvars.ResetProgramCache()
	defer encvars.ResetProgramCache()

	// 1. Calling default Marshal first must NOT prevent explicit OnlyOmitNull config from omitting only nil.
	defOut, err := ConfigDefault.Marshal(serviceARoot{Child: sharedChild{Tag: "ok"}})
	if err != nil || string(defOut) != `{"child":{"tag":"ok"}}` {
		t.Fatalf("default Marshal got (%s, %v)", defOut, err)
	}

	cfgOmitNull := Config{OnlyOmitNull: true}.Froze()
	omitNullOut, err := cfgOmitNull.Marshal(serviceARoot{Child: sharedChild{Tag: "ok"}})
	if err != nil || string(omitNullOut) != `{"child":{"zero_int":0,"tag":"ok"}}` {
		t.Fatalf("Config{OnlyOmitNull:true} Marshal got (%s, %v)", omitNullOut, err)
	}

	// Default Marshal after OnlyOmitNull must remain unchanged.
	defOutAfter, err := ConfigDefault.Marshal(serviceARoot{Child: sharedChild{Tag: "ok"}})
	if err != nil || string(defOutAfter) != `{"child":{"tag":"ok"}}` {
		t.Fatalf("default Marshal after OnlyOmitNull got (%s, %v)", defOutAfter, err)
	}

	// 2. Pretouch(..., WithCompileEncOnlyOmitNull(true))
	// warms ONLY the omitNull=true cache and never alters ConfigDefault.Marshal on
	// serviceARoot, serviceBRoot, or sharedChild; explicit cfgOmitNull selects OnlyOmitNull.
	encvars.ResetProgramCache()
	if err := encoder.Pretouch(
		reflect.TypeOf(serviceARoot{}),
		option.WithCompileMaxInlineDepth(1),
		option.WithCompileRecursiveDepth(2),
		option.WithCompileEncOnlyOmitNull(true),
	); err != nil {
		t.Fatal(err)
	}
	bOut, err := ConfigDefault.Marshal(serviceBRoot{Child: sharedChild{Tag: "b"}})
	if err != nil || string(bOut) != `{"child":{"tag":"b"}}` {
		t.Fatalf("Pretouch on serviceARoot polluted serviceBRoot: got (%s, %v)", bOut, err)
	}
	cOut, err := ConfigDefault.Marshal(sharedChild{Tag: "c"})
	if err != nil || string(cOut) != `{"tag":"c"}` {
		t.Fatalf("Pretouch on serviceARoot polluted sharedChild: got (%s, %v)", cOut, err)
	}
	aDefaultOut, err := ConfigDefault.Marshal(serviceARoot{Child: sharedChild{Tag: "a"}})
	if err != nil || string(aDefaultOut) != `{"child":{"tag":"a"}}` {
		t.Fatalf("Pretouch on serviceARoot must not flip default Marshal: got (%s, %v)", aDefaultOut, err)
	}
	aExplicitOut, err := cfgOmitNull.Marshal(serviceARoot{Child: sharedChild{Tag: "a"}})
	if err != nil || string(aExplicitOut) != `{"child":{"zero_int":0,"tag":"a"}}` {
		t.Fatalf("explicit cfgOmitNull on serviceARoot mismatch: got (%s, %v)", aExplicitOut, err)
	}
}

type omitNullReentrantCarrier struct {
	Mode string
}

type omitNullInnerPayload struct {
	Zero int  `json:"zero,omitempty"`
	Ptr  *int `json:"ptr,omitempty"`
}

func (c omitNullReentrantCarrier) MarshalJSON() ([]byte, error) {
	inner := omitNullInnerPayload{}
	if c.Mode == "explicit_omit_null" {
		return Config{OnlyOmitNull: true}.Froze().Marshal(inner)
	}
	return ConfigDefault.Marshal(inner)
}

func TestOnlyOmitNullExplicitContractMatrix(t *testing.T) {
	type recNode struct {
		N    int      `json:"n,omitempty"`
		P    *int     `json:"p,omitempty"`
		Next *recNode `json:"next,omitempty"`
	}
	type itemD1 struct {
		N int  `json:"n,omitempty"`
		P *int `json:"p,omitempty"`
	}
	type envD1 struct {
		Direct  itemD1            `json:"direct"`
		Ptr     *itemD1           `json:"ptr"`
		Dynamic any               `json:"dynamic"`
		AnyList []any             `json:"any_list"`
		MapVal  map[string]itemD1 `json:"map_val"`
		Rec     *recNode          `json:"rec"`
	}
	type itemD2 struct {
		N int  `json:"n,omitempty"`
		P *int `json:"p,omitempty"`
	}
	type envD2 struct {
		Direct  itemD2            `json:"direct"`
		Ptr     *itemD2           `json:"ptr"`
		Dynamic any               `json:"dynamic"`
		AnyList []any             `json:"any_list"`
		MapVal  map[string]itemD2 `json:"map_val"`
		Rec     *recNode          `json:"rec"`
	}
	type itemD16 struct {
		N int  `json:"n,omitempty"`
		P *int `json:"p,omitempty"`
	}
	type envD16 struct {
		Direct  itemD16            `json:"direct"`
		Ptr     *itemD16           `json:"ptr"`
		Dynamic any                `json:"dynamic"`
		AnyList []any              `json:"any_list"`
		MapVal  map[string]itemD16 `json:"map_val"`
		Rec     *recNode           `json:"rec"`
	}

	const wantDefault = `{"direct":{},"ptr":{},"dynamic":{},"any_list":[{},{}],"map_val":{"k":{}},"rec":{"next":{}}}`
	const wantOmitNull = `{"direct":{"n":0},"ptr":{"n":0},"dynamic":{"n":0},"any_list":[{"n":0},{"n":0}],"map_val":{"k":{"n":0}},"rec":{"n":0,"next":{"n":0}}}`

	cfgOmit := Config{OnlyOmitNull: true}.Froze()

	checkAllSurfaces := func(label string, val any, ptrVal any) {
		t.Helper()
		for _, target := range []struct {
			tag string
			v   any
		}{
			{"T", val},
			{"*T", ptrVal},
		} {
			for _, preset := range []struct {
				name string
				api  API
				want string
			}{
				{"ConfigDefault", ConfigDefault, wantDefault},
				{"ConfigStd", ConfigStd, wantDefault},
				{"ConfigOnlyOmitNull", cfgOmit, wantOmitNull},
			} {
				out, err := preset.api.Marshal(target.v)
				if err != nil || string(out) != preset.want {
					t.Fatalf("%s/%s/%s Marshal = (%s, %v), want %s", label, target.tag, preset.name, out, err, preset.want)
				}
				var streamBuf bytes.Buffer
				if err := preset.api.NewEncoder(&streamBuf).Encode(target.v); err != nil {
					t.Fatalf("%s/%s/%s Stream Encode error: %v", label, target.tag, preset.name, err)
				}
				if got := bytes.TrimRight(streamBuf.Bytes(), "\n"); string(got) != preset.want {
					t.Fatalf("%s/%s/%s Stream = %s, want %s", label, target.tag, preset.name, got, preset.want)
				}
				indOut, err := preset.api.MarshalIndent(target.v, "", "  ")
				if err != nil {
					t.Fatalf("%s/%s/%s MarshalIndent error: %v", label, target.tag, preset.name, err)
				}
				var compactBuf bytes.Buffer
				if err := json.Compact(&compactBuf, indOut); err != nil || compactBuf.String() != preset.want {
					t.Fatalf("%s/%s/%s MarshalIndent compacted = (%s, %v), want %s", label, target.tag, preset.name, compactBuf.String(), err, preset.want)
				}
			}
			// Also verify encoder.EncodeInto preserves prefix and respects explicit flag.
			intoDef := []byte("PFX:")
			if err := encoder.EncodeInto(&intoDef, target.v, 0); err != nil || string(intoDef) != "PFX:"+wantDefault {
				t.Fatalf("%s/%s EncodeInto(0) = (%s, %v)", label, target.tag, intoDef, err)
			}
			intoOmit := []byte("PFX:")
			if err := encoder.EncodeInto(&intoOmit, target.v, encoder.OnlyOmitNull); err != nil || string(intoOmit) != "PFX:"+wantOmitNull {
				t.Fatalf("%s/%s EncodeInto(OnlyOmitNull) = (%s, %v)", label, target.tag, intoOmit, err)
			}
		}
	}

	// Test both Pretouch warmup orders across isolated types & legal inline depths 1, 2, and 16.
	for _, order := range []string{"omitNull_first", "default_first"} {
		encvars.ResetProgramCache()
		v1 := envD1{
			Ptr: &itemD1{}, Dynamic: itemD1{}, AnyList: []any{itemD1{}, &itemD1{}},
			MapVal: map[string]itemD1{"k": {}}, Rec: &recNode{Next: &recNode{}},
		}
		if order == "omitNull_first" {
			_ = encoder.Pretouch(reflect.TypeOf(itemD1{}), option.WithCompileMaxInlineDepth(1), option.WithCompileEncOnlyOmitNull(true))
			_ = encoder.Pretouch(reflect.TypeOf(envD1{}), option.WithCompileMaxInlineDepth(1), option.WithCompileEncOnlyOmitNull(true))
			_ = encoder.Pretouch(reflect.TypeOf(envD1{}), option.WithCompileMaxInlineDepth(1), option.WithCompileEncOnlyOmitNull(false))
		} else {
			_ = encoder.Pretouch(reflect.TypeOf(itemD1{}), option.WithCompileMaxInlineDepth(1), option.WithCompileEncOnlyOmitNull(false))
			_ = encoder.Pretouch(reflect.TypeOf(envD1{}), option.WithCompileMaxInlineDepth(1), option.WithCompileEncOnlyOmitNull(false))
			_ = encoder.Pretouch(reflect.TypeOf(itemD1{}), option.WithCompileMaxInlineDepth(1), option.WithCompileEncOnlyOmitNull(true))
		}
		checkAllSurfaces("depth1/"+order, v1, &v1)

		encvars.ResetProgramCache()
		v2 := envD2{
			Ptr: &itemD2{}, Dynamic: itemD2{}, AnyList: []any{itemD2{}, &itemD2{}},
			MapVal: map[string]itemD2{"k": {}}, Rec: &recNode{Next: &recNode{}},
		}
		_ = encoder.Pretouch(reflect.TypeOf(itemD2{}), option.WithCompileMaxInlineDepth(2), option.WithCompileEncOnlyOmitNull(true))
		_ = encoder.Pretouch(reflect.TypeOf(envD2{}), option.WithCompileMaxInlineDepth(2), option.WithCompileEncOnlyOmitNull(order == "omitNull_first"))
		checkAllSurfaces("depth2/"+order, v2, &v2)

		encvars.ResetProgramCache()
		v16 := envD16{
			Ptr: &itemD16{}, Dynamic: itemD16{}, AnyList: []any{itemD16{}, &itemD16{}},
			MapVal: map[string]itemD16{"k": {}}, Rec: &recNode{Next: &recNode{}},
		}
		_ = encoder.Pretouch(reflect.TypeOf(itemD16{}), option.WithCompileMaxInlineDepth(16), option.WithCompileEncOnlyOmitNull(true))
		_ = encoder.Pretouch(reflect.TypeOf(envD16{}), option.WithCompileMaxInlineDepth(16), option.WithCompileEncOnlyOmitNull(order == "omitNull_first"))
		checkAllSurfaces("depth16/"+order, v16, &v16)
	}

	// Verify re-entrant Marshaler callbacks use their own explicit configuration,
	// never inheriting outer OnlyOmitNull or default state implicitly.
	type callbackEnvelope struct {
		OuterZero int                      `json:"outer_zero,omitempty"`
		DefInner  omitNullReentrantCarrier `json:"def_inner"`
		OmitInner omitNullReentrantCarrier `json:"omit_inner"`
	}
	cbVal := callbackEnvelope{
		DefInner:  omitNullReentrantCarrier{Mode: "default"},
		OmitInner: omitNullReentrantCarrier{Mode: "explicit_omit_null"},
	}
	outDefOuter, err := ConfigDefault.Marshal(cbVal)
	if err != nil || string(outDefOuter) != `{"def_inner":{},"omit_inner":{"zero":0}}` {
		t.Fatalf("reentrant callback under default outer mismatch: (%s, %v)", outDefOuter, err)
	}
	outOmitOuter, err := cfgOmit.Marshal(cbVal)
	if err != nil || string(outOmitOuter) != `{"outer_zero":0,"def_inner":{},"omit_inner":{"zero":0}}` {
		t.Fatalf("reentrant callback under OnlyOmitNull outer mismatch: (%s, %v)", outOmitOuter, err)
	}
}

type reviewCountingAppender struct {
	ID          int
	appendCalls *atomic.Int64
	jsonCalls   *atomic.Int64
	textCalls   *atomic.Int64
}

func (c reviewCountingAppender) AppendSonicJSON(dst []byte) ([]byte, error) {
	if c.appendCalls != nil {
		c.appendCalls.Add(1)
	}
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, int64(c.ID), 10)
	dst = append(dst, `,"mode":"clean_ascii"}`...)
	return dst, nil
}

func (c reviewCountingAppender) MarshalJSON() ([]byte, error) {
	if c.jsonCalls != nil {
		c.jsonCalls.Add(1)
	}
	if c.appendCalls != nil {
		// Do not double-count AppendSonicJSON when MarshalJSON delegates internally.
		dst := make([]byte, 0, 32)
		dst = append(dst, `{"id":`...)
		dst = strconv.AppendInt(dst, int64(c.ID), 10)
		dst = append(dst, `,"mode":"clean_ascii"}`...)
		return dst, nil
	}
	return c.AppendSonicJSON(nil)
}

func (c reviewCountingAppender) MarshalText() ([]byte, error) {
	if c.textCalls != nil {
		c.textCalls.Add(1)
	}
	return []byte("text_fallback"), nil
}

func TestReview_JSONAppenderDispatchPriorityAndZeroAlloc(t *testing.T) {
	var appendCalls, jsonCalls, textCalls atomic.Int64
	makeItem := func(id int) reviewCountingAppender {
		return reviewCountingAppender{
			ID:          id,
			appendCalls: &appendCalls,
			jsonCalls:   &jsonCalls,
			textCalls:   &textCalls,
		}
	}

	type DispatchContainer struct {
		Concrete    reviewCountingAppender   `json:"concrete"`
		Ptr         *reviewCountingAppender  `json:"ptr"`
		Slice       []reviewCountingAppender `json:"slice"`
		AnyVal      any                      `json:"any_val"`
		AppenderIfc prim.JSONAppender        `json:"appender_ifc"`
		JSONIfc     json.Marshaler           `json:"json_ifc"`
		TextIfc     encoding.TextMarshaler   `json:"text_ifc"`
	}

	ptrItem := makeItem(2)
	c := DispatchContainer{
		Concrete:    makeItem(1),
		Ptr:         &ptrItem,
		Slice:       []reviewCountingAppender{makeItem(3)},
		AnyVal:      makeItem(4),
		AppenderIfc: makeItem(5),
		JSONIfc:     makeItem(6),
		TextIfc:     makeItem(7),
	}

	got, err := ConfigStd.Marshal(c)
	if err != nil {
		t.Fatalf("ConfigStd.Marshal(DispatchContainer) error: %v", err)
	}
	if appendCalls.Load() != 6 {
		t.Fatalf("expected strictly 6 AppendSonicJSON calls (including under -race), got %d (MarshalJSON=%d, MarshalText=%d)",
			appendCalls.Load(), jsonCalls.Load(), textCalls.Load())
	}
	if jsonCalls.Load() != 0 || textCalls.Load() != 1 {
		t.Fatalf("expected MarshalJSON=0 and MarshalText=1 (for static encoding.TextMarshaler field), got MarshalJSON=%d, MarshalText=%d",
			jsonCalls.Load(), textCalls.Load())
	}

	const want = `{"concrete":{"id":1,"mode":"clean_ascii"},"ptr":{"id":2,"mode":"clean_ascii"},"slice":[{"id":3,"mode":"clean_ascii"}],"any_val":{"id":4,"mode":"clean_ascii"},"appender_ifc":{"id":5,"mode":"clean_ascii"},"json_ifc":{"id":6,"mode":"clean_ascii"},"text_ifc":"text_fallback"}`
	if string(got) != want {
		t.Fatalf("DispatchContainer parity mismatch:\ngot:  %s\nwant: %s", got, want)
	}

	// Verify 0 allocations when encoding into preallocated buffer under both default and ConfigStd options.
	type SingleHolder struct {
		Item reviewCountingAppender `json:"item"`
	}
	holder := SingleHolder{Item: reviewCountingAppender{ID: 77}}
	buf := make([]byte, 0, 256)
	stdOpts := encoder.EscapeHTML | encoder.CompactMarshaler | encoder.ValidateString
	for _, opts := range []encoder.Options{0, stdOpts} {
		if err := encoder.EncodeInto(&buf, &holder, opts); err != nil {
			t.Fatalf("warmup EncodeInto(opts=%v) error: %v", opts, err)
		}
		allocs := testing.AllocsPerRun(100, func() {
			buf = buf[:0]
			if err := encoder.EncodeInto(&buf, &holder, opts); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 && !hasCheckptrInstrumentation() {
			t.Fatalf("expected 0 allocs for clean compact JSONAppender with opts=%v, got %v", opts, allocs)
		}
	}
}

func hasCheckptrInstrumentation() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-gcflags" && strings.Contains(s.Value, "checkptr") {
			return true
		}
	}
	return false
}

func TestReview_ConcurrentPretouchAndFirstRequestInterleaving(t *testing.T) {
	initStage3DynamicVals()
	types := make([]reflect.Type, 32)
	for i := 0; i < 32; i++ {
		types[i] = reflect.TypeOf(stage3DynamicVals[i]).Elem()
	}

	for round := 0; round < 5; round++ {
		encvars.ResetProgramCache()
		optdec.ResetProgramCache()

		var wg sync.WaitGroup
		errCh := make(chan error, 32)

		// Goroutines running PretouchMany on overlapping slices.
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				sub := types[offset : offset+16]
				if err := PretouchMany(sub, option.WithCompileRecursiveDepth(2)); err != nil {
					errCh <- err
				}
			}(g * 4)
		}

		// Goroutines simultaneously executing first-request Marshal and Unmarshal on both V and *V.
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for i := 0; i < 32; i++ {
					idx := (i + worker*5) & 31
					ptrVal := stage3DynamicVals[idx]
					elemVal := reflect.ValueOf(ptrVal).Elem().Interface()

					b1, err := Marshal(elemVal)
					if err != nil {
						errCh <- err
						return
					}
					b2, err := Marshal(ptrVal)
					if err != nil {
						errCh <- err
						return
					}
					if !bytes.Equal(b1, b2) {
						errCh <- fmt.Errorf("Marshal(V) != Marshal(*V) for type %d: %s vs %s", idx, b1, b2)
						return
					}
					dst := reflect.New(types[idx]).Interface()
					if err := Unmarshal(b1, dst); err != nil {
						errCh <- err
						return
					}
				}
			}(w)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatalf("round %d concurrent Pretouch + Marshal/Unmarshal error: %v", round, err)
		}
	}
}

type cooldownGateField struct {
	gate *cooldownBarrier
}

type cooldownBarrier struct {
	active    atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
}

func (g cooldownGateField) MarshalJSON() ([]byte, error) {
	if g.gate != nil && g.gate.active.Load() {
		g.gate.enterOnce.Do(func() {
			close(g.gate.entered)
			<-g.gate.release
		})
	}
	return []byte(`"ok"`), nil
}

type cooldownE2EMediumA struct {
	ID      int64             `json:"id"`
	Title   string            `json:"title"`
	Payload string            `json:"payload"`
	Gate    cooldownGateField `json:"gate"`
}

type cooldownE2EMediumB struct {
	Seq     int64  `json:"seq"`
	Service string `json:"service"`
	Body    string `json:"body"`
	Active  bool   `json:"active"`
}

type cooldownE2ESmallC struct {
	Code int    `json:"code"`
	Name string `json:"name"`
}

func TestEncoderRootCooldownEndToEndRecovery(t *testing.T) {
	barrier := &cooldownBarrier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	medA := &cooldownE2EMediumA{
		ID:      101,
		Title:   "cooldown-e2e-medium-a",
		Payload: strings.Repeat("a", 1024),
		Gate:    cooldownGateField{gate: barrier},
	}
	medB := &cooldownE2EMediumB{
		Seq:     202,
		Service: "cooldown-e2e-medium-b",
		Body:    strings.Repeat("b", 1024),
		Active:  true,
	}
	smallC := &cooldownE2ESmallC{Code: 7, Name: "tiny"}

	wantA, err := json.Marshal(medA)
	if err != nil {
		t.Fatal(err)
	}
	wantB, err := json.Marshal(medB)
	if err != nil {
		t.Fatal(err)
	}
	wantC, err := json.Marshal(smallC)
	if err != nil {
		t.Fatal(err)
	}

	pubA, err := ConfigDefault.Marshal(medA)
	if err != nil || !bytes.Equal(pubA, wantA) {
		t.Fatalf("public Marshal(medA) error=%v", err)
	}
	encodeRoot := func(v interface{}) ([]byte, error) {
		out, ok, err := encvm.EncodeRoot(v, 0)
		if !ok {
			return nil, fmt.Errorf("expected EncodeRoot handled=true for %T", v)
		}
		return out, err
	}

	// Verify returned slices are detached from pooled scratch buffers across calls.
	firstA, err := encodeRoot(medA)
	if err != nil || !bytes.Equal(firstA, wantA) {
		t.Fatalf("first encodeRoot(medA) error=%v", err)
	}
	firstSnap := append([]byte(nil), firstA...)
	for i := 0; i < 4; i++ {
		gotB, err := encodeRoot(medB)
		if err != nil || !bytes.Equal(gotB, wantB) {
			t.Fatalf("interleaved medB error=%v", err)
		}
	}
	if !bytes.Equal(firstA, firstSnap) {
		t.Fatal("EncodeRoot output aliased pooled scratch buffer across subsequent encodes")
	}

	// Hold medA mid-encode on goroutine 1 while 16 workers concurrently encode medA, medB, and smallC.
	barrier.active.Store(true)
	var holderWG sync.WaitGroup
	holderWG.Add(1)
	go func() {
		defer holderWG.Done()
		got, err := encodeRoot(medA)
		if err != nil || !bytes.Equal(got, wantA) {
			t.Errorf("holder EncodeRoot(medA) mismatch: err=%v", err)
		}
	}()
	<-barrier.entered
	barrier.active.Store(false)

	var workersWG sync.WaitGroup
	for g := 0; g < 16; g++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			for i := 0; i < 32; i++ {
				bA, err := encodeRoot(medA)
				if err != nil || !bytes.Equal(bA, wantA) {
					t.Errorf("contended medA mismatch: err=%v", err)
					return
				}
				bB, err := encodeRoot(medB)
				if err != nil || !bytes.Equal(bB, wantB) {
					t.Errorf("contended medB mismatch: err=%v", err)
					return
				}
				bC, err := encodeRoot(smallC)
				if err != nil || !bytes.Equal(bC, wantC) {
					t.Errorf("contended smallC mismatch: err=%v", err)
					return
				}
			}
		}()
	}
	workersWG.Wait()
	close(barrier.release)
	holderWG.Wait()

	gotAfter, err := encodeRoot(medA)
	if err != nil || !bytes.Equal(gotAfter, wantA) {
		t.Fatalf("post-contention EncodeRoot(medA) failed: err=%v", err)
	}
}

type singleExecMarshaler struct {
	calls     *atomic.Int64
	fail      bool
	useStdLib bool
	payload   int
}

func (m singleExecMarshaler) MarshalJSON() ([]byte, error) {
	m.calls.Add(1)
	if m.fail {
		return nil, fmt.Errorf("intentional marshaler error")
	}
	if m.useStdLib {
		return json.Marshal(struct {
			Std int `json:"std"`
		}{Std: m.payload})
	}
	return []byte(fmt.Sprintf(`{"v":%d}`, m.payload)), nil
}

func TestEncoderSingleCallbackCountOnSuccessErrorAndStdDelegate(t *testing.T) {
	// 1. Success path: Marshal and EncodeInto each invoke MarshalJSON strictly once (even under -race).
	var okCalls atomic.Int64
	out, err := ConfigStd.Marshal(struct {
		A singleExecMarshaler `json:"a"`
	}{A: singleExecMarshaler{calls: &okCalls, payload: 9}})
	if err != nil || string(out) != `{"a":{"v":9}}` {
		t.Fatalf("Marshal got (%s, %v)", out, err)
	}
	if okCalls.Load() != 1 {
		t.Fatalf("expected 1 MarshalJSON call on Marshal, got %d", okCalls.Load())
	}

	var intoBuf []byte
	if err := encoder.EncodeInto(&intoBuf, singleExecMarshaler{calls: &okCalls, payload: 11}, 0); err != nil || string(intoBuf) != `{"v":11}` {
		t.Fatalf("EncodeInto got (%s, %v)", intoBuf, err)
	}
	if okCalls.Load() != 2 {
		t.Fatalf("expected 2 total MarshalJSON calls after EncodeInto, got %d", okCalls.Load())
	}

	// 2. Error path: failing MarshalJSON must execute once and never trigger a post-error re-encode.
	var errCalls atomic.Int64
	var tailCalls atomic.Int64
	_, err = ConfigStd.Marshal(struct {
		Bad  singleExecMarshaler `json:"bad"`
		Tail singleExecMarshaler `json:"tail"`
	}{
		Bad:  singleExecMarshaler{calls: &errCalls, fail: true},
		Tail: singleExecMarshaler{calls: &tailCalls, payload: 1},
	})
	if err == nil {
		t.Fatalf("expected error from failing MarshalJSON")
	}
	if errCalls.Load() != 1 || tailCalls.Load() != 0 {
		t.Fatalf("expected (errCalls=1, tailCalls=0) on error, got (%d, %d)", errCalls.Load(), tailCalls.Load())
	}

	// 3. User MarshalJSON explicitly delegating to encoding/json.Marshal remains unaffected and executes once.
	var stdCalls atomic.Int64
	stdOut, err := ConfigStd.Marshal(singleExecMarshaler{calls: &stdCalls, useStdLib: true, payload: 42})
	if err != nil || string(stdOut) != `{"std":42}` || stdCalls.Load() != 1 {
		t.Fatalf("user std delegate got (%s, %v, calls=%d)", stdOut, err, stdCalls.Load())
	}
}
