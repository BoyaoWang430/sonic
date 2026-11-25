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
	"math/bits"
	"testing"
)

func TestStringSpecialMaskFirstEvent(t *testing.T) {
	check := func(word uint64) {
		t.Helper()
		want := 8
		for i := 0; i < 8; i++ {
			b := byte(word >> (8 * i))
			if b == '"' || b == '\\' || b < 0x20 {
				want = i
				break
			}
		}
		got := bits.TrailingZeros64(StringSpecialMask(word)) / 8
		if got != want {
			t.Fatalf("word=%016x: first event=%d, want=%d", word, got, want)
		}
	}
	// High bytes remain ordinary. Adjacent values exercise subtraction borrows:
	// later false positives are permitted, but the first marked byte is exact.
	for _, fill := range []byte{0x20, 'a', 0x7f, 0x80, 0xa2, 0xff} {
		base := uint64(fill) * 0x0101010101010101
		check(base)
		for at := 0; at < 8; at++ {
			for b := 0; b < 256; b++ {
				word := base &^ (uint64(0xff) << (8 * at))
				check(word | uint64(b)<<(8*at))
			}
		}
	}
	for at := 0; at < 7; at++ {
		for pair := 0; pair < 1<<16; pair++ {
			word := uint64(0x6161616161616161) &^ (uint64(0xffff) << (8 * at))
			check(word | uint64(pair)<<(8*at))
		}
	}
}
