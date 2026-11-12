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
