//go:build !sonic_compat && (darwin || linux) && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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
	"os"
	"syscall"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestLexBatchGuardPageBoundaries(t *testing.T) {
	pageSize := os.Getpagesize()
	// Allocate 3 contiguous pages: [PROT_NONE | RW | PROT_NONE]
	mem, err := syscall.Mmap(-1, 0, 3*pageSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatalf("mmap failed: %v", err)
	}
	defer func() {
		_ = syscall.Munmap(mem)
	}()
	if err := syscall.Mprotect(mem[:pageSize], syscall.PROT_NONE); err != nil {
		t.Fatalf("mprotect left guard failed: %v", err)
	}
	if err := syscall.Mprotect(mem[2*pageSize:], syscall.PROT_NONE); err != nil {
		t.Fatalf("mprotect right guard failed: %v", err)
	}

	rw := mem[pageSize : 2*pageSize]
	samples := [][]byte{
		// Multi-64B sample with escapes (exercises slash unpack on full 64B blocks).
		[]byte(`{"id":9876543210,"esc":"a\\b\"c\n","arr":[1,2,3,true,false,null],"tail":"end"}` +
			`{"extra":"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"}`),
		// Multi-64B backslash-free sample (exercises zero-slash fast path on full 64B blocks).
		[]byte(`{"id":9876543210,"msg":"plain ascii text without any escape chars","arr":[1,2,3,true,false,null]}` +
			`{"extra":"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"}`),
	}
	flagsList := []types.LexFlags{types.LexFlagNone, types.LexFlagRejectControl}

	// Test every document length 1..len(sample) under both LexFlagNone and
	// LexFlagRejectControl placed flush against BOTH:
	//   1. the right PROT_NONE guard page (catches any 1-byte overread past src+size)
	//   2. the left PROT_NONE guard page (catches any 1-byte underread before src)
	for _, sample := range samples {
		for _, flags := range flagsList {
			for n := 1; n <= len(sample); n++ {
				// 1. Right guard boundary
				rightSlice := rw[pageSize-n : pageSize]
				copy(rightSlice, sample[:n])
				rightStr := unsafe.String(unsafe.SliceData(rightSlice), n)
				for pos := 0; pos <= n; pos += 1 + n/5 {
					assertLexBatchHardwareEqualsScalar(t, rightStr, pos, 16, flags, types.LexBlockCarry{})
				}
				if n > 1 {
					assertLexBatchHardwareEqualsScalar(t, rightStr, n-1, 16, flags, types.LexBlockCarry{})
				}

				// 2. Left guard boundary
				leftSlice := rw[:n]
				copy(leftSlice, sample[:n])
				leftStr := unsafe.String(unsafe.SliceData(leftSlice), n)
				assertLexBatchHardwareEqualsScalar(t, leftStr, 0, 16, flags, types.LexBlockCarry{})
			}
		}
	}
}

func TestScanStringDenseUnicodeGuardPage(t *testing.T) {
	pageSize := os.Getpagesize()
	mem, err := syscall.Mmap(-1, 0, 3*pageSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatalf("mmap failed: %v", err)
	}
	defer func() { _ = syscall.Munmap(mem) }()
	if err := syscall.Mprotect(mem[:pageSize], syscall.PROT_NONE); err != nil {
		t.Fatalf("mprotect left guard failed: %v", err)
	}
	if err := syscall.Mprotect(mem[2*pageSize:], syscall.PROT_NONE); err != nil {
		t.Fatalf("mprotect right guard failed: %v", err)
	}
	rw := mem[pageSize : 2*pageSize]
	cases := []string{
		`\u0061\uD83D\uDE00\u0062\u0063\u0064\u0065\u0066"`,
		`abcdefghijklmnop\u0061\u0062\u0063\u0064\u0065\u0066"`,
		`\u0061\u0062\u0063\u0064\u0065\u0066\uZZ`,
	}
	for _, full := range cases {
		for n := 1; n <= len(full); n++ {
			rightSlice := rw[pageSize-n : pageSize]
			copy(rightSlice, full[:n])
			ptr := unsafe.SliceData(rightSlice)
			e1, esc1, ep1, err1 := scanStringStrict(ptr, n)
			e2, esc2, ep2, err2 := scanStringStrictScalar(ptr, n)
			if e1 != e2 || esc1 != esc2 || ep1 != ep2 || err1 != err2 {
				t.Fatalf("guardpage scanStringStrict n=%d src=%q: got=(%d,%d,%d,%v) want=(%d,%d,%d,%v)",
					n, full[:n], e1, esc1, ep1, err1, e2, esc2, ep2, err2)
			}
			checkScanStringBlock(t, ptr, n, 1)
		}
	}
}

func TestScanStringRelaxedGuardPageBoundaries(t *testing.T) {
	pageSize := os.Getpagesize()
	mem, err := syscall.Mmap(-1, 0, 3*pageSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatalf("mmap failed: %v", err)
	}
	defer func() { _ = syscall.Munmap(mem) }()
	if err := syscall.Mprotect(mem[:pageSize], syscall.PROT_NONE); err != nil {
		t.Fatalf("mprotect left guard failed: %v", err)
	}
	if err := syscall.Mprotect(mem[2*pageSize:], syscall.PROT_NONE); err != nil {
		t.Fatalf("mprotect right guard failed: %v", err)
	}
	rw := mem[pageSize : 2*pageSize]

	// Build a 192-byte clean prefix + closing quote, an escaped string across
	// short/bulk boundaries, and an unterminated EOF payload.
	cleanBase := make([]byte, 192)
	for i := range cleanBase {
		cleanBase[i] = byte('a' + (i % 26))
	}
	checkOne := func(label string, buf []byte) {
		n := len(buf)
		ptr := unsafe.SliceData(buf)
		e1, esc1, ep1, err1 := scanStringRelaxed(ptr, n)
		e2, esc2, ep2, err2 := scanStringRelaxedScalar(ptr, n)
		if e1 != e2 || esc1 != esc2 || ep1 != ep2 || err1 != err2 {
			t.Fatalf("%s scanStringRelaxed n=%d: got=(%d,%d,%d,%v) want=(%d,%d,%d,%v)",
				label, n, e1, esc1, ep1, err1, e2, esc2, ep2, err2)
		}
		checkScanStringBlock(t, ptr, n, 0)
	}

	// 1. Clean closing quote at every position 0..192, flush against BOTH left & right guard pages,
	//    plus unaligned start offsets 0..7 so bulk alignment & all 4 bulk words are hit.
	for n := 1; n <= 192; n++ {
		rightSlice := rw[pageSize-n : pageSize]
		copy(rightSlice, cleanBase[:n])
		rightSlice[n-1] = '"'
		checkOne("right_clean_quote", rightSlice)

		// Unterminated EOF at length n against right guard page
		rightSlice[n-1] = 'z'
		checkOne("right_clean_eof", rightSlice)

		// Left guard page with quote at end and with quote at every offset mod 32
		leftSlice := rw[:n]
		copy(leftSlice, cleanBase[:n])
		leftSlice[n-1] = '"'
		checkOne("left_clean_quote", leftSlice)
		leftSlice[n-1] = 'z'
		checkOne("left_clean_eof", leftSlice)
	}

	// 2. Escapes at every position across short (0..40) and bulk (40..120) boundaries,
	//    including trailing lone backslash and \" before closing quote.
	escPatterns := []string{
		`\"` + string(cleanBase[:120]) + `"`,
		string(cleanBase[:43]) + `\"` + string(cleanBase[:47]) + `"`,
		string(cleanBase[:64]) + `\\"` + string(cleanBase[:32]) + `"`,
		string(cleanBase[:71]) + `\n\t\"\\` + string(cleanBase[:55]) + `"`,
		string(cleanBase[:95]) + `\`,
	}
	for _, pat := range escPatterns {
		for n := 1; n <= len(pat); n++ {
			rightSlice := rw[pageSize-n : pageSize]
			copy(rightSlice, pat[:n])
			checkOne("right_esc", rightSlice)

			leftSlice := rw[:n]
			copy(leftSlice, pat[:n])
			checkOne("left_esc", leftSlice)
		}
	}
}
