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

package alg

import "github.com/bytedance/sonic/internal/native/number"

// The Go formatter already appends to a Go slice. Keep that slice through
// the caller instead of reserving a native output region and rebuilding it.
// Preserve the existing encoder policy that represents either zero as "0".
func F64toa(buf []byte, v float64) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	return number.AppendFloat64(buf, v)
}

func F32toa(buf []byte, v float32) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	return number.AppendFloat32(buf, v)
}
