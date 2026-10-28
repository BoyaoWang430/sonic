//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestUTF8GuardPages(t *testing.T) {
	page := os.Getpagesize()
	mapPages := func() []byte {
		mapping, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := syscall.Munmap(mapping); err != nil {
				t.Error(err)
			}
		})
		if err := syscall.Mprotect(mapping[page:2*page], syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
			t.Fatal(err)
		}
		return mapping
	}
	inputMap, outputMap := mapPages(), mapPages()
	inputPage := inputMap[page : 2*page]
	vl := utf8VectorBytes()
	maxLen := 3*vl + 4
	if maxLen > page {
		maxLen = page
	}
	for n := 1; n <= maxLen; n++ {
		for _, base := range []int{0, page - n} {
			input := inputPage[base : base+n]
			for _, pattern := range []string{"a", "α", "中", "😊", "α中😊"} {
				for _, suffix := range []string{"", "α", "中", "😊", "\xff", "\xe0", "\xf0\x90", "\xed\xa0\x80"} {
					if len(suffix) > n {
						continue
					}
					for i := range input {
						input[i] = pattern[i%len(pattern)]
					}
					copy(input[n-len(suffix):], suffix)
					src := unsafe.String(unsafe.SliceData(input), len(input))
					checkUTF8AgainstStdlib(t, src, 0)
					for slots := 0; slots <= 4; slots++ {
						// The first byte after the output slice is inaccessible. The
						// zero-slot pointer itself is inaccessible and must not be read.
						ptr := (*int)(unsafe.Pointer(&outputMap[2*page-slots*int(unsafe.Sizeof(int(0)))]))
						out := unsafe.Slice(ptr, slots)
						want := make([]int, slots)
						wn, wc, wf := InvalidUTF8(src, 0, want)
						next, count, full := InvalidUTF8(src, 0, out)
						if next != wn || count != wc || full != wf || !equalUTF8Positions(out[:count], want[:wc]) {
							t.Fatalf("page base=%d length=%d slots=%d: (%d, %d, %v), want (%d, %d, %v)", base, n, slots, next, count, full, wn, wc, wf)
						}
					}
				}
			}
		}
	}
	// Empty input must not be dereferenced, even when the pointer is protected.
	guardInput := &inputMap[2*page]
	guardOutput := (*int)(unsafe.Pointer(&outputMap[2*page]))
	if next, count, full := utf8Scan(guardInput, 0, 7, guardOutput, 0); next != 7 || count != 0 || full {
		t.Fatalf("empty guard-pointer call = (%d, %d, %v)", next, count, full)
	}
	// Input remains read-only during both public operations.
	for i := range inputPage {
		inputPage[i] = "αβ"[i%len("αβ")]
	}
	copy(inputPage[page-4:], "😊")
	if err := syscall.Mprotect(inputPage, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	src := unsafe.String(unsafe.SliceData(inputPage), len(inputPage))
	if !ValidUTF8(src) {
		t.Fatal("read-only input was rejected")
	}
	if next, count, full := InvalidUTF8(src, 0, nil); next != len(src) || count != 0 || full {
		t.Fatalf("read-only input = (%d, %d, %v)", next, count, full)
	}
}
