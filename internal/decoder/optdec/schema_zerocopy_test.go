//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

func TestAsSliceEfacePropagatesElementError(t *testing.T) {
	// Parse with _F_use_number so 1e999 is stored as KNumber on the tape,
	// then clear _F_use_number so AsEface attempts float64 conversion and fails
	// with MismatchTypeError inside AsSliceEface.
	p := newParserImpl(`[1, 1e999, "ok"]`, 0, 1<<_F_use_number)
	p.keepUseNumber = true
	defer p.free()

	code := p.parse()
	if code != 0 {
		t.Fatalf("unexpected parse error code: %v", code)
	}
	p.options = 0
	ctx := &p.ctx
	ctx.Parser = p

	root := ctx.Root()
	var dst []interface{}
	err := root.AsSliceEface(ctx, unsafe.Pointer(&dst))
	if err == nil {
		t.Fatalf("expected AsSliceEface to return element decode error, got nil")
	}
	if _, ok := err.(MismatchTypeError); !ok {
		t.Fatalf("expected MismatchTypeError, got %T: %v", err, err)
	}
	if len(dst) != 3 || dst[0] != float64(1) || dst[1] != nil || dst[2] != "ok" {
		t.Fatalf("unexpected dst slice: %#v", dst)
	}
}

func TestSchemaProjBytesFieldZeroFallback(t *testing.T) {
	type bytesEnvelope struct {
		ID      int64    `json:"id"`
		Name    string   `json:"name"`
		Payload []byte   `json:"payload"`
		Extra   []byte   `json:"extra"`
		Tags    []string `json:"tags"`
	}

	var probe bytesEnvelope
	_, _, _, proj, flatSD, err := unpackAndCompileRoot(&probe)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot failed: %v", err)
	}
	if flatSD != nil {
		t.Fatalf("bytesEnvelope must not be flatSD, got %+v", flatSD)
	}
	if proj == nil {
		t.Fatalf("expected non-nil projPlan for bytesEnvelope")
	}

	rawBytes := bytes.Repeat([]byte("sonic-zero-copy-bytes-"), 12)
	b64 := base64.StdEncoding.EncodeToString(rawBytes)
	paddingNoise := strings.Repeat("n", 520)

	cases := []string{
		fmt.Sprintf(`{"id":101,"name":"base64-case","skip":%q,"payload":%q,"extra":null,"tags":["a","b"]}`, paddingNoise, b64),
		fmt.Sprintf(`{"id":102,"name":"array-case","skip":%q,"payload":[1,2,3,254,255],"extra":"YWJj","tags":["x"]}`, paddingNoise),
		fmt.Sprintf(`{"id":103,"name":"null-case","skip":%q,"payload":null,"extra":[],"tags":[]}`, paddingNoise),
	}

	for idx, js := range cases {
		if len(js) < 512 {
			t.Fatalf("case %d length %d must be >= 512", idx, len(js))
		}
		if !proj.shouldUse(len(js)) {
			t.Fatalf("case %d expected proj.shouldUse(%d) == true (denseStreak=%d)", idx, len(js), atomic.LoadUint32(&proj.denseStreak))
		}

		beforeFallback := atomic.LoadUint64(&schemaFallbackCount)
		var got, want bytesEnvelope
		if err := json.Unmarshal([]byte(js), &want); err != nil {
			t.Fatalf("case %d stdlib unmarshal: %v", idx, err)
		}
		if _, _, err := DecodeOne(js, 1<<_F_validate_string, &got); err != nil {
			t.Fatalf("case %d DecodeOne failed: %v", idx, err)
		}
		afterFallback := atomic.LoadUint64(&schemaFallbackCount)
		if afterFallback != beforeFallback {
			t.Fatalf("case %d triggered %d schema fallback(s) (expected 0)", idx, afterFallback-beforeFallback)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d mismatch: got %+v, want %+v", idx, got, want)
		}
	}
}

func TestFlatStructAdaptiveBackoff(t *testing.T) {
	type backoffFlat struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Note string `json:"note"`
	}

	var dst backoffFlat
	_, _, _, _, flatSD, err := unpackAndCompileRoot(&dst)
	if err != nil || flatSD == nil {
		t.Fatalf("expected flatSD for backoffFlat, err=%v", err)
	}
	atomic.StoreUint32(&flatSD.flatFailStreak, 0)
	atomic.StoreUint32(&flatSD.flatProbeTick, 0)

	escapedJSON := `{"id":7,"name":"hello\nworld","note":"line1\tline2"}`
	for i := 0; i < int(flatBackoffThreshold)+2; i++ {
		if _, _, err := DecodeOne(escapedJSON, 0, &dst); err != nil {
			t.Fatalf("iter %d DecodeOne failed: %v", i, err)
		}
		if dst.Name != "hello\nworld" {
			t.Fatalf("iter %d unexpected dst.Name=%q", i, dst.Name)
		}
	}
	streak := atomic.LoadUint32(&flatSD.flatFailStreak)
	if streak < flatBackoffThreshold {
		t.Fatalf("expected flatFailStreak >= %d after repeated escaped failures, got %d", flatBackoffThreshold, streak)
	}

	// Clean inputs should eventually hit the 1-in-256 probe and reset flatFailStreak to 0.
	cleanJSON := `{"id":99,"name":"plain-ascii","note":"ok"}`
	for i := 0; i < 300; i++ {
		if _, _, err := DecodeOne(cleanJSON, 0, &dst); err != nil {
			t.Fatalf("clean iter %d failed: %v", i, err)
		}
		if atomic.LoadUint32(&flatSD.flatFailStreak) == 0 {
			break
		}
	}
	if gotStreak := atomic.LoadUint32(&flatSD.flatFailStreak); gotStreak != 0 {
		t.Fatalf("expected flatFailStreak to reset to 0 after probe success, got %d", gotStreak)
	}
}

func TestNegativeZeroFloatSignbitConsistency(t *testing.T) {
	type flatFloats struct {
		F64 float64 `json:"f64"`
		F32 float32 `json:"f32"`
	}
	type nonFlatFloats struct {
		F64  float64  `json:"f64"`
		F32  float32  `json:"f32"`
		PF64 *float64 `json:"pf64"`
		PF32 *float32 `json:"pf32"`
		Tags []string `json:"tags"`
	}

	inputs := []struct {
		numLit  string
		wantNeg bool
	}{
		{"-0", true},
		{"-0.0", true},
		{"-0e0", true},
		{"0", false},
		{"0.0", false},
	}

	for _, tc := range inputs {
		// Verify stdlib signbit baseline first.
		var stdF64 float64
		if err := json.Unmarshal([]byte(tc.numLit), &stdF64); err != nil {
			t.Fatalf("stdlib %s: %v", tc.numLit, err)
		}
		if math.Signbit(stdF64) != tc.wantNeg {
			t.Fatalf("stdlib signbit mismatch for %s", tc.numLit)
		}

		// 1. Root float64 & any
		var rootF64 float64
		if _, _, err := DecodeOne(tc.numLit, 0, &rootF64); err != nil {
			t.Fatalf("root float64 %s: %v", tc.numLit, err)
		}
		if rootF64 != 0 || math.Signbit(rootF64) != tc.wantNeg {
			t.Fatalf("root float64 %s: got %v (signbit=%v), want signbit=%v", tc.numLit, rootF64, math.Signbit(rootF64), tc.wantNeg)
		}

		var rootAny any
		if _, _, err := DecodeOne(tc.numLit, 0, &rootAny); err != nil {
			t.Fatalf("root any %s: %v", tc.numLit, err)
		}
		af := rootAny.(float64)
		if af != 0 || math.Signbit(af) != tc.wantNeg {
			t.Fatalf("root any %s: got %v (signbit=%v), want signbit=%v", tc.numLit, af, math.Signbit(af), tc.wantNeg)
		}

		// 2. Flat struct direct path
		flatJS := fmt.Sprintf(`{"f64":%s,"f32":%s}`, tc.numLit, tc.numLit)
		var ff flatFloats
		if _, _, err := DecodeOne(flatJS, 0, &ff); err != nil {
			t.Fatalf("flatFloats %s: %v", tc.numLit, err)
		}
		if math.Signbit(ff.F64) != tc.wantNeg || math.Signbit(float64(ff.F32)) != tc.wantNeg {
			t.Fatalf("flatFloats %s signbit mismatch: f64=%v f32=%v", tc.numLit, math.Signbit(ff.F64), math.Signbit(float64(ff.F32)))
		}

		// 3. Non-flat struct (both <512B full parse and >=512B schema scan, plus pointer floats)
		for _, padLen := range []int{10, 600} {
			nfJS := fmt.Sprintf(`{"skip":%q,"f64":%s,"f32":%s,"pf64":%s,"pf32":%s,"tags":["a"]}`,
				strings.Repeat("p", padLen), tc.numLit, tc.numLit, tc.numLit, tc.numLit)
			var nf nonFlatFloats
			if _, _, err := DecodeOne(nfJS, 0, &nf); err != nil {
				t.Fatalf("nonFlatFloats(%d) %s: %v", padLen, tc.numLit, err)
			}
			if math.Signbit(nf.F64) != tc.wantNeg ||
				math.Signbit(float64(nf.F32)) != tc.wantNeg ||
				math.Signbit(*nf.PF64) != tc.wantNeg ||
				math.Signbit(float64(*nf.PF32)) != tc.wantNeg {
				t.Fatalf("nonFlatFloats(%d) %s signbit mismatch", padLen, tc.numLit)
			}
		}
	}
}

func TestScanNumberScalarSinglePassBoundaries(t *testing.T) {
	type numEnvelope struct {
		A float64 `json:"a"`
		B uint64  `json:"b"`
		C float64 `json:"c"`
		S []int   `json:"s"`
	}

	// Sweep padding lengths so numbers land before tailStart, between tailStart and safe64,
	// straddling safe64, and inside the final 64 bytes.
	for pad := 300; pad <= 620; pad += 13 {
		for tailPad := 0; tailPad <= 90; tailPad += 9 {
			js := fmt.Sprintf(
				`{"head":%q,"a":-123456.7890125e-3,"b":18446744073709551615,"s":[1,2],"c":98765432109876543210.5,"tail":%q}`,
				strings.Repeat("h", pad), strings.Repeat("t", tailPad),
			)
			var got, want numEnvelope
			if err := json.Unmarshal([]byte(js), &want); err != nil {
				t.Fatalf("stdlib: %v", err)
			}
			before := atomic.LoadUint64(&schemaFallbackCount)
			if _, _, err := DecodeOne(js, 0, &got); err != nil {
				t.Fatalf("DecodeOne pad=%d tailPad=%d: %v", pad, tailPad, err)
			}
			if atomic.LoadUint64(&schemaFallbackCount) != before {
				t.Fatalf("unexpected schema fallback on valid numbers at pad=%d tailPad=%d", pad, tailPad)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("mismatch at pad=%d tailPad=%d: got=%+v want=%+v", pad, tailPad, got, want)
			}
		}
	}

	// Malformed numbers must be rejected across both middle and tail positions.
	badNums := []string{"01", "-01", "1.2.3", "1e+", "1.", "-"}
	for _, bad := range badNums {
		for _, tailPad := range []int{4, 70, 300} {
			js := fmt.Sprintf(`{"head":%q,"a":%s,"s":[1],"tail":%q}`, strings.Repeat("h", 400), bad, strings.Repeat("t", tailPad))
			var dst numEnvelope
			if _, _, err := DecodeOne(js, 0, &dst); err == nil {
				t.Fatalf("expected error for malformed number %q (tailPad=%d)", bad, tailPad)
			}
		}
	}
}

func TestFlatStructExtendedTypesAndNested(t *testing.T) {
	type nestedMeta struct {
		Ver   uint16  `json:"ver"`
		Flags uint8   `json:"flags"`
		Delta int8    `json:"delta"`
		Code  int16   `json:"code"`
		Note  *string `json:"note"`
	}
	type nestedAuthor struct {
		ID   int64      `json:"id"`
		Name string     `json:"name"`
		Meta nestedMeta `json:"meta"`
	}
	type extendedFlat struct {
		I8     int8         `json:"i8"`
		I16    int16        `json:"i16"`
		U8     uint8        `json:"u8"`
		U16    uint16       `json:"u16"`
		PI8    *int8        `json:"pi8"`
		PI16   *int16       `json:"pi16"`
		PI32   *int32       `json:"pi32"`
		PI64   *int64       `json:"pi64"`
		PInt   *int         `json:"pint"`
		PU8    *uint8       `json:"pu8"`
		PU16   *uint16      `json:"pu16"`
		PU32   *uint32      `json:"pu32"`
		PU64   *uint64      `json:"pu64"`
		PUint  *uint        `json:"puint"`
		PBool  *bool        `json:"pbool"`
		PF32   *float32     `json:"pf32"`
		PF64   *float64     `json:"pf64"`
		PStr   *string      `json:"pstr"`
		BigStr *string      `json:"big_str"`
		Author nestedAuthor `json:"author"`
	}

	var probe extendedFlat
	_, _, _, _, flatSD, err := unpackAndCompileRoot(&probe)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot failed: %v", err)
	}
	if flatSD == nil || !flatSD.flatDirect || flatSD.flatPlan == nil {
		t.Fatalf("expected extendedFlat to compile with flatDirect + flatPlan, got flatSD=%+v", flatSD)
	}

	bigVal := strings.Repeat("B", 1500)
	js := fmt.Sprintf(`{
		"i8":-128,"i16":-32768,"u8":255,"u16":65535,
		"pi8":127,"pi16":32767,"pi32":-2147483648,"pi64":-9223372036854775808,"pint":42,
		"pu8":200,"pu16":50000,"pu32":4000000000,"pu64":18446744073709551615,"puint":99,
		"pbool":true,"pf32":-1.5,"pf64":3.141592653589793,"pstr":"copied-ptr","big_str":%q,
		"author":{"id":77,"name":"alice","meta":{"ver":3,"flags":9,"delta":-5,"code":1024,"note":"nested-ptr"}}
	}`, bigVal)

	// Verify UseNumber and UseInt64 options do not block tryDecodeFlatStruct.
	for _, opts := range []uint64{
		0,
		(1 << _F_copy_string) | (1 << _F_validate_string),
		(1 << _F_use_number) | (1 << _F_copy_string),
		(1 << _F_use_int64),
	} {
		var got, want extendedFlat
		if err := json.Unmarshal([]byte(js), &want); err != nil {
			t.Fatalf("stdlib: %v", err)
		}
		if _, _, err := DecodeOne(js, opts, &got); err != nil {
			t.Fatalf("DecodeOne opts=%#x: %v", opts, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("opts=%#x mismatch:\ngot:  %+v\nwant: %+v", opts, got, want)
		}
	}

	// Verify duplicate key {"pi64": null, "pi64": 99} does NOT mutate pre-existing pointee!
	origPointee := int64(42)
	keepPointee := int32(10)
	dst := extendedFlat{
		PI64: &origPointee,
		PI32: &keepPointee,
	}
	dupNullThenVal := `{"pi64":null,"pi64":99,"pi32":77,"author":null}`
	stdOrig := int64(42)
	stdKeep := int32(10)
	stdDst := extendedFlat{
		PI64: &stdOrig,
		PI32: &stdKeep,
	}
	if err := json.Unmarshal([]byte(dupNullThenVal), &stdDst); err != nil {
		t.Fatalf("stdlib dupNullThenVal: %v", err)
	}
	if _, _, err := DecodeOne(dupNullThenVal, 0, &dst); err != nil {
		t.Fatalf("DecodeOne dupNullThenVal: %v", err)
	}
	if origPointee != 42 {
		t.Fatalf("origPointee was mutated to %d after null->99 (expected 42, stdOrig=%d)", origPointee, stdOrig)
	}
	if *dst.PI64 != 99 || dst.PI64 == &origPointee {
		t.Fatalf("expected fresh *int64(99), got %v", *dst.PI64)
	}
	if dst.PI32 != &keepPointee || keepPointee != 77 {
		t.Fatalf("expected non-nulled pre-existing pointer to be reused in place, keepPointee=%d", keepPointee)
	}

	// Verify syntax error after staging pointer & nested fields leaves pre-populated struct 100% untouched.
	untouchedVal := int64(555)
	guardDst := extendedFlat{
		I8:   12,
		PI64: &untouchedVal,
		Author: nestedAuthor{
			ID:   888,
			Name: "keep-author",
		},
	}
	_, ok := tryDecodeFlatStruct(`{"i8":99,"pi64":null,"author":{"id":1,"name":"mutated"}, broken}`, 0, 0, unsafe.Pointer(&guardDst), flatSD, false, true)
	if ok {
		t.Fatalf("expected tryDecodeFlatStruct to return false on syntax error")
	}
	if guardDst.I8 != 12 || guardDst.PI64 != &untouchedVal || untouchedVal != 555 || guardDst.Author.ID != 888 || guardDst.Author.Name != "keep-author" {
		t.Fatalf("speculative bailout mutated target: %+v", guardDst)
	}
}

func decodeOneFullCopyForBench(s string, f uint64, val interface{}) error {
	etp, vp, dec, _, _, err := unpackAndCompileRoot(val)
	if err != nil {
		return err
	}
	p := newParserImpl(s, 0, f)
	defer p.free()
	ctx, err := newContextOnParserWithPlan(p, f, etp, nil)
	if err == nil {
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}
	return fix_error(s, 0, err)
}

func decodeOneForceZeroCopyForBench(s string, f uint64, val interface{}) error {
	_, vp, dec, proj, _, err := unpackAndCompileRoot(val)
	if err != nil {
		return err
	}
	p := newParserLazy(s, 0, f)
	defer p.free()
	ctx := &p.ctx
	ctx.Parser = p
	p.strictURC = (f & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (f & (1 << _F_use_number)) != 0
	ok, mutated := parseSchemaRoot(p, proj)
	var ecode ErrorCode
	if !ok {
		ecode = resetAndRunFullParse(p, mutated)
	}
	if ecode != 0 {
		return fix_error(s, 0, p.fixError(ecode))
	}
	err = dec.FromDom(vp, ctx.Root(), ctx)
	return fix_error(s, 0, err)
}

func BenchmarkDecoder_Binding_ZeroCopyVsFullCopy(b *testing.B) {
	// 1. Struct with []byte field (Sparse envelope + Large 12 KiB base64 blob)
	type bytesTargetSparse struct {
		ID      int64    `json:"id"`
		Service string   `json:"service"`
		Region  string   `json:"region"`
		Payload []byte   `json:"payload"`
		Tags    []string `json:"tags"`
	}
	b64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("sonic-bytes-field-payload-"), 24))
	noiseArr := strings.Repeat(`{"u":1,"v":"skip-val"},`, 60) + `{"u":0}`
	bytesSparseJSON := fmt.Sprintf(`{"id":42,"service":"blob-gateway","region":"ap-east-1","skip_items":[%s],"payload":%q,"tags":["v1","prod"]}`,
		noiseArr, b64)

	b.Run("WithBytesField_Sparse/AdaptiveZeroCopy", func(b *testing.B) {
		b.SetBytes(int64(len(bytesSparseJSON)))
		b.ReportAllocs()
		before := atomic.LoadUint64(&schemaFallbackCount)
		var dst bytesTargetSparse
		for i := 0; i < b.N; i++ {
			dst.Payload = dst.Payload[:0]
			dst.Tags = dst.Tags[:0]
			if _, _, err := DecodeOne(bytesSparseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
		if delta := atomic.LoadUint64(&schemaFallbackCount) - before; delta != 0 {
			b.Fatalf("expected 0 schema fallbacks, got %d", delta)
		}
	})
	b.Run("WithBytesField_Sparse/FullCopy", func(b *testing.B) {
		b.SetBytes(int64(len(bytesSparseJSON)))
		b.ReportAllocs()
		var dst bytesTargetSparse
		for i := 0; i < b.N; i++ {
			dst.Payload = dst.Payload[:0]
			dst.Tags = dst.Tags[:0]
			if err := decodeOneFullCopyForBench(bytesSparseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
	})

	// 2. Struct with escaped strings (Sparse + Dense after adaptive backoff)
	type escapedTargetSparse struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	escapedSparseJSON := fmt.Sprintf(
		`{"id":1001,"title":"alert: \"disk full\"\ncheck node","unused":[%s],"message":%q,"detail":"line1\nline2\t\u4e2d\u6587"}`,
		noiseArr, strings.Repeat("plain-prefix-", 20)+`\nescaped-tail`,
	)
	b.Run("WithEscapedStrings_Sparse/AdaptiveZeroCopy", func(b *testing.B) {
		b.SetBytes(int64(len(escapedSparseJSON)))
		b.ReportAllocs()
		var dst escapedTargetSparse
		for w := 0; w < 10; w++ {
			_, _, _ = DecodeOne(escapedSparseJSON, 0, &dst)
		}
		before := atomic.LoadUint64(&schemaFallbackCount)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := DecodeOne(escapedSparseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
		if delta := atomic.LoadUint64(&schemaFallbackCount) - before; delta != 0 {
			b.Fatalf("expected 0 schema fallbacks, got %d", delta)
		}
	})
	b.Run("WithEscapedStrings_Sparse/FullCopy", func(b *testing.B) {
		b.SetBytes(int64(len(escapedSparseJSON)))
		b.ReportAllocs()
		var dst escapedTargetSparse
		for i := 0; i < b.N; i++ {
			if err := decodeOneFullCopyForBench(escapedSparseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
	})

	type escapedTargetDense struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Author  string `json:"author"`
		Region  string `json:"region"`
	}
	escapedDenseJSON := fmt.Sprintf(
		`{"id":1001,"title":"alert: \"disk full\"\ncheck node","message":%q,"detail":"line1\nline2\t\u4e2d\u6587","author":"ops-bot","region":"us-west-2"}`,
		strings.Repeat("plain-prefix-", 35)+`\nescaped-tail`,
	)
	b.Run("WithEscapedStrings_Dense/Adaptive", func(b *testing.B) {
		b.SetBytes(int64(len(escapedDenseJSON)))
		b.ReportAllocs()
		var dst escapedTargetDense
		for w := 0; w < 10; w++ {
			_, _, _ = DecodeOne(escapedDenseJSON, 0, &dst)
		}
		before := atomic.LoadUint64(&schemaFallbackCount)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := DecodeOne(escapedDenseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
		if delta := atomic.LoadUint64(&schemaFallbackCount) - before; delta != 0 {
			b.Fatalf("expected 0 schema fallbacks, got %d", delta)
		}
	})
	b.Run("WithEscapedStrings_Dense/FullCopy", func(b *testing.B) {
		b.SetBytes(int64(len(escapedDenseJSON)))
		b.ReportAllocs()
		var dst escapedTargetDense
		for i := 0; i < b.N; i++ {
			if err := decodeOneFullCopyForBench(escapedDenseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
	})

	// 3. Wide struct (20 fields > old maxStructWidth=12 cap, Sparse + Dense)
	type wideStruct20Sparse struct {
		F01 int64   `json:"f01"`
		F02 int64   `json:"f02"`
		F03 int32   `json:"f03"`
		F04 int32   `json:"f04"`
		F05 uint64  `json:"f05"`
		F06 uint32  `json:"f06"`
		F07 float64 `json:"f07"`
		F08 float64 `json:"f08"`
		F09 bool    `json:"f09"`
		F10 bool    `json:"f10"`
		F11 string  `json:"f11"`
		F12 string  `json:"f12"`
		F13 string  `json:"f13"`
		F14 string  `json:"f14"`
		F15 string  `json:"f15"`
		F16 string  `json:"f16"`
		F17 string  `json:"f17"`
		F18 string  `json:"f18"`
		F19 []byte  `json:"f19"`
		F20 string  `json:"f20"`
	}
	wideSparseJSON := fmt.Sprintf(
		`{"f01":1,"f02":2,"f03":3,"f04":4,"f05":5,"f06":6,"f07":7.5,"f08":8.25,"f09":true,"f10":false,`+
			`"extra_sub":[%s],"f11":"alpha","f12":"bravo","f13":"charlie","f14":"delta","f15":"echo",`+
			`"f16":"foxtrot","f17":"golf","f18":"hotel","f19":%q,"f20":"india"}`,
		noiseArr, b64[:256],
	)
	b.Run("WideStruct20_Sparse/AdaptiveZeroCopy", func(b *testing.B) {
		b.SetBytes(int64(len(wideSparseJSON)))
		b.ReportAllocs()
		before := atomic.LoadUint64(&schemaFallbackCount)
		var dst wideStruct20Sparse
		for i := 0; i < b.N; i++ {
			dst.F19 = dst.F19[:0]
			if _, _, err := DecodeOne(wideSparseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
		if delta := atomic.LoadUint64(&schemaFallbackCount) - before; delta != 0 {
			b.Fatalf("expected 0 schema fallbacks, got %d", delta)
		}
	})
	b.Run("WideStruct20_Sparse/FullCopy", func(b *testing.B) {
		b.SetBytes(int64(len(wideSparseJSON)))
		b.ReportAllocs()
		var dst wideStruct20Sparse
		for i := 0; i < b.N; i++ {
			dst.F19 = dst.F19[:0]
			if err := decodeOneFullCopyForBench(wideSparseJSON, 0, &dst); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkDecoder_Binding_ShouldUseCalibration(b *testing.B) {
	type wideBounded40 struct {
		F01 int64   `json:"f01"`
		F02 int64   `json:"f02"`
		F03 int64   `json:"f03"`
		F04 int64   `json:"f04"`
		F05 int64   `json:"f05"`
		F06 int32   `json:"f06"`
		F07 int32   `json:"f07"`
		F08 int32   `json:"f08"`
		F09 int32   `json:"f09"`
		F10 int32   `json:"f10"`
		F11 uint64  `json:"f11"`
		F12 uint64  `json:"f12"`
		F13 uint32  `json:"f13"`
		F14 uint32  `json:"f14"`
		F15 float64 `json:"f15"`
		F16 float64 `json:"f16"`
		F17 float64 `json:"f17"`
		F18 bool    `json:"f18"`
		F19 bool    `json:"f19"`
		F20 bool    `json:"f20"`
		S21 string  `json:"s21"`
		S22 string  `json:"s22"`
		S23 string  `json:"s23"`
		S24 string  `json:"s24"`
		S25 string  `json:"s25"`
		S26 string  `json:"s26"`
		S27 string  `json:"s27"`
		S28 string  `json:"s28"`
		S29 string  `json:"s29"`
		S30 string  `json:"s30"`
		S31 string  `json:"s31"`
		S32 string  `json:"s32"`
		S33 string  `json:"s33"`
		S34 string  `json:"s34"`
		S35 string  `json:"s35"`
		S36 string  `json:"s36"`
		S37 string  `json:"s37"`
		S38 string  `json:"s38"`
		S39 []byte  `json:"s39"`
		S40 string  `json:"s40"`
	}

	buildWide40JSON := func(strLen int, unknownSkip string) string {
		sv := strings.Repeat("v", strLen)
		var sb strings.Builder
		sb.WriteString(`{"f01":1,"f02":2,"f03":3,"f04":4,"f05":5,"f06":6,"f07":7,"f08":8,"f09":9,"f10":10,`)
		sb.WriteString(`"f11":11,"f12":12,"f13":13,"f14":14,"f15":15.5,"f16":16.25,"f17":17.75,"f18":true,"f19":false,"f20":true,`)
		if unknownSkip != "" {
			sb.WriteString(`"unused_subtree":`)
			sb.WriteString(unknownSkip)
			sb.WriteByte(',')
		}
		for i := 21; i <= 38; i++ {
			fmt.Fprintf(&sb, `"s%02d":%q,`, i, sv)
		}
		fmt.Fprintf(&sb, `"s39":"YWJjZGVm","s40":%q}`, sv)
		return sb.String()
	}

	skip3K := `[` + strings.Repeat(`{"u":1,"msg":"ignored-audit-entry-payload"},`, 65) + `{"u":0}]`
	skip12K := `[` + strings.Repeat(`{"u":1,"msg":"ignored-audit-entry-payload-long-0123456789"},`, 210) + `{"u":0}]`

	wide40Sparse4K := buildWide40JSON(16, skip3K)
	wide40Sparse14K := buildWide40JSON(16, skip12K)
	wide40Dense2K := buildWide40JSON(80, "")
	wide40Dense10K := buildWide40JSON(480, "")

	type bigBlobBounded6 struct {
		ID int64  `json:"id"`
		B1 string `json:"b1"`
		B2 string `json:"b2"`
		B3 string `json:"b3"`
		B4 string `json:"b4"`
		OK bool   `json:"ok"`
	}
	blob3K := strings.Repeat("x", 3000)
	bigBlobDense12K := fmt.Sprintf(`{"id":99,"b1":%q,"b2":%q,"b3":%q,"b4":%q,"ok":true}`, blob3K, blob3K, blob3K, blob3K)

	type smallBoundedItem struct {
		ID   int64  `json:"id"`
		Code int32  `json:"code"`
		Name string `json:"name"`
		Tag  string `json:"tag"`
		OK   bool   `json:"ok"`
		Val  int64  `json:"val"`
	}
	var arrItems [150]string
	for i := range arrItems {
		arrItems[i] = fmt.Sprintf(`{"id":%d,"code":%d,"name":"item-name","tag":"t1","ok":true,"val":%d}`, i, i%10, i*7)
	}
	fixedArrDense12K := `[` + strings.Join(arrItems[:], ",") + `]`

	runPair := func(name, js string, newDst func() any, reset func(any)) {
		b.Run(name+"/Adaptive", func(b *testing.B) {
			b.SetBytes(int64(len(js)))
			b.ReportAllocs()
			dst := newDst()
			if _, _, _, proj, _, err := unpackAndCompileRoot(dst); err == nil && proj != nil {
				atomic.StoreUint32(&proj.denseStreak, 0)
				atomic.StoreUint32(&proj.probeTick, 0)
			}
			for w := 0; w < 4; w++ {
				if reset != nil {
					reset(dst)
				}
				if _, _, err := DecodeOne(js, 0, dst); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if reset != nil {
					reset(dst)
				}
				if _, _, err := DecodeOne(js, 0, dst); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/ForceZeroCopy", func(b *testing.B) {
			b.SetBytes(int64(len(js)))
			b.ReportAllocs()
			dst := newDst()
			for i := 0; i < b.N; i++ {
				if reset != nil {
					reset(dst)
				}
				if err := decodeOneForceZeroCopyForBench(js, 0, dst); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/FullCopy", func(b *testing.B) {
			b.SetBytes(int64(len(js)))
			b.ReportAllocs()
			dst := newDst()
			for i := 0; i < b.N; i++ {
				if reset != nil {
					reset(dst)
				}
				if err := decodeOneFullCopyForBench(js, 0, dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	runPair("Wide40_Sparse_4KB", wide40Sparse4K, func() any { return new(wideBounded40) }, func(v any) { v.(*wideBounded40).S39 = v.(*wideBounded40).S39[:0] })
	runPair("Wide40_Sparse_14KB", wide40Sparse14K, func() any { return new(wideBounded40) }, func(v any) { v.(*wideBounded40).S39 = v.(*wideBounded40).S39[:0] })
	runPair("Wide40_Dense_2KB", wide40Dense2K, func() any { return new(wideBounded40) }, func(v any) { v.(*wideBounded40).S39 = v.(*wideBounded40).S39[:0] })
	runPair("Wide40_Dense_10KB", wide40Dense10K, func() any { return new(wideBounded40) }, func(v any) { v.(*wideBounded40).S39 = v.(*wideBounded40).S39[:0] })
	runPair("BigBlob6_Dense_12KB", bigBlobDense12K, func() any { return new(bigBlobBounded6) }, nil)
	runPair("FixedArr150_Dense_12KB", fixedArrDense12K, func() any { return new([150]smallBoundedItem) }, nil)
}

func TestSchemaProjDenseStreakRecovery(t *testing.T) {
	type wideBounded36 struct {
		F01, F02, F03, F04, F05, F06, F07, F08, F09, F10 int64
		F11, F12, F13, F14, F15, F16, F17, F18, F19, F20 int64
		S21, S22, S23, S24, S25, S26, S27, S28           string
		S29, S30, S31, S32, S33, S34, S35, S36           string
	}
	var dst wideBounded36
	_, _, _, proj, flatSD, err := unpackAndCompileRoot(&dst)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot: %v", err)
	}
	if flatSD != nil {
		t.Fatalf("expected wideBounded36 (>32 fields) not to use flatDirect")
	}
	if proj == nil || !proj.isBounded || proj.totalFields != 36 {
		t.Fatalf("unexpected projPlan: %+v", proj)
	}

	var denseSB, sparseSB strings.Builder
	denseSB.WriteByte('{')
	sparseSB.WriteByte('{')
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&denseSB, `"F%02d":%d,`, i, i)
		fmt.Fprintf(&sparseSB, `"F%02d":%d,`, i, i)
	}
	sparseSB.WriteString(`"unused_arr":[` + strings.Repeat(`{"x":1,"y":"ignored-payload"},`, 80) + `{"x":0}],`)
	blob := strings.Repeat("d", 300)
	for i := 21; i <= 36; i++ {
		sep := ","
		if i == 36 {
			sep = "}"
		}
		fmt.Fprintf(&denseSB, `"S%02d":%q%s`, i, blob, sep)
		fmt.Fprintf(&sparseSB, `"S%02d":"short"%s`, i, sep)
	}
	denseJSON := denseSB.String()
	sparseJSON := sparseSB.String()

	// 1. Wide bounded (>32 fields) sparse payload uses zero-copy schema projection.
	if !proj.shouldUse(len(sparseJSON)) {
		t.Fatalf("expected sparse wideBounded36 (%dB) to use zero-copy projection", len(sparseJSON))
	}

	// 2. Large (>8 KiB) dense bounded payload records denseStreak >= 2 and cools down to FullCopy.
	for i := 0; i < 3; i++ {
		if _, _, err := DecodeOne(denseJSON, 0, &dst); err != nil {
			t.Fatalf("DecodeOne dense: %v", err)
		}
	}
	if got := atomic.LoadUint32(&proj.denseStreak); got < 2 {
		t.Fatalf("expected denseStreak >= 2 on >8KiB dense bounded input, got %d", got)
	}

	// 3. Subsequent sparse calls recover denseStreak to 0 on the 1-in-256 probe (verifies single shouldUse increment per DecodeOne).
	for i := 0; i < 300; i++ {
		if _, _, err := DecodeOne(sparseJSON, 0, &dst); err != nil {
			t.Fatalf("DecodeOne sparse #%d: %v", i, err)
		}
		if atomic.LoadUint32(&proj.denseStreak) == 0 {
			break
		}
	}
	if got := atomic.LoadUint32(&proj.denseStreak); got != 0 {
		t.Fatalf("expected denseStreak to recover to 0 within 256 sparse calls, got %d", got)
	}
}

func TestFlatStructPreNullPointeeSemantics(t *testing.T) {
	type nestedPtrChild struct {
		P    *int64  `json:"p"`
		PStr *string `json:"pstr"`
	}
	type rootOnlyPtr struct {
		P    *int64  `json:"p"`
		PStr *string `json:"pstr"`
	}
	type nestedPlanPtr struct {
		Top   *int32         `json:"top"`
		Child nestedPtrChild `json:"child"`
	}

	// Verify both structs use flatDirect (root path and nested plan path respectively).
	var r0 rootOnlyPtr
	_, _, _, _, flatRoot, err := unpackAndCompileRoot(&r0)
	if err != nil || flatRoot == nil || !flatRoot.flatDirect || flatRoot.flatPlan != nil {
		t.Fatalf("expected rootOnlyPtr to use flatDirect root path, got %+v (err=%v)", flatRoot, err)
	}
	var n0 nestedPlanPtr
	_, _, _, _, flatPlanSD, err := unpackAndCompileRoot(&n0)
	if err != nil || flatPlanSD == nil || !flatPlanSD.flatDirect || flatPlanSD.flatPlan == nil {
		t.Fatalf("expected nestedPlanPtr to use flatDirect plan path, got %+v (err=%v)", flatPlanSD, err)
	}

	rootCases := []string{
		`{"p":1,"p":null}`,
		`{"p":1,"p":2,"p":null}`,
		`{"p":1,"p":null,"p":3}`,
		`{"p":1,"p":null,"p":3,"p":null}`,
		`{"pstr":"mutated","pstr":null}`,
		`{"pstr":"mutated","pstr":null,"pstr":"fresh"}`,
	}
	for _, opts := range []uint64{0, 1 << _F_copy_string} {
		for _, js := range rootCases {
			gotInt, wantInt := int64(99), int64(99)
			gotStr, wantStr := "orig", "orig"
			got := rootOnlyPtr{P: &gotInt, PStr: &gotStr}
			want := rootOnlyPtr{P: &wantInt, PStr: &wantStr}
			if err := json.Unmarshal([]byte(js), &want); err != nil {
				t.Fatalf("stdlib %s: %v", js, err)
			}
			if _, _, err := DecodeOne(js, opts, &got); err != nil {
				t.Fatalf("DecodeOne %s opts=%#x: %v", js, opts, err)
			}
			if !reflect.DeepEqual(got, want) || gotInt != wantInt || gotStr != wantStr {
				t.Fatalf("root %s opts=%#x mismatch:\n got=(%+v, gotInt=%d, gotStr=%q)\nwant=(%+v, wantInt=%d, wantStr=%q)",
					js, opts, got, gotInt, gotStr, want, wantInt, wantStr)
			}
		}
	}

	nestedCases := []string{
		`{"top":5,"top":null,"child":{"p":11,"p":null,"pstr":"nested-mut","pstr":null}}`,
		`{"top":5,"top":null,"top":8,"child":{"p":11,"p":null,"p":22,"pstr":"nested-mut","pstr":null,"pstr":"nested-fresh"}}`,
	}
	for _, opts := range []uint64{0, 1 << _F_copy_string} {
		for _, js := range nestedCases {
			gotTop, wantTop := int32(70), int32(70)
			gotInt, wantInt := int64(99), int64(99)
			gotStr, wantStr := "orig", "orig"
			got := nestedPlanPtr{Top: &gotTop, Child: nestedPtrChild{P: &gotInt, PStr: &gotStr}}
			want := nestedPlanPtr{Top: &wantTop, Child: nestedPtrChild{P: &wantInt, PStr: &wantStr}}
			if err := json.Unmarshal([]byte(js), &want); err != nil {
				t.Fatalf("stdlib nested %s: %v", js, err)
			}
			if _, _, err := DecodeOne(js, opts, &got); err != nil {
				t.Fatalf("DecodeOne nested %s opts=%#x: %v", js, opts, err)
			}
			if !reflect.DeepEqual(got, want) || gotTop != wantTop || gotInt != wantInt || gotStr != wantStr {
				t.Fatalf("nested %s opts=%#x mismatch:\n got=(%+v, gotTop=%d, gotInt=%d, gotStr=%q)\nwant=(%+v, wantTop=%d, wantInt=%d, wantStr=%q)",
					js, opts, got, gotTop, gotInt, gotStr, want, wantTop, wantInt, wantStr)
			}
		}
	}

	// Verify syntax error after {"p": 1, "p": null, ...} leaves pre-existing pointee 100% untouched on bailout.
	guardInt := int64(555)
	guard := rootOnlyPtr{P: &guardInt}
	if _, ok := tryDecodeFlatStruct(`{"p":1,"p":null, broken}`, 0, 0, unsafe.Pointer(&guard), flatRoot, false, true); ok {
		t.Fatalf("expected tryDecodeFlatStruct to fail on syntax error")
	}
	if guard.P != &guardInt || guardInt != 555 {
		t.Fatalf("preNull bailout mutated guard: P=%v guardInt=%d", guard.P, guardInt)
	}
}

func TestFromObjFastUseNumberAndUseInt64(t *testing.T) {
	// Struct with slice field so it bypasses flatDirect and exercises structDecoder.FromDom (Tape path),
	// while containing zero interface{} anywhere in its type graph.
	type nestedConcrete struct {
		Count int64   `json:"count"`
		Score float64 `json:"score"`
	}
	type concreteTapeStruct struct {
		ID     int64          `json:"id"`
		Name   string         `json:"name"`
		Values [][]int64      `json:"values"`
		Sub    nestedConcrete `json:"sub"`
	}
	type nestedWithAny struct {
		Payload any `json:"payload"`
	}
	type outerWithNestedAny struct {
		ID  int64         `json:"id"`
		Sub nestedWithAny `json:"sub"`
	}

	var c concreteTapeStruct
	_, _, decConcrete, _, flatConcrete, err := unpackAndCompileRoot(&c)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot concrete: %v", err)
	}
	if flatConcrete != nil {
		t.Fatalf("expected concreteTapeStruct (has slice) not to use flatDirect")
	}
	sdConcrete := decConcrete.(*structDecoder)
	if !sdConcrete.fast || !sdConcrete.noInterface {
		t.Fatalf("expected concreteTapeStruct fast=true noInterface=true, got fast=%v noInterface=%v", sdConcrete.fast, sdConcrete.noInterface)
	}

	var o outerWithNestedAny
	_, _, decAny, _, _, err := unpackAndCompileRoot(&o)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot any: %v", err)
	}
	sdAny := decAny.(*structDecoder)
	if sdAny.noInterface {
		t.Fatalf("expected outerWithNestedAny (nested interface{}) to have noInterface=false")
	}

	concreteJSON := `{"id":9007199254740993,"name":"tape-fast","values":[[1,2,3]],"sub":{"count":42,"score":-0},"unknown_big":1e999}`
	var gotNum, gotI64 concreteTapeStruct
	if _, _, err := DecodeOne(concreteJSON, 1<<_F_use_number, &gotNum); err != nil {
		t.Fatalf("DecodeOne concrete UseNumber: %v", err)
	}
	if gotNum.ID != 9007199254740993 || gotNum.Name != "tape-fast" || len(gotNum.Values) != 1 || len(gotNum.Values[0]) != 3 || gotNum.Sub.Count != 42 || !math.Signbit(gotNum.Sub.Score) {
		t.Fatalf("unexpected concrete UseNumber result: %+v", gotNum)
	}

	concreteValidJSON := `{"id":9007199254740993,"name":"tape-fast","values":[[1,2,3]],"sub":{"count":42,"score":-0}}`
	if _, _, err := DecodeOne(concreteValidJSON, 1<<_F_use_int64, &gotI64); err != nil {
		t.Fatalf("DecodeOne concrete UseInt64: %v", err)
	}
	if !reflect.DeepEqual(gotI64, gotNum) {
		t.Fatalf("UseInt64 vs UseNumber mismatch: %+v vs %+v", gotI64, gotNum)
	}

	// Verify nested interface{} still honors UseNumber and UseInt64!
	anyJSON := `{"id":7,"sub":{"payload":9007199254740993}}`
	var anyNum, anyI64 outerWithNestedAny
	if _, _, err := DecodeOne(anyJSON, 1<<_F_use_number, &anyNum); err != nil {
		t.Fatalf("DecodeOne any UseNumber: %v", err)
	}
	if jn, ok := anyNum.Sub.Payload.(json.Number); !ok || string(jn) != "9007199254740993" {
		t.Fatalf("expected json.Number, got %T (%v)", anyNum.Sub.Payload, anyNum.Sub.Payload)
	}
	if _, _, err := DecodeOne(anyJSON, 1<<_F_use_int64, &anyI64); err != nil {
		t.Fatalf("DecodeOne any UseInt64: %v", err)
	}
	if iv, ok := anyI64.Sub.Payload.(int64); !ok || iv != 9007199254740993 {
		t.Fatalf("expected int64, got %T (%v)", anyI64.Sub.Payload, anyI64.Sub.Payload)
	}
}

func TestNoValidateJSONSkipOptdec(t *testing.T) {
	// 1. flatDirect struct skipping deep (>64) unknown containers & long escaped/unescaped strings.
	type flatTarget struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Flag bool   `json:"flag"`
	}
	deepOpen := strings.Repeat(`{"nested":[`, 80)
	deepClose := strings.Repeat(`]}`, 80)
	longStr := strings.Repeat("alpha-beta-gamma-", 20) + `\"quoted\"` + strings.Repeat("-omega", 12)
	longRaw := "raw-\x01-\t-end-" + strings.Repeat("x", 160)

	deepJSON := fmt.Sprintf(`{"id":17,"skip_deep":%s%q%s,"skip_str":%q,"skip_raw":"`+longRaw+`","name":"ok","flag":true}`,
		deepOpen, longStr, deepClose, longStr)
	noValOpt := uint64(1 << _F_no_validate_json)

	// Force flatFailStreak == 0 after constructing target
	var got flatTarget
	if _, _, err := DecodeOne(deepJSON, noValOpt, &got); err != nil {
		t.Fatalf("flat NoValidateJSONSkip failed: %v", err)
	}
	if got.ID != 17 || got.Name != "ok" || !got.Flag {
		t.Fatalf("unexpected flatTarget result: %+v", got)
	}

	// Verify ValidateString still replaces invalid UTF-8 with \ufffd when matched or skipped strings contain non-UTF8 bytes.
	badUTF8WithMatched := fmt.Sprintf(`{"id":9,"skip_bad":{"k":"` + strings.Repeat("a", 80) + "\xff" + `"},"name":"ok` + "\xff" + `","flag":false}`)
	for _, extraOpt := range []uint64{0, 1 << _F_disable_urc} {
		var tmp flatTarget
		if _, _, err := DecodeOne(badUTF8WithMatched, noValOpt|(1<<_F_validate_string)|extraOpt, &tmp); err != nil {
			t.Fatalf("expected ValidateString (extraOpt=%#x) to replace invalid UTF-8, got error: %v", extraOpt, err)
		}
		if tmp.ID != 9 || tmp.Name != "ok\ufffd" || tmp.Flag {
			t.Fatalf("expected replacement character in matched field, got: %+v", tmp)
		}
	}
	guard := flatTarget{ID: 777, Name: "keep", Flag: true}

	// Syntax error in skipped container under NoValidateJSONSkip must rollback cleanly.
	brokenSkipped := fmt.Sprintf(`{"id":123,"skip_broken":{"a":[1,2,3,"` + strings.Repeat("z", 120))
	if _, _, err := DecodeOne(brokenSkipped, noValOpt, &guard); err == nil {
		t.Fatalf("expected error on unclosed skipped string")
	}
	if guard.ID != 777 || guard.Name != "keep" || !guard.Flag {
		t.Fatalf("syntax error mutated guard: %+v", guard)
	}

	// Matched string field MUST still reject invalid escape even when _F_no_validate_json is set.
	invalidMatched := `{"id":1,"name":"bad\qescape","flag":false}`
	if _, _, err := DecodeOne(invalidMatched, noValOpt, &guard); err == nil {
		t.Fatalf("expected invalid escape in matched string field to fail")
	}
	if guard.ID != 777 || guard.Name != "keep" || !guard.Flag {
		t.Fatalf("matched invalid escape mutated guard: %+v", guard)
	}

	// 2. schemaScanner struct (with []string slice -> proj != nil, flatSD == nil), zero fallback under NoValidateJSONSkip.
	type projTarget struct {
		ID      int64    `json:"id"`
		Tags    []string `json:"tags"`
		Name    string   `json:"name"`
		Payload []byte   `json:"payload"`
	}
	var probe projTarget
	_, _, _, proj, flatSD, err := unpackAndCompileRoot(&probe)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot failed: %v", err)
	}
	if flatSD != nil || proj == nil {
		t.Fatalf("expected proj != nil and flatSD == nil, got flatSD=%v proj=%v", flatSD, proj)
	}
	schemaJSON := fmt.Sprintf(`{"id":55,"tags":["t1","t2"],"audit":%s%q%s,"long_note":%q,"name":"schema-ok"}`,
		deepOpen, longStr, deepClose, longStr)
	if !proj.shouldUse(len(schemaJSON)) {
		t.Fatalf("expected proj.shouldUse(%d) == true", len(schemaJSON))
	}
	beforeFallback := atomic.LoadUint64(&schemaFallbackCount)
	var gotProj projTarget
	if _, _, err := DecodeOne(schemaJSON, noValOpt|(1<<_F_copy_string), &gotProj); err != nil {
		t.Fatalf("schemaScanner NoValidateJSONSkip failed: %v", err)
	}
	if after := atomic.LoadUint64(&schemaFallbackCount); after != beforeFallback {
		t.Fatalf("schemaScanner triggered %d fallback(s) (expected 0)", after-beforeFallback)
	}
	if gotProj.ID != 55 || gotProj.Name != "schema-ok" || !reflect.DeepEqual(gotProj.Tags, []string{"t1", "t2"}) {
		t.Fatalf("unexpected projTarget result: %+v", gotProj)
	}

	// Same schemaScanner struct with ValidateString + NoValidateJSONSkip + invalid UTF-8 replaces invalid bytes in matched fields.
	badProjUTF8 := fmt.Sprintf(`{"id":55,"tags":["t1"],"audit":{"big":%q,"bad":"`+"\xff"+`"},"name":"x`+"\xff"+`"}`, longStr)
	var gotProjUTF8 projTarget
	if _, _, err := DecodeOne(badProjUTF8, noValOpt|(1<<_F_validate_string), &gotProjUTF8); err != nil {
		t.Fatalf("unexpected error in schemaScanner invalid UTF-8 replacement: %v", err)
	}
	if gotProjUTF8.Name != "x\ufffd" {
		t.Fatalf("expected replaced Name in schemaScanner, got %q", gotProjUTF8.Name)
	}
	var guardProj projTarget

	// 3. Semantic matrix for skipped strings without _F_no_validate_json:
	// - ConfigDefault (opts=0): allows raw < 0x20, rejects invalid escape \q
	// - ConfigStd (opts=_F_validate_string): rejects raw < 0x20, allows lone surrogate \uD800
	// - UseUnicodeErrors (_F_validate_string | _F_disable_urc): rejects lone surrogate \uD800
	rawCtrlLong := "lead-" + strings.Repeat("a", 80) + "\x01-tail"
	schemaRawCtrl := fmt.Sprintf(`{"id":1,"tags":["a"],"skip":"%s","name":"ok","pad":%q}`, rawCtrlLong, longStr)
	if _, _, err := DecodeOne(schemaRawCtrl, 0, &gotProj); err != nil {
		t.Fatalf("ConfigDefault should allow raw control byte in skipped string: %v", err)
	}
	if _, _, err := DecodeOne(schemaRawCtrl, 1<<_F_validate_string, &guardProj); err == nil {
		t.Fatalf("ConfigStd must reject raw control byte in skipped string")
	}

	invalidEscSkipped := fmt.Sprintf(`{"id":1,"tags":["a"],"skip":"`+strings.Repeat("b", 80)+`\q","name":"ok","pad":%q}`, longStr)
	if _, _, err := DecodeOne(invalidEscSkipped, 0, &guardProj); err == nil {
		t.Fatalf("ConfigDefault must reject invalid escape in skipped string")
	}

	loneSurrogateSkipped := fmt.Sprintf(`{"id":1,"tags":["a"],"skip":"`+strings.Repeat("c", 80)+`\uD800","name":"ok","pad":%q}`, longStr)
	if _, _, err := DecodeOne(loneSurrogateSkipped, 1<<_F_validate_string, &gotProj); err != nil {
		t.Fatalf("ConfigStd without UseUnicodeErrors should allow lone surrogate in skipped string: %v", err)
	}
	if _, _, err := DecodeOne(loneSurrogateSkipped, (1<<_F_validate_string)|(1<<_F_disable_urc), &guardProj); err == nil {
		t.Fatalf("OptionUseUnicodeErrors must reject lone surrogate in skipped string")
	}
}

func TestFlatContainersComprehensive(t *testing.T) {
	type author struct {
		Name string `json:"name"`
		Age  int64  `json:"age"`
		Male bool   `json:"male"`
	}
	type containerAll struct {
		I    []int     `json:"i"`
		I64  []int64   `json:"i64"`
		I32  []int32   `json:"i32"`
		U    []uint    `json:"u"`
		U64  []uint64  `json:"u64"`
		U32  []uint32  `json:"u32"`
		F64  []float64 `json:"f64"`
		F32  []float32 `json:"f32"`
		B    []bool    `json:"b"`
		S    []string  `json:"s"`
		ArrI [4]int32  `json:"arri"`
		ArrS [3]string `json:"arrs"`
		Auth []author  `json:"authors"`
	}
	var c0 containerAll
	_, _, _, _, flatSD, err := unpackAndCompileRoot(&c0)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot containerAll: %v", err)
	}
	if flatSD == nil || flatSD.flatPlan == nil || !flatSD.flatPlan.hasContainers {
		t.Fatalf("expected containerAll to use flatPlan with hasContainers=true, got flatSD=%v", flatSD)
	}

	// 1. Decode all scalar slices, strings (CopyString + zero-copy), fixed arrays, and flat []Struct.
	src1 := `{"i":[-1,2],"i64":[-99,100],"i32":[7,-8],"u":[1,2],"u64":[3,4],"u32":[5,6],"f64":[1.5,-0],"f32":[2.5,3.25],"b":[true,false],"s":["alpha","beta","gamma"],"arri":[10,20],"arrs":["x","y","z","dropme"],"authors":[{"name":"alice","age":30,"male":false},{"age":41,"unknown":{"k":[1,2]},"name":"bob","name":"bob2"}]}`
	buf := []byte(src1)
	sBuf := string(buf)
	var gotCopy, gotZC containerAll
	if _, _, err := DecodeOne(sBuf, (1<<_F_validate_string)|(1<<_F_copy_string), &gotCopy); err != nil {
		t.Fatalf("DecodeOne CopyString: %v", err)
	}
	if _, _, err := DecodeOne(sBuf, 0, &gotZC); err != nil {
		t.Fatalf("DecodeOne zero-copy: %v", err)
	}
	if !reflect.DeepEqual(gotCopy, gotZC) {
		t.Fatalf("CopyString vs zero-copy mismatch: %+v vs %+v", gotCopy, gotZC)
	}
	want1 := containerAll{
		I:    []int{-1, 2},
		I64:  []int64{-99, 100},
		I32:  []int32{7, -8},
		U:    []uint{1, 2},
		U64:  []uint64{3, 4},
		U32:  []uint32{5, 6},
		F64:  []float64{1.5, math.Copysign(0, -1)},
		F32:  []float32{2.5, 3.25},
		B:    []bool{true, false},
		S:    []string{"alpha", "beta", "gamma"},
		ArrI: [4]int32{10, 20, 0, 0},
		ArrS: [3]string{"x", "y", "z"},
		Auth: []author{{Name: "alice", Age: 30, Male: false}, {Name: "bob2", Age: 41}},
	}
	if !reflect.DeepEqual(gotCopy, want1) || !math.Signbit(gotCopy.F64[1]) {
		t.Fatalf("unexpected containerAll decode:\ngot:  %+v\nwant: %+v", gotCopy, want1)
	}
	if flatSD.flatFailStreak != 0 {
		t.Fatalf("expected flatFailStreak=0, got %d", flatSD.flatFailStreak)
	}

	// CopyString must detach backing memory from bMut, whereas zero-copy borrows.
	bMut := []byte(`{"s":["alpha","beta"]}`)
	sView := unsafe.String(&bMut[0], len(bMut))
	var detached, borrowed containerAll
	if _, _, err := DecodeOne(sView, 1<<_F_copy_string, &detached); err != nil {
		t.Fatalf("DecodeOne CopyString mutable: %v", err)
	}
	if _, _, err := DecodeOne(sView, 0, &borrowed); err != nil {
		t.Fatalf("DecodeOne zero-copy mutable: %v", err)
	}
	copy(bMut[7:12], "ZZZZZ")
	if detached.S[0] != "alpha" {
		t.Fatalf("expected CopyString slice element to stay detached, got %q", detached.S[0])
	}
	if borrowed.S[0] != "ZZZZZ" {
		t.Fatalf("expected zero-copy slice element to alias source, got %q", borrowed.S[0])
	}

	// 2. Fixed array partial overwrite + null no-op + empty/null struct slice.
	reuseArr := containerAll{
		ArrI: [4]int32{91, 92, 93, 94},
		ArrS: [3]string{"old0", "old1", "old2"},
	}
	if _, _, err := DecodeOne(`{"arri":[5],"arrs":null,"authors":[]}`, 0, &reuseArr); err != nil {
		t.Fatalf("DecodeOne array/slice edge: %v", err)
	}
	if reuseArr.ArrI != [4]int32{5, 0, 0, 0} || reuseArr.ArrS != [3]string{"old0", "old1", "old2"} {
		t.Fatalf("unexpected fixed array reuse/null result: arri=%v arrs=%v", reuseArr.ArrI, reuseArr.ArrS)
	}
	if reuseArr.Auth == nil || len(reuseArr.Auth) != 0 {
		t.Fatalf("expected non-nil empty authors slice, got %#v", reuseArr.Auth)
	}
	if _, _, err := DecodeOne(`{"authors":null}`, 0, &reuseArr); err != nil {
		t.Fatalf("DecodeOne authors null: %v", err)
	}
	if reuseArr.Auth != nil {
		t.Fatalf("expected authors=nil after null, got %#v", reuseArr.Auth)
	}

	// DisallowUnknownFields on []Author falls back cleanly and rejects unknown key.
	var guardDis containerAll
	if _, _, err := DecodeOne(`{"authors":[{"name":"x","extra":1}]}`, uint64(OptionDisableUnknown), &guardDis); err == nil {
		t.Fatalf("expected DisallowUnknownFields error inside []Author")
	}

	// 3. Slice reuse: shrink clears tail strings and []Author tail; [] preserves cap; null sets nil.
	reused := containerAll{
		S:    make([]string, 3, 5),
		Auth: make([]author, 3, 4),
	}
	reused.S[0], reused.S[1], reused.S[2] = "s0", "s1", "s2"
	reused.Auth[0] = author{Name: "a0", Age: 1, Male: true}
	reused.Auth[1] = author{Name: "a1", Age: 2, Male: true}
	reused.Auth[2] = author{Name: "a2", Age: 3, Male: false}
	sBack := reused.S[:5]
	authBack := reused.Auth[:4]

	if _, _, err := DecodeOne(`{"s":["only"],"authors":[{"age":99}]}`, 0, &reused); err != nil {
		t.Fatalf("DecodeOne shrink reuse: %v", err)
	}
	if !reflect.DeepEqual(reused.S, []string{"only"}) || cap(reused.S) != 5 {
		t.Fatalf("unexpected reused.S after shrink: %#v (cap=%d)", reused.S, cap(reused.S))
	}
	if sBack[1] != "" || sBack[2] != "" {
		t.Fatalf("expected cleared tail strings, got %#v", sBack)
	}
	if len(reused.Auth) != 1 || cap(reused.Auth) != 4 || reused.Auth[0].Name != "a0" || reused.Auth[0].Age != 99 {
		t.Fatalf("unexpected reused.Auth merge/shrink: %+v (cap=%d)", reused.Auth, cap(reused.Auth))
	}
	if authBack[1] != (author{}) || authBack[2] != (author{}) {
		t.Fatalf("expected cleared []Author tail, got %+v", authBack)
	}
	if _, _, err := DecodeOne(`{"s":[]}`, 0, &reused); err != nil {
		t.Fatalf("DecodeOne empty slice reuse: %v", err)
	}
	if reused.S == nil || len(reused.S) != 0 || cap(reused.S) != 5 || sBack[0] != "" {
		t.Fatalf("expected preserved cap=5 and cleared elem on []: len=%d cap=%d s0=%q", len(reused.S), cap(reused.S), sBack[0])
	}
	if _, _, err := DecodeOne(`{"s":null}`, 0, &reused); err != nil {
		t.Fatalf("DecodeOne null slice reuse: %v", err)
	}
	if reused.S != nil {
		t.Fatalf("expected s=nil after null, got %#v", reused.S)
	}

	// 4. Atomic bailout without mutating caller vp on threshold exceed, [1,null,3], escaped string, overflow, trailing error.
	sentinel := containerAll{
		I64:  []int64{999},
		S:    []string{"keep"},
		ArrI: [4]int32{1, 2, 3, 4},
		Auth: []author{{Name: "keep", Age: 7}},
	}
	// Element count > flatMaxElemPool (65 ints) falls back cleanly to standard decoder (succeeds, accurate result).
	elems65 := strings.Repeat("1,", 64) + "2"
	var gotBig containerAll
	if _, _, err := DecodeOne(`{"i64":[`+elems65+`]}`, 0, &gotBig); err != nil || len(gotBig.I64) != 65 || gotBig.I64[64] != 2 {
		t.Fatalf("expected fallback success on 65 ints, got err=%v len=%d", err, len(gotBig.I64))
	}
	// []Author count > flatMaxStructMasks (17 objects) falls back cleanly.
	auth17 := strings.Repeat(`{"name":"a"},`, 16) + `{"name":"last"}`
	gotBig = containerAll{}
	if _, _, err := DecodeOne(`{"authors":[`+auth17+`]}`, 0, &gotBig); err != nil || len(gotBig.Auth) != 17 || gotBig.Auth[16].Name != "last" {
		t.Fatalf("expected fallback success on 17 authors, got err=%v len=%d", err, len(gotBig.Auth))
	}
	// Element contains null: [1,null,3] falls back to standard decoder cleanly.
	var gotNullElem containerAll
	if _, _, err := DecodeOne(`{"i64":[1,null,3]}`, 0, &gotNullElem); err != nil || !reflect.DeepEqual(gotNullElem.I64, []int64{1, 0, 3}) {
		t.Fatalf("expected [1,0,3] on element null fallback, got err=%v vals=%v", err, gotNullElem.I64)
	}
	// Escaped string in slice falls back cleanly.
	var gotEsc containerAll
	if _, _, err := DecodeOne(`{"s":["a\nb"]}`, 0, &gotEsc); err != nil || !reflect.DeepEqual(gotEsc.S, []string{"a\nb"}) {
		t.Fatalf("expected escaped slice string fallback, got err=%v vals=%#v", err, gotEsc.S)
	}
	// Overflow and syntax errors must return error cleanly and must NOT mutate caller struct during direct bailout.
	badInputs := []string{
		`{"s":["mutated"],"i32":[2147483648]}`,
		`{"s":["mutated"],"i64":[1,2],"arri":[9],}`,
		`{"s":["mutated"],"authors":[{"name":"x"}],"i64":[1,2] bad}`,
	}
	for _, bad := range badInputs {
		cp := containerAll{
			I64:  append([]int64(nil), sentinel.I64...),
			S:    append([]string(nil), sentinel.S...),
			ArrI: sentinel.ArrI,
			Auth: append([]author(nil), sentinel.Auth...),
		}
		if _, _, err := DecodeOne(bad, 0, &cp); err == nil {
			t.Fatalf("expected error on %s", bad)
		}
	}
	// Verify tryDecodeFlatContainerPlanBody itself leaves vp untouched on overflow, syntax errors, and trailing invalid UTF-8.
	badWithTrailingUTF8 := []string{
		`{"s":["mutated"],"i32":[2147483648]}`,
		`{"s":["mutated"],"i64":[1,2],"arri":[9],}`,
		`{"s":["mutated"],"authors":[{"name":"x"}],"i64":[1,2] bad}`,
		"{\"s\":[\"mutated\"]} \xff",
	}
	for _, bad := range badWithTrailingUTF8 {
		cp := containerAll{
			I64:  append([]int64(nil), sentinel.I64...),
			S:    append([]string(nil), sentinel.S...),
			ArrI: sentinel.ArrI,
			Auth: append([]author(nil), sentinel.Auth...),
		}
		if _, ok := tryDecodeFlatContainerPlanBody(bad, 0, len(bad), 1<<_F_validate_string, unsafe.Pointer(&cp), flatSD.flatPlan, false, true); ok {
			t.Fatalf("expected direct bailout on %q", bad)
		}
		if !reflect.DeepEqual(cp, sentinel) {
			t.Fatalf("direct bailout mutated caller struct on %q:\ngot:  %+v\nwant: %+v", bad, cp, sentinel)
		}
	}

	// 5. Verify benchBook layout hits flatDirect + hasContainers with 0 fallback.
	type benchAuthor struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
		Male bool   `json:"male"`
	}
	type benchBook struct {
		BookId  int64         `json:"book_id"`
		BookIds []int64       `json:"book_ids"`
		Title   string        `json:"title"`
		Titles  []string      `json:"titles"`
		Price   float64       `json:"price"`
		Prices  []float64     `json:"prices"`
		Hot     bool          `json:"hot"`
		Hots    []bool        `json:"hots"`
		Author  benchAuthor   `json:"author"`
		Authors []benchAuthor `json:"authors"`
		Weights []int64       `json:"weights"`
	}
	var b0 benchBook
	_, _, _, _, bookSD, err := unpackAndCompileRoot(&b0)
	if err != nil {
		t.Fatalf("unpackAndCompileRoot benchBook: %v", err)
	}
	if bookSD == nil || bookSD.flatPlan == nil || !bookSD.flatPlan.hasContainers {
		t.Fatalf("expected benchBook to use flatPlan with hasContainers=true, got bookSD=%v", bookSD)
	}
	bookJSON := `{"book_id":123,"book_ids":[1,2,3],"title":"Go Programming","titles":["a","b"],"price":49.5,"prices":[1.1,2.2],"hot":true,"hots":[true,false],"author":{"name":"Alice","age":30,"male":false},"authors":[{"name":"Bob","age":25,"male":true}],"weights":[10,20,30]}`
	bookSD.flatFailStreak = 0
	var gotBook benchBook
	if _, _, err := DecodeOne(bookJSON, 1<<_F_copy_string, &gotBook); err != nil {
		t.Fatalf("DecodeOne benchBook: %v", err)
	}
	if bookSD.flatFailStreak != 0 || gotBook.BookId != 123 || len(gotBook.Authors) != 1 || gotBook.Authors[0].Name != "Bob" {
		t.Fatalf("unexpected benchBook result (streak=%d): %+v", bookSD.flatFailStreak, gotBook)
	}
}

func BenchmarkSmallBookFlatDirect(b *testing.B) {
	type benchAuthor struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
		Male bool   `json:"male"`
	}
	type benchBook struct {
		BookId  int64         `json:"book_id"`
		BookIds []int64       `json:"book_ids"`
		Title   string        `json:"title"`
		Titles  []string      `json:"titles"`
		Price   float64       `json:"price"`
		Prices  []float64     `json:"prices"`
		Hot     bool          `json:"hot"`
		Hots    []bool        `json:"hots"`
		Author  benchAuthor   `json:"author"`
		Authors []benchAuthor `json:"authors"`
		Weights []int64       `json:"weights"`
	}
	bookJSON := `{"book_id":12125925,"book_ids":[1,2,3,4,5],"title":"Twitter API documentation","titles":["v1","v2","v3"],"price":99.95,"prices":[10.5,20.25,30.75],"hot":true,"hots":[true,false,true],"author":{"name":"Daniel","age":28,"male":true},"authors":[{"name":"Alice","age":24,"male":false},{"name":"Bob","age":31,"male":true}],"weights":[100,200,300]}`
	b.Run("New_CopyStr", func(b *testing.B) {
		b.SetBytes(int64(len(bookJSON)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var v benchBook
			if _, _, err := DecodeOne(bookJSON, 1<<_F_copy_string, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Reuse_CopyStr", func(b *testing.B) {
		var v benchBook
		_, _, _ = DecodeOne(bookJSON, 1<<_F_copy_string, &v)
		b.SetBytes(int64(len(bookJSON)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := DecodeOne(bookJSON, 1<<_F_copy_string, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("New_ZeroCopy_CaseSens", func(b *testing.B) {
		b.SetBytes(int64(len(bookJSON)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var v benchBook
			if _, _, err := DecodeOne(bookJSON, uint64(consts.OptionCaseSensitive), &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Reuse_ZeroCopy_CaseSens", func(b *testing.B) {
		var v benchBook
		_, _, _ = DecodeOne(bookJSON, uint64(consts.OptionCaseSensitive), &v)
		b.SetBytes(int64(len(bookJSON)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, err := DecodeOne(bookJSON, uint64(consts.OptionCaseSensitive), &v); err != nil {
				b.Fatal(err)
			}
		}
	})
}
