//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestSkipContainerGuardPages(t *testing.T) {
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
	for pad := 0; pad <= min(3*64+1, page-32); pad++ {
		for _, body := range []string{"", `"\\\"[]"`, `{"x":[1]}`, `\\\"`, `"\x\uQQQQ"`} {
			input := `[` + strings.Repeat("x", pad) + body + `]`
			for _, suffix := range []string{input, input[:len(input)-1]} {
				wantErr := types.ParsingError(0)
				if len(suffix) != len(input) {
					wantErr = types.ERR_EOF
				}
				for _, base := range []int{0, page - len(suffix)} {
					copy(middle[base:], suffix)
					src := unsafe.String(&middle[base], len(suffix))
					checkContainer(t, src, 0, len(suffix), wantErr)
				}
			}
		}
	}
	end, code := skipContainer(&mapping[2*page], 0, '[')
	if end != 0 || code != types.ERR_EOF {
		t.Fatalf("zero-length guard pointer = (%d, %v)", end, code)
	}
	input := `[` + strings.Repeat(`{"x":"[]\\\""},`, 40) + `null]`
	base := page - len(input)
	copy(middle[base:], input)
	if err := syscall.Mprotect(middle, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	checkContainer(t, unsafe.String(&middle[base], len(input)), 0, len(input), 0)
}
