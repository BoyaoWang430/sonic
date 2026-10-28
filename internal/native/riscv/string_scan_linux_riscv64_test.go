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

func TestScanStringGuardPages(t *testing.T) {
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
	maxPad := 3*64 + 1
	if maxPad > page-16 {
		maxPad = page - 16
	}
	for pad := 0; pad <= maxPad; pad++ {
		prefix := `"` + strings.Repeat("a", pad)
		slash := len(prefix)
		for _, tt := range []struct {
			src           string
			loose, strict stringScanResult
		}{
			{prefix + `"`, stringScanResult{slash + 1, -1, -1, 0}, stringScanResult{slash + 1, -1, -1, 0}},
			{prefix + `\""`, stringScanResult{slash + 3, slash, -1, 0}, stringScanResult{slash + 3, slash, -1, 0}},
			{prefix + `\n"`, stringScanResult{slash + 3, slash, -1, 0}, stringScanResult{slash + 3, slash, -1, 0}},
			{prefix + `\u1234"`, stringScanResult{slash + 7, slash, -1, 0}, stringScanResult{slash + 7, slash, -1, 0}},
			{prefix + `\uD800"`, stringScanResult{slash + 7, slash, -1, 0}, stringScanResult{slash + 7, slash, -1, 0}},
			{prefix, stringScanResult{slash, -1, slash, types.ERR_EOF}, stringScanResult{slash, -1, slash, types.ERR_EOF}},
			{prefix + `\`, stringScanResult{slash + 1, slash, slash + 1, types.ERR_EOF}, stringScanResult{slash + 1, slash, slash + 1, types.ERR_EOF}},
			{prefix + `\"`, stringScanResult{slash + 2, slash, slash + 2, types.ERR_EOF}, stringScanResult{slash + 2, slash, slash + 2, types.ERR_EOF}},
			{prefix + `\u12`, stringScanResult{slash + 4, slash, slash + 4, types.ERR_EOF}, stringScanResult{slash + 4, slash, slash + 4, types.ERR_EOF}},
			{prefix + `\u12"`, stringScanResult{slash + 5, slash, -1, 0}, stringScanResult{slash + 5, slash, slash + 5, types.ERR_EOF}},
			{prefix + `\u12+4"`, stringScanResult{slash + 7, slash, -1, 0}, stringScanResult{slash, slash, slash, types.ERR_INVALID_CHAR}},
			{prefix + `\x"`, stringScanResult{slash + 3, slash, -1, 0}, stringScanResult{slash, slash, slash, types.ERR_INVALID_CHAR}},
			{prefix + "\x00\"", stringScanResult{slash + 2, -1, -1, 0}, stringScanResult{slash, -1, slash, types.ERR_INVALID_CHAR}},
		} {
			for _, base := range []int{0, page - len(tt.src)} {
				input := middle[base : base+len(tt.src)]
				copy(input, tt.src)
				checkStringScan(t, unsafe.String(unsafe.SliceData(input), len(input)), 0, false, tt.loose)
				checkStringScan(t, unsafe.String(unsafe.SliceData(input), len(input)), 0, true, tt.strict)
			}
		}
	}
	// Finish a dense -> ordinary -> dense sequence at the protected page.
	// Padding rotates every native input alignment without changing the EOF
	// condition, including a final slash or an escaped final quote.
	for pad := 0; pad < 16; pad++ {
		body := `"` + strings.Repeat("a", pad) + strings.Repeat(`\n\t\\\"`, 3) + "xy" + strings.Repeat(`\n\t\\\"`, 2)
		for _, suffix := range []string{`"`, `\`, `\"`} {
			content := body + suffix
			want := stringScanResult{len(content), 1 + pad, -1, 0}
			if suffix != `"` {
				want = stringScanResult{len(content), 1 + pad, len(content), types.ERR_EOF}
			}
			base := page - len(content)
			copy(middle[base:], content)
			input := unsafe.String(&middle[base], len(content))
			checkStringScan(t, input, 0, false, want)
			checkStringScan(t, input, 0, true, want)
			for _, validate := range []uint64{0, 1} {
				checkScanStringBlock(t, &middle[base+1], len(content)-1, validate)
			}
		}
	}
	for _, validate := range []uint64{0, 1} {
		end, escape, errorPos, err := scanString(&mapping[2*page], 0, validate)
		if got, want := (stringScanResult{end, escape, errorPos, err}), (stringScanResult{0, -1, 0, types.ERR_EOF}); got != want {
			t.Fatalf("zero-length guard pointer, validate=%d: got %+v, want %+v", validate, got, want)
		}
	}
	// Read-only input also covers the Unicode scalar path without allowing
	// any writes to the source, including scratch writes.
	input := `"` + strings.Repeat("a", maxPad) + `\uD800\uDC00\n"`
	base := page - len(input)
	copy(middle[base:], input)
	if err := syscall.Mprotect(middle, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	checkStringScan(t, unsafe.String(&middle[base], len(input)), 0, true, stringScanResult{len(input), 1 + maxPad, -1, 0})
}
