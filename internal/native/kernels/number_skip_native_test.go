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

// A relaxed candidate ends only at the seven documented delimiters, even
// when its contents are not a valid number. Exercise every byte at each
// position around the vector and block boundaries, with nonzero starts.
func TestSkipNumberBoundaries(t *testing.T) {
	for prefix := 0; prefix < 16; prefix++ {
		for offset := 0; offset <= 129; offset++ {
			for c := 0; c < 256; c++ {
				input := make([]byte, prefix+offset+18)
				for i := range input {
					input[i] = '7'
				}
				input[prefix+offset] = byte(c)
				input[len(input)-1] = ']'
				want := len(input) - 1
				switch byte(c) {
				case ',', ']', '}', ' ', '\t', '\n', '\r':
					want = prefix + offset
				}
				if got := SkipNumber(string(input), prefix); got != want {
					t.Fatalf("start=%d offset=%d byte=%02x: end=%d, want %d", prefix, offset, c, got, want)
				}
			}
		}
	}
}

// Dense matches exercise the block reduction as well as the first-hit mask.
// In particular, a whole vector of the largest delimiter class must not
// overflow the byte reduction and be mistaken for a block without a boundary.
func TestSkipNumberDenseBoundaries(t *testing.T) {
	for _, size := range []int{16, 17, 31, 32, 63, 64, 65, 127, 128, 129} {
		for c := 0; c < 256; c++ {
			input := make([]byte, size+1)
			for i := 0; i < size; i++ {
				input[i] = byte(c)
			}
			input[size] = ']'
			want := size
			switch byte(c) {
			case ',', ']', '}', ' ', '\t', '\n', '\r':
				want = 0
			}
			if got := SkipNumber(string(input), 0); got != want {
				t.Fatalf("size=%d byte=%02x: end=%d, want %d", size, c, got, want)
			}
		}
	}
}
