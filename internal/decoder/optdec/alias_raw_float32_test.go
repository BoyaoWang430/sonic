//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"
)

func TestFlatPrePopulatedAliasPreservesWriteOrder(t *testing.T) {
	// 1. Shared pointer target: {"b":2,"a":1} must leave x == 1, not 2.
	type SharedPtrStruct struct {
		A *int `json:"a"`
		B *int `json:"b"`
	}
	_, _, _, _, flatSD, err := unpackAndCompileRoot(&SharedPtrStruct{})
	if err != nil || flatSD == nil {
		t.Fatalf("expected flatSD != nil, err=%v", err)
	}
	atomic.StoreUint32(&flatSD.flatFailStreak, 0)

	for _, src := range []string{
		`{"b":2,"a":1}`,
		`{"a":1,"b":2}`,
		`{"b":2,"a":null}`,
		`{"a":null,"b":2}`,
	} {
		xGot, xStd := 99, 99
		got := SharedPtrStruct{A: &xGot, B: &xGot}
		want := SharedPtrStruct{A: &xStd, B: &xStd}
		_, end, errGot := DecodeOne(src, diffStdOpts, &got)
		errGot = checkSingleDocEnd(src, end, errGot)
		errStd := json.Unmarshal([]byte(src), &want)
		if (errGot != nil) != (errStd != nil) {
			t.Fatalf("err mismatch on %s: got=%v std=%v", src, errGot, errStd)
		}
		if xGot != xStd || (got.A == nil) != (want.A == nil) || (got.B == nil) != (want.B == nil) {
			t.Fatalf("shared ptr mismatch on %s: xGot=%d xStd=%d got=%+v want=%+v", src, xGot, xStd, got, want)
		}
	}
	if streak := atomic.LoadUint32(&flatSD.flatFailStreak); streak != 0 {
		t.Fatalf("alias guard bailout must not increment flatFailStreak, got %d", streak)
	}

	// 2. Self-referential pointer into sibling field of same struct.
	type SelfPtrStruct struct {
		A *int `json:"a"`
		B int  `json:"b"`
	}
	for _, src := range []string{`{"b":20,"a":10}`, `{"a":10,"b":20}`} {
		var got, want SelfPtrStruct
		got.B, want.B = 7, 7
		got.A = &got.B
		want.A = &want.B
		_, end, errGot := DecodeOne(src, diffStdOpts, &got)
		if checkSingleDocEnd(src, end, errGot) != nil {
			t.Fatalf("DecodeOne failed on %s: %v", src, errGot)
		}
		if err := json.Unmarshal([]byte(src), &want); err != nil {
			t.Fatal(err)
		}
		if got.B != want.B || *got.A != *want.A {
			t.Fatalf("self ptr mismatch on %s: got.B=%d want.B=%d", src, got.B, want.B)
		}
	}

	// 3. Shared backing array across two slice fields + pointer into slice backing array.
	type SharedSliceStruct struct {
		P *int  `json:"p"`
		X []int `json:"x"`
		Y []int `json:"y"`
	}
	for _, src := range []string{
		`{"y":[9,8],"x":[1,2]}`,
		`{"x":[1,2],"y":[9,8]}`,
		`{"p":77,"x":[1,2,3]}`,
		`{"x":[1,2,3],"p":77}`,
	} {
		bGot := []int{5, 6, 7, 8}
		bStd := []int{5, 6, 7, 8}
		got := SharedSliceStruct{P: &bGot[1], X: bGot[:2:4], Y: bGot[:2:4]}
		want := SharedSliceStruct{P: &bStd[1], X: bStd[:2:4], Y: bStd[:2:4]}
		_, end, errGot := DecodeOne(src, diffStdOpts, &got)
		if checkSingleDocEnd(src, end, errGot) != nil {
			t.Fatalf("DecodeOne failed on %s: %v", src, errGot)
		}
		if err := json.Unmarshal([]byte(src), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(bGot, bStd) || !reflect.DeepEqual(got.X, want.X) || !reflect.DeepEqual(got.Y, want.Y) || *got.P != *want.P {
			t.Fatalf("shared slice/ptr backing mismatch on %s:\n  bGot=%v bStd=%v\n  got=%+v want=%+v", src, bGot, bStd, got, want)
		}
	}
}

type rawGuardUnmarshaler struct {
	called bool
	raw    string
}

func (g *rawGuardUnmarshaler) UnmarshalJSON(b []byte) error {
	g.called = true
	g.raw = string(b)
	return nil
}

func TestRawMessageHugeExponentAllPaths(t *testing.T) {
	// 1. Root RawMessage nested 65 levels deep (> schema 64-level container bit-stack -> full Runs fallback).
	deep65 := strings.Repeat("[", 65) + "1e999" + strings.Repeat("]", 65)
	var gotRaw json.RawMessage
	_, end, err := DecodeOne(deep65, diffStdOpts, &gotRaw)
	if err := checkSingleDocEnd(deep65, end, err); err != nil {
		t.Fatalf("root 65-deep RawMessage failed: %v", err)
	}
	if string(gotRaw) != deep65 {
		t.Fatalf("root 65-deep RawMessage mismatch: len=%d want=%d", len(gotRaw), len(deep65))
	}

	// 2. Outer struct containing both RawMessage (65-deep 1e999) and sibling interface{} (must stay float64, not json.Number!).
	type MixedEnvelope struct {
		Guard rawGuardUnmarshaler `json:"guard"`
		Raw   json.RawMessage     `json:"raw"`
		Val   any                 `json:"val"`
	}
	envSrc := `{"guard":{"ok":1e999},"raw":` + deep65 + `,"val":1.5}`
	var env MixedEnvelope
	_, end, err = DecodeOne(envSrc, diffStdOpts, &env)
	if err := checkSingleDocEnd(envSrc, end, err); err != nil {
		t.Fatalf("MixedEnvelope 65-deep RawMessage failed: %v", err)
	}
	if string(env.Raw) != deep65 || env.Guard.raw != `{"ok":1e999}` {
		t.Fatalf("MixedEnvelope raw mismatch: rawLen=%d guard=%q", len(env.Raw), env.Guard.raw)
	}
	if f, ok := env.Val.(float64); !ok || f != 1.5 {
		t.Fatalf("MixedEnvelope sibling any field changed type: got %#v (%T), want float64(1.5)", env.Val, env.Val)
	}

	// Sibling any field with 1e999 (without UseNumber) must fail BEFORE Guard.UnmarshalJSON runs (SYN-PREMUT).
	var badEnv MixedEnvelope
	badAnySrc := `{"guard":{"ok":1},"raw":[1e999],"val":1e999}`
	_, _, err = DecodeOne(badAnySrc, diffStdOpts, &badEnv)
	if err == nil {
		t.Fatal("expected float infinity error for sibling any=1e999 without UseNumber")
	}
	if badEnv.Guard.called {
		t.Fatal("Guard.UnmarshalJSON was called before rejecting sibling any=1e999")
	}

	// Skipped unknown field with 1e999 on a fallback-forced struct must ALSO preserve SYN-PREMUT.
	var badSkipEnv MixedEnvelope
	badSkipSrc := `{"guard":{"ok":1},"raw":` + deep65 + `,"unknown_inf":1e999}`
	_, _, err = DecodeOne(badSkipSrc, diffStdOpts, &badSkipEnv)
	if err == nil {
		t.Fatal("expected float infinity error for skipped unknown field 1e999")
	}
	if badSkipEnv.Guard.called {
		t.Fatal("Guard.UnmarshalJSON was called before rejecting unknown_inf=1e999")
	}

	// 3. Giant (>4 MiB) MixedEnvelope with 65-deep 1e999 RawMessage.
	giantEnvSrc := `{"pad":"` + strings.Repeat("p", (4<<20)+1024) + `","guard":[1e999],"raw":` + deep65 + `,"val":2.25}`
	var giantEnv MixedEnvelope
	_, end, err = DecodeOne(giantEnvSrc, diffStdOpts, &giantEnv)
	if err := checkSingleDocEnd(giantEnvSrc, end, err); err != nil {
		t.Fatalf("Giant MixedEnvelope failed: %v", err)
	}
	if string(giantEnv.Raw) != deep65 || giantEnv.Val != float64(2.25) {
		t.Fatalf("Giant MixedEnvelope mismatch: rawLen=%d val=%#v (%T)", len(giantEnv.Raw), giantEnv.Val, giantEnv.Val)
	}

	// 4. Incremental StreamDecoder (chunked DecodeStream) parity on RawMessage vs any.
	gotStreamRaw, err := decodeViaChunkedStreamWithInit[json.RawMessage](`[1e999, -2.5e+9999]`, diffStdOpts, 3, nil)
	if err != nil || string(gotStreamRaw) != `[1e999, -2.5e+9999]` {
		t.Fatalf("DecodeStream RawMessage failed: err=%v got=%q", err, string(gotStreamRaw))
	}
	gotStreamEnv, err := decodeViaChunkedStreamWithInit[MixedEnvelope](envSrc, diffStdOpts, 5, nil)
	if err != nil || string(gotStreamEnv.Raw) != deep65 || gotStreamEnv.Val != float64(1.5) {
		t.Fatalf("DecodeStream MixedEnvelope failed: err=%v val=%#v (%T)", err, gotStreamEnv.Val, gotStreamEnv.Val)
	}
	if _, err := decodeViaChunkedStreamWithInit[any](`[1e999]`, diffStdOpts, 3, nil); err == nil {
		t.Fatal("expected DecodeStream into any without UseNumber to reject [1e999]")
	}
}

func TestFloat32SingleRoundingAllShapes(t *testing.T) {
	// Literals where decimal -> float64 -> float32 double rounding diverges from
	// single-step decimal -> float32 rounding, plus subnormals and MaxFloat32 boundaries.
	literals := []string{
		"1.0000000596046448",     // > midpoint 1 + 2^-24: exact f32 = 0x3f800001 (double-rounded = 0x3f800000)
		"1.0000000596046447",     // < midpoint 1 + 2^-24: exact f32 = 0x3f800000
		"-1.0000000596046448",    // negative midpoint: exact f32 = 0xbf800001
		"1.0000001788139343",     // < odd midpoint 1 + 3*2^-24: exact f32 = 0x3f800001 (f64 rounds to midpoint -> 0x3f800002)
		"1.0000001788139344",     // > odd midpoint 1 + 3*2^-24: exact f32 = 0x3f800002
		"1.401298464324817e-45",  // MinNonzeroFloat32
		"7.006492321624086e-46",  // > 0.5 * MinNonzeroFloat32 -> rounds up to 0x00000001
		"7.006492321624085e-46",  // <= 0.5 * MinNonzeroFloat32 -> rounds to +0
		"1.1754942807573643e-38", // subnormal/normal boundary near 2^-126
		"3.4028235677973364e+38", // just below MaxFloat32/+Inf midpoint -> MaxFloat32
		"3.4028235677973366e+38", // at/above MaxFloat32/+Inf midpoint -> overflow error
	}

	type f32AllShapes struct {
		Val float32            `json:"val"`
		Ptr *float32           `json:"ptr"`
		Slc []float32          `json:"slc"`
		Arr [1]float32         `json:"arr"`
		Map map[string]float32 `json:"map"`
		Str float32            `json:"str,string"`
	}
	type f32FlatOnly struct {
		Val float32    `json:"val"`
		Ptr *float32   `json:"ptr"`
		Slc []float32  `json:"slc"`
		Arr [1]float32 `json:"arr"`
	}

	for _, lit := range literals {
		var stdScalar float32
		errStd := json.Unmarshal([]byte(lit), &stdScalar)
		wantBits := math.Float32bits(stdScalar)

		// Scalar across Router, Runs, Giant, Stream, UseNumber
		for _, opt := range []uint64{diffStdOpts, diffUseNumberOpts} {
			var gotScalar float32
			_, end, errGot := DecodeOne(lit, opt, &gotScalar)
			errGot = checkSingleDocEnd(lit, end, errGot)
			if (errGot != nil) != (errStd != nil) {
				t.Fatalf("scalar err mismatch on %s (opt=0x%x): got=%v std=%v", lit, opt, errGot, errStd)
			}
			if errStd == nil && math.Float32bits(gotScalar) != wantBits {
				t.Fatalf("scalar bits mismatch on %s (opt=0x%x): got=0x%08x want=0x%08x", lit, opt, math.Float32bits(gotScalar), wantBits)
			}
		}

		// Flat struct (Val, *float32, []float32, [1]float32)
		flatDoc := fmt.Sprintf(`{"val":%s,"ptr":%s,"slc":[%s],"arr":[%s]}`, lit, lit, lit, lit)
		var gotFlat f32FlatOnly
		_, flatSD := mustCompileFlatSD[f32FlatOnly](t)
		endFlat, okFlat := tryDecodeFlatStructForced(flatDoc, 0, diffStdOpts, unsafe.Pointer(&gotFlat), flatSD, false, true)
		if errStd == nil {
			if !okFlat || endFlat != len(flatDoc) {
				t.Fatalf("expected flat path to accept %s, okFlat=%v end=%d", lit, okFlat, endFlat)
			}
			if math.Float32bits(gotFlat.Val) != wantBits ||
				math.Float32bits(*gotFlat.Ptr) != wantBits ||
				math.Float32bits(gotFlat.Slc[0]) != wantBits ||
				math.Float32bits(gotFlat.Arr[0]) != wantBits {
				t.Fatalf("flat bits mismatch on %s: val=0x%08x ptr=0x%08x slc=0x%08x arr=0x%08x want=0x%08x",
					lit, math.Float32bits(gotFlat.Val), math.Float32bits(*gotFlat.Ptr),
					math.Float32bits(gotFlat.Slc[0]), math.Float32bits(gotFlat.Arr[0]), wantBits)
			}
		} else if okFlat {
			t.Fatalf("expected flat path to reject overflowing float32 %s", lit)
		}

		// Full struct (Val, Ptr, Slc, Arr, Map, ,string) across Schema, Runs, Giant, and Stream
		fullDoc := fmt.Sprintf(`{"val":%s,"ptr":%s,"slc":[%s],"arr":[%s],"map":{"k":%s},"str":%q,"pad":%q}`,
			lit, lit, lit, lit, lit, lit, strings.Repeat("z", 300))
		gotRuns, errRuns := decodeViaRunsWithInit[f32AllShapes](fullDoc, diffStdOpts, nil)
		gotSchema, _, errSchema := decodeViaSchemaOrFullWithInit[f32AllShapes](fullDoc, diffStdOpts, nil)
		gotGiant, errGiant := decodeViaGiantWithInit[f32AllShapes](fullDoc, diffStdOpts, nil)
		gotStream, errStream := decodeViaChunkedStreamWithInit[f32AllShapes](fullDoc, diffStdOpts, 7, nil)
		if (errRuns != nil) != (errStd != nil) || (errSchema != nil) != (errStd != nil) ||
			(errGiant != nil) != (errStd != nil) || (errStream != nil) != (errStd != nil) {
			t.Fatalf("all-shapes err mismatch on %s: std=%v runs=%v schema=%v giant=%v stream=%v",
				lit, errStd, errRuns, errSchema, errGiant, errStream)
		}
		if errStd == nil {
			for name, g := range map[string]f32AllShapes{
				"runs":   gotRuns,
				"schema": gotSchema,
				"giant":  gotGiant,
				"stream": gotStream,
			} {
				if math.Float32bits(g.Val) != wantBits ||
					math.Float32bits(*g.Ptr) != wantBits ||
					math.Float32bits(g.Slc[0]) != wantBits ||
					math.Float32bits(g.Arr[0]) != wantBits ||
					math.Float32bits(g.Map["k"]) != wantBits ||
					math.Float32bits(g.Str) != wantBits {
					t.Fatalf("%s bits mismatch on %s: want=0x%08x got=%+v", name, lit, wantBits, g)
				}
			}
		}
	}
}

func TestFlatAndSchemaBackoffIndependentRecovery(t *testing.T) {
	type InterleavedTarget struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	var dst InterleavedTarget
	_, _, _, proj, flatSD, err := unpackAndCompileRoot(&dst)
	if err != nil || proj == nil || flatSD == nil {
		t.Fatalf("expected both proj and flatSD, err=%v proj=%v flatSD=%v", err, proj, flatSD)
	}

	// Force both Flat and Schema into backoff simultaneously on the exact same &dst address.
	atomic.StoreUint32(&flatSD.flatFailStreak, flatBackoffThreshold)
	atomic.StoreUint32(&proj.denseStreak, 4)

	cleanSparse := `{"a":1,"b":"ok","skip":"` + strings.Repeat("s", 400) + `"}`
	flatRecovered := false
	schemaRecovered := false

	// Alternate calls on the same &dst: first check Flat (which if still backed off
	// would let DecodeOne try Schema on the exact same &dst in lockstep!).
	for i := 0; i < 512 && (!flatRecovered || !schemaRecovered); i++ {
		// Interleave both probes on &dst in the same iteration (the exact parity-lock scenario).
		if !flatRecovered {
			if _, ok := tryDecodeFlatStruct(cleanSparse, 0, diffStdOpts, unsafe.Pointer(&dst), flatSD, false, true); ok {
				if atomic.LoadUint32(&flatSD.flatFailStreak) == 0 {
					flatRecovered = true
				}
			}
		}
		if !schemaRecovered {
			if proj.shouldUseSeededOpts(len(cleanSparse), diffStdOpts, uintptr(unsafe.Pointer(&dst))) {
				p := newParserLazy(cleanSparse, 0, diffStdOpts)
				ok, _ := parseSchemaRoot(p, proj)
				p.free()
				if ok && atomic.LoadUint32(&proj.denseStreak) == 0 {
					schemaRecovered = true
				}
			}
		}
	}

	if !flatRecovered || !schemaRecovered {
		t.Fatalf("backoff failed to recover within 512 interleaved calls: flatRecovered=%v (streak=%d) schemaRecovered=%v (streak=%d)",
			flatRecovered, atomic.LoadUint32(&flatSD.flatFailStreak),
			schemaRecovered, atomic.LoadUint32(&proj.denseStreak))
	}

	// Verify specific review case: 1.0000000596046448 exact bits check
	var f float32
	_, _, err = DecodeOne("1.0000000596046448", diffStdOpts, &f)
	if err != nil || math.Float32bits(f) != 0x3f800001 {
		t.Fatalf("expected 0x3f800001, got 0x%08x (err=%v)", math.Float32bits(f), err)
	}
	_ = bytes.Equal
}

func mustCompileFlatSD[T any](t *testing.T) (T, *structDecoder) {
	t.Helper()
	var zero T
	_, _, _, _, flatSD, err := unpackAndCompileRoot(&zero)
	if err != nil || flatSD == nil {
		t.Fatalf("expected flatSD for %T, err=%v", zero, err)
	}
	return zero, flatSD
}
