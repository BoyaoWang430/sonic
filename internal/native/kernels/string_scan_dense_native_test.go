//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package kernels

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

func TestScanStringDenseUnicodeParity(t *testing.T) {
	var inputs []string
	// 1. Consecutive \uXXXX runs spanning 0..40 escapes across 16/32/64B boundaries,
	// with varying plain prefixes and mixed simple escapes before/after.
	for _, pref := range []string{"", "a", "abc", strings.Repeat("x", 13), strings.Repeat("x", 16), `\n\uD83D\uDE00`} {
		for _, count := range []int{1, 2, 3, 5, 8, 12, 20, 35} {
			run := strings.Repeat(`\u0061`, count)
			inputs = append(inputs,
				pref+run+`"`,
				pref+run+`"`+`#"\x00\u1234`,
				pref+run+`abc"`,
				pref+run+`\n\"\\\/\b\f\r\t"`,
				pref+`\t`+run+`\r\n`+run+`"`,
				pref+run+`\u`,
				pref+run+`\u1`,
				pref+run+`\u12`,
				pref+run+`\u123`,
				pref+run+`\uZZ`,
				pref+run+`\`,
				pref+run+`\"`,
			)
		}
	}
	// 2. Every non-hex byte in every lane (0..3) of both 1st and 3rd consecutive \uXXXX.
	badBytes := []byte{0x00, 0x1f, 0x20, '"', '\\', '/', ':', '@', 'G', '`', 'g', 0x7f, 0x80, 0xaf, 0xb0, 0xc0, 0xff}
	for _, prefix := range []string{"", `\u0061\uABCD`, strings.Repeat("a", 14) + `\u0061\u9fF0`} {
		for lane := 0; lane < 4; lane++ {
			for _, bb := range badBytes {
				hex4 := [4]byte{'0', 'a', 'F', '9'}
				hex4[lane] = bb
				inputs = append(inputs, prefix+`\u`+string(hex4[:])+`"`)
				inputs = append(inputs, prefix+`\u`+string(hex4[:])+`\u0061"`)
			}
		}
	}
	// 3. Valid uppercase/lowercase/digit hex combinations in every lane.
	inputs = append(inputs,
		`\u0123\u4567\u89ab\ucdef\uABCD\uEF09"`,
		strings.Repeat(`\u0061\uD83D\uDE00\u9aF0`, 16)+`"`,
	)

	for _, src := range inputs {
		for _, align := range []int{0, 1, 7, 15} {
			storage, ptr := alignedStringBlockInput(src, align)
			gotEnd, gotEsc, gotErrPos, gotErr := scanStringStrict(ptr, len(src))
			wantEnd, wantEsc, wantErrPos, wantErr := scanStringStrictScalar(ptr, len(src))
			if gotEnd != wantEnd || gotEsc != wantEsc || gotErrPos != wantErrPos || gotErr != wantErr {
				t.Fatalf("scanStringStrict mismatch align=%d src=%q: got=(%d,%d,%d,%v) want=(%d,%d,%d,%v)",
					align, src, gotEnd, gotEsc, gotErrPos, gotErr, wantEnd, wantEsc, wantErrPos, wantErr)
			}
			checkScanStringBlock(t, ptr, len(src), 1)
			runtime.KeepAlive(storage)
		}
	}
}
