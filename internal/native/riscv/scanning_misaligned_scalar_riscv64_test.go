//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestScalarMaskAlignments(t *testing.T) {
	rng := rand.New(rand.NewSource(28191))
	storage := make([]byte, 160)
	for size := 0; size <= 129; size++ {
		for align := 0; align < 16; align++ {
			data := storage[align : align+size]
			rng.Read(data)
			for i := 0; i < size; i += 2 {
				data[i] = " \t\r\n\"\\,}]"[rng.Intn(9)]
			}
			src := unsafe.String(unsafe.SliceData(data), len(data))
			width := min(size, 64)
			var events, controls, nonspace, boundaries uint64
			for i := 0; i < width; i++ {
				c, bit := src[i], uint64(1)<<i
				space := c == ' ' || c == '\t' || c == '\r' || c == '\n'
				if c == '"' || c == '\\' {
					events |= bit
				}
				if c < 32 {
					controls |= bit
				}
				if !space {
					nonspace |= bit
				}
				if space || c == ',' || c == '}' || c == ']' {
					boundaries |= bit
				}
			}
			if got, n := NonSpaceMask(src, 0); got != nonspace || n != width {
				t.Fatalf("nonspace size=%d align=%d: (%x,%d), want (%x,%d)", size, align, got, n, nonspace, width)
			}
			if e, ns, b, n := PathMasks(src, 0); e != events || ns != nonspace || b != boundaries || n != width {
				t.Fatalf("path size=%d align=%d: (%x,%x,%x,%d), want (%x,%x,%x,%d)", size, align, e, ns, b, n, events, nonspace, boundaries, width)
			}
			for _, strict := range []bool{false, true} {
				want := events
				if strict {
					want |= controls
				}
				if e, ns, n := TraversalMasks(src, 0, strict); e != want || ns != nonspace || n != width {
					t.Fatalf("traversal size=%d align=%d strict=%v: (%x,%x,%d), want (%x,%x,%d)", size, align, strict, e, ns, n, want, nonspace, width)
				}
			}
			// Every possible stopping position includes word-boundary and
			// overlapping-tail cases, including a wholly whitespace input.
			for i := range data {
				data[i] = " \t\r\n"[i%4]
			}
			if got := SkipSpace(src, 0); got != size {
				t.Fatalf("space size=%d align=%d: %d", size, align, got)
			}
			for i := range data {
				data[i] = '!'
				if got := SkipSpace(src, 0); got != i {
					t.Fatalf("space size=%d align=%d stop=%d: %d", size, align, i, got)
				}
				data[i] = " \t\r\n"[i%4]
			}
		}
	}
	runtime.KeepAlive(storage)
}

// Compare structural scanning against an independent byte oracle.
func TestScalarContainerByteReference(t *testing.T) {
	rng := rand.New(rand.NewSource(78913))
	storage := make([]byte, 512)
	for size := 0; size <= 129; size++ {
		random := make([]byte, size)
		for i := range random {
			random[i] = "x\"\\[]{}\x00\xff"[rng.Intn(9)]
		}
		for _, body := range []string{
			string(random),
			strings.Repeat("[", size) + strings.Repeat("]", size) + "]",
			strings.Repeat("x", size) + `\"]"]`,
			`"` + strings.Repeat(`\`, size) + `"]"]`,
			`"[{}]"` + strings.Repeat("x", size) + "]",
		} {
			for _, open := range []byte{'[', '{'} {
				input := string(open) + body
				want, wantErr := scalarContainerReference(input)
				for align := 0; align < 16; align++ {
					copy(storage[align:], input)
					src := unsafe.String(unsafe.SliceData(storage), align+len(input))
					end, err := SkipContainer(src, align)
					if end != align+want || err != wantErr {
						t.Fatalf("container align=%d input=%q: (%d,%v), want (%d,%v)", align, input, end, err, align+want, wantErr)
					}
				}
			}
		}
	}
	runtime.KeepAlive(storage)
}

func scalarContainerReference(src string) (int, types.ParsingError) {
	depth, inside, escaped := 1, false, false
	open, close := src[0], src[0]+2
	for i := 1; i < len(src); i++ {
		c := src[i]
		if c == '\\' {
			escaped = !escaped
			continue
		}
		if c == '"' && !escaped {
			inside = !inside
		}
		escaped = false
		if inside {
			continue
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				return i + 1, 0
			}
		}
	}
	return len(src), types.ERR_EOF
}
