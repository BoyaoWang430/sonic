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

package number

import (
	"errors"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestDecimalLongMantissaAndFloat32SingleRounding(t *testing.T) {
	// 1. F64-LONG-OVF: 1712-digit mantissa with e-913 is ~5.33e+798, overflowing both float64 and float32.
	ovf := "5" + strings.Repeat("3", 383) + "5" + strings.Repeat("3", 1327) + "e-913"
	for _, neg := range []bool{false, true} {
		tok := ovf
		sign := 1
		if neg {
			tok = "-" + tok
			sign = -1
		}
		v64, ok64 := ParseValidJSONFloat64(tok)
		if ok64 || !math.IsInf(v64, sign) {
			t.Fatalf("ParseValidJSONFloat64(%s...) = (%v, %v), want (Inf(%d), false)", tok[:20], v64, ok64, sign)
		}
		res, end, perr := ParsePrefix(tok, 0)
		if perr != types.ERR_FLOAT_INFINITY || end != len(tok) || !math.IsInf(res.Float64, sign) {
			t.Fatalf("ParsePrefix(%s...) = (%+v, %d, %v), want ERR_FLOAT_INFINITY", tok[:20], res, end, perr)
		}
		v32, ok32 := ParseValidJSONFloat32(tok)
		if ok32 || v32 != 0 {
			t.Fatalf("ParseValidJSONFloat32(%s...) = (%v, %v), want (0, false)", tok[:20], v32, ok32)
		}
		_, errGo64 := ParseGoFloat64(tok)
		if !errors.Is(errGo64, strconv.ErrRange) {
			t.Fatalf("ParseGoFloat64 expected ErrRange, got %v", errGo64)
		}
		_, errGo32 := ParseGoFloat32(tok)
		if !errors.Is(errGo32, strconv.ErrRange) {
			t.Fatalf("ParseGoFloat32 expected ErrRange, got %v", errGo32)
		}
	}

	// 2. Long mantissa cancelled by negative exponent yields exact finite float64 and float32.
	cancelled := []struct {
		tok    string
		want64 float64
		want32 float32
	}{
		{"1" + strings.Repeat("0", 900) + "e-900", 1.0, 1.0},
		{"-1" + strings.Repeat("0", 900) + "e-900", -1.0, -1.0},
		{"125" + strings.Repeat("0", 1200) + "e-1203", 0.125, 0.125},
		{"0." + strings.Repeat("0", 1200) + "5e+1201", 5.0, 5.0},
		{"1" + strings.Repeat("0", 200000) + "e-200000", 1.0, 1.0},
	}
	for _, tc := range cancelled {
		v64, ok64 := ParseValidJSONFloat64(tc.tok)
		if !ok64 || math.Float64bits(v64) != math.Float64bits(tc.want64) {
			t.Fatalf("ParseValidJSONFloat64(len=%d) = (%v, %v), want %v", len(tc.tok), v64, ok64, tc.want64)
		}
		v32, ok32 := ParseValidJSONFloat32(tc.tok)
		if !ok32 || math.Float32bits(v32) != math.Float32bits(tc.want32) {
			t.Fatalf("ParseValidJSONFloat32(len=%d) = (%v, %v), want %v", len(tc.tok), v32, ok32, tc.want32)
		}
	}

	// 3. Single-step float32 midpoint with >800 digits (1 + 2^-24 = 1.000000059604644775390625,
	// exact tie between 1.0 and 1+2^-23: zero tail rounds to even 1.0; nonzero tail at digit 900 rounds up).
	const f32Midpoint = "1.000000059604644775390625"
	tieEven := f32Midpoint + strings.Repeat("0", 900)
	tieUp := f32Midpoint + strings.Repeat("0", 900) + "1"
	gotEven, okEven := ParseValidJSONFloat32(tieEven)
	gotUp, okUp := ParseValidJSONFloat32(tieUp)
	if !okEven || math.Float32bits(gotEven) != math.Float32bits(1.0) {
		t.Fatalf("tieEven float32 = %08x (ok=%v), want %08x", math.Float32bits(gotEven), okEven, math.Float32bits(1.0))
	}
	wantUp := math.Float32frombits(math.Float32bits(1.0) + 1)
	if !okUp || math.Float32bits(gotUp) != math.Float32bits(wantUp) {
		t.Fatalf("tieUp float32 = %08x (ok=%v), want %08x", math.Float32bits(gotUp), okUp, math.Float32bits(wantUp))
	}

	// 4. Go float syntax (,string) remains distinct from JSON number syntax.
	if v, err := ParseGoFloat64("+1_000.5"); err != nil || v != 1000.5 {
		t.Fatalf("ParseGoFloat64(+1_000.5) = (%v, %v)", v, err)
	}
	if _, _, perr := ParsePrefix("+1000.5", 0); perr == 0 {
		t.Fatalf("JSON ParsePrefix must reject leading '+'")
	}
}

func TestFastJSONFloatEiselLemireDifferential(t *testing.T) {
	checkToken := func(tok string) {
		t.Helper()
		want64, err64 := strconv.ParseFloat(tok, 64)
		wantOK64 := err64 == nil && !math.IsInf(want64, 0) && !math.IsNaN(want64)
		got64, gotOK64 := ParseValidJSONFloat64(tok)
		if gotOK64 != wantOK64 || math.Float64bits(got64) != math.Float64bits(want64) {
			t.Fatalf("ParseValidJSONFloat64(%q) = (%016x, %v), want (%016x, %v)",
				tok, math.Float64bits(got64), gotOK64, math.Float64bits(want64), wantOK64)
		}

		f32, err32 := strconv.ParseFloat(tok, 32)
		wantOK32 := err32 == nil && !math.IsInf(f32, 0) && !math.IsNaN(f32)
		var want32 float32
		if wantOK32 {
			want32 = float32(f32)
		}
		got32, gotOK32 := ParseValidJSONFloat32(tok)
		if gotOK32 != wantOK32 || math.Float32bits(got32) != math.Float32bits(want32) {
			t.Fatalf("ParseValidJSONFloat32(%q) = (%08x, %v), want (%08x, %v)",
				tok, math.Float32bits(got32), gotOK32, math.Float32bits(want32), wantOK32)
		}
	}

	specials := []string{
		"0", "-0", "0.0", "-0.0", "0.00000000000000000000", "-0e100", "-0.0e-300",
		"1.0000000596046448", "-1.0000000596046448",
		"1.000000059604644775390625", "-1.000000059604644775390625",
		"9007199254740991", "9007199254740992", "9007199254740993",
		"18446744073709551615", "9999999999999999999",
		"2.2250738585072014e-308", "2.2250738585072009e-308",
		"1.7976931348623157e308", "1.7976931348623159e308",
		"1.17549435e-38", "3.402823466e38", "3.4028236e38",
	}
	for _, s := range specials {
		checkToken(s)
	}

	rng := rand.New(rand.NewSource(20261118))
	var buf [40]byte
	for i := 0; i < 50000; i++ {
		w := 0
		if rng.Intn(2) == 1 {
			buf[w] = '-'
			w++
		}
		nd := rng.Intn(22) + 1
		if rng.Intn(8) == 0 {
			buf[w] = '0'
			w++
			buf[w] = '.'
			w++
			z := rng.Intn(8)
			for k := 0; k < z; k++ {
				buf[w] = '0'
				w++
			}
			for k := 0; k < nd && w < 30; k++ {
				d := byte(rng.Intn(10)) + '0'
				if k == 0 && d == '0' {
					d = '1'
				}
				buf[w] = d
				w++
			}
		} else {
			intLen := rng.Intn(nd) + 1
			for k := 0; k < intLen && w < 30; k++ {
				d := byte(rng.Intn(10)) + '0'
				if k == 0 && d == '0' {
					d = '1'
				}
				buf[w] = d
				w++
			}
			if intLen < nd && w+2 < 32 {
				buf[w] = '.'
				w++
				for k := intLen; k < nd && w < 31; k++ {
					buf[w] = byte(rng.Intn(10)) + '0'
					w++
				}
			}
		}
		tok := string(buf[:w])
		if rng.Intn(2) == 1 {
			var exp int
			switch rng.Intn(4) {
			case 0:
				exp = rng.Intn(51) - 25
			case 1:
				exp = rng.Intn(91) - 45
			case 2:
				exp = rng.Intn(661) - 330
			default:
				exp = rng.Intn(800) - 400
			}
			tok += "e" + strconv.Itoa(exp)
		}
		checkToken(tok)
	}

	// Verify ConvertExactMantFloat32 matches single-step strconv.ParseFloat(..., 32)
	// across exact midpoints and 50,000 random 53-bit mantissas.
	checkExactMant32 := func(mant uint64, frac int, neg bool) {
		t.Helper()
		s := strconv.FormatUint(mant, 10) + "e-" + strconv.Itoa(frac)
		if neg {
			s = "-" + s
		}
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			t.Fatalf("ParseFloat(%q): %v", s, err)
		}
		want := float32(f)
		got := ConvertExactMantFloat32(mant, frac, neg)
		if math.Float32bits(got) != math.Float32bits(want) {
			t.Fatalf("ConvertExactMantFloat32(%d, %d, %v) = %08x, want %08x (%q)",
				mant, frac, neg, math.Float32bits(got), math.Float32bits(want), s)
		}
	}
	// Exact float32 midpoints where double-rounding via float64 would fail:
	// e.g. 9007199523160065e-16 = 0.9007199523160065... and 2^24+1 scaled by 5^k.
	checkExactMant32(1000000059604645, 15, false)
	checkExactMant32(1000000059604645, 15, true)
	checkExactMant32((1<<53)-1, 16, false)
	checkExactMant32((1<<53)-1, 22, true)
	for i := 0; i < 50000; i++ {
		mant := (uint64(rng.Uint64()) & ((uint64(1) << 53) - 1))
		if mant == 0 {
			mant = 1
		}
		frac := rng.Intn(22) + 1
		neg := rng.Intn(2) == 1
		checkExactMant32(mant, frac, neg)
	}
}
