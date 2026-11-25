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

// JSONAppender is an opt-in Sonic native extension interface for custom types
// that append their JSON representation directly into the destination buffer's
// spare capacity.
//
// Portability contract across Native/Scalar and compat backends:
//   - On Native & Scalar Sonic backends (sonic.APIKind == sonic.UseSonicJSON),
//     AppendSonicJSON takes priority over json.Marshaler (MarshalJSON) across
//     root values, struct fields, pointers, slices, maps, and interfaces.
//   - On the explicit stdlib adapter (-tags=sonic_compat, sonic.APIKind == sonic.UseStdJSON)
//     or when passed to encoding/json directly, MarshalJSON is used instead.
//   - Portable types that target both backends must implement an equivalent
//     MarshalJSON alongside AppendSonicJSON.
type JSONAppender interface {
	AppendSonicJSON(dst []byte) ([]byte, error)
}
