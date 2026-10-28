//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"
	"unsafe"
)

func TestStringDecodeShortSpansGuardPages(t *testing.T) {
	_, inputPage := stringDecodePages(t)
	_, outputPage := stringDecodePages(t)
	const plain = "0123456789abcdef"
	for gap := 0; gap <= len(plain); gap++ {
		for contraction := 1; contraction <= 9; contraction++ {
			// Short plain spans between escapes exercise overlapping copies.
			body := strings.Repeat(`\n`, contraction) + plain[:gap] + `\t`
			want := stringDecodeOracle(t, body)
			input := inputPage[len(inputPage)-len(body):]
			output := outputPage[len(outputPage)-len(body):]
			copy(input, body)
			for i := range output {
				output[i] = 0xa5
			}
			written, pos, code := Unquote(unsafe.String(unsafe.SliceData(input), len(input)), output, 0)
			if written != len(want) || pos != -1 || code != 0 || string(output[:written]) != want {
				t.Fatalf("guarded short span gap=%d contraction=%d: written=%d pos=%d code=%d",
					gap, contraction, written, pos, code)
			}
			for _, c := range output[written:] {
				if c != 0xa5 {
					t.Fatalf("guarded short span wrote beyond output: gap=%d contraction=%d", gap, contraction)
				}
			}
			written, pos, code = Unquote(unsafe.String(unsafe.SliceData(input), len(input)), input, 0)
			if written != len(want) || pos != -1 || code != 0 || string(input[:written]) != want ||
				string(input[written:]) != body[written:] {
				t.Fatalf("guarded in-place short span gap=%d contraction=%d: written=%d pos=%d code=%d",
					gap, contraction, written, pos, code)
			}
		}
	}
}
