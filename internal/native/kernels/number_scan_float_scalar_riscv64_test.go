//go:build !sonic_compat && !sonic_scalar && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

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
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestNumberScanStrictShortFloatContinuation(t *testing.T) {
	// Exercise transitions between short fractional/exponent runs and aligned
	// word scanning. Earlier markers must survive that transition, including
	// when a later duplicate takes priority over an already misplaced sign.
	for _, digits := range []int{1, 3, 4, 5, 7, 8, 9, 15, 16, 17, 63, 64} {
		for _, sign := range []string{"", "-"} {
			body := sign + "12." + strings.Repeat("7", digits)
			for align := 0; align < 16; align++ {
				padding := strings.Repeat("x", align)
				for _, tc := range []struct {
					suffix string
					end    int
					err    types.ParsingError
				}{
					{"", 0, 0}, {",++..ee", 0, 0},
					{"e+3", 3, 0}, {"E-12345,", 7, 0},
					{"e", 0, types.ERR_INVALID_CHAR},
					{"e+", 1, types.ERR_INVALID_CHAR},
					{"e.2", 1, types.ERR_INVALID_CHAR},
					{"+3", 0, types.ERR_INVALID_CHAR},
					{"+3+4", 2, types.ERR_INVALID_CHAR},
					{"+3e+4", 3, types.ERR_INVALID_CHAR},
					{"e+3.4", 3, types.ERR_INVALID_CHAR},
					{"e+3e4", 3, types.ERR_INVALID_CHAR},
				} {
					checkNumberScan(t, "short-float-continuation", padding+body+tc.suffix, align,
						align+len(body)+tc.end, tc.err, ScanNumberStrict)
				}
			}
		}
	}
}
