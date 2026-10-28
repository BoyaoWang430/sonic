//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestSkipSpace(t *testing.T) {
	t.Logf("toolchain=%s scan width=%d bits", runtime.Version(), spaceVectorBytes()*8)
	for _, tt := range []struct {
		name        string
		src         string
		start, want int
	}{
		{"empty", "", 0, 0},
		{"json-space", " \t\r\n", 0, 4},
		{"object", " \t\r\n{", 0, 4},
		{"array-offset", "x \t[1]", 1, 3},
		{"within-whitespace", " \t\r\n{", 2, 4},
		{"at-end", "x \t", 3, 3},
		{"already-at-token", " true", 1, 1},
		{"nul", " \x00", 0, 1},
		{"vertical-tab", " \v", 0, 1},
		{"form-feed", " \f", 0, 1},
		{"unit-separator", " \x1f", 0, 1},
		{"punctuation", " !", 0, 1},
		{"delete", " \x7f", 0, 1},
		{"invalid-utf8", " \xff", 0, 1},
		{"unicode-space", " \u00a0", 0, 1},
		{"unicode-line-separator", " \u2028", 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := SkipSpace(tt.src, tt.start); got != tt.want {
				t.Fatalf("SkipSpace(%q, %d) = %d, want %d", tt.src, tt.start, got, tt.want)
			}
		})
	}
	if got := space(nil, 0); got != 0 {
		t.Fatalf("space(nil, 0) = %d, want 0", got)
	}
}

func TestSkipSpaceEveryByte(t *testing.T) {
	// JSON defines exactly these four whitespace bytes; every other byte
	// must stop the scan, including non-JSON controls and invalid UTF-8.
	wants := [256]int{' ': 1, '\t': 1, '\r': 1, '\n': 1}
	for b, want := range wants {
		src := string([]byte{byte(b)})
		if got := SkipSpace(src, 0); got != want {
			t.Fatalf("SkipSpace byte %#02x = %d, want %d", b, got, want)
		}
	}
}

func TestSkipSpaceLengthsAndAlignments(t *testing.T) {
	vl := spaceVectorBytes()
	// The expected offset follows from the constructed prefix length. No
	// second scanner is used as an oracle.
	// All inputs share one allocation, so offsets [0, vl) cover every actual
	// address alignment modulo the vector register size.
	storage := make([]byte, 4*vl+6)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			white := strings.Repeat(" \t\r\n", (n+3)/4)[:n]
			for _, suffix := range []string{"", "x \t\r\n", "\x00 \t\r\n"} {
				for i := 0; i < align; i++ {
					storage[i] = 'x'
				}
				copy(storage[align:], white)
				copy(storage[align+n:], suffix)
				got := SkipSpace(unsafe.String(unsafe.SliceData(storage), align+n+len(suffix)), align)
				if want := align + n; got != want {
					t.Fatalf("alignment=%d length=%d suffix=%q: got %d, want %d", align, n, suffix, got, want)
				}
			}
		}
	}
	// Check every possible stopping lane, including lanes in later vectors.
	src := []byte(strings.Repeat(" ", 3*vl+1))
	for pos := range src {
		src[pos] = 'x'
		if got := space(unsafe.SliceData(src), len(src)); got != pos {
			t.Fatalf("stopping lane=%d: got %d", pos, got)
		}
		src[pos] = ' '
	}
}
