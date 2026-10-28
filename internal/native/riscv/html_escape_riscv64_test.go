//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unsafe"
)

func checkHTMLEscape(t *testing.T, src string, capacity int, wantConsumed int, want string) {
	t.Helper()
	storage := bytes.Repeat([]byte{0xa5}, capacity+2)
	dst := storage[1 : 1+capacity]
	consumed, written := HTMLEscape(src, dst)
	if consumed != wantConsumed || written != len(want) {
		t.Fatalf("HTMLEscape(%q, capacity=%d) = (%d,%d), want (%d,%d)", src, capacity, consumed, written, wantConsumed, len(want))
	}
	if got := string(dst[:written]); got != want {
		t.Fatalf("HTMLEscape(%q, capacity=%d) output %q, want %q", src, capacity, got, want)
	}
	if storage[0] != 0xa5 || !bytes.Equal(storage[1+written:], bytes.Repeat([]byte{0xa5}, capacity-written+1)) {
		t.Fatalf("HTMLEscape wrote outside its reported output: %x", storage)
	}
}

var htmlEscapeCases = []struct{ src, output string }{
	{"<", `\u003c`}, {">", `\u003e`}, {"&", `\u0026`},
	{"\u2028", `\u2028`}, {"\u2029", `\u2029`},
}

func TestHTMLEscapeNativeCases(t *testing.T) {
	src := "hello\u2029\u2028<&>world"
	checkHTMLEscape(t, src, 256, len(src), `hello\u2029\u2028\u003c\u0026\u003eworld`)
	checkHTMLEscape(t, src, 10, -6, "hello")
	for _, tt := range htmlEscapeCases {
		for capacity := 0; capacity <= 7; capacity++ {
			if capacity < 6 {
				checkHTMLEscape(t, tt.src, capacity, -1, "")
			} else {
				checkHTMLEscape(t, tt.src, capacity, len(tt.src), tt.output)
			}
		}
	}
	// JSON quoting and invalid/incomplete UTF-8 are preserved by this operation.
	for _, src := range []string{"", "hello", "\"\\\n\x00", "\xe2", "\xe2\x80", "\xe2\x80\xa7", "\xe2\x81\xa8", "\x80\xa8\xa9", "\xff\xc0\x80"} {
		checkHTMLEscape(t, src, len(src), len(src), src)
	}
	consumed, written := HTMLEscape("", nil)
	if consumed != 0 || written != 0 {
		t.Fatalf("HTMLEscape(empty, nil) = (%d,%d)", consumed, written)
	}
}

func TestHTMLEscapeCapacity(t *testing.T) {
	for _, tt := range []struct {
		src                string
		capacity, consumed int
		output             string
	}{
		{"abc", 0, -1, ""}, {"abc", 1, -2, "a"}, {"abc", 2, -3, "ab"}, {"abc", 3, 3, "abc"},
		{"a<b", 1, -2, "a"}, {"a<b", 6, -2, "a"}, {"a<b", 7, -3, `a\u003c`}, {"a<b", 8, 3, `a\u003cb`},
		{"a\u2028b", 6, -2, "a"}, {"a\u2028b", 7, -5, `a\u2028`}, {"a\u2028b", 8, 5, `a\u2028b`},
		{"\xe2\x80", 1, -2, "\xe2"}, {"\xe2a", 1, -2, "\xe2"},
		{"\xe2\u2028", 1, -2, "\xe2"}, {"\xe2\u2028", 6, -2, "\xe2"}, {"\xe2\u2028", 7, 4, "\xe2" + `\u2028`},
		{"<>", 6, -2, `\u003c`}, {"<>", 11, -2, `\u003c`}, {"<>", 12, 2, `\u003c\u003e`},
	} {
		checkHTMLEscape(t, tt.src, tt.capacity, tt.consumed, tt.output)
	}
}

func TestHTMLEscapeStandardLibrary(t *testing.T) {
	// Reuse the same existing implementation as the community Go fallback.
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	for _, src := range []string{string(allBytes), `{"key":"<&>"}`, "a\u2028b\u2029c", "\xe2\xe2\x80\xa8\xe2\x80\xa9\xe2\x80\xa7", "\xe2<&\xe2\x80>"} {
		var expected bytes.Buffer
		json.HTMLEscape(&expected, []byte(src))
		checkHTMLEscape(t, src, expected.Len(), len(src), expected.String())
	}
}

func TestHTMLEscapeVectorBoundaries(t *testing.T) {
	vl := quoteVectorBytes()
	for n := 0; n <= 3*vl+1; n++ {
		prefix := strings.Repeat("a", n)
		for _, tt := range htmlEscapeCases {
			src := prefix + tt.src + strings.Repeat("b", vl+1)
			want := prefix + tt.output + strings.Repeat("b", vl+1)
			checkHTMLEscape(t, src, len(want), len(src), want)
			checkHTMLEscape(t, src, n+5, ^n, prefix)
			checkHTMLEscape(t, src, n+6, ^(n + len(tt.src)), prefix+tt.output)
		}
	}
	srcStorage := make([]byte, 5*vl+16)
	dstStorage := make([]byte, 5*vl+32)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			input := strings.Repeat("a", n) + "\u2028\xe2"
			want := strings.Repeat("a", n) + `\u2028` + "\xe2"
			copy(srcStorage[align:], input)
			dstAlign := vl - 1 - align
			dst := dstStorage[dstAlign : dstAlign+len(want)]
			consumed, written := HTMLEscape(unsafe.String(&srcStorage[align], len(input)), dst)
			if consumed != len(input) || written != len(want) || string(dst) != want {
				t.Fatalf("alignment=%d length=%d: consumed=%d written=%d output=%q", align, n, consumed, written, dst)
			}
		}
	}
}
