//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestScanStringDenseTransitions(t *testing.T) {
	type scanCase struct {
		src  string
		want [2]stringScanResult
	}
	var cases []scanCase
	add := func(src string, loose, strict stringScanResult) {
		cases = append(cases, scanCase{src, [2]stringScanResult{loose, strict}})
	}
	// Inputs begin after the opening quote, as required by the native ABI.
	// Short ordinary runs can stay in the dense loop; longer gaps can resume
	// word scanning before a second run of escapes. The first slash stays 3.
	const dense = `\n\t\\\"`
	const tail = "#\"#\\]\x00\x1f\\\""
	for _, gap := range []int{1, 2, 7, 8, 33} {
		body := "abc" + strings.Repeat(dense, 4) + strings.Repeat("x", gap) + strings.Repeat(dense, 3)
		success := stringScanResult{len(body) + 1, 3, -1, 0}
		add(body+`"`+tail, success, success)
		for _, suffix := range []string{`\`, `\"`} {
			src := body + suffix
			eof := stringScanResult{len(src), 3, len(src), types.ERR_EOF}
			add(src, eof, eof)
		}
		// Missing Unicode bytes win over the malformed available hex, while
		// relaxed scanning still accepts the closing quote.
		src := body + `\uZZ"`
		add(src, stringScanResult{len(src), 3, -1, 0},
			stringScanResult{len(src), 3, len(src), types.ERR_EOF})
		src = body + `\x"`
		add(src, stringScanResult{len(src), 3, -1, 0},
			stringScanResult{len(body), 3, len(body), types.ERR_INVALID_CHAR})
	}
	prefix := "abc" + strings.Repeat(dense, 4) + "xy"
	for _, run := range []int{3, 4, 9} {
		body := prefix + strings.Repeat(`\`, run)
		end := len(body) + 1 + run%2
		success := stringScanResult{end, 3, -1, 0}
		add(body+`""`+tail, success, success)
		src := body + `"`
		want := stringScanResult{len(src), 3, -1, 0}
		if run%2 != 0 {
			want = stringScanResult{len(src), 3, len(src), types.ERR_EOF}
		}
		add(src, want, want)
	}
	for align := 0; align < 16; align++ {
		for _, tc := range cases {
			storage, ptr := alignedStringBlockInput(tc.src, align)
			for validate := uint64(0); validate < 2; validate++ {
				end, escape, errorPos, err := scanString(ptr, len(tc.src), validate)
				got := stringScanResult{end, escape, errorPos, err}
				if got != tc.want[validate] {
					t.Fatalf("align=%d validate=%d src=%q: got %+v, want %+v", align, validate, tc.src, got, tc.want[validate])
				}
				// Validate the block API's first four returns and exact raw
				// suffix events; legal changes to tail width remain allowed.
				checkScanStringBlock(t, ptr, len(tc.src), validate)
			}
			runtime.KeepAlive(storage)
		}
	}
}
