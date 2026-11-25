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
