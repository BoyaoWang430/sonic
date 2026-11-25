//go:build !sonic_compat && (linux || darwin) && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package optdec

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Place unpadded read-only JSON inputs directly flush against inaccessible
// PROT_NONE guard pages across both <=256B (tailBuf) and >256B (direct source
// + safeLimit handoff) regimes, proving zero out-of-bounds reads and zero
// source mutation.
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
	for prefix := 0; prefix < 16; prefix += 5 {
		for _, length := range []int{0, 1, 7, 8, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 191, 192, 193, 255, 256, 257, 320, 512} {
			for _, suffix := range []string{`"`, ``, `\n"`, `\u4e2d"`, `\ud834\udd1e"`, `\u`, `\x"`, "\x01\""} {
				source := strings.Repeat(" ", prefix) + `"` + strings.Repeat("a", length) + suffix
				for _, options := range []uint64{0, uint64(OptionValidateString)} {
					want := cParityRunsTuneParser(source, 0, 8, options)
					wantCode := want.parse()
					wantUsed := want.nbuf.nlen
					size := len(source)
					for _, edge := range []int{0, len(page) - size} {
						input := page[edge : edge+size : edge+size]
						copy(input, source)
						var guarded string
						if size > 0 {
							guarded = unsafe.String(&input[0], size)
						}
						for _, forceScalar := range []bool{false, true} {
							p := newParser(guarded, 0, options)
							p.isEface = true
							p.nodes = make([]node, 8)
							p.nbuf.init(p.nodes)
							var code ErrorCode
							if forceScalar {
								code = parseRunsGrowingMode(p, true)
							} else {
								code = p.parse()
							}
							used := p.nbuf.nlen
							if code != wantCode || p.Pos() != want.Pos() || used != wantUsed || !bytes.Equal(input, []byte(source)) {
								t.Fatalf("scalar=%v prefix=%d length=%d suffix=%q options=%d edge=%d: code/pos/used=(%d,%d,%d), want=(%d,%d,%d)",
									forceScalar, prefix, length, suffix, options, edge, code, p.Pos(), used, wantCode, want.Pos(), wantUsed)
							}
							for i := 0; i < used; i++ {
								if p.nodes[i] != want.nodes[i] {
									t.Fatalf("scalar=%v source=%q edge=%d node=%d: got=%+v want=%+v", forceScalar, source, edge, i, p.nodes[i], want.nodes[i])
								}
							}
							if !bytes.Equal(p.unesc, want.unesc) {
								t.Fatalf("scalar=%v source=%q unesc mismatch", forceScalar, source)
							}
						}
					}
				}
			}
		}
	}

	// Also verify structural, numeric, literal, and whitespace tokens straddling safeLimit
	// flush against the trailing PROT_NONE guard page.
	tailCorpus := []string{
		strings.Repeat(" ", 280) + `{"a":1,"b":[true,false,null,0.000000000000000000000000001,1234567890123456789012345]}`,
		`[` + strings.Repeat("1,", 150) + `true]`,
		`[` + strings.Repeat("[", 80) + "0" + strings.Repeat("]", 80) + `]`,
		strings.Repeat(" ", 300),
		strings.Repeat(" ", 298) + "tr",
		strings.Repeat(" ", 298) + "1.",
		strings.Repeat(" ", 295) + `{"k"`,
		`{"big":0.` + strings.Repeat("0", 300) + `125}`,
	}
	for _, source := range tailCorpus {
		size := len(source)
		edge := len(page) - size
		input := page[edge : edge+size : edge+size]
		copy(input, source)
		guarded := unsafe.String(&input[0], size)

		pAsm := newParser(guarded, 0, 0)
		codeAsm := pAsm.parse()
		pSca := newParser(guarded, 0, 0)
		codeSca := parseRunsGrowingMode(pSca, true)
		if codeAsm != codeSca || pAsm.Pos() != pSca.Pos() || pAsm.nbuf.nlen != pSca.nbuf.nlen {
			t.Fatalf("guard tail corpus=%q: asm=(%d,%d,%d) scalar=(%d,%d,%d)",
				source, codeAsm, pAsm.Pos(), pAsm.nbuf.nlen, codeSca, pSca.Pos(), pSca.nbuf.nlen)
		}
		pAsm.free()
		pSca.free()
	}
}
