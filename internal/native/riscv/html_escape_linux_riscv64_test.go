//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestHTMLEscapeGuardPages(t *testing.T) {
	sourcePage, destPage := newEscapingGuardPage(t), newEscapingGuardPage(t)
	// Cover scalar bulk loops independently of the quoting scan width.
	maxPrefix := max(129, 3*quoteVectorBytes()+1)
	if maxPrefix > len(sourcePage)-16 {
		maxPrefix = len(sourcePage) - 16
	}
	for n := 0; n <= maxPrefix; n++ {
		prefix := strings.Repeat("a", n)
		checkEscapingGuarded(t, sourcePage, destPage, prefix, n, HTMLEscape, n, prefix)
		for _, tt := range htmlEscapeCases {
			input := prefix + tt.src
			for capacity := n; capacity < n+6; capacity++ {
				checkEscapingGuarded(t, sourcePage, destPage, input, capacity, HTMLEscape, ^n, prefix)
			}
			checkEscapingGuarded(t, sourcePage, destPage, input, n+6, HTMLEscape, len(input), prefix+tt.output)
		}
		// The e2 lookahead must not read past the source, and an unrecognized
		// sequence remains ordinary bytes even when the destination fills.
		for _, tail := range []string{"\xe2", "\xe2\x80", "\xe2\x80\xa7", "\xe2\x81\xa8"} {
			input := prefix + tail
			for capacity := n; capacity < len(input); capacity++ {
				checkEscapingGuarded(t, sourcePage, destPage, input, capacity, HTMLEscape, ^capacity, input[:capacity])
			}
			checkEscapingGuarded(t, sourcePage, destPage, input, len(input), HTMLEscape, len(input), input)
		}
	}
	src := "<&>\u2028\u2029\xe2"
	want := `\u003c\u0026\u003e\u2028\u2029` + "\xe2"
	input := sourcePage[len(sourcePage)-len(src):]
	copy(input, src)
	if err := syscall.Mprotect(sourcePage, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	dst := destPage[len(destPage)-len(want):]
	consumed, written := HTMLEscape(unsafe.String(unsafe.SliceData(input), len(input)), dst)
	if consumed != len(src) || written != len(want) || string(dst) != want {
		t.Fatalf("read-only HTMLEscape: (%d,%d) %q", consumed, written, dst)
	}
}
