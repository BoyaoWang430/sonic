//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestStringDecodeSimpleEscapeBytes(t *testing.T) {
	// Every byte indexes either a legal simple escape or the invalid path.
	// Prefixes also exercise entry after a decoded byte and Unicode sequence.
	simple := map[byte]string{
		'"': "\"", '\\': "\\", '/': "/", 'b': "\b", 'f': "\f", 'n': "\n", 'r': "\r", 't': "\t",
	}
	for _, prefix := range []struct{ body, decoded string }{{"", ""}, {`\nx`, "\nx"}, {`\u0041x`, "Ax"}} {
		for c := 0; c < 256; c++ {
			body := prefix.body + `\` + string([]byte{byte(c)}) + "tail"
			escape, valid := simple[byte(c)]
			for flags := uint64(0); flags < 4; flags++ {
				want, pos, code := prefix.decoded+escape+"tail", -1, types.ParsingError(0)
				if !valid {
					pos, code = len(prefix.body)+1, types.ERR_INVALID_ESCAPE
					if c == 'u' {
						pos, code = len(prefix.body)+2, types.ERR_INVALID_CHAR
					}
				} else if flags&types.F_DOUBLE_UNQUOTE != 0 && c == '\\' {
					// Native double-unquote consumes the extra backslash and
					// interprets the following 't' as the escape letter.
					want = prefix.decoded + "\t" + "ail"
				}
				checkUnquote(t, body, want, flags, pos, code)
			}
			for _, validate := range []bool{false, true} {
				src := `"` + body + `",after`
				input := []byte(src)
				end, written, escaped, pos, code := ParseString(input, 0, validate, false)
				want, wantEnd, wantPos, wantCode := prefix.decoded+escape+"tail", len(body)+2, -1, 0
				if !valid {
					want = prefix.decoded
					wantPos, wantCode = len(prefix.body)+1, 2
					if c == 'u' {
						wantCode = 12
					}
					wantEnd = wantPos
				}
				if end != wantEnd || written != len(want) || !escaped || pos != wantPos || code != wantCode || string(input[1:1+written]) != want {
					t.Fatalf("prefix=%q escape=%02x validate=%v: got (%d,%d,%v,%d,%d,%q), want (%d,%d,true,%d,%d,%q)", prefix.body, c, validate, end, written, escaped, pos, code, input[1:1+written], wantEnd, len(want), wantPos, wantCode, want)
				}
				if valid && string(input[end-1:]) != src[end-1:] {
					t.Fatal("decoder modified the closing quote or following input")
				}
			}
		}
	}
}
