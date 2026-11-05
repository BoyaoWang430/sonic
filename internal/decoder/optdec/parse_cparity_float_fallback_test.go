//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
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

func TestCParityFloatFallbackPreservesCapacityBehavior(t *testing.T) {
	// The old C decimal stores at most scratch capacity digits and uses that
	// stored length as its integer decimal point. A very long integer part can
	// therefore differ from strconv even when its exponent makes it finite.
	// Preserve this existing behavior outside the replacement's bounded domain.
	for _, tc := range []struct {
		src      string
		capacity int
	}{
		{"1" + strings.Repeat("0", 800) + "e-801", 800},
		{"1" + strings.Repeat("0", 999) + "e-00000000000001000", 800},
		{"1" + strings.Repeat("0", 63) + "e-64", 32},
	} {
		for _, sign := range []string{"", "-"} {
			src := sign + tc.src
			input := append([]byte(src), padding...)
			start := unsafe.Pointer(&input[0])
			scratch := make([]byte, tc.capacity)
			want := cParityAtofNative(start, len(src), &scratch)
			got := cParityAtofFallback(start, len(src), &scratch)
			standard, err := strconv.ParseFloat(src, 64)
			if err != nil {
				t.Fatal(err)
			}
			if math.Float64bits(want) == math.Float64bits(standard) {
				t.Fatalf("fixture no longer distinguishes C capacity behavior: len=%d capacity=%d", len(src), tc.capacity)
			}
			if math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("len=%d capacity=%d: fallback=%016x native=%016x strconv=%016x", len(src), tc.capacity,
					math.Float64bits(got), math.Float64bits(want), math.Float64bits(standard))
			}
		}
	}
}
