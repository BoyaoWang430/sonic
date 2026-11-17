package number

import (
	"errors"
	"math"
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
