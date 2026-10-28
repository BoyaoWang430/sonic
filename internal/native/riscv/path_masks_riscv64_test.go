//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

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
		ww := min(len(src)-start, 64)
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
			check(src, min(n, 32))
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
