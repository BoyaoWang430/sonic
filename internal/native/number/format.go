package number

import (
	"math"
	"strconv"
)

// AppendFloat appends the shortest JSON representation of v at the requested
// precision, which must be 32 or 64 bits. It uses fixed notation from 1e-6 up to
// (but excluding) 1e21, and scientific notation elsewhere, without a redundant
// exponent zero. For 32 bits, v is first rounded to float32.
//
// Like native f32toa/f64toa, it preserves negative zero and leaves dst unchanged
// for NaN or infinity. Public encoders remain responsible for rejecting these
// non-finite values and for their existing zero-normalization policy.
func AppendFloat(dst []byte, v float64, bits int) []byte {
	var integerLimit, lower, upper float64
	switch bits {
	case 64:
		integerLimit, lower, upper = 1<<53, 1e-6, 1e21
	case 32:
		v = float64(float32(v))
		integerLimit = 1 << 24
		lower, upper = float64(float32(1e-6)), float64(float32(1e21))
	default:
		panic("number.AppendFloat: bits must be 32 or 64")
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dst
	}
	abs := math.Abs(v)
	if abs != 0 {
		// In each precision's exact-integer range, the shortest decimal is
		// the integer itself. Larger integers may have a shorter rounded
		// representation and must retain shortest-float formatting.
		if abs <= integerLimit {
			integer := int64(v)
			if float64(integer) == v {
				return strconv.AppendInt(dst, integer, 10)
			}
		}
	}
	if abs == 0 || abs >= lower && abs < upper {
		return strconv.AppendFloat(dst, v, 'f', -1, bits)
	}
	dst = strconv.AppendFloat(dst, v, 'e', -1, bits)
	// strconv emits e-07, whereas the native formatter emits e-7.
	tail := dst[len(dst)-4:]
	if tail[0] == 'e' && tail[1] == '-' && tail[2] == '0' {
		tail[2] = tail[3]
		dst = dst[:len(dst)-1]
	}
	return dst
}
