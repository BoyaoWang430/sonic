//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/internal/decoder/optdec"
	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/prim"
	encvars "github.com/bytedance/sonic/internal/encoder/vars"
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

func TestStage3PointerValueCacheSeparation(t *testing.T) {
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

func TestStage3CustomFragmentSanitizeAndCompactParity(t *testing.T) {
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

func TestStage3StreamEncoderSingleWriteAndIndent(t *testing.T) {
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

func TestB02_RawBufferOverlapSafety(t *testing.T) {
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

var stage3BenchFixture = stage3FragmentEnvelope{
	ID: 42,
	Raw: json.RawMessage(`{
  "service": "gateway",
  "meta": {
    "env": "prod",
    "expr": "latency < 10ms & ok > 0",
    "tags": ["a", "b", "c", "quo\"ted"]
  },
  "counts": [10, 20, 30, 40]
}`),
	Cust: stage3CustomJSON(`{ "status": "ok", "note": "a < b & c > d", "items": [1, 2, 3] }`),
	Text: stage3CustomText("custom-text-value-with-safe-ascii"),
}

func BenchmarkStage3MarshalRawMessageStd(b *testing.B) {
	_, _ = ConfigStd.Marshal(&stage3BenchFixture)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := ConfigStd.Marshal(&stage3BenchFixture)
		if err != nil || len(out) == 0 {
			b.Fatal(err)
		}
	}
}

func BenchmarkStage3MarshalRawMessageStd_StdLib(b *testing.B) {
	_, _ = json.Marshal(&stage3BenchFixture)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := json.Marshal(&stage3BenchFixture)
		if err != nil || len(out) == 0 {
			b.Fatal(err)
		}
	}
}

func BenchmarkStage3StreamEncoder(b *testing.B) {
	enc := ConfigStd.NewEncoder(io.Discard)
	_ = enc.Encode(&stage3BenchFixture)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := enc.Encode(&stage3BenchFixture); err != nil {
			b.Fatal(err)
		}
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

func BenchmarkStage3ConcurrentColdCompile(b *testing.B) {
	initStage3DynamicVals()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encvars.ResetProgramCache()
		var idx uint32
		var wg sync.WaitGroup
		workers := 8
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for {
					k := int(atomic.AddUint32(&idx, 1)) - 1
					if k >= len(stage3DynamicVals) {
						return
					}
					if _, err := encoder.Encode(stage3DynamicVals[k], 0); err != nil {
						panic(err)
					}
				}
			}()
		}
		wg.Wait()
	}
}

func TestE03bSinglePassCompactedJSONParity(t *testing.T) {
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

func TestT03BatchPretouchDeduplicationAndFailurePolicy(t *testing.T) {
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

func BenchmarkStage3PretouchMany64_T03(b *testing.B) {
	initStage3DynamicVals()
	types64 := make([]reflect.Type, len(stage3DynamicVals))
	for i, v := range stage3DynamicVals {
		types64[i] = reflect.TypeOf(v).Elem()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		encvars.ResetProgramCache()
		optdec.ResetProgramCache()
		b.StartTimer()
		if err := PretouchMany(types64); err != nil {
			b.Fatal(err)
		}
	}
}

func TestStage3_T04_KeyBlobAndSlabContracts(t *testing.T) {
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

	// Verify all fields' PaddedCommaKey and PaddedStructBeginKey share a single contiguous KeyBlob
	// with >=32 bytes of readable memory from the start of every key.
	blob := fms[0].KeyBlob
	firstPtr := uintptr(unsafe.Pointer(unsafe.SliceData(blob)))
	lastField := fms[len(fms)-1]
	lastEnd := firstPtr + uintptr(lastField.KeyOffset+lastField.CommaKeyLen)
	for i, fm := range fms {
		if unsafe.SliceData(fm.KeyBlob) != unsafe.SliceData(blob) {
			t.Fatalf("field %d KeyBlob does not share backing slice", i)
		}
		ptr := uintptr(unsafe.Pointer(&fm.KeyBlob[fm.KeyOffset]))
		if ptr < firstPtr || ptr >= lastEnd {
			t.Fatalf("field %d KeyOffset pointer out of contiguous KeyBlob range", i)
		}
		raw32 := fm.KeyBlob[fm.KeyOffset : fm.KeyOffset+32]
		if !bytes.HasPrefix(raw32, []byte(fm.PaddedCommaKey)) {
			t.Fatalf("field %d 32B read prefix mismatch", i)
		}
		if fm.Name == "nested" {
			if fm.PaddedStructBeginKey != `,"nested":{` || !fm.HasStructBegin {
				t.Fatalf("unexpected PaddedStructBeginKey: %q", fm.PaddedStructBeginKey)
			}
			sbPtr := uintptr(unsafe.Pointer(unsafe.StringData(fm.PaddedStructBeginKey)))
			if sbPtr != ptr {
				t.Fatalf("expected PaddedStructBeginKey to share prefix with KeyBlob in tight KeyBlob")
			}
		}
	}
	// Verify trailing 32 bytes after the last key are all zero.
	trailingZeros := blob[lastField.KeyOffset+lastField.CommaKeyLen : lastField.KeyOffset+lastField.CommaKeyLen+32]
	for i, b := range trailingZeros {
		if b != 0 {
			t.Fatalf("expected trailing zero at byte %d after KeyBlob, got %d", i, b)
		}
	}
}

type stage3AppenderRecord struct {
	ID   int64
	Code string
	Expr string
	Fail bool
	Raw  string
}

func (r stage3AppenderRecord) AppendJSON(dst []byte) ([]byte, error) {
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
	return r.AppendJSON(nil)
}

func TestStage3_E04a_JSONAppenderContracts(t *testing.T) {
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

func BenchmarkStage3ResolveKeyBlob_T04(b *testing.B) {
	initStage3DynamicVals()
	types64 := make([]reflect.Type, len(stage3DynamicVals))
	for i, v := range stage3DynamicVals {
		types64[i] = reflect.TypeOf(v).Elem()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resolver.ResetFieldCacheForTest()
		for _, vt := range types64 {
			_ = resolver.ResolveStruct(vt)
		}
	}
}

func BenchmarkStage3MarshalJSONAppender_E04a(b *testing.B) {
	type Batch struct {
		Items [16]stage3AppenderRecord `json:"items"`
	}
	var batch Batch
	for i := range batch.Items {
		batch.Items[i] = stage3AppenderRecord{
			ID:   int64(1000 + i),
			Code: "ORD-2026-X99",
			Expr: "normal_clean_ascii_payload_value",
		}
	}
	_, _ = ConfigDefault.Marshal(&batch)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := ConfigDefault.Marshal(&batch)
		if err != nil || len(out) == 0 {
			b.Fatal(err)
		}
	}
}

func TestReview_PretouchMergedAddressabilityPlans(t *testing.T) {
	type Child struct {
		X int `json:"x,omitempty"`
	}
	type Root struct {
		Items [1]Child
	}

	const want = `{"Items":[{"x":0}]}`
	for iter := 0; iter < 20; iter++ {
		encvars.ResetProgramCache()
		optdec.ResetProgramCache()
		if err := Pretouch(
			reflect.TypeOf(Root{}),
			option.WithCompileMaxInlineDepth(2),
			option.WithCompileRecursiveDepth(1),
			option.WithCompileEncOnlyOmitNull(true),
		); err != nil {
			t.Fatalf("iter %d Pretouch error: %v", iter, err)
		}
		if encvars.GetProgram(reflect.TypeOf(Child{}), false) == nil {
			t.Fatalf("iter %d missing (Child, pv=false) program after Pretouch", iter)
		}
		if encvars.GetProgram(reflect.TypeOf(Child{}), true) == nil {
			t.Fatalf("iter %d missing (Child, pv=true) program after Pretouch", iter)
		}

		gotVal, err := Marshal(Root{})
		if err != nil {
			t.Fatalf("iter %d Marshal(Root{}) error: %v", iter, err)
		}
		if string(gotVal) != want {
			t.Fatalf("iter %d Marshal(Root{}) = %s, want %s", iter, gotVal, want)
		}

		gotPtr, err := Marshal(&Root{})
		if err != nil {
			t.Fatalf("iter %d Marshal(&Root{}) error: %v", iter, err)
		}
		if string(gotPtr) != want {
			t.Fatalf("iter %d Marshal(&Root{}) = %s, want %s", iter, gotPtr, want)
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
		if encvars.GetProgram(reflect.TypeOf(Child{}), false) == nil || encvars.GetProgram(reflect.TypeOf(Child{}), true) == nil {
			t.Fatalf("iter %d MixedRoot missing one of (Child, pv=false/true) plans", iter)
		}
	}
}

type reviewCountingAppender struct {
	ID           int
	appendCalls  *atomic.Int64
	jsonCalls    *atomic.Int64
	textCalls    *atomic.Int64
	stdRaceCalls *atomic.Int64
}

func (c reviewCountingAppender) AppendJSON(dst []byte) ([]byte, error) {
	if c.appendCalls != nil || c.stdRaceCalls != nil {
		if calledFromStdEncodingJSON() {
			if c.stdRaceCalls != nil {
				c.stdRaceCalls.Add(1)
			}
		} else if c.appendCalls != nil {
			c.appendCalls.Add(1)
		}
	}
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, int64(c.ID), 10)
	dst = append(dst, `,"mode":"clean_ascii"}`...)
	return dst, nil
}

func (c reviewCountingAppender) MarshalJSON() ([]byte, error) {
	if c.jsonCalls != nil || c.stdRaceCalls != nil {
		if calledFromStdEncodingJSON() {
			if c.stdRaceCalls != nil {
				c.stdRaceCalls.Add(1)
			}
		} else if c.jsonCalls != nil {
			c.jsonCalls.Add(1)
		}
	}
	return c.AppendJSON(nil)
}

func (c reviewCountingAppender) MarshalText() ([]byte, error) {
	if c.textCalls != nil && !calledFromStdEncodingJSON() {
		c.textCalls.Add(1)
	}
	return []byte("text_fallback"), nil
}

func TestReview_JSONAppenderDispatchPriorityAndZeroAlloc(t *testing.T) {
	var appendCalls, jsonCalls, textCalls, stdRaceCalls atomic.Int64
	makeItem := func(id int) reviewCountingAppender {
		return reviewCountingAppender{
			ID:           id,
			appendCalls:  &appendCalls,
			jsonCalls:    &jsonCalls,
			textCalls:    &textCalls,
			stdRaceCalls: &stdRaceCalls,
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
		t.Fatalf("expected 6 AppendJSON calls across concrete/ptr/slice/any/JSONAppender/json.Marshaler fields, got %d (MarshalJSON=%d, MarshalText=%d)",
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

	// Verify 0 allocations when encoding into preallocated buffer under both default and ConfigStd options
	// (skipped under -race where encodeIntoCheckRace intentionally invokes encoding/json.Marshal).
	if stdRaceCalls.Load() > 0 {
		return
	}
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
		if allocs != 0 {
			t.Fatalf("expected 0 allocs for clean compact JSONAppender with opts=%v, got %v", opts, allocs)
		}
	}
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
