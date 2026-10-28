//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
)

func TestStringDecodeScalarTailGuardPages(t *testing.T) {
	inMap, inputPage := stringDecodePages(t)
	outMap, outputPage := stringDecodePages(t)
	page := len(inputPage)
	var inputs []string
	for n := 0; n <= 72; n++ {
		plain := strings.Repeat("p", n)
		inputs = append(inputs, plain, plain+`"`, plain+`",0]`)
	}
	for n := 0; n < 16; n++ {
		for _, escape := range []string{`\u005c`, `\u0022`, `\u0001`, `\`, `\u12`, `\u12x4`} {
			body := strings.Repeat("p", n%8) + escape + strings.Repeat("]#! ", 4)[:n]
			inputs = append(inputs, body, body+`"`)
		}
	}
	for _, input := range inputs {
		for mode := uint64(0); mode < 16; mode++ {
			want, wantOutput, wantAlias, wantInPlace := stringDecodeAlignedBaseline(input, mode)
			for _, srcStart := range []int{0, page - len(input)} {
				source := inputPage[srcStart : srcStart+len(input)]
				copy(source, input)
				if err := syscall.Mprotect(inputPage, syscall.PROT_READ); err != nil {
					t.Fatal(err)
				}
				for _, dstStart := range []int{0, page - len(input)} {
					output := outputPage[dstStart : dstStart+len(input)]
					for i := range output {
						output[i] = 0xa5
					}
					got := callStringDecodeTail(source, output, mode)
					if got != want || !bytes.Equal(output, wantOutput) {
						t.Fatalf("guard input=%q mode=%d source=%d destination=%d: got (%+v,%x), want (%+v,%x)", input, mode, srcStart, dstStart, got, output, want, wantOutput)
					}
				}
				// With no escapes, exact aliasing must also work on a
				// read-only page: even overlapping stores are unnecessary.
				if !strings.Contains(input, `\`) {
					if got := callStringDecodeTail(source, source, mode); got != wantAlias {
						t.Fatalf("read-only in-place input=%q mode=%d: got %+v, want %+v", input, mode, got, wantAlias)
					}
				}
				if err := syscall.Mprotect(inputPage, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
					t.Fatal(err)
				}
				got := callStringDecodeTail(source, source, mode)
				if got != wantAlias || !bytes.Equal(source, wantInPlace) {
					t.Fatalf("in-place guard input=%q mode=%d start=%d: got (%+v,%x), want (%+v,%x)", input, mode, srcStart, got, source, wantAlias, wantInPlace)
				}
			}
		}
	}
	for mode := uint64(0); mode < 16; mode++ {
		end, written, escaped, pos, code := stringDecode(&inMap[2*page], 0, &outMap[2*page], mode)
		wantPos, wantCode := -1, 0
		if mode&4 != 0 {
			wantPos, wantCode = 0, 5
		}
		if end != 0 || written != 0 || escaped != 0 || pos != wantPos || code != wantCode {
			t.Fatalf("empty guard mode=%d: (%d,%d,%d,%d,%d)", mode, end, written, escaped, pos, code)
		}
	}
}
