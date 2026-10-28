//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"
)

func TestTraversalMasks(t *testing.T) {
	check := func(src string, start int, validate bool) {
		t.Helper()
		events, n, width := TraversalMasks(src, start, validate)
		var wantQ, wantS, wantC, wantN uint64
		wantWidth := min(len(src)-start, 64)
		for i := 0; i < wantWidth; i++ {
			b := src[start+i]
			bit := uint64(1) << uint(i)
			if b == '"' {
				wantQ |= bit
			}
			if b == '\\' {
				wantS |= bit
			}
			if b < ' ' {
				wantC |= bit
			}
			if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				wantN |= bit
			}
		}
		wantEvents := wantQ | wantS
		if validate {
			wantEvents |= wantC
		}
		if events != wantEvents || n != wantN || width != wantWidth {
			t.Fatalf("start=%d len=%d validate=%t got=(%x,%x,%d) want=(%x,%x,%d)", start, len(src), validate, events, n, width, wantEvents, wantN, wantWidth)
		}
	}
	for value := 0; value < 256; value++ {
		for n := 0; n <= 70; n++ {
			check("prefix"+strings.Repeat(string([]byte{byte(value)}), n), 6, false)
			check("prefix"+strings.Repeat(string([]byte{byte(value)}), n), 6, true)
		}
	}
	for lane := 0; lane < 64; lane++ {
		for _, value := range []byte{'"', '\\', ' ', '\n', '\r', '\t', 0, 31, 255} {
			src := []byte(strings.Repeat("x", 64))
			src[lane] = value
			check(string(src), 0, false)
			check(string(src), 0, true)
		}
	}
}
