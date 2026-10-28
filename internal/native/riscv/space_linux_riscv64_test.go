//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestSkipSpaceGuardPages(t *testing.T) {
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
	for i := range middle {
		middle[i] = ' '
	}
	vl := spaceVectorBytes()
	maxLen := 3*vl + 1
	if maxLen > page {
		maxLen = page
	}
	for n := 1; n <= maxLen; n++ {
		// Both ends of the readable page are exercised. Changing n also
		// changes the alignment of the input ending at the trailing guard.
		for _, base := range []int{0, page - n} {
			input := middle[base : base+n]
			if got := SkipSpace(unsafe.String(unsafe.SliceData(input), len(input)), 0); got != n {
				t.Fatalf("base=%d length=%d all whitespace: got %d", base, n, got)
			}
			for _, stop := range []int{0, n / 2, n - 1} {
				input[stop] = 'x'
				if got := SkipSpace(unsafe.String(unsafe.SliceData(input), len(input)), 0); got != stop {
					t.Fatalf("base=%d length=%d stop=%d: got %d", base, n, stop, got)
				}
				input[stop] = ' '
			}
			if got := SkipSpace(unsafe.String(unsafe.SliceData(input), len(input)), n); got != n {
				t.Fatalf("base=%d length=%d empty tail: got %d", base, n, got)
			}
		}
	}
	// A zero-length call must not read even an inaccessible address.
	if got := space(&mapping[2*page], 0); got != 0 {
		t.Fatalf("zero-length guard pointer: got %d", got)
	}
	if err := syscall.Mprotect(middle, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	src := unsafe.String(unsafe.SliceData(middle), len(middle))
	if got := SkipSpace(src, 0); got != page {
		t.Fatalf("read-only page: got %d, want %d", got, page)
	}
}
