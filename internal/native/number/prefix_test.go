package number_test

import (
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

// scanAndConvert checks fused parsing against Scan and strconv.
func scanAndConvert(s string, start int) (number.Result, int, types.ParsingError) {
	end, err := number.Scan(s, start)
	if err != 0 {
		return number.Result{}, end, err
	}
	token := s[start:end]
	if !strings.ContainsAny(token, ".eE") {
		if token[0] == '-' {
			if v, err := strconv.ParseInt(token, 10, 64); err == nil {
				return number.Result{Kind: number.Signed, Int64: v}, end, 0
			}
		} else if v, err := strconv.ParseUint(token, 10, 64); err == nil {
			return number.Result{Kind: number.Unsigned, Uint64: v}, end, 0
		}
	}
	v, convErr := strconv.ParseFloat(token, 64)
	result := number.Result{Kind: number.Float, Float64: v}
	if convErr != nil {
		return result, end, types.ERR_FLOAT_INFINITY
	}
	return result, end, 0
}

func checkPrefix(t testing.TB, s string, start int) {
	t.Helper()
	got, end, err := number.ParsePrefix(s, start)
	want, wantEnd, wantErr := scanAndConvert(s, start)
	if end != wantEnd || err != wantErr || got != want || math.Float64bits(got.Float64) != math.Float64bits(want.Float64) {
		t.Fatalf("ParsePrefix(%q,%d)=(%+v,%d,%v); Scan+strconv=(%+v,%d,%v)", s, start, got, end, err, want, wantEnd, wantErr)
	}
}

func TestParsePrefixBoundaries(t *testing.T) {
	for _, s := range []string{
		"", "-", "0", "-0", "01", "-01", "0+1", "0e-0", "-0.0",
		"0x1p2", "1_000", "1+2", "1-2", "1 ",
		"1.", "1.e2", "1e", "1e+", "1e--2", "1e2e3", "1.2.3", "1\x00",
		"9223372036854775807", "9223372036854775808", "9223372036854775809",
		"-9223372036854775807", "-9223372036854775808", "-9223372036854775809",
		"18446744073709551614", "18446744073709551615", "18446744073709551616",
		"-18446744073709551615", "9999999999999999999", "99999999999999999999",
		"1e309", "-1e309", "1e-999", "-1e-999", "1.7976931348623158e308",
		"0." + strings.Repeat("0", 1024) + "1", strings.Repeat("9", 1024),
		strings.Repeat("9", 400) + ".1.2",
	} {
		for _, suffix := range []string{"", ",0]", "e+", "\x00", "\xff"} {
			src := "[ " + s + suffix
			checkPrefix(t, src, 2)
		}
		for _, start := range []int{-1, 0, len(s) / 2, len(s), len(s) + 1} {
			checkPrefix(t, s, start)
		}
	}
}

func TestParsePrefixDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(5641307))
	alphabet := "00011123456789.eE++--\x00\xffxyz,} \n"
	for i := 0; i < 20000; i++ {
		var token string
		switch i % 4 {
		case 0:
			token = strconv.FormatUint(rng.Uint64(), 10)
		case 1:
			token = strconv.FormatInt(int64(rng.Uint64()), 10)
		case 2:
			value := math.Float64frombits(rng.Uint64())
			token = strconv.FormatFloat(value, 'g', -1, 64)
		default:
			buf := make([]byte, rng.Intn(256))
			for j := range buf {
				buf[j] = alphabet[rng.Intn(len(alphabet))]
			}
			token = string(buf)
		}
		checkPrefix(t, token, 0)
		checkPrefix(t, "["+token+",null]", 1)
	}
}

func TestParsePrefixIntegerRangeTransitions(t *testing.T) {
	// Exercise every possible next byte on both sides of the integer range
	// boundaries. A digit extends the integer; punctuation may introduce a
	// fraction/exponent or terminate it, including malformed continuations.
	for digits := 1; digits <= 24; digits++ {
		for _, magnitude := range []string{
			strings.Repeat("9", digits),
			"1" + strings.Repeat("0", digits-1),
		} {
			for _, sign := range []string{"", "-"} {
				token := sign + magnitude
				checkPrefix(t, token, 0)
				for next := 0; next < 256; next++ {
					checkPrefix(t, "[ "+token+string(byte(next))+"7]", 2)
				}
			}
		}
	}
	for _, token := range []string{
		"18446744073709551614", "18446744073709551615", "18446744073709551616",
		"-9223372036854775807", "-9223372036854775808", "-9223372036854775809",
	} {
		for end := 0; end <= len(token); end++ {
			checkPrefix(t, token[:end], 0)
		}
		for _, suffix := range []string{".", ".0", "e", "e+", "e-2", ".1e2", ".1.2"} {
			checkPrefix(t, token+suffix, 0)
		}
	}
}

func FuzzParsePrefix(f *testing.F) {
	for _, s := range []string{"0", "-0", "01", "1e+", "1.25", "18446744073709551616", "-9223372036854775809", "1e999", "-1e-999", "1\x00"} {
		f.Add(s, 0)
	}
	f.Fuzz(func(t *testing.T, s string, start int) {
		if len(s) <= 4096 {
			checkPrefix(t, s, start)
			// Keep exercising syntax after mutations make start out of range.
			checkPrefix(t, s, int(uint(start)%uint(len(s)+1)))
		}
	})
}
