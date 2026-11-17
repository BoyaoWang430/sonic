//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"encoding/json"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

func TestCParityFloatFallbackAgainstNative(t *testing.T) {
	compare := func(src string) {
		t.Helper()
		input := append([]byte(src), padding...)
		start := unsafe.Pointer(&input[0])
		scratch := make([]byte, 800)
		want := cParityAtofNative(start, len(src), &scratch)
		got := cParityAtofFallback(start, len(src), &scratch)
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("len=%d token=%.100q: fallback=%016x native=%016x", len(src), src, math.Float64bits(got), math.Float64bits(want))
		}
		if len(src) <= 800 {
			standard, err := strconv.ParseFloat(src, 64)
			if err != nil && !math.IsInf(standard, 0) {
				t.Fatalf("invalid test token %.100q: %v", src, err)
			}
			if math.Float64bits(standard) != math.Float64bits(want) {
				t.Fatalf("len=%d token=%.100q: strconv=%016x native=%016x", len(src), src, math.Float64bits(standard), math.Float64bits(want))
			}
		}
	}
	const halfway = "1.00000000000000011102230246251565404236316680908203125"
	cases := []string{
		"0", "0.0", "0e999999999999999999999", "1e-999999999999999999999",
		"1e999999999999999999999", "1e000000000000000000001", "1e-000000000000000000001",
		"1.00000000000000011102230246251565404236316680908203124", halfway,
		"1.00000000000000011102230246251565404236316680908203126",
		"1.00000000000000033306690738754696212708950042724609375",
		"1090544144181609348671888949248", "1090544144181609348835077142190",
		"2.2250738585072011e-308", "2.2250738585072012e-308", "2.2250738585072014e-308",
		"1.7976931348623157e308", "1.7976931348623158e308", "1.797693134862315808e308",
		"2e-324", "3e-324", "5e-324", "2.4703282292062327e-324", "2.4703282292062328e-324",
		"0." + strings.Repeat("1234567890", 12) + "e-10",
		halfway + strings.Repeat("0", 10000) + "1",
		"2." + strings.Repeat("2", 4000) + "e+1",
		"1e-" + strings.Repeat("0", 1000) + "1",
	}
	// Exercise both sides of the token-length guard and a discarded nonzero
	// tail after an exact rounding midpoint.
	for _, size := range []int{799, 800, 801, 1024} {
		cases = append(cases, halfway+strings.Repeat("0", size-len(halfway)-1)+"1")
	}
	for _, src := range cases {
		compare(src)
		compare("-" + src)
	}

	rng := rand.New(rand.NewSource(4))
	lengths := []int{1, 2, 8, 17, 18, 19, 20, 32, 64, 120, 256, 700}
	for i := 0; i < 3000; i++ {
		digits := make([]byte, lengths[rng.Intn(len(lengths))])
		digits[0] = byte('1' + rng.Intn(9))
		for j := 1; j < len(digits); j++ {
			digits[j] = byte('0' + rng.Intn(10))
		}
		point := rng.Intn(len(digits) + 1)
		var src string
		switch point {
		case 0:
			src = "0." + string(digits)
		case len(digits):
			src = string(digits)
		default:
			src = string(digits[:point]) + "." + string(digits[point:])
		}
		exponent := rng.Intn(721) - 360
		if exponent < 0 {
			src += "e-"
			exponent = -exponent
		} else {
			src += "e+"
		}
		src += strings.Repeat("0", rng.Intn(13)) + strconv.Itoa(exponent)
		if i&1 != 0 {
			src = "-" + src
		}
		compare(src)
	}
}

func TestCParityFloatLongMantissaExactAndOverflow(t *testing.T) {
	// Long mantissas (>800 digits) must track the exact logical decimal point
	// independent of scratch capacity, returning exact finite values when offset
	// by negative exponents and signed +/-Inf on mathematical overflow.
	finiteCases := []struct {
		src      string
		want     float64
		capacity int
	}{
		{"1" + strings.Repeat("0", 800) + "e-801", 0.1, 800},
		{"1" + strings.Repeat("0", 999) + "e-00000000000001000", 0.1, 800},
		{"1" + strings.Repeat("0", 63) + "e-64", 0.1, 32},
		{"1" + strings.Repeat("0", 200000) + "e-200000", 1.0, 800},
		{"5" + strings.Repeat("0", 1200) + "e-1201", 0.5, 800},
	}
	for _, tc := range finiteCases {
		for _, neg := range []bool{false, true} {
			src := tc.src
			want := tc.want
			if neg {
				src = "-" + src
				want = -want
			}
			input := append([]byte(src), padding...)
			start := unsafe.Pointer(&input[0])
			scratch := make([]byte, tc.capacity)
			gotNative := cParityAtofNative(start, len(src), &scratch)
			gotFallback := cParityAtofFallback(start, len(src), &scratch)
			if math.Float64bits(gotNative) != math.Float64bits(want) || math.Float64bits(gotFallback) != math.Float64bits(want) {
				t.Fatalf("len=%d cap=%d: fallback=%016x native=%016x want=%016x",
					len(src), tc.capacity, math.Float64bits(gotFallback), math.Float64bits(gotNative), math.Float64bits(want))
			}
		}
	}

	overflowToken := "5" + strings.Repeat("3", 383) + "5" + strings.Repeat("3", 1327) + "e-913"
	for _, neg := range []bool{false, true} {
		src := overflowToken
		sign := 1
		if neg {
			src = "-" + src
			sign = -1
		}
		input := append([]byte(src), padding...)
		start := unsafe.Pointer(&input[0])
		scratch := make([]byte, 800)
		gotNative := cParityAtofNative(start, len(src), &scratch)
		gotFallback := cParityAtofFallback(start, len(src), &scratch)
		if !math.IsInf(gotNative, sign) || !math.IsInf(gotFallback, sign) {
			t.Fatalf("expected sign=%d Inf on F64-LONG-OVF, got fallback=%v native=%v", sign, gotFallback, gotNative)
		}
	}
}

func TestLongNumberEndToEndAcrossAllPaths(t *testing.T) {
	ovf := "5" + strings.Repeat("3", 383) + "5" + strings.Repeat("3", 1327) + "e-913"
	cancelled := "1" + strings.Repeat("0", 900) + "e-900"
	const f32Mid = "1.000000059604644775390625"
	f32TieUp := f32Mid + strings.Repeat("0", 900) + "1"
	wantF32Up := math.Float32frombits(math.Float32bits(1.0) + 1)

	type targetAll struct {
		F64   float64         `json:"f64"`
		F32   float32         `json:"f32"`
		Raw   json.RawMessage `json:"raw"`
		Num   json.Number     `json:"num"`
		SF64  float64         `json:"sf64,string"`
		SF32  float32         `json:"sf32,string"`
		Extra int             `json:"extra"`
	}

	// 1. Cancelled >800-digit mantissa + >800-digit float32 midpoint + RawMessage/Number overflow preservation.
	validPayload := `{"f64":` + cancelled +
		`,"f32":` + f32TieUp +
		`,"raw":` + ovf +
		`,"num":` + ovf +
		`,"sf64":"` + cancelled + `"` +
		`,"sf32":"` + f32TieUp + `"` +
		`,"unknown_ovf":` + ovf +
		`,"extra":7}`
	for _, mode := range []string{"router", "runs", "schema", "giant", "stream"} {
		var got targetAll
		var err error
		switch mode {
		case "router":
			var end int
			_, end, err = DecodeOne(validPayload, diffStdOpts, &got)
			if err == nil && end != len(validPayload) {
				t.Fatalf("router incomplete consume: %d/%d", end, len(validPayload))
			}
		case "runs":
			got, err = decodeViaRunsWithInit[targetAll](validPayload, diffStdOpts, nil)
		case "schema":
			got, _, err = decodeViaSchemaOrFullWithInit[targetAll](validPayload, diffStdOpts, nil)
		case "giant":
			got, err = decodeViaGiantWithInit[targetAll](validPayload, diffStdOpts, nil)
		case "stream":
			got, err = decodeViaChunkedStreamWithInit[targetAll](validPayload, diffStdOpts, 31, nil)
		}
		if err != nil {
			t.Fatalf("mode %s unexpected error: %v", mode, err)
		}
		if got.F64 != 1.0 || got.SF64 != 1.0 {
			t.Fatalf("mode %s f64=%v sf64=%v, want 1.0", mode, got.F64, got.SF64)
		}
		if math.Float32bits(got.F32) != math.Float32bits(wantF32Up) || math.Float32bits(got.SF32) != math.Float32bits(wantF32Up) {
			t.Fatalf("mode %s f32=%08x sf32=%08x, want %08x", mode, math.Float32bits(got.F32), math.Float32bits(got.SF32), math.Float32bits(wantF32Up))
		}
		if string(got.Raw) != ovf || string(got.Num) != ovf || got.Extra != 7 {
			t.Fatalf("mode %s raw/num/extra mismatch", mode)
		}
	}

	// 2. F64-LONG-OVF into float64, float32, any, and ,string must fail with range mismatch and never fabricate finite values.
	for _, mode := range []string{"router", "runs", "schema", "giant", "stream"} {
		ovfF64JSON := `{"f64":` + ovf + `,"extra":9}`
		var dst64 targetAll
		dst64.F64 = 42.5
		init64 := func(v *targetAll) { v.F64 = 42.5 }
		var err error
		switch mode {
		case "router":
			_, _, err = DecodeOne(ovfF64JSON, diffStdOpts, &dst64)
		case "runs":
			dst64, err = decodeViaRunsWithInit[targetAll](ovfF64JSON, diffStdOpts, init64)
		case "schema":
			dst64, _, err = decodeViaSchemaOrFullWithInit[targetAll](ovfF64JSON, diffStdOpts, init64)
		case "giant":
			dst64, err = decodeViaGiantWithInit[targetAll](ovfF64JSON, diffStdOpts, init64)
		case "stream":
			dst64, err = decodeViaChunkedStreamWithInit[targetAll](ovfF64JSON, diffStdOpts, 29, init64)
		}
		if err == nil {
			t.Fatalf("mode %s expected error on F64-LONG-OVF into float64, got %v", mode, dst64.F64)
		}
		if dst64.F64 != 42.5 || dst64.Extra != 9 {
			t.Fatalf("mode %s unexpected post-error state: f64=%v extra=%d", mode, dst64.F64, dst64.Extra)
		}
	}
}
