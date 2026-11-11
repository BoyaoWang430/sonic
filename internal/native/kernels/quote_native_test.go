//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// Expected spellings copied from native/parsing.h's quote tables.
var quoteEscapes = []struct{ src, single, double string }{
	{"\x00", `\u0000`, `\\u0000`},
	{"\x01", `\u0001`, `\\u0001`},
	{"\x02", `\u0002`, `\\u0002`},
	{"\x03", `\u0003`, `\\u0003`},
	{"\x04", `\u0004`, `\\u0004`},
	{"\x05", `\u0005`, `\\u0005`},
	{"\x06", `\u0006`, `\\u0006`},
	{"\x07", `\u0007`, `\\u0007`},
	{"\x08", `\u0008`, `\\u0008`},
	{"\x09", `\t`, `\\t`},
	{"\x0a", `\n`, `\\n`},
	{"\x0b", `\u000b`, `\\u000b`},
	{"\x0c", `\u000c`, `\\u000c`},
	{"\x0d", `\r`, `\\r`},
	{"\x0e", `\u000e`, `\\u000e`},
	{"\x0f", `\u000f`, `\\u000f`},
	{"\x10", `\u0010`, `\\u0010`},
	{"\x11", `\u0011`, `\\u0011`},
	{"\x12", `\u0012`, `\\u0012`},
	{"\x13", `\u0013`, `\\u0013`},
	{"\x14", `\u0014`, `\\u0014`},
	{"\x15", `\u0015`, `\\u0015`},
	{"\x16", `\u0016`, `\\u0016`},
	{"\x17", `\u0017`, `\\u0017`},
	{"\x18", `\u0018`, `\\u0018`},
	{"\x19", `\u0019`, `\\u0019`},
	{"\x1a", `\u001a`, `\\u001a`},
	{"\x1b", `\u001b`, `\\u001b`},
	{"\x1c", `\u001c`, `\\u001c`},
	{"\x1d", `\u001d`, `\\u001d`},
	{"\x1e", `\u001e`, `\\u001e`},
	{"\x1f", `\u001f`, `\\u001f`},
	{"\x22", `\"`, `\\\"`},
	{"\x5c", `\\`, `\\\\`},
}

func checkQuote(t *testing.T, src string, capacity int, flags uint64, wantConsumed int, want string) {
	t.Helper()
	storage := bytes.Repeat([]byte{0xa5}, capacity+2)
	dst := storage[1 : 1+capacity]
	consumed, written := Quote(src, dst, flags)
	if consumed != wantConsumed || written != len(want) {
		t.Fatalf("Quote(%q, capacity=%d, flags=%x) = (%d,%d), want (%d,%d)", src, capacity, flags, consumed, written, wantConsumed, len(want))
	}
	if got := string(dst[:written]); got != want {
		t.Fatalf("Quote(%q, capacity=%d, flags=%x) output %q, want %q", src, capacity, flags, got, want)
	}
	if storage[0] != 0xa5 || !bytes.Equal(storage[1+written:], bytes.Repeat([]byte{0xa5}, capacity-written+1)) {
		t.Fatalf("Quote wrote outside its reported output: %x", storage)
	}
}

func TestQuoteTables(t *testing.T) {
	t.Logf("toolchain=%s scan width=%d bits", runtime.Version(), quoteVectorBytes()*8)
	for _, tt := range quoteEscapes {
		for _, mode := range []struct {
			flags  uint64
			output string
		}{
			{0, tt.single}, {types.F_DOUBLE_UNQUOTE, tt.double},
			{1 << 63, tt.single}, {1<<63 | types.F_DOUBLE_UNQUOTE, tt.double},
		} {
			for capacity := 0; capacity <= len(mode.output)+1; capacity++ {
				if capacity < len(mode.output) {
					checkQuote(t, tt.src, capacity, mode.flags, -1, "")
				} else {
					checkQuote(t, tt.src, capacity, mode.flags, 1, mode.output)
				}
			}
		}
	}
}

func TestQuoteNativeCases(t *testing.T) {
	// Reuse TestNative_Quote/DoubleQuote/QuoteNoMem's exact expected output.
	src := "hello\b\f\n\r\t\\\"\u666fworld"
	checkQuote(t, src, 256, 0, len(src), `hello\u0008\u000c\n\r\t\\\"景world`)
	checkQuote(t, src, 256, types.F_DOUBLE_UNQUOTE, len(src), `hello\\u0008\\u000c\\n\\r\\t\\\\\\\"景world`)
	checkQuote(t, src, 10, 0, -6, "hello")
	for _, src := range []string{"", "plain ASCII / <>&", "中文\u2028\u2029", "\xff\xc0\x80\xe2\x80"} {
		for _, flags := range []uint64{0, types.F_DOUBLE_UNQUOTE} {
			checkQuote(t, src, len(src), flags, len(src), src)
		}
	}
	consumed, written := Quote("", nil, 0)
	if consumed != 0 || written != 0 {
		t.Fatalf("Quote(empty, nil) = (%d,%d)", consumed, written)
	}
}

func TestQuoteCapacity(t *testing.T) {
	for _, tt := range []struct {
		src      string
		capacity int
		flags    uint64
		consumed int
		output   string
	}{
		{"abc", 0, 0, -1, ""}, {"abc", 1, 0, -2, "a"}, {"abc", 2, 0, -3, "ab"}, {"abc", 3, 0, 3, "abc"},
		{"a\nb", 1, 0, -2, "a"}, {"a\nb", 2, 0, -2, "a"}, {"a\nb", 3, 0, -3, `a\n`}, {"a\nb", 4, 0, 3, `a\nb`},
		{"a\nb", 2, 1, -2, "a"}, {"a\nb", 3, 1, -2, "a"}, {"a\nb", 4, 1, -3, `a\\n`}, {"a\nb", 5, 1, 3, `a\\nb`},
		{"a\bb", 6, 0, -2, "a"}, {"a\bb", 7, 0, -3, `a\u0008`}, {"a\bb", 8, 0, 3, `a\u0008b`},
		{"a\bb", 7, 1, -2, "a"}, {"a\bb", 8, 1, -3, `a\\u0008`}, {"a\bb", 9, 1, 3, `a\\u0008b`},
		{`a"b`, 4, 1, -2, "a"}, {`a"b`, 5, 1, -3, `a\\\"`}, {`a"b`, 6, 1, 3, `a\\\"b`},
		{"\n\b", 2, 0, -2, `\n`}, {"\n\b", 7, 0, -2, `\n`}, {"\n\b", 8, 0, 2, `\n\u0008`},
	} {
		checkQuote(t, tt.src, tt.capacity, tt.flags, tt.consumed, tt.output)
	}
}

func TestQuoteVectorBoundaries(t *testing.T) {
	vl := quoteVectorBytes()
	for n := 0; n <= 3*vl+1; n++ {
		prefix := strings.Repeat("a", n)
		for _, tt := range quoteEscapes {
			for _, mode := range []struct {
				flags  uint64
				output string
			}{{0, tt.single}, {1, tt.double}} {
				src := prefix + tt.src + strings.Repeat("b", vl+1)
				want := prefix + mode.output + strings.Repeat("b", vl+1)
				checkQuote(t, src, len(want), mode.flags, len(src), want)
				checkQuote(t, src, n+len(mode.output)-1, mode.flags, ^n, prefix)
				checkQuote(t, src, n+len(mode.output), mode.flags, ^(n + 1), prefix+mode.output)
			}
		}
	}
	// Source and destination offsets range over every vector-byte alignment.
	// Expected outputs are constructed from fixed escape fixtures, not a scanner.
	srcStorage := make([]byte, 5*vl+16)
	dstStorage := make([]byte, 5*vl+32)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			input := strings.Repeat("a", n) + "\n\xff"
			want := strings.Repeat("a", n) + `\n` + "\xff"
			copy(srcStorage[align:], input)
			dstAlign := vl - 1 - align
			dst := dstStorage[dstAlign : dstAlign+len(want)]
			consumed, written := Quote(rt.String(&srcStorage[align], len(input)), dst, 0)
			if consumed != len(input) || written != len(want) || string(dst) != want {
				t.Fatalf("alignment=%d length=%d: consumed=%d written=%d output=%q", align, n, consumed, written, dst)
			}
		}
	}
}

func TestQuoteBulkAndByteValues(t *testing.T) {
	escapeMap := make(map[byte][2]string, len(quoteEscapes))
	for _, e := range quoteEscapes {
		escapeMap[e.src[0]] = [2]string{e.single, e.double}
	}
	for value := 0; value < 256; value++ {
		b := byte(value)
		src := strings.Repeat(string([]byte{b}), 129)
		if esc, ok := escapeMap[b]; ok {
			checkQuote(t, src, 129*len(esc[0]), 0, len(src), strings.Repeat(esc[0], 129))
			checkQuote(t, src, 129*len(esc[1]), types.F_DOUBLE_UNQUOTE, len(src), strings.Repeat(esc[1], 129))
		} else {
			checkQuote(t, src, len(src), 0, len(src), src)
			checkQuote(t, src, len(src), types.F_DOUBLE_UNQUOTE, len(src), src)
		}
	}
	for pos := 0; pos <= 129; pos++ {
		prefix := strings.Repeat("a", pos)
		suffix := strings.Repeat("b", 40)
		for _, tt := range []struct{ raw, escaped string }{
			{`"`, `\"`},
			{`\`, `\\`},
			{"\n", `\n`},
			{"\x00", `\u0000`},
			{"\x1f", `\u001f`},
		} {
			src := prefix + tt.raw + suffix
			want := prefix + tt.escaped + suffix
			checkQuote(t, src, len(want), 0, len(src), want)
			checkQuote(t, src, pos+len(tt.escaped)-1, 0, ^pos, prefix)
		}
	}
}

var quoteHTMLEscapes = []struct{ src, want string }{
	{"<", `\u003c`},
	{">", `\u003e`},
	{"&", `\u0026`},
	{"\u2028", `\u2028`},
	{"\u2029", `\u2029`},
	{"—", "—"},                                 // 0xe2 0x80 0x94
	{"€", "€"},                                 // 0xe2 0x82 0xac
	{"\xe2", "\xe2"},                           // lone 0xe2 at EOF
	{"\xe2\x80", "\xe2\x80"},                   // truncated 2-byte 0xe2 0x80
	{"\xe2\x80\xa7", "\xe2\x80\xa7"},           // U+2027 (neighbor below U+2028)
	{"\xe2\x80\xaa", "\xe2\x80\xaa"},           // U+202A (neighbor above U+2029)
	{"\xe2\xe2\x80\xa8", "\xe2\\u2028"},        // 0xe2 immediately before U+2028
	{"\xe2\xe2\x80\xa9", "\xe2\\u2029"},        // 0xe2 immediately before U+2029
	{`<a href="https://x.test?a=1&b=2">x</a>`, `\u003ca href=\"https://x.test?a=1\u0026b=2\"\u003ex\u003c/a\u003e`},
}

func checkQuoteHTML(t *testing.T, src string, capacity int, wantConsumed int, want string) {
	t.Helper()
	storage := bytes.Repeat([]byte{0xa5}, capacity+2)
	dst := storage[1 : 1+capacity]
	consumed, written := QuoteHTML(src, dst)
	if consumed != wantConsumed || written != len(want) {
		t.Fatalf("QuoteHTML(%q, capacity=%d) = (%d,%d), want (%d,%d)", src, capacity, consumed, written, wantConsumed, len(want))
	}
	if got := string(dst[:written]); got != want {
		t.Fatalf("QuoteHTML(%q, capacity=%d) output %q, want %q", src, capacity, got, want)
	}
	if storage[0] != 0xa5 || !bytes.Equal(storage[1+written:], bytes.Repeat([]byte{0xa5}, capacity-written+1)) {
		t.Fatalf("QuoteHTML wrote outside its reported output: %x", storage)
	}
}

func TestQuoteHTMLTables(t *testing.T) {
	for _, tt := range quoteEscapes {
		for capacity := 0; capacity <= len(tt.single)+1; capacity++ {
			if capacity < len(tt.single) {
				checkQuoteHTML(t, tt.src, capacity, -1, "")
			} else {
				checkQuoteHTML(t, tt.src, capacity, 1, tt.single)
			}
		}
	}
	for _, tt := range quoteHTMLEscapes[:5] {
		for capacity := 0; capacity <= len(tt.want)+1; capacity++ {
			if capacity < len(tt.want) {
				checkQuoteHTML(t, tt.src, capacity, -1, "")
			} else {
				checkQuoteHTML(t, tt.src, capacity, len(tt.src), tt.want)
			}
		}
	}
	for _, tt := range quoteHTMLEscapes[5:] {
		checkQuoteHTML(t, tt.src, len(tt.want), len(tt.src), tt.want)
		checkQuoteHTML(t, tt.src, len(tt.want)+16, len(tt.src), tt.want)
	}
	consumed, written := QuoteHTML("", nil)
	if consumed != 0 || written != 0 {
		t.Fatalf("QuoteHTML(empty, nil) = (%d,%d)", consumed, written)
	}
}

func TestQuoteHTMLVectorBoundaries(t *testing.T) {
	vl := quoteVectorBytes()
	cases := append([]struct{ src, want string }{}, quoteHTMLEscapes[:5]...)
	for _, e := range quoteEscapes {
		cases = append(cases, struct{ src, want string }{e.src, e.single})
	}
	for n := 0; n <= 3*vl+3; n++ {
		prefix := strings.Repeat("a", n)
		suffix := strings.Repeat("b", vl+3)
		for _, tt := range cases {
			src := prefix + tt.src + suffix
			want := prefix + tt.want + suffix
			checkQuoteHTML(t, src, len(want), len(src), want)
			checkQuoteHTML(t, src, n+len(tt.want)-1, ^n, prefix)
			checkQuoteHTML(t, src, n+len(tt.want), ^(n + len(tt.src)), prefix+tt.want)
		}
		for _, lit := range quoteHTMLEscapes[5:12] {
			src := prefix + lit.src + suffix
			want := prefix + lit.want + suffix
			checkQuoteHTML(t, src, len(want), len(src), want)
		}
	}

	srcStorage := make([]byte, 6*vl+32)
	dstStorage := make([]byte, 6*vl+64)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+2; n++ {
			input := strings.Repeat("a", n) + "<&\u2028—>"
			want := strings.Repeat("a", n) + `\u003c\u0026\u2028—\u003e`
			copy(srcStorage[align:], input)
			dstAlign := vl - 1 - align
			dst := dstStorage[dstAlign : dstAlign+len(want)]
			consumed, written := QuoteHTML(rt.String(&srcStorage[align], len(input)), dst)
			if consumed != len(input) || written != len(want) || string(dst) != want {
				t.Fatalf("QuoteHTML align=%d n=%d: consumed=%d written=%d output=%q", align, n, consumed, written, dst)
			}
		}
	}
}

func TestQuoteHTMLAllBytes(t *testing.T) {
	escapeMap := make(map[byte]string, len(quoteEscapes)+3)
	for _, e := range quoteEscapes {
		escapeMap[e.src[0]] = e.single
	}
	escapeMap['<'] = `\u003c`
	escapeMap['>'] = `\u003e`
	escapeMap['&'] = `\u0026`
	for value := 0; value < 256; value++ {
		b := byte(value)
		src := strings.Repeat(string([]byte{b}), 129)
		if esc, ok := escapeMap[b]; ok {
			checkQuoteHTML(t, src, 129*len(esc), len(src), strings.Repeat(esc, 129))
		} else {
			checkQuoteHTML(t, src, len(src), len(src), src)
		}
	}
}
