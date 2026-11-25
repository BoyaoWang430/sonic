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

import "testing"

func TestNumberWordFirstNonDigit(t *testing.T) {
	// Exhaust each byte in each lane, with low/high digits before it and
	// non-ASCII bytes after it to exercise carry and borrow boundaries. Put the
	// run after an exponent: the production prefix scanner must stop at every
	// non-digit, including bytes that are markers in earlier number phases.
	for _, digit := range []byte{'0', '9'} {
		for offset := 0; offset < 16; offset++ {
			for size := 0; size <= 32; size++ {
				for lane := 0; lane < size; lane++ {
					for value := 0; value < 256; value++ {
						data := make([]byte, offset+3+size)
						copy(data[offset:], "1e1")
						start := offset + 3
						for i := start; i < len(data); i++ {
							data[i] = digit
						}
						data[start+lane] = byte(value)
						if lane+1 < size {
							data[start+lane+1] = 0xff
						}
						want := start
						for want < len(data) && data[want] >= '0' && data[want] <= '9' {
							want++
						}
						if got, _ := ScanNumber(string(data), offset); got != want {
							t.Fatalf("digit=%q offset=%d size=%d lane=%d value=%#x: got %d, want %d", digit, offset, size, lane, value, got, want)
						}
					}
				}
			}
		}
	}
}
