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

package encoder

import "github.com/bytedance/sonic/internal/encoder/alg"

// JSONAppender is Sonic's opt-in buffer-appending marshaler interface
// (AppendSonicJSON(dst []byte) ([]byte, error)).
//
// On Native & Scalar Sonic backends (sonic.APIKind == sonic.UseSonicJSON),
// JSONAppender takes priority over json.Marshaler across root values, struct
// fields, pointers, slices, maps, and interfaces. On the explicit stdlib
// adapter (-tags=sonic_compat, sonic.APIKind == sonic.UseStdJSON), encoding/json
// uses MarshalJSON; portable types targeting both backends must implement both
// AppendSonicJSON and an equivalent MarshalJSON.
type JSONAppender = alg.JSONAppender
