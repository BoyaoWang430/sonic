//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"bytes"
	"strings"
	"testing"
	"unsafe"
)

type scalarEscapingPart struct {
	src, dst string
}

func scalarEscapingTailResult(parts []scalarEscapingPart, capacity int) (string, int, string) {
	var source, output strings.Builder
	consumed := 0
	full := true
	for _, part := range parts {
		source.WriteString(part.src)
		if full && output.Len()+len(part.dst) <= capacity {
			output.WriteString(part.dst)
			consumed += len(part.src)
		} else {
			full = false
		}
	}
	if !full {
		consumed = ^consumed
	}
	return source.String(), consumed, output.String()
}

func checkScalarEscapingTail(t *testing.T, operation func(string, []byte) (int, int), source string, capacity, srcAlign, dstAlign, wantConsumed int, want string) {
	t.Helper()
	const guard = 0xa5
	srcStart, dstStart := 8+srcAlign, 8+dstAlign
	srcStorage := bytes.Repeat([]byte{guard}, srcStart+len(source)+8)
	copy(srcStorage[srcStart:], source)
	dstStorage := bytes.Repeat([]byte{guard}, dstStart+capacity+8)
	src := unsafe.String(&srcStorage[srcStart], len(source))
	dst := dstStorage[dstStart : dstStart+capacity]
	consumed, written := operation(src, dst)
	if consumed != wantConsumed || written != len(want) || string(dst[:written]) != want {
		t.Fatalf("src=%q capacity=%d align=%d/%d: got (%d,%d) %q, want (%d,%d) %q", source, capacity, srcAlign, dstAlign, consumed, written, dst[:written], wantConsumed, len(want), want)
	}
	for i, b := range dstStorage {
		if (i < dstStart || i >= dstStart+written) && b != guard {
			t.Fatalf("write outside output at %d, capacity=%d written=%d", i, capacity, written)
		}
	}
	if src != source {
		t.Fatal("source modified")
	}
}

func TestScalarEscapingOverlappingTail(t *testing.T) {
	operations := []struct {
		name  string
		run   func(string, []byte) (int, int)
		parts []scalarEscapingPart
	}{
		{"Quote", func(src string, dst []byte) (int, int) { return Quote(src, dst, 0) }, []scalarEscapingPart{{"\n", `\n`}, {"\x1f", `\u001f`}, {`"`, `\"`}}},
		{"DoubleQuote", func(src string, dst []byte) (int, int) { return Quote(src, dst, 1) }, []scalarEscapingPart{{"\n", `\\n`}, {"\x1f", `\\u001f`}, {`"`, `\\\"`}}},
		{"HTML", HTMLEscape, []scalarEscapingPart{{"<", `\u003c`}, {"\u2028", `\u2028`}, {"\xe2", "\xe2"}}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			for n := 8; n < 32; n++ {
				// An earlier escape changes the source/destination offset.
				// Clean tails may only overwrite the copied ordinary suffix.
				for _, leading := range []bool{false, true} {
					for tailPos := 0; tailPos < 8; tailPos++ {
						for _, event := range append([]scalarEscapingPart{{"c", "c"}}, op.parts...) {
							var parts []scalarEscapingPart
							if leading {
								parts = append(parts, op.parts[0])
							}
							for i := 0; i < n-tailPos; i++ {
								parts = append(parts, scalarEscapingPart{"a", "a"})
							}
							parts = append(parts, event)
							for i := 0; i < tailPos; i++ {
								parts = append(parts, scalarEscapingPart{"b", "b"})
							}
							_, _, full := scalarEscapingTailResult(parts, 128)
							for capacity := 0; capacity <= len(full)+1; capacity++ {
								src, consumed, want := scalarEscapingTailResult(parts, capacity)
								checkScalarEscapingTail(t, op.run, src, capacity, (n+tailPos)%8, (capacity+n)%8, consumed, want)
							}
						}
					}
				}
			}
		})
	}
}

func TestScalarEscapingOverlappingTailGuardPages(t *testing.T) {
	sourcePage, destPage := newEscapingGuardPage(t), newEscapingGuardPage(t)
	for n := 8; n < 40; n++ {
		ordinary := strings.Repeat("a", n)
		quote := func(src string, dst []byte) (int, int) { return Quote(src, dst, 0) }
		for _, op := range []func(string, []byte) (int, int){quote, HTMLEscape} {
			checkEscapingGuarded(t, sourcePage, destPage, ordinary, n, op, n, ordinary)
		}
		for tailPos := 0; tailPos < 8; tailPos++ {
			prefix, suffix := ordinary[:n-tailPos], ordinary[:tailPos]
			for _, tc := range []struct {
				run       func(string, []byte) (int, int)
				src, want string
			}{
				{quote, prefix + "\n" + suffix, prefix + `\n` + suffix},
				{HTMLEscape, prefix + "<" + suffix, prefix + `\u003c` + suffix},
				{HTMLEscape, prefix + "\u2028" + suffix, prefix + `\u2028` + suffix},
			} {
				checkEscapingGuarded(t, sourcePage, destPage, tc.src, len(tc.want), tc.run, len(tc.src), tc.want)
			}
		}
	}
}
