//go:build linux && ((amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package kernels

import (
	"os"
	"syscall"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestTraversalMasksGuardPages(t *testing.T) {
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
			src := rt.String(&mapping[page+base], n)
			events, got, width := TraversalMasks(src, 0, true)
			wantWidth := minInt(n, 64)
			want := ^uint64(0) >> uint(64-wantWidth)
			if events != 0 || got != want || width != wantWidth {
				t.Fatalf("base=%d len=%d: (%x,%d)", base, n, got, width)
			}
		}
	}
	events, n := traversalMasks(&mapping[2*page], 0, 1)
	if events|n != 0 {
		t.Fatal(events, n)
	}
}
