//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"
)

func TestNonSpaceMask(t *testing.T) {
	for value := 0; value < 256; value++ {
		for n := 0; n <= 70; n++ {
			src := "prefix" + strings.Repeat(string([]byte{byte(value)}), n)
			got, width := NonSpaceMask(src, 6)
			wantWidth := min(n, 64)
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
		mask, width := NonSpaceMask(string(buf), 0)
		if mask != uint64(1)<<uint(lane) || width != 64 {
			t.Fatalf("lane=%d: (%x,%d)", lane, mask, width)
		}
	}
}
