//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"testing"
	"unsafe"
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
				src := unsafe.String(&storage[start], n)
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
