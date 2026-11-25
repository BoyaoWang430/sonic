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
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestNumberScanStrictEventMaskBoundaries(t *testing.T) {
	// Cross 64-bit mask and kernel-block boundaries with digits and markers.
	vl := spaceVectorBytes()
	for digits := 1; digits <= 2*vl+65; digits++ {
		integer := strings.Repeat("7", digits)
		for _, padding := range []string{"", "x["} {
			prefix := padding + "-" + integer
			for _, tc := range []struct {
				suffix string
				end    int
				err    types.ParsingError
			}{
				{",++..EE", 0, 0},
				{".25e-9,++..EE", 6, 0},
				{"e+2.3E4", 5, types.ERR_INVALID_CHAR},
				{"+2.3.4", 4, types.ERR_INVALID_CHAR},
			} {
				checkNumberScan(t, "event-mask", prefix+tc.suffix, len(padding), len(prefix)+tc.end, tc.err, ScanNumberStrict)
			}
		}
		// Carry an already-seen decimal point across complete digit-only blocks.
		fraction := "1." + integer
		checkNumberScan(t, "event-mask-fraction", fraction+"e-4,++..EE", 0, len(fraction)+3, 0, ScanNumberStrict)
		checkNumberScan(t, "event-mask-duplicate", fraction+"e.4", 0, len(fraction)+1, types.ERR_INVALID_CHAR, ScanNumberStrict)
	}
}
