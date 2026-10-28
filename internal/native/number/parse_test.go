package number_test

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

func TestParsePrefixClassification(t *testing.T) {
	tests := []struct {
		s    string
		want number.Result
	}{
		{"0", number.Result{Kind: number.Unsigned}},
		{"-0", number.Result{Kind: number.Signed}},
		{"123", number.Result{Kind: number.Unsigned, Uint64: 123}},
		{"-123", number.Result{Kind: number.Signed, Int64: -123}},
		{"9223372036854775807", number.Result{Kind: number.Unsigned, Uint64: math.MaxInt64}},
		{"9223372036854775808", number.Result{Kind: number.Unsigned, Uint64: 1 << 63}},
		{"18446744073709551615", number.Result{Kind: number.Unsigned, Uint64: math.MaxUint64}},
		{"-9223372036854775808", number.Result{Kind: number.Signed, Int64: math.MinInt64}},
		{"-9223372036854775809", number.Result{Kind: number.Float, Float64: -9223372036854775809.0}},
		{"18446744073709551616", number.Result{Kind: number.Float, Float64: 18446744073709551616.0}},
		{"1.0", number.Result{Kind: number.Float, Float64: 1}},
		{"1e0", number.Result{Kind: number.Float, Float64: 1}},
		{"-0.0", number.Result{Kind: number.Float, Float64: math.Copysign(0, -1)}},
		{"-0e999999999999999999999", number.Result{Kind: number.Float, Float64: math.Copysign(0, -1)}},
		{"0e999999999999999999999", number.Result{Kind: number.Float}},
		{"1e-999999999999999999999", number.Result{Kind: number.Float}},
		{"-1e-999999999999999999999", number.Result{Kind: number.Float, Float64: math.Copysign(0, -1)}},
		{"1.7976931348623157e308", number.Result{Kind: number.Float, Float64: math.MaxFloat64}},
		{"4.9406564584124654e-324", number.Result{Kind: number.Float, Float64: math.SmallestNonzeroFloat64}},
		{"2.2250738585072014e-308", number.Result{Kind: number.Float, Float64: 0x1p-1022}},
		// Exact halfway values exercise round-to-nearest, ties-to-even.
		{"1.00000000000000011102230246251565404236316680908203125", number.Result{Kind: number.Float, Float64: 1}},
		{"1.00000000000000033306690738754696212708950042724609375", number.Result{Kind: number.Float, Float64: math.Float64frombits(math.Float64bits(1) + 2)}},
		{"0." + strings.Repeat("0", 800) + "1", number.Result{Kind: number.Float}},
	}
	for _, tt := range tests {
		got, end, err := number.ParsePrefix(tt.s, 0)
		if err != 0 || end != len(tt.s) || got != tt.want || math.Float64bits(got.Float64) != math.Float64bits(tt.want.Float64) {
			t.Errorf("ParsePrefix(%q) = (%+v, %d, %d), want (%+v, %d, 0) (float bits %x)", tt.s, got, end, err, tt.want, len(tt.s), math.Float64bits(tt.want.Float64))
		}
	}
}

func TestParsePrefixInvalid(t *testing.T) {
	tests := []struct {
		s   string
		end int
		err types.ParsingError
	}{
		{"", 0, types.ERR_EOF}, {"-", 1, types.ERR_EOF}, {"1.", 2, types.ERR_EOF},
		{"1e", 2, types.ERR_EOF}, {"1e+", 3, types.ERR_EOF},
		{"+1", 0, types.ERR_INVALID_CHAR}, {".1", 0, types.ERR_INVALID_CHAR},
		{"1.e2", 2, types.ERR_INVALID_CHAR}, {"1e--2", 3, types.ERR_INVALID_CHAR},
		{"NaN", 0, types.ERR_INVALID_CHAR}, {"Inf", 0, types.ERR_INVALID_CHAR},
		{"-Infinity", 1, types.ERR_INVALID_CHAR}, {" 1", 0, types.ERR_INVALID_CHAR},
	}
	for _, tt := range tests {
		got, end, err := number.ParsePrefix(tt.s, 0)
		if got != (number.Result{}) || end != tt.end || err != tt.err {
			t.Errorf("ParsePrefix(%q) = (%+v, %d, %d), want zero result, %d, %d", tt.s, got, end, err, tt.end, tt.err)
		}
	}
}

func TestParsePrefixOverflowAndRawNumbers(t *testing.T) {
	for _, s := range []string{"1e309", "-1e309", "1.7976931348623159e308", strings.Repeat("9", 1000)} {
		end, err := number.Scan(s, 0)
		if err != 0 || end != len(s) {
			t.Fatalf("raw number %q: Scan = (%d, %d)", s, end, err)
		}
		got, end, err := number.ParsePrefix(s, 0)
		if end != len(s) || err != types.ERR_FLOAT_INFINITY || got.Kind != number.Float || !math.IsInf(got.Float64, 0) || math.Signbit(got.Float64) != (s[0] == '-') {
			t.Errorf("ParsePrefix(%q) = (%+v, %d, %d), want end %d, signed infinity and ERR_FLOAT_INFINITY", s, got, end, err, len(s))
		}
		// UseNumber accepts overflowed raw numbers.
		decoder := json.NewDecoder(strings.NewReader(s))
		decoder.UseNumber()
		var raw interface{}
		if err := decoder.Decode(&raw); err != nil || raw != json.Number(s) {
			t.Errorf("UseNumber(%q) = (%v, %v)", s, raw, err)
		}
	}
}

func TestParsePrefixAgainstJSON(t *testing.T) {
	for _, s := range []string{
		"0", "-0", "-0.0", "0.1", "1234567890123456789012345678901234567890",
		"9007199254740993", "9223372036854775808", "18446744073709551615",
		"1.234567890123456789012345678901234567890123456789e-123",
		"2.4703282292062327e-324", "2.4703282292062328e-324",
		"-1e-400", "1.7976931348623158e308",
	} {
		comparePrefixWithJSON(t, s)
	}
	for exp := -340; exp <= 310; exp++ {
		comparePrefixWithJSON(t, "-12345678901234567890123456789e"+strconv.Itoa(exp))
	}
}

func comparePrefixWithJSON(t *testing.T, s string) {
	t.Helper()
	var want float64
	jsonErr := json.Unmarshal([]byte(s), &want)
	got, end, err := number.ParsePrefix(s, 0)
	if end != len(s) {
		t.Fatalf("ParsePrefix(%q) stopped at %d, want %d", s, end, len(s))
	}
	if jsonErr != nil {
		if err == 0 {
			t.Errorf("ParsePrefix(%q) succeeded, but encoding/json rejected it: %v", s, jsonErr)
		}
		return
	}
	if err != 0 {
		t.Errorf("ParsePrefix(%q) failed with %d; encoding/json returned %g", s, err, want)
		return
	}
	var value float64
	switch got.Kind {
	case number.Signed:
		value = float64(got.Int64)
	case number.Unsigned:
		value = float64(got.Uint64)
	case number.Float:
		value = got.Float64
	default:
		t.Fatalf("ParsePrefix(%q) returned invalid kind %d", s, got.Kind)
	}
	// Integer -0 is Signed zero; conversion to float64 loses its sign.
	if s == "-0" {
		want = 0
	}
	if math.Float64bits(value) != math.Float64bits(want) {
		t.Errorf("ParsePrefix(%q) converted to %g (%x), want %g (%x)", s, value, math.Float64bits(value), want, math.Float64bits(want))
	}
}

func FuzzParseJSONNumber(f *testing.F) {
	for _, s := range []string{"0", "-0", "01", "1e+", "1.25", "18446744073709551616", "1e999", "-1e-999", "1\x00", "NaN"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			return
		}
		end, err := number.Scan(s, 0)
		valid := err == 0 && end == len(s)
		wantValid := len(s) > 0 && (s[0] == '-' || s[0] >= '0' && s[0] <= '9') &&
			strings.TrimSpace(s) == s && json.Valid([]byte(s))
		if valid != wantValid {
			t.Fatalf("Scan(%q) = (%d, %d), JSON number validity = %v", s, end, err, wantValid)
		}
		checkPrefix(t, s, 0)
		if valid {
			comparePrefixWithJSON(t, s)
		}
	})
}
