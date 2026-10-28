//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// The byte oracle is independent of word classification and accepts the same
// relaxed escape syntax as the short probes and their native continuation.
func assertRISCVStringProbeTail(t *testing.T, src string, pos int) {
	t.Helper()
	wantEnd, wantEscape, wantErr := len(src), -1, types.ERR_EOF
	for i := pos; i < len(src); i++ {
		if src[i] == '"' {
			wantEnd, wantErr = i+1, 0
			break
		}
		if src[i] == '\\' {
			if wantEscape < 0 {
				wantEscape = i
			}
			i++
		}
	}
	wantToken := wantEnd
	if wantErr != 0 {
		wantToken = -1
	}
	if got := tokenString(src, pos); got != wantToken {
		t.Fatalf("token src=%q pos=%d: end=%d, want %d", src, pos, got, wantToken)
	}
	var block pathBlock
	end, escape, err := block.stringTail(src, pos)
	if end != wantEnd || escape != wantEscape || err != wantErr {
		t.Fatalf("path src=%q pos=%d: (%d,%d,%d), want (%d,%d,%d)", src, pos, end, escape, err, wantEnd, wantEscape, wantErr)
	}
}

func TestRISCVStringProbeTail(t *testing.T) {
	// Cover every word remainder, the 32-byte Go probe limit, incomplete
	// escapes, and a quote before or after a backslash in the same last word.
	for offset := 0; offset < 16; offset++ {
		prefix := strings.Repeat("\"\\#]", (offset+3)/4)[:offset]
		for n := 0; n < 41; n++ {
			for _, tail := range []string{"", `"`, `\`, `\"`, `\""`, `\\"`, `\n"`, `"\`, `#]"`, "\x00\xff\""} {
				src := prefix + strings.Repeat("a", n) + tail
				assertRISCVStringProbeTail(t, src, offset)
			}
		}
	}
	// Every byte can occupy the first event in a tail, including the bytes
	// adjacent to quotes and slashes that expose subtraction-borrow mistakes.
	for offset := 0; offset < 8; offset++ {
		prefix := strings.Repeat("\"", offset)
		for n := 0; n < 32; n++ {
			for b := 0; b < 256; b++ {
				body := strings.Repeat("a", n) + string([]byte{byte(b)}) + `#]"`
				assertRISCVStringProbeTail(t, prefix+body, offset)
			}
		}
	}
}

var riscvStringProbeTailEnd int

// Include the one- and two-byte tails as well as longer suffixes, so word-only
// input lengths cannot hide either byte-loop overhead or classification costs.
func BenchmarkRISCVStringProbeTail(b *testing.B) {
	for _, tc := range []struct {
		name, suffix string
	}{
		{"Plain", `"`},
		{"TailEscape", `\n"`},
	} {
		for _, n := range []int{9, 10, 11, 15, 17, 23, 31} {
			src := strings.Repeat("a", n-len(tc.suffix)) + tc.suffix
			b.Run(fmt.Sprintf("Token/%s/N%d", tc.name, n), func(b *testing.B) {
				if end := tokenString(src, 0); end != len(src) {
					b.Fatalf("end=%d, want %d", end, len(src))
				}
				b.ReportAllocs()
				b.SetBytes(int64(n))
				b.ResetTimer()
				var end int
				for i := 0; i < b.N; i++ {
					end = tokenString(src, 0)
				}
				riscvStringProbeTailEnd = end
			})
			b.Run(fmt.Sprintf("Path/%s/N%d", tc.name, n), func(b *testing.B) {
				var block pathBlock
				if end, _, err := block.stringTail(src, 0); end != len(src) || err != 0 {
					b.Fatalf("end=%d err=%d, want %d and success", end, err, len(src))
				}
				b.ReportAllocs()
				b.SetBytes(int64(n))
				b.ResetTimer()
				var end int
				for i := 0; i < b.N; i++ {
					end, _, _ = block.stringTail(src, 0)
				}
				riscvStringProbeTailEnd = end
			})
		}
	}
}
