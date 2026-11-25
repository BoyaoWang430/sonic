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
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestTraversalMasksOffsetsAndTails(t *testing.T) {
	classes := [...]byte{'x', '"', '\\', ' ', '\t', '\n', '\r', 0, 31, 0x80, 0xff}
	for offset := 0; offset < 8; offset++ {
		for n := 0; n <= 64; n++ {
			for rotation := range classes {
				storage := make([]byte, n+24)
				for i := range storage {
					storage[i] = '\\'
				}
				start := 8 + offset
				for i := 0; i < n; i++ {
					storage[start+i] = classes[(i+rotation)%len(classes)]
				}
				// Preserve the address offset; converting the slice to string would
				// copy it and could hide unaligned loads and overlapping tail errors.
				src := rt.String(&storage[start], n)
				for _, validate := range []bool{false, true} {
					var wantEvents, wantNonspace uint64
					for i := 0; i < n; i++ {
						c := src[i]
						bit := uint64(1) << uint(i)
						if c == '"' || c == '\\' || validate && c < ' ' {
							wantEvents |= bit
						}
						if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
							wantNonspace |= bit
						}
					}
					events, nonspace, width := TraversalMasks(src, 0, validate)
					if events != wantEvents || nonspace != wantNonspace || width != n {
						t.Fatalf("offset=%d size=%d rotation=%d validate=%t: got (%x,%x,%d), want (%x,%x,%d)", offset, n, rotation, validate, events, nonspace, width, wantEvents, wantNonspace, n)
					}
				}
			}
		}
	}
}
