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

// SkipSpace returns the absolute offset of the first byte at or after start
// that is not JSON whitespace (space, tab, carriage return, or line feed).
// It returns len(src) if the remaining input contains only whitespace.
// start must be in [0, len(src)]. The input needs no readable padding.
func SkipSpace(src string, start int) int {
	tail := src[start:]
	if len(tail) == 0 {
		return start
	}
	// Most tokens have no leading whitespace, so avoid starting a bulk scan.
	if tail[0] > ' ' {
		return start
	}
	// Short indentation and separators are common even in large documents.
	// Consume up to four bytes before paying for a bulk scan.
	for n := 0; n < 4 && n < len(tail); n++ {
		switch tail[n] {
		case ' ', '\t', '\r', '\n':
		default:
			return start + n
		}
	}
	if len(tail) <= 4 {
		return start + len(tail)
	}
	if cpu.UseScalar {
		return start + 4 + spaceScalar(rt.StringData(tail[4:]), len(tail)-4)
	}
	return start + 4 + space(rt.StringData(tail[4:]), len(tail)-4)
}
