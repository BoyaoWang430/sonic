//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestScanStringBlockGuardPages(t *testing.T) {
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
	input := mapping[page : 2*page]
	if err := syscall.Mprotect(input, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	for prefix := 0; prefix <= 129; prefix++ {
		for _, body := range []string{`"`, `\""`, `\u1234"`, `\u12"`, `\x"`, `\`} {
			for _, tail := range []string{"", `#"\]`, "\x00\x1f\\\""} {
				content := strings.Repeat("a", prefix) + body + tail
				for _, base := range []int{0, page - len(content)} {
					copy(input[base:], content)
					for _, validate := range []uint64{0, 1} {
						checkScanStringBlock(t, &mapping[page+base], len(content), validate)
					}
				}
			}
		}
	}
	for _, validate := range []uint64{0, 1} {
		checkScanStringBlock(t, &mapping[2*page], 0, validate)
	}
	content := strings.Repeat("a", 63) + `\u1234"` + "#\"#\\]\x00\x1f"
	copy(input[page-len(content):], content)
	if err := syscall.Mprotect(input, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	for _, validate := range []uint64{0, 1} {
		checkScanStringBlock(t, &input[page-len(content)], len(content), validate)
	}
}
