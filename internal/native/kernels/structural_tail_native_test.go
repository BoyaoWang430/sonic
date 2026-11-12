//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// A readable allocation beyond the logical end must not supply the closing
// bracket. Unlike a guard-page test, this also catches padding being classified
// when the complete vector load would happen to stay on the same page.
func TestSkipContainerLogicalTail(t *testing.T) {
	storage := make([]byte, 32+1+130+128)
	for _, open := range []byte{'[', '{'} {
		for align := 0; align < 32; align++ {
			for n := 0; n <= 129; n++ {
				for _, suffix := range []string{"", `"`, `\`} {
					input := string(open) + strings.Repeat("x", n) + suffix
					for i := range storage {
						storage[i] = open + 2
					}
					copy(storage[align:], input)
					end := align + len(input)
					// An unterminated string could otherwise absorb all the
					// poisoned brackets, so include a quote after the bound.
					storage[end+1] = '"'
					before := string(storage)
					src := rt.String(rt.SliceData(storage), end)
					checkContainer(t, src, align, end, types.ERR_EOF)
					if string(storage) != before {
						t.Fatalf("input changed: open=%q align=%d n=%d suffix=%q", open, align, n, suffix)
					}
				}
			}
		}
	}
}

// Counting all opens and closes in a vector cannot identify its first matching
// close: later opens must not hide an already complete container.
func TestSkipContainerFirstClose(t *testing.T) {
	for _, open := range []byte{'[', '{'} {
		close := string(open + 2)
		for n := 0; n <= 129; n++ {
			prefix := string(open) + strings.Repeat("x", n)
			for _, middle := range []string{"", string(open) + close, `"[{]}"`} {
				input := prefix + middle + close
				checkContainer(t, input+strings.Repeat(string(open), 128), 0, len(input), 0)
			}
		}
	}
}
