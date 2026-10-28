//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unsafe"
)

func checkScalarHTMLEscape(t *testing.T, src string, capacity, srcAlign, dstAlign, wantConsumed int, want string) {
	t.Helper()
	const guard = 0xa5
	srcStart, dstStart := 8+srcAlign, 8+dstAlign
	source := bytes.Repeat([]byte{guard}, srcStart+len(src)+8)
	copy(source[srcStart:], src)
	dest := bytes.Repeat([]byte{guard}, dstStart+capacity+8)
	input := unsafe.String(&source[srcStart], len(src))
	output := dest[dstStart : dstStart+capacity]
	consumed, written := HTMLEscape(input, output)
	if consumed != wantConsumed || written != len(want) {
		t.Fatalf("src=%q capacity=%d srcAlign=%d dstAlign=%d: got (%d,%d), want (%d,%d)", src, capacity, srcAlign, dstAlign, consumed, written, wantConsumed, len(want))
	}
	if string(output[:written]) != want {
		t.Fatalf("src=%q capacity=%d srcAlign=%d dstAlign=%d: output %q, want %q", src, capacity, srcAlign, dstAlign, output[:written], want)
	}
	for i, value := range dest {
		if (i < dstStart || i >= dstStart+written) && value != guard {
			t.Fatalf("destination modified outside reported output at %d: srcAlign=%d dstAlign=%d capacity=%d written=%d", i, srcAlign, dstAlign, capacity, written)
		}
	}
	if input != src || !bytes.Equal(source[:srcStart], bytes.Repeat([]byte{guard}, srcStart)) || !bytes.Equal(source[srcStart+len(src):], bytes.Repeat([]byte{guard}, 8)) {
		t.Fatal("HTML escaping modified the source")
	}
}

func TestScalarHTMLEscapeBulkClassification(t *testing.T) {
	// Repeating each byte exercises every byte class throughout all four
	// words of a bulk load, including non-ASCII bytes and invalid UTF-8.
	for value := 0; value < 256; value++ {
		src := strings.Repeat(string([]byte{byte(value)}), 129)
		var want bytes.Buffer
		json.HTMLEscape(&want, []byte(src))
		checkScalarHTMLEscape(t, src, want.Len(), value%8, value/8%8, len(src), want.String())
	}
	// The first candidate can occupy any byte of the first four bulk
	// loads. Ordinary e2 sequences must resume copying, while separators
	// can cross both word and bulk boundaries.
	for pos := 0; pos <= 128; pos++ {
		for _, marker := range []string{"<", ">", "&", "\u2028", "\u2029", "\xe2a", "\xe2\x80\xa7", "\xe2\x81\xa8"} {
			src := strings.Repeat("a", pos) + marker + strings.Repeat("b", 40)
			var want bytes.Buffer
			json.HTMLEscape(&want, []byte(src))
			checkScalarHTMLEscape(t, src, want.Len(), pos%8, pos/8%8, len(src), want.String())
		}
	}
}

func TestScalarHTMLEscapeBulkAlignment(t *testing.T) {
	for srcAlign := 0; srcAlign < 8; srcAlign++ {
		for dstAlign := 0; dstAlign < 8; dstAlign++ {
			for _, pos := range []int{0, 7, 31, 63, 127} {
				// Resume the bulk loop after several different escape sizes
				// and ordinary candidates, with independent output alignment.
				src := strings.Repeat("a", pos) + "<" + strings.Repeat("b", 33) + "\u2028" + strings.Repeat("c", 32) + "\xe2x>&\u2029"
				var want bytes.Buffer
				json.HTMLEscape(&want, []byte(src))
				checkScalarHTMLEscape(t, src, want.Len()+7, srcAlign, dstAlign, len(src), want.String())
			}
		}
	}
}

func TestScalarHTMLEscapeBulkCapacityAndResume(t *testing.T) {
	for _, pos := range []int{0, 7, 8, 31, 32, 63, 64, 127, 128} {
		prefix, suffix := strings.Repeat("a", pos), strings.Repeat("b", 33)
		for _, escape := range htmlEscapeCases {
			src := prefix + escape.src + suffix
			want := prefix + escape.output + suffix
			for _, extra := range []int{0, 5, 6, 7} {
				// Insufficient capacity must stop before the escape; six
				// bytes consume it atomically, including its UTF-8 input.
				consumed, output := pos, prefix
				if extra >= 6 {
					consumed += len(escape.src) + extra - 6
					output += escape.output + suffix[:extra-6]
				}
				checkScalarHTMLEscape(t, src, pos+extra, pos%8, extra%8, ^consumed, output)
				tail := src[consumed:]
				remaining := want[len(output):]
				checkScalarHTMLEscape(t, tail, len(remaining), (pos+consumed)%8, (extra+len(output))%8, len(tail), remaining)
			}
		}
	}
}
