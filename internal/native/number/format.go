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
	if bits != 32 && bits != 64 {
		panic("number.AppendFloat: bits must be 32 or 64")
	}
	if bits == 32 {
		v = float64(float32(v))
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dst
	}
	format := byte('f')
	abs := math.Abs(v)
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, v, format, -1, bits)
	if format == 'e' {
		// strconv emits e-07, whereas the native formatter emits e-7.
		n := len(dst)
		if dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}
