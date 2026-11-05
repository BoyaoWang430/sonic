package number

// exactPowersOfTen contains exactly representable binary64 integers. For
// 10^k = 2^k * 5^k with k <= 22, 5^k fits in the binary64 significand.
var exactPowersOfTen = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9,
	1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19,
	1e20, 1e21, 1e22,
}

// exactFloatSuffix continues an already validated integer prefix. i points to
// its decimal point or exponent marker; mantissa is its unsigned magnitude.
// It accepts a conservative subset of strconv's atof64exact: an exact integer
// mantissa and one multiplication or division by an exact power of ten. The
// single floating-point operation therefore supplies the final rounding.
//
// A positive end with ok=false means the complete token was validated but
// requires general float conversion. An end of zero means validation stopped
// early; the caller must scan the token to preserve its syntax error position.
func exactFloatSuffix(s string, i int, mantissa uint64, negative bool) (float64, int, bool) {
	if mantissa >= 1<<52 || uint(i) >= uint(len(s)) {
		return 0, 0, false
	}
	exponent := 0
	if s[i] == '.' {
		i++
		first := i
		for i < len(s) {
			digit := uint64(s[i]) - '0'
			if digit > 9 {
				break
			}
			if exponent == -22 {
				return 0, 0, false
			}
			// The previous mantissa is below 2^52, so this cannot overflow uint64.
			mantissa = mantissa*10 + digit
			if mantissa >= 1<<52 {
				return 0, 0, false
			}
			exponent--
			i++
		}
		if i == first {
			return 0, 0, false
		}
	}
	if i < len(s) && s[i]|0x20 == 'e' {
		i++
		expNegative := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			expNegative = s[i] == '-'
			i++
		}
		first := i
		exp := 0
		for i < len(s) {
			digit := uint64(s[i]) - '0'
			if digit > 9 {
				break
			}
			// Three exponent digits cover the ordinary binary64 decimal range.
			// Longer exponents retain the bounded fallback to the bulk scanner.
			if i-first == 3 {
				return 0, 0, false
			}
			exp = exp*10 + int(digit)
			i++
		}
		if i == first {
			return 0, 0, false
		}
		if expNegative {
			exponent -= exp
		} else {
			exponent += exp
		}
	}
	if exponent < -22 || exponent > 22 || exponent > 0 && mantissa > 1000000000000000 {
		return 0, i, false
	}
	value := float64(mantissa)
	// Apply the sign before arithmetic so a zero mantissa retains negative zero.
	if negative {
		value = -value
	}
	if exponent > 0 {
		value *= exactPowersOfTen[exponent]
	} else if exponent < 0 {
		value /= exactPowersOfTen[-exponent]
	}
	return value, i, true
}
