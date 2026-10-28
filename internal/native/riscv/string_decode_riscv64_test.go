//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

func TestParseString(t *testing.T) {
	for _, tt := range []struct {
		src, want string
		escaped   bool
	}{
		{`""`, "", false},
		{`"hello中文"`, "hello中文", false},
		{"\"\xff\xc0\x80\"", "\xff\xc0\x80", false},
		{`"\b\f\n\r\t\\\"\/"`, "\b\f\n\r\t\\\"/", true},
		{`"\u0000\u007f\u0080\u07ff\u0800\uffff"`, "\x00\x7f\u0080\u07ff\u0800\uffff", true},
		{`"\ud800\udc00\udbff\udfff"`, "\U00010000\U0010ffff", true},
		{`"\ud800\u0041\ud800\ud800\udc00"`, "\ufffdA\ufffd\U00010000", true},
		{`"\udfffend"`, "\ufffdend", true},
	} {
		for _, start := range []int{0, 1, 7, stringDecodeVectorBytes() - 1} {
			for _, validate := range []bool{false, true} {
				prefix := strings.Repeat("p", start)
				suffix := `, "trailing\\n":123}`
				input := []byte(prefix + tt.src + suffix)
				end, written, escaped, pos, code := ParseString(input, start, validate, false)
				if end != start+len(tt.src) || written != len(tt.want) || escaped != tt.escaped || pos != -1 || code != 0 || string(input[start+1:start+1+written]) != tt.want {
					t.Fatalf("ParseString(%q,start=%d,validate=%v)=(%d,%d,%v,%d,%d) output=%q, want %q", tt.src, start, validate, end, written, escaped, pos, code, input[start+1:start+1+written], tt.want)
				}
				if string(input[:start+1]) != prefix+`"` || string(input[end-1:]) != `"`+suffix {
					t.Fatalf("ParseString modified its opening quote, original closing quote, or trailing input: %q", input)
				}
				if !tt.escaped && string(input) != prefix+tt.src+suffix {
					t.Fatal("plain input was modified")
				}
			}
		}
	}
}

func TestParseStringErrorPositions(t *testing.T) {
	for _, tt := range []struct {
		src           string
		validate      bool
		unicodeErrors bool
		pos, code     int
		escaped       bool
	}{
		{`"`, true, false, 1, 5, false},
		{`"plain`, true, false, 6, 5, false},
		{`"x\`, true, false, 3, 5, true},
		{`"\"`, true, false, 3, 5, true},
		{`"\x"`, true, false, 1, 2, true},
		{`"\x"`, false, false, 1, 2, true},
		{`"\'"`, false, false, 1, 2, true},
		{`"\u`, true, false, 3, 5, true},
		{`"\u123`, true, false, 6, 5, true},
		{`"\u12x4"`, true, false, 1, 12, true},
		{`"\ud800\u12x4"`, true, false, 7, 12, true},
		{`"\ud800"`, true, true, 1, 12, true},
		{`"\udc00"`, false, true, 1, 12, true},
		{`"\ud800\u0041"`, true, true, 7, 12, true},
		{`"\ud800\ud800"`, true, true, 7, 12, true},
		{"\"a\x00b\"", true, false, 2, 1, false},
		{"\"\\nX\x00Y\"", true, false, 4, 1, true},
	} {
		for _, start := range []int{0, 11} {
			input := []byte(strings.Repeat("p", start) + tt.src)
			end, written, escaped, pos, code := ParseString(input, start, tt.validate, tt.unicodeErrors)
			if pos != start+tt.pos || end != pos || code != tt.code || escaped != tt.escaped || written < 0 || written > len(tt.src)-1 {
				t.Fatalf("ParseString(%q,start=%d,validate=%v,unicodeErrors=%v)=(%d,%d,%v,%d,%d), want pos=%d code=%d escaped=%v", tt.src, start, tt.validate, tt.unicodeErrors, end, written, escaped, pos, code, start+tt.pos, tt.code, tt.escaped)
			}
		}
	}
	for _, src := range []string{"\"a\x00b\"", "\"\\nX\x00Y\""} {
		input := []byte(src)
		_, _, _, _, code := ParseString(input, 0, false, false)
		if code != 0 {
			t.Fatalf("loose raw control rejected: %q code=%d", src, code)
		}
	}
}

func TestParseStringPublicOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20261023))
	alphabet := []rune{'a', ' ', '\x00', '\b', '\f', '\n', '\r', '\t', '"', '\\', '/', '<', '>', '&', '中', '\u2028', '\u2029', '\U0001f600'}
	vl := stringDecodeVectorBytes()
	for i := 0; i < 1200; i++ {
		var text strings.Builder
		length := rng.Intn(4*vl + 17)
		for j := 0; j < length; j++ {
			text.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		want := text.String()
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		for _, unicodeErrors := range []bool{false, true} {
			input := append([]byte(nil), encoded...)
			end, written, escaped, pos, code := ParseString(input, 0, true, unicodeErrors)
			if end != len(encoded) || pos != -1 || code != 0 || string(input[1:1+written]) != want || escaped != bytes.ContainsRune(encoded, '\\') {
				t.Fatalf("round-trip case %d strictUnicode=%v: end=%d written=%d escaped=%v pos=%d code=%d", i, unicodeErrors, end, written, escaped, pos, code)
			}
		}
	}
}
