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

func TestNumberScanGuardPages(t *testing.T) {
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
	for n := 1; n <= min(page, 3*spaceVectorBytes()+7); n++ {
		for _, base := range []int{0, page - n} {
			for _, suffix := range []string{"", ".", "e+", "e-2", ".2E+3", ".1234", ".12345", "e+1234", "e+12345", ",", " ", "\x00", ".2.3", "e+2.3E4"} {
				if len(suffix) > n {
					continue
				}
				body := strings.Repeat("7", n-len(suffix)) + suffix
				copy(input[base:base+n], body)
				src := unsafe.String(&input[base], n)
				for _, scan := range []func(string, int) (int, types.ParsingError){ScanNumber, ScanNumberStrict} {
					wantEnd, wantErr := scan(body, 0)
					checkNumberScan(t, "guard", src, 0, wantEnd, wantErr, scan)
				}
				if end, want := SkipNumber(src, 0), SkipNumber(body, 0); end != want {
					t.Fatalf("guard SkipNumber=%d, want %d", end, want)
				}
			}
		}
	}
	// The bounded strict integer lookahead must check the input length before
	// every scalar load, including when its eighth byte is the final byte.
	for _, digits := range []int{7, 8, 9} {
		for _, sign := range []string{"", "-"} {
			integer := sign + strings.Repeat("7", digits)
			for b := -1; b < 256; b++ {
				body := integer
				end := len(integer)
				var err types.ParsingError
				if b >= 0 {
					body += string([]byte{byte(b)})
					switch byte(b) {
					case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
						end++
					case '.', 'e', 'E', '+', '-':
						err = types.ERR_INVALID_CHAR
					}
				}
				copy(input[page-len(body):], body)
				src := unsafe.String(&input[page-len(body)], len(body))
				checkNumberScan(t, "guard-strict-short", src, 0, end, err, ScanNumberStrict)
			}
		}
	}
	for mode := uint64(0); mode <= 2; mode++ {
		wantErr := types.ERR_EOF
		if mode == 2 {
			wantErr = 0
		}
		if end, err := numberScan(&mapping[2*page], 0, mode); end != 0 || err != wantErr {
			t.Fatalf("protected empty mode %d: (%d,%v)", mode, end, err)
		}
	}
	copy(input, strings.Repeat("9", page))
	if err := syscall.Mprotect(input, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	src := unsafe.String(unsafe.SliceData(input), len(input))
	checkNumberScan(t, "read-only-prefix", src, 0, page, 0, ScanNumber)
	checkNumberScan(t, "read-only-strict", src, 0, page, 0, ScanNumberStrict)
	if end := SkipNumber(src, 0); end != page {
		t.Fatalf("read-only SkipNumber=%d, want %d", end, page)
	}
}
