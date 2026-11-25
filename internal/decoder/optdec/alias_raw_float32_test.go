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
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFlatPrePopulatedAliasPreservesWriteOrder(t *testing.T) {
	// 1. Shared pointer target: {"b":2,"a":1} must leave x == 1, not 2.
	type SharedPtrStruct struct {
		A *int `json:"a"`
		B *int `json:"b"`
	}
	var zeroHit SharedPtrStruct
	if _, end, err := DecodeOne(`{"b":2,"a":1}`, diffStdOpts, &zeroHit); checkSingleDocEnd(`{"b":2,"a":1}`, end, err) != nil || *zeroHit.A != 1 || *zeroHit.B != 2 {
		t.Fatalf("zero-initialized SharedPtrStruct failed: err=%v got=%+v", err, zeroHit)
	}
	disjointA, disjointB := 10, 20
	disjointHit := SharedPtrStruct{A: &disjointA, B: &disjointB}
	if _, end, err := DecodeOne(`{"b":2,"a":1}`, diffStdOpts, &disjointHit); checkSingleDocEnd(`{"b":2,"a":1}`, end, err) != nil || disjointA != 1 || disjointB != 2 {
		t.Fatalf("disjoint pre-populated SharedPtrStruct failed: err=%v a=%d b=%d", err, disjointA, disjointB)
	}

	for _, src := range []string{
		`{"b":2,"a":1}`,
		`{"a":1,"b":2}`,
		`{"b":2,"a":null}`,
		`{"a":null,"b":2}`,
	} {
		xGot, xRuns, xSchema, xGiant, xStream, xStd := 99, 99, 99, 99, 99, 99
		got := SharedPtrStruct{A: &xGot, B: &xGot}
		want := SharedPtrStruct{A: &xStd, B: &xStd}
		_, end, errGot := DecodeOne(src, diffStdOpts, &got)
		errGot = checkSingleDocEnd(src, end, errGot)
		gotRuns, errRuns := decodeViaRunsWithInit[SharedPtrStruct](src, diffStdOpts, func(d *SharedPtrStruct) { d.A, d.B = &xRuns, &xRuns })
		gotSchema, _, errSchema := decodeViaSchemaOrFullWithInit[SharedPtrStruct](src, diffStdOpts, func(d *SharedPtrStruct) { d.A, d.B = &xSchema, &xSchema })
		gotGiant, errGiant := decodeViaGiantWithInit[SharedPtrStruct](src, diffStdOpts, func(d *SharedPtrStruct) { d.A, d.B = &xGiant, &xGiant })
		gotStream, errStream := decodeViaChunkedStreamWithInit[SharedPtrStruct](src, diffStdOpts, 5, func(d *SharedPtrStruct) { d.A, d.B = &xStream, &xStream })
		errStd := json.Unmarshal([]byte(src), &want)
		if (errGot != nil) != (errStd != nil) || errRuns != nil || errSchema != nil || errGiant != nil || errStream != nil {
			t.Fatalf("err mismatch on %s: got=%v runs=%v schema=%v giant=%v stream=%v std=%v",
				src, errGot, errRuns, errSchema, errGiant, errStream, errStd)
		}
		for name, pair := range map[string]struct {
			x int
			s SharedPtrStruct
		}{
			"router": {xGot, got},
			"runs":   {xRuns, gotRuns},
			"schema": {xSchema, gotSchema},
			"giant":  {xGiant, gotGiant},
			"stream": {xStream, gotStream},
		} {
			if pair.x != xStd || (pair.s.A == nil) != (want.A == nil) || (pair.s.B == nil) != (want.B == nil) {
				t.Fatalf("%s shared ptr mismatch on %s: x=%d xStd=%d got=%+v want=%+v", name, src, pair.x, xStd, pair.s, want)
			}
		}
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

	// Explicit Schema hit across both <=64 levels and >64 levels:
	// Schema projection now consumes the shared 4096-depth
	// internal/jsonscan.Machine instead of the legacy 64-level sharedValidateContainer bit-stack,
	// so both 32-deep and 65-deep RawMessage payloads execute directly in Schema without fallback.
	shallow32 := strings.Repeat("[", 32) + "1e999" + strings.Repeat("]", 32)
	gotShallowSchema, usedShallowSchema, err := decodeViaSchemaOrFullWithInit[MixedEnvelope](
		`{"guard":{"ok":1e999},"raw":`+shallow32+`,"val":1.5}`, diffStdOpts, nil)
	if err != nil || !usedShallowSchema || string(gotShallowSchema.Raw) != shallow32 {
		t.Fatalf("expected <=64-deep RawMessage to hit Schema directly: usedSchema=%v err=%v", usedShallowSchema, err)
	}
	gotDeepSchema, usedDeepSchema, err := decodeViaSchemaOrFullWithInit[MixedEnvelope](envSrc, diffStdOpts, nil)
	if err != nil || !usedDeepSchema || string(gotDeepSchema.Raw) != deep65 {
		t.Fatalf("expected 65-deep RawMessage to succeed directly in shared 4096-depth Schema scanner: usedSchema=%v err=%v", usedDeepSchema, err)
	}

	// Category 3 (numeric range mismatch during FromDom): fails on bare any=1e999, while matching
	// encoding/json sequential field execution on earlier valid fields and leaving Val == nil.
	var badEnv, stdBadEnv MixedEnvelope
	badAnySrc := `{"guard":{"ok":1},"raw":[1e999],"val":1e999}`
	_, _, err = DecodeOne(badAnySrc, diffStdOpts, &badEnv)
	stdBadErr := json.Unmarshal([]byte(badAnySrc), &stdBadEnv)
	if err == nil || stdBadErr == nil {
		t.Fatalf("expected range mismatch error for sibling any=1e999: got=%v std=%v", err, stdBadErr)
	}
	if badEnv.Guard.called != stdBadEnv.Guard.called || badEnv.Val != nil {
		t.Fatalf("range mismatch side-effect diverged from std: gotCalled=%v stdCalled=%v val=%#v",
			badEnv.Guard.called, stdBadEnv.Guard.called, badEnv.Val)
	}

	// Category 1 (JSON syntax error within single value): rejected at scan time BEFORE any UnmarshalJSON runs.
	for _, badSynSrc := range []string{
		`{"guard":{"ok":1},"raw":[1e999],"val":1e+}`,
		`{"guard":{"ok":1},"raw":[1e999],"val":1e999x}`,
	} {
		var synEnv MixedEnvelope
		_, _, err = DecodeOne(badSynSrc, diffStdOpts, &synEnv)
		if err == nil {
			t.Fatalf("expected syntax error on %s", badSynSrc)
		}
		if synEnv.Guard.called {
			t.Fatalf("Guard.UnmarshalJSON was called before rejecting syntax error on %s", badSynSrc)
		}
	}

	// Skipped unknown field with 1e999 aligns with encoding/json and succeeds across all paths.
	skipSrc := `{"guard":{"ok":1},"raw":` + deep65 + `,"unknown_inf":1e999,"unknown_nested":{"x":-1e999}}`
	var skipEnv, stdSkipEnv MixedEnvelope
	_, end, err = DecodeOne(skipSrc, diffStdOpts, &skipEnv)
	if err := checkSingleDocEnd(skipSrc, end, err); err != nil {
		t.Fatalf("expected skipped unknown field 1e999 to succeed, got %v", err)
	}
	if err := json.Unmarshal([]byte(skipSrc), &stdSkipEnv); err != nil {
		t.Fatalf("std failed on skipped unknown field 1e999: %v", err)
	}
	if !skipEnv.Guard.called || string(skipEnv.Raw) != deep65 {
		t.Fatalf("skipped unknown 1e999 result mismatch: guardCalled=%v rawLen=%d", skipEnv.Guard.called, len(skipEnv.Raw))
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

		// Compact struct (Val, *float32, []float32, [1]float32) via DecodeOne
		flatDoc := fmt.Sprintf(`{"val":%s,"ptr":%s,"slc":[%s],"arr":[%s]}`, lit, lit, lit, lit)
		var gotFlat f32FlatOnly
		_, endFlat, errFlat := DecodeOne(flatDoc, diffStdOpts, &gotFlat)
		errFlat = checkSingleDocEnd(flatDoc, endFlat, errFlat)
		if errStd == nil {
			if errFlat != nil {
				t.Fatalf("expected DecodeOne to accept %s, err=%v", lit, errFlat)
			}
			if math.Float32bits(gotFlat.Val) != wantBits ||
				math.Float32bits(*gotFlat.Ptr) != wantBits ||
				math.Float32bits(gotFlat.Slc[0]) != wantBits ||
				math.Float32bits(gotFlat.Arr[0]) != wantBits {
				t.Fatalf("compact struct bits mismatch on %s: val=0x%08x ptr=0x%08x slc=0x%08x arr=0x%08x want=0x%08x",
					lit, math.Float32bits(gotFlat.Val), math.Float32bits(*gotFlat.Ptr),
					math.Float32bits(gotFlat.Slc[0]), math.Float32bits(gotFlat.Arr[0]), wantBits)
			}
		} else if errFlat == nil {
			t.Fatalf("expected DecodeOne to reject overflowing float32 %s", lit)
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
	_, _, _, proj, err := unpackAndCompileRoot(&dst)
	if err != nil || proj == nil {
		t.Fatalf("expected non-nil proj, err=%v proj=%v", err, proj)
	}

	cleanSparse := `{"a":1,"b":"ok","skip":"` + strings.Repeat("s", 400) + `"}`
	if _, end, err := DecodeOne(cleanSparse, diffStdOpts, &dst); checkSingleDocEnd(cleanSparse, end, err) != nil || dst.A != 1 || dst.B != "ok" {
		t.Fatalf("DecodeOne cleanSparse failed: err=%v dst=%+v", err, dst)
	}

	// Verify specific review case: 1.0000000596046448 exact bits check
	var f float32
	_, _, err = DecodeOne("1.0000000596046448", diffStdOpts, &f)
	if err != nil || math.Float32bits(f) != 0x3f800001 {
		t.Fatalf("expected 0x3f800001, got 0x%08x (err=%v)", math.Float32bits(f), err)
	}
	_ = bytes.Equal
}

type siblingMutatorHook struct {
	target *any
	raw    *json.RawMessage
}

func (h *siblingMutatorHook) UnmarshalJSON(_ []byte) error {
	if h.target != nil && h.raw != nil {
		*h.target = h.raw
	}
	return nil
}

func TestDynamicTargetReuseAndScanNumberBoundaries(t *testing.T) {
	// 1. Review 2.1: Pre-populated slice element interface holding *json.RawMessage.
	type Item struct {
		Value any `json:"value"`
	}
	const itemSrc = `[{"value":1e999}]`
	var rawRouter, rawRuns, rawGiant, rawStream, rawStd json.RawMessage
	dstRouter := []Item{{Value: &rawRouter}}
	_, end, err := DecodeOne(itemSrc, diffStdOpts, &dstRouter)
	if err := checkSingleDocEnd(itemSrc, end, err); err != nil {
		t.Fatalf("Router []Item{{Value:&raw}} failed: %v", err)
	}
	_, errRuns := decodeViaRunsWithInit[[]Item](itemSrc, diffStdOpts, func(d *[]Item) { *d = []Item{{Value: &rawRuns}} })
	_, errGiant := decodeViaGiantWithInit[[]Item](itemSrc, diffStdOpts, func(d *[]Item) { *d = []Item{{Value: &rawGiant}} })
	_, errStream := decodeViaChunkedStreamWithInit[[]Item](itemSrc, diffStdOpts, 4, func(d *[]Item) { *d = []Item{{Value: &rawStream}} })
	dstStd := []Item{{Value: &rawStd}}
	if err := json.Unmarshal([]byte(itemSrc), &dstStd); err != nil {
		t.Fatal(err)
	}
	if errRuns != nil || errGiant != nil || errStream != nil {
		t.Fatalf("[]Item{{Value:&raw}} path errors: runs=%v giant=%v stream=%v", errRuns, errGiant, errStream)
	}
	for name, r := range map[string]json.RawMessage{
		"router": rawRouter,
		"runs":   rawRuns,
		"giant":  rawGiant,
		"stream": rawStream,
	} {
		if !bytes.Equal(r, rawStd) || string(r) != "1e999" {
			t.Fatalf("%s raw mismatch: got=%q want=%q", name, string(r), string(rawStd))
		}
	}

	// 2. []any{&raw} (len=1, cap=1) and ([]any{&raw})[:0] (len=0, cap=1) at both root and struct fieldKindSliceEface.
	type SliceEfaceHolder struct {
		Anys []any `json:"anys"`
	}
	for _, resliced := range []bool{false, true} {
		var rRoot, rField, rSchema, rStd json.RawMessage
		initSlice := func(r *json.RawMessage) []any {
			s := []any{r}
			if resliced {
				return s[:0]
			}
			return s
		}
		rootTarget := initSlice(&rRoot)
		_, end, err := DecodeOne(`[1e999]`, diffStdOpts, &rootTarget)
		if err := checkSingleDocEnd(`[1e999]`, end, err); err != nil {
			t.Fatalf("root []any (resliced=%v) failed: %v", resliced, err)
		}
		fieldHolder := SliceEfaceHolder{Anys: initSlice(&rField)}
		_, end, err = DecodeOne(`{"anys":[-1e999]}`, diffStdOpts, &fieldHolder)
		if err := checkSingleDocEnd(`{"anys":[-1e999]}`, end, err); err != nil {
			t.Fatalf("field []any (resliced=%v) failed: %v", resliced, err)
		}
		_, usedSchema, errSchema := decodeViaSchemaOrFullWithInit[SliceEfaceHolder](`{"anys":[-1e999]}`, diffStdOpts, func(h *SliceEfaceHolder) {
			h.Anys = initSlice(&rSchema)
		})
		if errSchema != nil || !usedSchema {
			t.Fatalf("Schema SliceEfaceHolder (resliced=%v) failed: usedSchema=%v err=%v", resliced, usedSchema, errSchema)
		}
		stdHolder := SliceEfaceHolder{Anys: initSlice(&rStd)}
		if err := json.Unmarshal([]byte(`{"anys":[-1e999]}`), &stdHolder); err != nil {
			t.Fatal(err)
		}
		if string(rRoot) != "1e999" || string(rField) != string(rStd) || string(rSchema) != "-1e999" {
			t.Fatalf("[]any backing reuse mismatch (resliced=%v): root=%q field=%q schema=%q std=%q",
				resliced, string(rRoot), string(rField), string(rSchema), string(rStd))
		}
	}

	// 3. Review 2.2: Duplicate-key target mutation: {"value":null,"value":1e999} on pre-populated Value=&raw MUST fail!
	type Holder struct {
		Value any `json:"value"`
	}
	const dupNullThenInf = `{"value":null,"value":1e999}`
	for _, pathName := range []string{"router", "runs", "schema", "giant", "stream"} {
		raw := json.RawMessage("untouched")
		var errPath error
		var finalVal any
		switch pathName {
		case "router":
			h := Holder{Value: &raw}
			_, end, e := DecodeOne(dupNullThenInf, diffStdOpts, &h)
			errPath = checkSingleDocEnd(dupNullThenInf, end, e)
			finalVal = h.Value
		case "runs":
			h, e := decodeViaRunsWithInit[Holder](dupNullThenInf, diffStdOpts, func(d *Holder) { d.Value = &raw })
			errPath, finalVal = e, h.Value
		case "schema":
			h, used, e := decodeViaSchemaOrFullWithInit[Holder](dupNullThenInf, diffStdOpts, func(d *Holder) { d.Value = &raw })
			if !used {
				t.Fatal("expected Holder to hit Schema path")
			}
			errPath, finalVal = e, h.Value
		case "giant":
			h, e := decodeViaGiantWithInit[Holder](dupNullThenInf, diffStdOpts, func(d *Holder) { d.Value = &raw })
			errPath, finalVal = e, h.Value
		case "stream":
			h, e := decodeViaChunkedStreamWithInit[Holder](dupNullThenInf, diffStdOpts, 5, func(d *Holder) { d.Value = &raw })
			errPath, finalVal = e, h.Value
		}
		stdRaw := json.RawMessage("untouched")
		stdH := Holder{Value: &stdRaw}
		stdErr := json.Unmarshal([]byte(dupNullThenInf), &stdH)
		if errPath == nil || stdErr == nil {
			t.Fatalf("%s failed to reject %s after null reset: got=%v std=%v", pathName, dupNullThenInf, errPath, stdErr)
		}
		if finalVal != nil || string(raw) != "untouched" {
			t.Fatalf("%s unexpected post-error state: finalVal=%#v raw=%q", pathName, finalVal, string(raw))
		}
	}

	// Custom Unmarshaler mutating sibling target from nil -> &raw before sibling 1e999 field is decoded.
	type MutatingSibling struct {
		Setup siblingMutatorHook `json:"setup"`
		Next  any                `json:"next"`
	}
	var dynRaw, stdDynRaw json.RawMessage
	var mutGot, mutStd MutatingSibling
	mutGot.Setup = siblingMutatorHook{target: &mutGot.Next, raw: &dynRaw}
	mutStd.Setup = siblingMutatorHook{target: &mutStd.Next, raw: &stdDynRaw}
	const mutSrc = `{"setup":1,"next":1e999}`
	_, end, err = DecodeOne(mutSrc, diffStdOpts, &mutGot)
	if err := checkSingleDocEnd(mutSrc, end, err); err != nil {
		t.Fatalf("MutatingSibling failed: %v", err)
	}
	if err := json.Unmarshal([]byte(mutSrc), &mutStd); err != nil {
		t.Fatal(err)
	}
	if string(dynRaw) != "1e999" || string(dynRaw) != string(stdDynRaw) {
		t.Fatalf("MutatingSibling raw mismatch: got=%q std=%q", string(dynRaw), string(stdDynRaw))
	}

	// 4. Stage 1.1: scanNumberScalar & root/container number token boundary matrix.
	type NumBoundaryEnv struct {
		Raw json.RawMessage `json:"raw"`
		F64 float64         `json:"f64"`
	}
	// Underflow literals must follow normal IEEE-754 underflow (+0.0 / -0.0), not KRawNumber error.
	for _, uf := range []struct {
		lit     string
		negZero bool
	}{
		{lit: "1e-999", negZero: false},
		{lit: "-1e-999", negZero: true},
	} {
		doc := fmt.Sprintf(`{"raw":%s,"f64":%s}`, uf.lit, uf.lit)
		gotS, usedS, errS := decodeViaSchemaOrFullWithInit[NumBoundaryEnv](doc, diffStdOpts, nil)
		if errS != nil || !usedS {
			t.Fatalf("Schema underflow %s failed: used=%v err=%v", uf.lit, usedS, errS)
		}
		if gotS.F64 != 0 || math.Signbit(gotS.F64) != uf.negZero || string(gotS.Raw) != uf.lit {
			t.Fatalf("underflow %s mismatch: f64=%v signbit=%v raw=%q",
				uf.lit, gotS.F64, math.Signbit(gotS.F64), string(gotS.Raw))
		}
	}

	// Malformed number boundaries must be rejected at scan time even when targeting json.RawMessage or skipped unknown fields!
	for _, badTok := range []string{"1e999x", "1e999.2", "01", "-01", "1e+", "-"} {
		for _, doc := range []string{
			badTok,
			`{"raw":` + badTok + `}`,
			`{"unknown":` + badTok + `}`,
			`[` + badTok + `]`,
		} {
			var r json.RawMessage
			_, end, e := DecodeOne(doc, diffStdOpts, &r)
			if checkSingleDocEnd(doc, end, e) == nil {
				t.Fatalf("DecodeOne accepted invalid number token %q in %q", badTok, doc)
			}
			if _, _, eSchema := decodeViaSchemaOrFullWithInit[NumBoundaryEnv](doc, diffStdOpts, nil); eSchema == nil {
				t.Fatalf("Schema accepted invalid number token %q in %q", badTok, doc)
			}
			if _, eStream := decodeViaChunkedStreamWithInit[json.RawMessage](doc, diffStdOpts, 2, nil); eStream == nil {
				t.Fatalf("Stream accepted invalid number token %q in %q", badTok, doc)
			}
		}
	}
}
