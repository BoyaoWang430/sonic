package vm

import (
	"math"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

// Encode non-nil slices using the scalar formatter's value policies.
func appendFloat64Slice(buf []byte, values []float64, flags uint64) ([]byte, error) {
	buf = append(buf, '[')
	for i, v := range values {
		if i != 0 {
			buf = append(buf, ',')
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if flags&(1<<alg.BitEncodeNullForInfOrNan) == 0 {
				return buf, vars.ERR_nan_or_infinite
			}
			buf = append(buf, 'n', 'u', 'l', 'l')
		} else {
			buf = alg.F64toa(buf, v)
		}
	}
	return append(buf, ']'), nil
}

func appendFloat32Slice(buf []byte, values []float32, flags uint64) ([]byte, error) {
	buf = append(buf, '[')
	for i, v := range values {
		if i != 0 {
			buf = append(buf, ',')
		}
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			if flags&(1<<alg.BitEncodeNullForInfOrNan) == 0 {
				return buf, vars.ERR_nan_or_infinite
			}
			buf = append(buf, 'n', 'u', 'l', 'l')
		} else {
			buf = alg.F32toa(buf, v)
		}
	}
	return append(buf, ']'), nil
}
