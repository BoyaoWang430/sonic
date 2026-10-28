//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestRISCVStringProbeProtectedTail(t *testing.T) {
	page := os.Getpagesize()
	memory, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(memory)
	for _, guard := range [][]byte{memory[:page], memory[2*page:]} {
		if err := syscall.Mprotect(guard, syscall.PROT_NONE); err != nil {
			t.Fatal(err)
		}
	}
	for offset := 0; offset < 16; offset++ {
		prefix := strings.Repeat("\"\\#]", (offset+3)/4)[:offset]
		for n := 0; n < 41; n++ {
			for _, tail := range []string{"", `"`, `\`, `\"`, `\""`, `\\"`, `"\`, `#]"`} {
				input := prefix + strings.Repeat("a", n) + tail
				for _, begin := range []int{page, 2*page - len(input)} {
					copy(memory[begin:begin+len(input)], input)
					src := unsafe.String(&memory[begin], len(input))
					assertRISCVStringProbeTail(t, src, offset)
				}
			}
		}
	}
}
