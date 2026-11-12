//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"testing"
	"unsafe"
)

func TestCParityNumberPaths(t *testing.T) {
	for _, tc := range []struct {
		src         string
		kind, value uint64
		pos         int
		code        ErrorCode
	}{
		{"0", KUint, 0, 1, SONIC_OK},
		{"-0", KSint, 0, 2, SONIC_OK},
		{"01", KUint, 0, 1, SONIC_OK},
		{"18446744073709551615", KUint, ^uint64(0), 20, SONIC_OK},
		{"-9223372036854775808", KSint, 1 << 63, 20, SONIC_OK},
		{"18446744073709551616", KReal, math.Float64bits(18446744073709551616), 20, SONIC_OK},
		{"-18446744073709551615", KReal, math.Float64bits(-18446744073709551616), 21, SONIC_OK},
		{"0.035", KReal, math.Float64bits(0.035), 5, SONIC_OK},
		{"-0.0", KReal, 1 << 63, 4, SONIC_OK},
		{"1.2345678901234567", KReal, math.Float64bits(1.2345678901234567), 18, SONIC_OK},
		{"5e-324", KReal, 1, 6, SONIC_OK},
		{"-1e-400", KReal, 1 << 63, 7, SONIC_OK},
		{"1.7976931348623157e308", KReal, math.Float64bits(math.MaxFloat64), 22, SONIC_OK},
		{"0e999999999999", KReal, 0, 14, SONIC_OK},
		{"1e309", 0xabc, 0xdef, 5, SONIC_FLOAT_INF},
		{"1.", 0xabc, 0xdef, 2, SONIC_INVALID_NUM},
		{"1e+", 0xabc, 0xdef, 3, SONIC_INVALID_NUM},
		{"-x", 0xabc, 0xdef, 1, SONIC_INVALID_NUM},
	} {
		t.Run(tc.src, func(t *testing.T) {
			input := append([]byte(tc.src), padding...)
			start := unsafe.Pointer(&input[0])
			cur := start
			negative := tc.src[0] == '-'
			if negative {
				cur = unsafe.Add(cur, 1)
			}
			n := cParityNum{typ: 0xabc, val: 0xdef}
			scratch := make([]byte, 800)
			code := cParityParseNumber(&cur, &n, negative, &scratch)
			if code != tc.code || int(uintptr(cur)-uintptr(start)) != tc.pos || n.typ != tc.kind || n.val != tc.value {
				t.Fatalf("got=(%d,%d,%#x,%#x), want=(%d,%d,%#x,%#x)", code, int(uintptr(cur)-uintptr(start)), n.typ, n.val, tc.code, tc.pos, tc.kind, tc.value)
			}
		})
	}
}

func TestCParityRawNumberBlockErrorPriority(t *testing.T) {
	for _, tc := range []struct {
		src                 string
		remain, result, pos int
	}{
		{"", 0, -SONIC_INVALID_NUM, 0},
		{"01", 2, 1, 1},
		{"1e+2,", 5, 4, 4},
		{"1e+", 3, -SONIC_INVALID_NUM, 2},
		{"1+2", 3, -SONIC_INVALID_NUM, 1},
		// C checks duplicate dots before duplicate exponents within a SIMD block.
		// Its scalar tail instead encounters the second exponent first.
		{"1e2e3.4.5xxxxxxxx", 17, -SONIC_INVALID_NUM, 7},
		{"1e2e3.4.5", 9, -SONIC_INVALID_NUM, 3},
		{"1234567890123456e+2,", 20, 19, 19},
	} {
		input := append([]byte(tc.src), padding...)
		start := unsafe.Pointer(&input[0])
		cur := start
		got := cParitySkipPositive(&cur, tc.remain)
		if got != tc.result || int(uintptr(cur)-uintptr(start)) != tc.pos {
			t.Fatalf("src=%q remain=%d got=(%d,%d), want=(%d,%d)", tc.src, tc.remain, got, int(uintptr(cur)-uintptr(start)), tc.result, tc.pos)
		}
	}
}

// TestCParityNonNegativeNeverProducesKSint guards the scanner invariant relied upon
// by sintToFloat64 / sintToFloat32: non-negative numbers never emit KSint, and KSint
// with raw value 0 is produced exclusively by "-0".
func TestCParityNonNegativeNeverProducesKSint(t *testing.T) {
	nonNegative := []string{
		"0",
		"00",
		"1",
		"42",
		"9223372036854775807",
		"9223372036854775808",
		"18446744073709551615",
		"18446744073709551616",
		"0.0",
		"0e0",
		"0e-10",
		"0e999999999999",
	}
	for _, src := range nonNegative {
		input := append([]byte(src), padding...)
		cur := unsafe.Pointer(&input[0])
		var n cParityNum
		scratch := make([]byte, 800)
		code := cParityParseNumber(&cur, &n, false, &scratch)
		if code != SONIC_OK {
			t.Fatalf("src=%q unexpected error code %d", src, code)
		}
		if n.typ == KSint {
			t.Fatalf("src=%q produced KSint (val=%#x); non-negative inputs must never emit KSint", src, n.val)
		}
	}

	// Verify via full Parser + schemaScanner that "0" produces +0.0 while "-0" produces -0.0.
	for _, tc := range []struct {
		json string
		neg  bool
	}{
		{`{"f":0}`, false},
		{`{"f":-0}`, true},
		{`{"f":0.0}`, false},
		{`{"f":-0.0}`, true},
	} {
		var out struct {
			F float64 `json:"f"`
			S []int   `json:"s"` // forces non-flat_direct path too when tested via Parser
		}
		_, _, err := DecodeOne(tc.json, 0, &out)
		if err != nil {
			t.Fatalf("json=%s err=%v", tc.json, err)
		}
		if out.F != 0 || math.Signbit(out.F) != tc.neg {
			t.Fatalf("json=%s got %v (signbit=%v), want signbit=%v", tc.json, out.F, math.Signbit(out.F), tc.neg)
		}
	}
}
