//go:build linux && ((amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Runs requires the owned padding supplied by newParser. Put that entire
// allocation against inaccessible pages, rather than passing an unpadded
// buffer that belongs to the RISC-V Go parser's different input contract.
func TestARMParserPaddedStringGuardPages(t *testing.T) {
	pageSize := os.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 3*pageSize, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(mapping)
	page := mapping[pageSize : 2*pageSize]
	if err := syscall.Mprotect(page, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	for prefix := 0; prefix < 16; prefix++ {
		for _, length := range []int{0, 1, 7, 8, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127} {
			for _, suffix := range []string{`"`, ``, `\n"`, `\u4e2d"`, `\ud834\udd1e"`, `\u`, `\x"`, "\x01\""} {
				source := strings.Repeat(" ", prefix) + `"` + strings.Repeat("a", length) + suffix
				for _, options := range []uint64{0, uint64(OptionValidateString)} {
					want := cParityRunsTuneParser(source, 0, 8, options)
					wantCode := want.parse()
					wantUsed := int((want.nbuf.ncur - want.nbuf.nstart) / unsafe.Sizeof(node{}))
					size := len(source) + len(padding)
					for _, edge := range []int{0, len(page) - size} {
						input := page[edge : edge+size : edge+size]
						copy(input, source)
						copy(input[len(source):], padding)
						p := cParityRunsTuneParser(source, 0, 8, options)
						p.padded = input
						p.start = uintptr(unsafe.Pointer(&input[0]))
						p.cur, p.end = p.start, p.start+uintptr(len(source))
						code := p.parse()
						used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
						if code != wantCode || p.Pos() != want.Pos() || used != wantUsed || !bytes.Equal(p.padded, want.padded) {
							t.Fatalf("prefix=%d length=%d suffix=%q options=%d edge=%d: code/pos/used=(%d,%d,%d), want=(%d,%d,%d)",
								prefix, length, suffix, options, edge, code, p.Pos(), used, wantCode, want.Pos(), wantUsed)
						}
						for i := 0; i < used; i++ {
							if p.nodes[i] != want.nodes[i] {
								t.Fatalf("source=%q edge=%d node=%d: got=%+v want=%+v", source, edge, i, p.nodes[i], want.nodes[i])
							}
						}
					}
				}
			}
		}
	}
}
