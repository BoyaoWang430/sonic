//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

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
