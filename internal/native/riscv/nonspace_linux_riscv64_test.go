//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestNonSpaceMaskGuardPages(t *testing.T) {
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
	mid := mapping[page : 2*page]
	if err := syscall.Mprotect(mid, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	for i := range mid {
		mid[i] = 'x'
	}
	if err := syscall.Mprotect(mid, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	for n := 0; n <= 70; n++ {
		for _, base := range []int{0, page - n} {
			src := unsafe.String(&mapping[page+base], n)
			got, width := NonSpaceMask(src, 0)
			wantWidth := min(n, 64)
			want := ^uint64(0) >> uint(64-wantWidth)
			if got != want || width != wantWidth {
				t.Fatalf("base=%d len=%d: (%x,%d)", base, n, got, width)
			}
		}
	}
	if got := nonSpaceMask(&mapping[2*page], 0); got != 0 {
		t.Fatal(got)
	}
}
