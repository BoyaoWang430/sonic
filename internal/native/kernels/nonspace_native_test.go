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
)

func TestNonSpaceMask(t *testing.T) {
	for value := 0; value < 256; value++ {
		for n := 0; n <= 70; n++ {
			src := "prefix" + strings.Repeat(string([]byte{byte(value)}), n)
			got, width := NonSpaceMask(src[6:])
			wantWidth := minInt(n, 64)
			want := ^uint64(0) >> uint(64-wantWidth)
			if wantWidth == 0 || value == ' ' || value == '\t' || value == '\n' || value == '\r' {
				want = 0
			}
			if got != want || width != wantWidth {
				t.Fatalf("byte=%x len=%d: (%x,%d), want (%x,%d)", value, n, got, width, want, wantWidth)
			}
		}
	}
	for lane := 0; lane < 64; lane++ {
		buf := []byte(strings.Repeat(" \t\r\n", 16))
		buf[lane] = 'x'
		mask, width := NonSpaceMask(string(buf))
		if mask != uint64(1)<<uint(lane) || width != 64 {
			t.Fatalf("lane=%d: (%x,%d)", lane, mask, width)
		}
	}
}
