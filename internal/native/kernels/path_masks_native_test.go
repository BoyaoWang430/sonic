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
	"math/rand"
	"strings"
	"testing"
)

func TestPathMasks(t *testing.T) {
	check := func(src string, start int) {
		t.Helper()
		e, n, b, width := PathMasks(src, start)
		var we, wn, wb uint64
		ww := minInt(len(src)-start, 64)
		for i := 0; i < ww; i++ {
			c := src[start+i]
			bit := uint64(1) << uint(i)
			if c == '"' || c == '\\' {
				we |= bit
			}
			switch c {
			case ' ', '\t', '\r', '\n':
				wb |= bit
			default:
				wn |= bit
			}
			switch c {
			case '}', ']', ',':
				wb |= bit
			}
		}
		if e != we || n != wn || b != wb || width != ww {
			t.Fatalf("src=%q start=%d: (%x,%x,%x,%d) want (%x,%x,%x,%d)", src, start, e, n, b, width, we, wn, wb, ww)
		}
	}
	for n := 0; n <= 65; n++ {
		for x := 0; x < 256; x++ {
			src := strings.Repeat("a", n) + string([]byte{byte(x)}) + "\t,\\\"} \r\n]"
			check(src, 0)
			check(src, minInt(n, 32))
			check(src, len(src))
		}
	}
	r := rand.New(rand.NewSource(20261024))
	for k := 0; k < 10000; k++ {
		b := make([]byte, r.Intn(200))
		for i := range b {
			b[i] = byte(r.Intn(256))
		}
		check(string(b), r.Intn(len(b)+1))
	}
}
