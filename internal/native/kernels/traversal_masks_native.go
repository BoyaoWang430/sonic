//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/rt"
)

// TraversalMasks classifies at most 64 bytes once for string boundaries and
// surrounding JSON whitespace. Events mark quotes, backslashes and, when
// validate is enabled, raw controls. Bits beyond width are zero. No padding is
// required; callers may retain these masks while traversing immutable src.
func TraversalMasks(src string, start int, validate bool) (events, nonspace uint64, width int) {
	tail := src[start:]
	width = minInt(len(tail), 64)
	if width == 0 {
		return 0, 0, 0
	}
	var strict uint64
	if validate {
		strict = 1
	}
	if cpu.UseScalar {
		events, nonspace = traversalMasksScalar(rt.StringData(tail), width, strict)
		return
	}
	events, nonspace = traversalMasks(rt.StringData(tail), width, strict)
	return
}
