//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestQuoteAndHTMLEscapeCapacity(t *testing.T) {
	for _, tt := range []struct {
		src, want string
		flags     uint64
		html      bool
	}{
		{"\b", `\u0008`, 0, false}, {"\f", `\u000c`, 0, false},
		{"\n", `\n`, 0, false}, {"\t", `\\t`, 1, false},
		{"\x00", `\\u0000`, 1, false}, {`"`, `\\\"`, 1, false},
		{`\`, `\\\\`, 1, false}, {"\r", `\r`, 1 << 63, false},
		{"<", `\u003c`, 0, true}, {">", `\u003e`, 0, true},
		{"&", `\u0026`, 0, true}, {"\u2028", `\u2028`, 0, true},
		{"\u2029", `\u2029`, 0, true},
	} {
		for prefix := 0; prefix < 25; prefix++ {
			plain := strings.Repeat("p", prefix)
			src := plain + tt.src + "tail"
			for capacity := 0; capacity <= prefix+len(tt.want)+5; capacity++ {
				storage := bytes.Repeat([]byte{0xa5}, capacity+2)
				dst := storage[1 : 1+capacity]
				consumed, written := 0, 0
				if tt.html {
					consumed, written = HTMLEscape(src, dst)
				} else {
					consumed, written = Quote(src, dst, tt.flags)
				}
				want, input := plain, prefix
				if capacity < prefix {
					want, input = plain[:capacity], capacity
				} else if capacity >= prefix+len(tt.want) {
					n := min(4, capacity-prefix-len(tt.want))
					want, input = plain+tt.want+"tail"[:n], prefix+len(tt.src)+n
				}
				if input != len(src) {
					input = ^input
				}
				if consumed != input || written != len(want) || string(dst[:written]) != want {
					t.Fatalf("src=%q flags=%d html=%v capacity=%d: (%d,%d,%q), want (%d,%d,%q)", src, tt.flags, tt.html, capacity, consumed, written, dst[:written], input, len(want), want)
				}
				if storage[0] != 0xa5 || !bytes.Equal(storage[1+written:], bytes.Repeat([]byte{0xa5}, capacity-written+1)) {
					t.Fatal("escape wrote outside the reported output")
				}
			}
		}
	}
}

func TestStringRoundTrips(t *testing.T) {
	rng := rand.New(rand.NewSource(20261026))
	alphabet := []rune{'a', ' ', '\x00', '\b', '\f', '\n', '\r', '\t', '"', '\\', '/', '<', '>', '&', '中', '\u2028', '\u2029', '\U0001f600'}
	for i := 0; i < 1200; i++ {
		var text strings.Builder
		for n := rng.Intn(160); n > 0; n-- {
			text.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		want := text.String()
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		for _, alias := range []bool{false, true} {
			body := string(encoded[1 : len(encoded)-1])
			buf := []byte(body)
			if alias {
				body = unsafe.String(unsafe.SliceData(buf), len(buf))
			}
			written, pos, code := Unquote(body, buf, types.F_UNICODE_REPLACE)
			if code != 0 || pos != -1 || string(buf[:written]) != want {
				t.Fatalf("roundtrip %d alias=%v: written=%d pos=%d code=%d output=%q want=%q", i, alias, written, pos, code, buf[:written], want)
			}
		}
		for _, validate := range []bool{false, true} {
			for _, unicodeErrors := range []bool{false, true} {
				input := append(append([]byte("prefix"), encoded...), []byte(",tail")...)
				end, written, escaped, pos, code := ParseString(input, 6, validate, unicodeErrors)
				if end != 6+len(encoded) || pos != -1 || code != 0 || string(input[7:7+written]) != want || escaped != bytes.ContainsRune(encoded, '\\') || string(input[end-1:]) != `",tail` || string(input[:7]) != `prefix"` {
					t.Fatalf("parser roundtrip %d validate=%v unicodeErrors=%v: (%d,%d,%v,%d,%d)", i, validate, unicodeErrors, end, written, escaped, pos, code)
				}
			}
		}
		quote := make([]byte, 7*len(want))
		consumed, written := Quote(want, quote, 0)
		var decoded string
		if consumed != len(want) || json.Unmarshal([]byte(`"`+string(quote[:written])+`"`), &decoded) != nil || decoded != want {
			t.Fatalf("quote roundtrip %d", i)
		}
		var html bytes.Buffer
		json.HTMLEscape(&html, []byte(want))
		consumed, written = HTMLEscape(want, quote)
		if consumed != len(want) || string(quote[:written]) != html.String() {
			t.Fatalf("HTML roundtrip %d", i)
		}
	}
}

func TestStringDecodeNativeErrors(t *testing.T) {
	for _, tt := range []struct {
		src   string
		flags uint64
		pos   int
		code  types.ParsingError
	}{
		{`asdf\`, 0, 5, types.ERR_EOF}, {`asdf\gqwer`, 0, 5, types.ERR_INVALID_ESCAPE},
		{`asdf\u1gggqwer`, 0, 7, types.ERR_INVALID_CHAR}, {`asdf\ud800qwer`, 0, 6, types.ERR_INVALID_UNICODE},
		{`asdf\\ud800qwer`, 1, 6, types.ERR_INVALID_UNICODE}, {`asdf\ud800\ud800qwer`, 0, 12, types.ERR_INVALID_UNICODE},
		{`asdf\\ud800\\ud800qwer`, 1, 14, types.ERR_INVALID_UNICODE}, {`\u`, 0, 2, types.ERR_EOF},
		{`\u123`, 0, 5, types.ERR_EOF}, {`\u12x4`, 0, 4, types.ERR_INVALID_CHAR},
		{`\ud800\u12x4`, 2, 10, types.ERR_INVALID_CHAR}, {`\'`, 0, 1, types.ERR_INVALID_ESCAPE},
		{`\n`, 1, 2, types.ERR_EOF}, {`\\\`, 1, 3, types.ERR_EOF},
		{`\\\x`, 1, 3, types.ERR_INVALID_CHAR}, {`\\ud800`, 1, 7, types.ERR_EOF},
	} {
		dst := bytes.Repeat([]byte{0xa5}, len(tt.src))
		written, pos, code := Unquote(tt.src, dst, tt.flags)
		if pos != tt.pos || code != tt.code || !bytes.Equal(dst[written:], bytes.Repeat([]byte{0xa5}, len(dst)-written)) {
			t.Fatalf("Unquote(%q, flags=%d)=(%d,%d,%d), want pos=%d code=%d", tt.src, tt.flags, written, pos, code, tt.pos, tt.code)
		}
	}
	for _, tt := range []struct {
		src           string
		pos, code     int
		unicodeErrors bool
	}{
		{`"plain`, 6, 5, false}, {`"x\`, 3, 5, false}, {`"\x"`, 1, 2, false},
		{`"\u123`, 6, 5, false}, {`"\u12x4"`, 1, 12, false},
		{`"\ud800\u12x4"`, 7, 12, false}, {`"\ud800"`, 1, 12, true},
		{`"\ud800\u0041"`, 7, 12, true}, {"\"a\x00b\"", 2, 1, false},
	} {
		buf := []byte(tt.src)
		end, _, _, pos, code := ParseString(buf, 0, true, tt.unicodeErrors)
		if end != tt.pos || pos != tt.pos || code != tt.code {
			t.Fatalf("ParseString(%q) end=%d pos=%d code=%d", tt.src, end, pos, code)
		}
	}
}

func TestUnquoteUnicode(t *testing.T) {
	for value := 0; value <= 0xffff; value++ {
		src := fmt.Sprintf(`\u%04x`, value)
		var want string
		if err := json.Unmarshal([]byte(`"`+src+`"`), &want); err != nil {
			t.Fatal(err)
		}
		dst := make([]byte, len(src))
		written, pos, code := Unquote(src, dst, types.F_UNICODE_REPLACE)
		if code != 0 || pos != -1 || string(dst[:written]) != want {
			t.Fatalf("Unquote(%q)=(%d,%d,%d), output=%q want=%q", src, written, pos, code, dst[:written], want)
		}
	}
}

func TestInvalidUTF8Resume(t *testing.T) {
	rng := rand.New(rand.NewSource(723))
	inputs := []string{"", "\ufffd", "α中😊", "a\xff中\xed\xa0\x80x", "\xf4\x90\x80\x80"}
	for i := 0; i < 1000; i++ {
		buf := make([]byte, rng.Intn(256))
		_, _ = rng.Read(buf)
		inputs = append(inputs, string(buf))
	}
	for _, src := range inputs {
		if ValidUTF8(src) != utf8.ValidString(src) {
			t.Fatalf("ValidUTF8(%x)", src)
		}
		for _, start := range []int{0, len(src) / 2, len(src)} {
			var want []int
			for pos, r := range src[start:] {
				if r == utf8.RuneError {
					_, size := utf8.DecodeRuneInString(src[start+pos:])
					if size == 1 {
						want = append(want, start+pos)
					}
				}
			}
			for _, capacity := range []int{0, 1, 3, len(src) + 1} {
				out := make([]int, capacity)
				next, count, full := InvalidUTF8(src, start, out)
				wantCount := min(capacity, len(want))
				wantNext := len(src)
				if wantCount < len(want) {
					wantNext = want[wantCount]
				}
				if next != wantNext || count != wantCount || full != (wantCount < len(want)) || fmt.Sprint(out[:count]) != fmt.Sprint(want[:wantCount]) {
					t.Fatalf("InvalidUTF8(%x,start=%d,capacity=%d)=(%d,%d,%v), positions=%v want=%v", src, start, capacity, next, count, full, out[:count], want)
				}
			}
		}
	}
}
