/*
 * Copyright 2021 ByteDance Inc.
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

package cpu

import (
	"fmt"
	"os"
)

var (
	HasAVX2   bool
	HasSSE    bool
	HasSSSE3  bool
	HasPOPCNT bool
	HasCLMUL  bool

	// UseAVX2, UseSSSE3, and UseScalar record the resolved kernel dispatch tier
	// selected once at process init (after applying SONIC_MODE overrides or
	// pure-Go scalar build constraints). Direct //go:noescape assembly entrypoints
	// read package-local mirrors of these booleans rather than indirect Go func
	// pointers so pointer arguments remain non-escaping and avoid ABI0 closure
	// trampolines.
	UseAVX2   bool
	UseSSSE3  bool
	UseScalar bool
)

func init() {
	HasAVX2, HasSSE, HasSSSE3, HasPOPCNT, HasCLMUL = detectCPU()
	switch v := os.Getenv("SONIC_MODE"); v {
	case "", "auto":
		break
	case "noavx", "noavx2":
		HasAVX2 = false
	case "nossse3":
		HasAVX2 = false
		HasSSSE3 = false
	case "scalar":
		UseScalar = true
		HasAVX2 = false
		HasSSSE3 = false
	default:
		panic(fmt.Sprintf("invalid mode: '%s', should be one of 'auto', 'noavx', 'noavx2', 'nossse3', 'scalar'", v))
	}
	if forceScalarBuild {
		UseScalar = true
		HasAVX2 = false
		HasSSSE3 = false
	}
	UseAVX2 = HasAVX2 && HasPOPCNT && HasCLMUL
	UseSSSE3 = HasSSSE3
}
