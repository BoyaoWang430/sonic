//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The source and destination each have independent inaccessible guard pages.
func newEscapingGuardPage(t *testing.T) []byte {
	t.Helper()
	page := os.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Munmap(mapping); err != nil {
			t.Error(err)
		}
	})
	middle := mapping[page : 2*page]
	if err := syscall.Mprotect(middle, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	return middle
}

func checkEscapingGuarded(t *testing.T, sourcePage, destPage []byte, src string, capacity int, operation func(string, []byte) (int, int), wantConsumed int, want string) {
	t.Helper()
	for _, srcBase := range []int{0, len(sourcePage) - len(src)} {
		input := sourcePage[srcBase : srcBase+len(src)]
		copy(input, src)
		for _, dstBase := range []int{0, len(destPage) - capacity} {
			dst := destPage[dstBase : dstBase+capacity]
			for i := range dst {
				dst[i] = 0xa5
			}
			consumed, written := operation(unsafe.String(unsafe.SliceData(input), len(input)), dst)
			if consumed != wantConsumed || written != len(want) {
				t.Fatalf("guarded escape src=%q cap=%d srcBase=%d dstBase=%d: got (%d,%d), want (%d,%d)", src, capacity, srcBase, dstBase, consumed, written, wantConsumed, len(want))
			}
			if string(dst[:written]) != want || !bytes.Equal(dst[written:], bytes.Repeat([]byte{0xa5}, capacity-written)) {
				t.Fatalf("guarded escape src=%q cap=%d: output %q, want %q", src, capacity, dst, want)
			}
			if string(input) != src {
				t.Fatalf("input modified: got %q, want %q", input, src)
			}
		}
	}
}

func TestQuoteGuardPages(t *testing.T) {
	sourcePage, destPage := newEscapingGuardPage(t), newEscapingGuardPage(t)
	maxPrefix := 3*quoteVectorBytes() + 1
	if maxPrefix > len(sourcePage)-16 {
		maxPrefix = len(sourcePage) - 16
	}
	for n := 0; n <= maxPrefix; n++ {
		prefix := strings.Repeat("a", n)
		for _, flags := range []uint64{0, 1} {
			operation := func(src string, dst []byte) (int, int) { return Quote(src, dst, flags) }
			checkEscapingGuarded(t, sourcePage, destPage, prefix, n, operation, n, prefix)
			if n > 0 {
				checkEscapingGuarded(t, sourcePage, destPage, prefix, n-1, operation, ^(n - 1), prefix[:n-1])
			}
			for _, tt := range []struct{ src, single, double string }{
				{"\n", `\n`, `\\n`}, {"\b", `\u0008`, `\\u0008`}, {"\x1f", `\u001f`, `\\u001f`},
				{`"`, `\"`, `\\\"`}, {`\`, `\\`, `\\\\`},
			} {
				output := tt.single
				if flags != 0 {
					output = tt.double
				}
				input := prefix + tt.src
				for capacity := n; capacity < n+len(output); capacity++ {
					checkEscapingGuarded(t, sourcePage, destPage, input, capacity, operation, ^n, prefix)
				}
				checkEscapingGuarded(t, sourcePage, destPage, input, n+len(output), operation, len(input), prefix+output)
			}
		}
	}
	// Source protection catches accidental stores even on scalar escape paths.
	src := "\n\"\\\b"
	input := sourcePage[len(sourcePage)-len(src):]
	copy(input, src)
	if err := syscall.Mprotect(sourcePage, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []struct {
		flags uint64
		want  string
	}{
		{0, `\n\"\\\u0008`}, {1, `\\n\\\"\\\\\\u0008`},
	} {
		dst := destPage[len(destPage)-len(mode.want):]
		consumed, written := Quote(unsafe.String(unsafe.SliceData(input), len(input)), dst, mode.flags)
		if consumed != len(src) || written != len(mode.want) || string(dst) != mode.want {
			t.Fatalf("read-only Quote flags=%d: (%d,%d) %q", mode.flags, consumed, written, dst)
		}
	}
}
