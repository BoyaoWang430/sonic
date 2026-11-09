//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// The cache may end on a backslash, its escaped byte, or just before the
// closing quote. The continuation must preserve the first escape and consume
// a crossing escape exactly once, while keeping later raw events reusable.
func TestPathCachedStringContinuation(t *testing.T) {
	for _, width := range []int{1, 7, 8, 15, 16, 31, 32, 63, 64} {
		for _, begin := range []int{0, 1, 3} {
			if begin >= width {
				continue
			}
			for slash := begin; slash <= width; slash++ {
				for _, suffix := range []string{
					`"`, `\`, `\"a"`, `\\"`, `\n"`,
					`\u1234"`, `\q"`, `\n` + strings.Repeat("x", 65) + `\"tail"`,
				} {
					prefix := strings.Repeat("x", slash)
					if slash-begin > 3 {
						prefix = prefix[:begin] + `\n` + prefix[begin+2:]
					}
					src := prefix + suffix
					for _, after := range []string{"", `,"next":"a\"b"`} {
						input := src + after
						var block pathBlock
						block.end = minNative(width, len(input))
						for i := 0; i < block.end; i++ {
							if input[i] == '"' || input[i] == '\\' {
								block.events |= uint64(1) << uint(i)
							}
						}
						wantEnd, wantEscape, wantErr := cachedStringReference(input, begin)
						end, escape, err := block.string(input, begin)
						if end != wantEnd || escape != wantEscape || err != wantErr {
							t.Fatalf("width=%d begin=%d input=%q: (%d,%d,%v), want (%d,%d,%v)", width, begin, input, end, escape, err, wantEnd, wantEscape, wantErr)
						}
						var events uint64
						for i := block.base; i < block.end; i++ {
							if input[i] == '"' || input[i] == '\\' {
								events |= uint64(1) << uint(i-block.base)
							}
						}
						if block.events != events {
							t.Fatalf("input=%q: cached raw events=%x, want %x", input, block.events, events)
						}
					}
				}
			}
		}
	}
}

func cachedStringReference(src string, start int) (end, escape int, err types.ParsingError) {
	escape = -1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '"':
			return i + 1, escape, 0
		case '\\':
			if escape < 0 {
				escape = i
			}
			i++
		}
	}
	return len(src), escape, types.ERR_EOF
}
