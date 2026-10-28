//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestStringDecodeResumeFollowingByte(t *testing.T) {
	// A raw byte between a completed escape and the next backslash must
	// retain the Unquote/parser distinction, including raw quotes and controls.
	for _, prefix := range []struct{ src, decoded string }{
		{`\n`, "\n"},
		{`\u0041`, "A"},
		{`\u07ff`, "\u07ff"},
		{`\u4e2d`, "中"},
		{`\ud83d\ude00`, "😀"},
	} {
		for c := 0; c < 256; c++ {
			body := prefix.src + string([]byte{byte(c)}) + `\t`
			want := prefix.decoded + string([]byte{byte(c)}) + "\t"
			if c == '\\' {
				want = prefix.decoded + `\t`
			}
			checkUnquote(t, body, want, 0, -1, 0)
			quoted := `"` + body + `"`
			for _, start := range []int{0, 1, 7} {
				for _, validate := range []bool{false, true} {
					padding := strings.Repeat("p", start)
					input := []byte(padding + quoted)
					end, written, escaped, pos, code := ParseString(input, start, validate, false)
					wantEnd, wantPos, wantCode, output := start+len(quoted), -1, 0, want
					if c == '"' {
						wantEnd = start + 1 + len(prefix.src) + 1
						output = prefix.decoded
					} else if c < 0x20 && validate {
						wantPos = start + 1 + len(prefix.src)
						wantEnd, wantCode, output = wantPos, 1, prefix.decoded
					}
					if end != wantEnd || written != len(output) || !escaped || pos != wantPos || code != wantCode || string(input[start+1:start+1+written]) != output {
						t.Fatalf("resume prefix=%q byte=%02x start=%d validate=%v: (%d,%d,%v,%d,%d), want (%d,%d,true,%d,%d) output=%q", prefix.src, c, start, validate, end, written, escaped, pos, code, wantEnd, len(output), wantPos, wantCode, output)
					}
					if code == 0 && string(input[end-1:]) != (padding + quoted)[end-1:] {
						t.Fatal("resume modified the closing quote or trailing input")
					}
				}
			}
		}
	}
}

func TestStringDecodeResumePlainRunTransition(t *testing.T) {
	for _, gap := range []int{0, 1, 2, 7, 8, 9, 31, 128} {
		plain := strings.Repeat("x", gap)
		body := strings.Join([]string{`\n`, `\t`, `\u4e2d`, `\\`, `\"`, `\ud83d\ude00`}, plain) + strings.Repeat("z", 257)
		want := stringDecodeOracle(t, body)
		checkUnquote(t, body, want, types.F_UNICODE_REPLACE, -1, 0)
		doubled, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		checkUnquote(t, string(doubled[1:len(doubled)-1]), want, types.F_DOUBLE_UNQUOTE|types.F_UNICODE_REPLACE, -1, 0)
	}
	for _, tc := range []struct {
		src  string
		pos  int
		code types.ParsingError
	}{
		{`\nx\q`, 4, types.ERR_INVALID_ESCAPE},
		{`\nx\u12x4`, 7, types.ERR_INVALID_CHAR},
		{`\nx\`, 4, types.ERR_EOF},
	} {
		checkUnquote(t, tc.src, "", 0, tc.pos, tc.code)
	}
}
