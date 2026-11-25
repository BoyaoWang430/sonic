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

// NonSpaceMask classifies at most 64 bytes from src. Bit i is set
// exactly when src[i] is not JSON whitespace. Bits beyond width are zero.
// The input needs no readable padding.
func NonSpaceMask(src string) (mask uint64, width int) {
	width = len(src)
	if width > 64 {
		width = 64
	}
	if cpu.UseScalar {
		return nonSpaceMaskScalar(rt.StringData(src), width), width
	}
	return nonSpaceMask(rt.StringData(src), width), width
}
