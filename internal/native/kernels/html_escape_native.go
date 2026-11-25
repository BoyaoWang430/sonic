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

// HTMLEscape copies src into dst, escaping <, >, &, U+2028, and U+2029 using
// lowercase Unicode escapes. Other bytes, including invalid UTF-8, are preserved.
// It does not add quotes or perform JSON string escaping.
//
// On success consumed equals len(src). If dst is too short, consumed is the
// complement of the input byte count processed. written is the output byte count
// in both cases. Every escape is written atomically, including the three-byte
// U+2028/U+2029 input. src and dst must not overlap; neither requires padding.
func HTMLEscape(src string, dst []byte) (consumed, written int) {
	return HTMLEscapeBuffer(rt.StringData(src), len(src), rt.SliceData(dst), len(dst))
}

// HTMLEscapeBuffer copies size bytes from src into dst capacity bytes while
// escaping HTML-unsafe runes, avoiding string/slice header construction.
func HTMLEscapeBuffer(src *byte, size int, dst *byte, capacity int) (consumed, written int) {
	if size <= 0 {
		return 0, 0
	}
	if cpu.UseScalar {
		return htmlEscapeScalar(src, size, dst, capacity)
	}
	return htmlEscape(src, size, dst, capacity)
}
