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
		fmt.Sprintf(`{"id":101,"name":"base64-case","skip1":%q,"skip2":1,"payload":%q,"extra":null,"tags":["a","b"]}`, paddingNoise, b64),
		fmt.Sprintf(`{"id":102,"name":"array-case","skip1":%q,"skip2":2,"payload":[1,2,3,254,255],"extra":"YWJj","tags":["x"]}`, paddingNoise),
		fmt.Sprintf(`{"id":103,"name":"null-case","skip1":%q,"skip2":3,"payload":null,"extra":[],"tags":[]}`, paddingNoise),
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
