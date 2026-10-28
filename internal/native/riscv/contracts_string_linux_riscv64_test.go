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

func stringGuardPage(t *testing.T) []byte {
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

func TestStringOperationsGuardPages(t *testing.T) {
	inputPage, outputPage := stringGuardPage(t), stringGuardPage(t)
	for prefix := 0; prefix <= 65; prefix++ {
		for _, tail := range []string{"", "\n\"\\\b", "\xe2", "\xe2\x80", "\u2028<&>\u2029", "中😊\xff", `\`, `\u`, `\u123`, `\u12x4`, `\ud800\udc00`, `\ud800\u0041`, `\ud800`, `\\ud800\\u12x4`} {
			src := strings.Repeat("a", prefix) + tail
			for _, offset := range []int{0, len(inputPage) - len(src)} {
				input := inputPage[offset : offset+len(src)]
				copy(input, src)
				if err := syscall.Mprotect(inputPage, syscall.PROT_READ); err != nil {
					t.Fatal(err)
				}
				guarded := unsafe.String(unsafe.SliceData(input), len(input))
				for _, capacity := range []int{0, len(src) / 2, len(src), 7 * len(src)} {
					for _, outputOffset := range []int{0, len(outputPage) - capacity} {
						out := outputPage[outputOffset : outputOffset+capacity]
						for flags := uint64(0); flags < 2; flags++ {
							want := make([]byte, capacity)
							an, aw := Quote(src, want, flags)
							bn, bw := Quote(guarded, out, flags)
							if an != bn || aw != bw || !bytes.Equal(want[:aw], out[:bw]) {
								t.Fatalf("guarded Quote src=%q capacity=%d flags=%d", src, capacity, flags)
							}
						}
						want := make([]byte, capacity)
						an, aw := HTMLEscape(src, want)
						bn, bw := HTMLEscape(guarded, out)
						if an != bn || aw != bw || !bytes.Equal(want[:aw], out[:bw]) {
							t.Fatalf("guarded HTMLEscape src=%q capacity=%d", src, capacity)
						}
					}
				}
				for flags := uint64(0); flags < 4; flags++ {
					want := make([]byte, len(src))
					out := outputPage[len(outputPage)-len(src):]
					aw, ap, ac := Unquote(src, want, flags)
					bw, bp, bc := Unquote(guarded, out, flags)
					if aw != bw || ap != bp || ac != bc || !bytes.Equal(want[:aw], out[:bw]) {
						t.Fatalf("guarded Unquote src=%q flags=%d", src, flags)
					}
				}
				if ValidUTF8(src) != ValidUTF8(guarded) {
					t.Fatal("guarded UTF8 validity")
				}
				for _, capacity := range []int{0, 1, 3, len(src)} {
					want := make([]int, capacity)
					// Offset alignment follows the page boundary and int width.
					out := unsafe.Slice((*int)(unsafe.Pointer(unsafe.SliceData(outputPage[len(outputPage)-capacity*8:]))), capacity)
					an, ac, af := InvalidUTF8(src, 0, want)
					bn, bc, bf := InvalidUTF8(guarded, 0, out)
					if an != bn || ac != bc || af != bf {
						t.Fatal("guarded UTF8 offsets")
					}
					for i := 0; i < ac; i++ {
						if want[i] != out[i] {
							t.Fatal("guarded UTF8 position")
						}
					}
				}
				if err := syscall.Mprotect(inputPage, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
					t.Fatal(err)
				}
			}
			for _, suffix := range []string{"", `",tail`} {
				quoted := `"` + src + suffix
				input := inputPage[len(inputPage)-len(quoted):]
				for _, validate := range []bool{false, true} {
					for _, unicodeErrors := range []bool{false, true} {
						copy(input, quoted)
						want := []byte(quoted)
						ae, aw, ax, ap, ac := ParseString(want, 0, validate, unicodeErrors)
						be, bw, bx, bp, bc := ParseString(input, 0, validate, unicodeErrors)
						if ae != be || aw != bw || ax != bx || ap != bp || ac != bc || !bytes.Equal(want, input) {
							t.Fatalf("guarded ParseString input=%q", quoted)
						}
					}
				}
			}
		}
	}
}

func TestParseStringPlainInputNeedsNoStores(t *testing.T) {
	page := stringGuardPage(t)
	src := `"` + strings.Repeat("plain string ", 128) + `"`
	input := page[len(page)-len(src):]
	copy(input, src)
	if err := syscall.Mprotect(page, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	end, written, escaped, pos, code := ParseString(input, 0, true, true)
	if end != len(src) || written != len(src)-2 || escaped || pos != -1 || code != 0 {
		t.Fatalf("read-only parser result=(%d,%d,%v,%d,%d)", end, written, escaped, pos, code)
	}
}
