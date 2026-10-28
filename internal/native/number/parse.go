package number

import (
	"strconv"

	"github.com/bytedance/sonic/internal/native/types"
)

// Kind identifies a parsed number's representation.
type Kind uint8

const (
	Invalid Kind = iota
	Signed
	Unsigned
	Float
)

// Result contains a parsed JSON number. Only the field selected by Kind is
// meaningful. Nonnegative integer tokens use Unsigned, negative integer tokens
// use Signed, and decimals, exponents, and integers outside those ranges use
// Float. The token "-0" is Signed zero; "-0.0" retains its floating-point sign.
type Result struct {
	Kind    Kind
	Int64   int64
	Uint64  uint64
	Float64 float64
}

// ParsePrefix validates and converts one JSON number beginning at start. Its
// end position and syntax errors follow Scan; the caller checks the following
// delimiter. Floating-point overflow returns the consumed end position, a
// Float result containing signed infinity, and ERR_FLOAT_INFINITY.
//
// Integer syntax and accumulation share a single bounded pass. Fractions,
// exponents, and integers beyond uint64 use the existing scanner and strconv's
// correctly rounded float conversion, without a second validation scan or a
// separate search for number-kind markers.
func ParsePrefix(s string, start int) (Result, int, types.ParsingError) {
	if start < 0 || start > len(s) {
		return Result{}, start, types.ERR_INVALID_CHAR
	}
	i := start
	if i == len(s) {
		return Result{}, i, types.ERR_EOF
	}
	negative := s[i] == '-'
	if negative {
		i++
		if i == len(s) {
			return Result{}, i, types.ERR_EOF
		}
	}
	digit := s[i] - '0'
	if digit > 9 {
		return Result{}, i, types.ERR_INVALID_CHAR
	}
	value := uint64(digit)
	i++
	if digit != 0 {
		// Every sequence of at most 19 decimal digits fits in uint64. Keep
		// overflow checks out of this bounded accumulation window; only a
		// twentieth digit can cross the unsigned limit.
		limit := len(s)
		if len(s)-i > 18 {
			limit = i + 18
		}
		for i < limit {
			digit = s[i] - '0'
			if digit > 9 {
				goto integerEnd
			}
			value = value*10 + uint64(digit)
			i++
		}
		for i < len(s) {
			digit = s[i] - '0'
			if digit > 9 {
				break
			}
			// Guard before multiplication, including MaxUint64's final digit.
			const cutoff = ^uint64(0) / 10
			if value > cutoff || value == cutoff && digit > 5 {
				return parseFloatPrefix(s, start)
			}
			value = value*10 + uint64(digit)
			i++
		}
	}
integerEnd:
	if i < len(s) && (s[i] == '.' || s[i]|0x20 == 'e') {
		return parseFloatPrefix(s, start)
	}
	if negative {
		if value <= 1<<63 {
			return Result{Kind: Signed, Int64: -int64(value)}, i, 0
		}
		result, err := parseFloat(s[start:i])
		return result, i, err
	}
	return Result{Kind: Unsigned, Uint64: value}, i, 0
}

func parseFloatPrefix(s string, start int) (Result, int, types.ParsingError) {
	end, err := Scan(s, start)
	if err != 0 {
		return Result{}, end, err
	}
	result, err := parseFloat(s[start:end])
	return result, end, err
}

func parseFloat(s string) (Result, types.ParsingError) {
	v, convErr := strconv.ParseFloat(s, 64)
	result := Result{Kind: Float, Float64: v}
	if convErr != nil {
		// The caller has already validated JSON number syntax, so only a
		// range error is possible here.
		return result, types.ERR_FLOAT_INFINITY
	}
	return result, 0
}
