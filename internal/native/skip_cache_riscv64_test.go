//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVTraversalCacheString(t *testing.T) {
	bodies := []string{"", `"`, `abc" tail`, `a\"b"`, `a\\b"`, `\u1234"`, `\u12`, `\q"`, string([]byte{'a', 0, 'b', '"'}), string([]byte{'a', 10, 'b', '"'})}
	for _, gap := range []int{31, 62, 63, 64, 65, 127, 128, 129} {
		for _, suffix := range []string{`"tail`, `\"x"`, `\\"tail`, `\u1234"`, `\u123`, `\q"`, string([]byte{0, '"'})} {
			bodies = append(bodies, strings.Repeat("x", gap)+suffix)
		}
	}
	for n := 0; n <= 130; n++ {
		bodies = append(bodies, strings.Repeat(`\`, n)+`"end"`)
	}
	for value := 0; value < 256; value++ {
		bodies = append(bodies, string([]byte{'x', byte(value), '"', ' ', '"', 'x', '"'}))
	}
	for _, scan := range []struct {
		name string
		run  func(*traversalBlock, string, *int, bool) types.ParsingError
	}{
		{"string", (*traversalBlock).skipString},
		{"key", (*traversalBlock).skipKey},
	} {
		t.Run(scan.name, func(t *testing.T) {
			for _, body := range bodies {
				for pad := 0; pad < 66; pad++ {
					src := strings.Repeat(" ", pad) + `"` + body
					start := pad + 1
					for _, flags := range []uint64{0, types.F_VALIDATE_STRING} {
						for _, primed := range []bool{false, true} {
							var b traversalBlock
							if primed {
								// Cached events before the content, including its opening
								// quote, must not affect either key or value scanning.
								b.load(src, max(0, start-31), flags != 0)
							}
							wantPos, gotPos := start, start
							want := skipStringToken(src, &wantPos, flags)
							got := scan.run(&b, src, &gotPos, flags != 0)
							if got != want || gotPos != wantPos {
								t.Fatalf("pad=%d flags=%d primed=%v body=%q: got (%d,%d) want (%d,%d)", pad, flags, primed, body, got, gotPos, want, wantPos)
							}
						}
					}
				}
			}
		})
	}
}

func TestRISCVTraversalCacheSuccessiveTokens(t *testing.T) {
	src := strings.Repeat(`"first" "second" "escaped\"value" "" "a" `, 40)
	for _, flags := range []uint64{0, types.F_VALIDATE_STRING} {
		var b traversalBlock
		pos := 0
		for pos < len(src) {
			if src[pos] == ' ' {
				pos++
				continue
			}
			pos++
			end, want := riscv.SkipString(src[pos:], flags != 0)
			wantPos := pos + end
			got := b.skipKey(src, &pos, flags != 0)
			if got != want || pos != wantPos {
				t.Fatalf("flags=%d got=(%d,%d) want=(%d,%d)", flags, got, pos, want, wantPos)
			}
		}
	}
}
