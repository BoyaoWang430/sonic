//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestScanningGuardPages(t *testing.T) {
	page := os.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Munmap(mapping); err != nil {
			t.Error(err)
		}
	}()
	middle := mapping[page : 2*page]
	if err := syscall.Mprotect(middle, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	for n := 0; n <= 129; n++ {
		for _, source := range []string{
			strings.Repeat(" ", n),
			strings.Repeat("9", n) + ".25e-2,",
			strings.Repeat("x", n) + "\x00\\\"",
			strings.Repeat("a", n) + `\u12`,
			strings.Repeat("a", n) + `\u1234"`,
			strings.Repeat("[", n) + strings.Repeat("]", n),
		} {
			for _, base := range []int{0, page - len(source)} {
				copy(middle[base:], source)
				bounded := unsafe.String(&mapping[page+base], len(source))

				if SkipSpace(bounded, 0) != SkipSpace(source, 0) || SkipNumber(bounded, 0) != SkipNumber(source, 0) {
					t.Fatalf("guard scan %q", source)
				}
				{
					e, err := ScanNumber(bounded, 0)
					we, werr := ScanNumber(source, 0)
					if e != we || err != werr {
						t.Fatalf("guard number %q", source)
					}
				}
				{
					e, err := ScanNumberStrict(bounded, 0)
					we, werr := ScanNumberStrict(source, 0)
					if e != we || err != werr {
						t.Fatalf("guard strict number %q", source)
					}
				}
				{
					e, ns, b, w := PathMasks(bounded, 0)
					we, wns, wb, ww := PathMasks(source, 0)
					if e != we || ns != wns || b != wb || w != ww {
						t.Fatalf("guard path masks %q", source)
					}
				}
				for _, strict := range []bool{false, true} {
					{
						e, ns, w := TraversalMasks(bounded, 0, strict)
						we, wns, ww := TraversalMasks(source, 0, strict)
						if e != we || ns != wns || w != ww {
							t.Fatalf("guard traversal %q", source)
						}
					}
					{
						e, err := SkipString(bounded, strict)
						we, werr := SkipString(source, strict)
						if e != we || err != werr {
							t.Fatalf("guard string %q", source)
						}
					}
				}
				if len(source) > 0 && (source[0] == '[' || source[0] == '{') {
					{
						e, err := SkipContainer(bounded, 0)
						we, werr := SkipContainer(source, 0)
						if e != we || err != werr {
							t.Fatalf("guard container %q", source)
						}
					}
				}
			}
		}
	}
	// A read-only source also detects any accidental scratch stores.
	source := `"` + strings.Repeat("a", 127) + `\u1234"`
	base := page - len(source)
	copy(middle[base:], source)
	if err := syscall.Mprotect(middle, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	bounded := unsafe.String(&middle[base], len(source))

	if end, _, _, err := ScanString(bounded, 0, true); end != len(source) || err != 0 {
		t.Fatalf("read-only source: (%d,%v)", end, err)
	}
}
