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
	"math"
	"testing"
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
		{"1e309", 0, 0, 5, SONIC_FLOAT_INF},
		{"1.", 0, 0, 2, SONIC_INVALID_NUM},
		{"1e+", 0, 0, 3, SONIC_INVALID_NUM},
		{"-x", 0, 0, 1, SONIC_INVALID_NUM},
	} {
		t.Run(tc.src, func(t *testing.T) {
			for _, forceScalar := range []bool{false, true} {
				p := newParser(tc.src, 0, 0)
				p.isEface = true
				code := parseRunsGrowingMode(p, forceScalar)
				if code != tc.code || p.Pos() != tc.pos {
					t.Fatalf("scalar=%v: got=(%d,%d), want=(%d,%d)", forceScalar, code, p.Pos(), tc.code, tc.pos)
				}
				if tc.code == SONIC_OK {
					nd := p.nodes[0]
					if (nd.typ&TypeMask) != tc.kind || nd.val != tc.value {
						t.Fatalf("scalar=%v: node=(%#x,%#x), want=(%#x,%#x)", forceScalar, nd.typ&TypeMask, nd.val, tc.kind, tc.value)
					}
				}
				p.free()
			}
		})
	}
}

func TestCParityUseNumberStrictLeftToRightErrorPriority(t *testing.T) {
	for _, tc := range []struct {
		src  string
		code ErrorCode
		pos  int
	}{
		{"", SONIC_EOF, 0},
		{"01", SONIC_OK, 1},
		{"1e+2,", SONIC_OK, 4},
		{"1e+", SONIC_INVALID_NUM, 2},
		{"1+2", SONIC_INVALID_NUM, 1},
		{"1e2e3.4.5xxxxxxxx", SONIC_INVALID_NUM, 3},
		{"1e2e3.4.5", SONIC_INVALID_NUM, 3},
		{"1234567890123456e+2,", SONIC_OK, 19},
	} {
		for _, forceScalar := range []bool{false, true} {
			p := newParser(tc.src, 0, uint64(OptionUseNumber))
			p.isEface = true
			code := parseRunsGrowingMode(p, forceScalar)
			if code != tc.code || p.Pos() != tc.pos {
				t.Fatalf("scalar=%v src=%q: got=(%d,%d), want=(%d,%d)", forceScalar, tc.src, code, p.Pos(), tc.code, tc.pos)
			}
			p.free()
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
		for _, forceScalar := range []bool{false, true} {
			p := newParser(src, 0, 0)
			p.isEface = true
			code := parseRunsGrowingMode(p, forceScalar)
			if code != SONIC_OK {
				t.Fatalf("scalar=%v src=%q unexpected error code %d", forceScalar, src, code)
			}
			if (p.nodes[0].typ & TypeMask) == KSint {
				t.Fatalf("scalar=%v src=%q produced KSint (val=%#x); non-negative inputs must never emit KSint", forceScalar, src, p.nodes[0].val)
			}
			p.free()
		}
	}

	// Verify via full DecodeOne that "0" produces +0.0 while "-0" produces -0.0.
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
			S []int   `json:"s"`
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
