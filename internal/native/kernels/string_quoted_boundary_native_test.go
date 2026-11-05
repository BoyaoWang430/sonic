//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"bytes"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestDecodeQuotedRelativeContract(t *testing.T) {
	for _, input := range []string{
		`prefix"plain"tail`,
		`prefix"a\nb\u0041"tail`,
		`prefix"\uD800"tail`,
		`prefix"\uD800\uDC00"tail`,
		`prefix"bad\x"tail`,
		`prefix"bad\u12x4"tail`,
		"prefix\"raw\ncontrol\"tail",
		`prefix"unterminated`,
		`prefix"`,
	} {
		for _, validate := range []bool{false, true} {
			for _, unicodeErrors := range []bool{false, true} {
				gotBuf, wantBuf := []byte(input), []byte(input)
				const start = 6
				mode := uint64(4)
				if validate {
					mode |= 8
				}
				if !unicodeErrors {
					mode |= 2
				}
				tail := gotBuf[start+1:]
				end, written, escaped, errorPos, code := DecodeQuoted(rt.SliceData(tail), len(tail), mode)
				wantEnd, wantWritten, wantEscaped, wantErrorPos, wantCode := ParseString(wantBuf, start, validate, unicodeErrors)
				end += start + 1
				if errorPos >= 0 {
					errorPos += start + 1
				}
				if end != wantEnd || written != wantWritten || (escaped != 0) != wantEscaped || errorPos != wantErrorPos || code != wantCode || !bytes.Equal(gotBuf, wantBuf) {
					t.Fatalf("input=%q validate=%v unicodeErrors=%v: got=(%d,%d,%d,%d,%d,%q) want=(%d,%d,%v,%d,%d,%q)", input, validate, unicodeErrors, end, written, escaped, errorPos, code, gotBuf, wantEnd, wantWritten, wantEscaped, wantErrorPos, wantCode, wantBuf)
				}
			}
		}
	}
}
