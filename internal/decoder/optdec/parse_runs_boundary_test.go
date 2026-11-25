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

package optdec

import (
	"strings"
	"testing"
	"unsafe"
)

// In the Runs kernel protocol, literal scanners (true/false/null) consume the
// first mismatching byte and report SONIC_INVALID_LITERAL without growing the
// tape or writing a partial node past the valid prefix.
func TestCParityRunsLiteralErrorBoundary(t *testing.T) {
	for _, context := range []struct {
		name, prefix, suffix string
		nodes                int
	}{
		{"root", " \t", " ", 1},
		{"array", " [0, ", "]", 3},
		{"object", ` {"ok":0,"bad": `, "}", 5},
	} {
		for _, literal := range []struct {
			name, text string
			kind       uint64
			consumed   int
		}{
			{"true_first", "tXXX", KTrue, 2},
			{"true_last", "truX", KTrue, 4},
			{"false_first", "fXXXX", KFalse, 2},
			{"false_last", "falsX", KFalse, 5},
			{"null_first", "nXXX", KNull, 2},
			{"null_last", "nulX", KNull, 4},
		} {
			t.Run(context.name+"/"+literal.name, func(t *testing.T) {
				src := context.prefix + literal.text + context.suffix
				p := cParityRunsTuneParser(src, 0, context.nodes, 0)
				startPtr, endCap := unsafe.SliceData(p.nodes), p.nbuf.ncap
				wantUntouched := node{typ: ^uint64(0), val: 0x8badf00dcafebabe}
				p.nodes[context.nodes-1] = wantUntouched

				code := p.parse()
				if code != SONIC_INVALID_LITERAL || p.Pos() != len(context.prefix)+literal.consumed {
					t.Fatalf("code=%d pos=%d, want literal error at %d", code, p.Pos(), len(context.prefix)+literal.consumed)
				}
				if unsafe.SliceData(p.nodes) != startPtr || p.nbuf.ncap != endCap || p.backup != nil || cap(p.nodes) != context.nodes {
					t.Fatal("literal error grew or replaced the full tape")
				}
				used := p.nbuf.nlen
				if used != context.nodes-1 {
					t.Fatalf("used=%d, want valid prefix of %d nodes", used, context.nodes-1)
				}
				if got := p.nodes[context.nodes-1]; got != wantUntouched {
					t.Fatalf("slot past valid prefix modified=%+v, want untouched %+v", got, wantUntouched)
				}
				if context.name == "root" {
					if p.nbuf.parent != -1 || p.nbuf.depth != 0 {
						t.Fatalf("root error changed parent=%d depth=%d", p.nbuf.parent, p.nbuf.depth)
					}
				} else if count := uint32(p.nodes[0].val); p.nbuf.parent != 0 || p.nbuf.depth != 1 || count != 1 {
					t.Fatalf("parent=%d depth=%d completed children=%d, want 0/1/1", p.nbuf.parent, p.nbuf.depth, count)
				}
			})
		}
	}
}

func TestZeroCapParserAndStreamSessionBeginReset(t *testing.T) {
	// 1. Bare Parser with zero-cap nodes slice must grow cleanly on parse().
	src := `{"a":1,"b":"ok"}`
	bare := &Parser{
		ScanContext: ScanContext{
			ScanKernelState: ScanKernelState{end: len(src)},
		},
		Document: Document{
			Json: src,
		},
	}
	bare.nbuf.init(nil)
	if code := bare.parse(); code != SONIC_OK {
		t.Fatalf("bare.parse() on zero-cap Parser failed: code=%v", code)
	}
	if bare.nbuf.nlen == 0 || cap(bare.nodes) == 0 {
		t.Fatalf("expected initialized nodes after zero-cap parse, nlen=%d cap=%d", bare.nbuf.nlen, cap(bare.nodes))
	}

	// 2. StreamSession.Begin must clear stale strSlab state in DecodeContext even if a prior run aborted without Reset.
	sess := getStreamSession()
	defer FreeStreamSession(sess)
	_ = sess.ensureParser()
	sess.ctx.strSlab = []byte("stale-slab")
	sess.ctx.strSlabOff = 5
	sess.ctx.strSlabNext = 2048
	sess.Begin(0, false)
	if sess.ctx.strSlab != nil || sess.ctx.strSlabOff != 0 || sess.ctx.strSlabNext != 0 {
		t.Fatalf("StreamSession.Begin left stale strSlab state: len=%d off=%d next=%d", len(sess.ctx.strSlab), sess.ctx.strSlabOff, sess.ctx.strSlabNext)
	}
}

// TestCL12WindowBoundariesAndLongTokens verifies the 4-boundary read-only window
// contract across 15/16/17, 31/32/33, 63/64/65, 255/256/257, 64KiB±1, >100 KiB
// plain/escaped strings, >100 KiB numbers, >100 KiB whitespace runs, and cap=1
// OutputFull tape growth (native == scalar == jsonscan).
func TestCL12WindowBoundariesAndLongTokens(t *testing.T) {
	boundarySizes := []int{15, 16, 17, 31, 32, 33, 63, 64, 65, 191, 192, 193, 255, 256, 257, 65535, 65536, 65537}
	for _, sz := range boundarySizes {
		pad := 0
		if sz > 16 {
			pad = sz - 16
		}
		doc := `{"k":"` + strings.Repeat("v", pad) + `","n":42}`
		if len(doc) > sz && sz >= 2 {
			doc = `"` + strings.Repeat("x", sz-2) + `"`
		}
		for _, capInit := range []int{1, 2, 4096} {
			pAsm := newParser(doc, 0, uint64(OptionValidateString))
			pAsm.isEface = true
			pAsm.nodes = make([]node, capInit)
			pAsm.nbuf.init(pAsm.nodes)
			codeAsm := pAsm.parse()

			pSca := newParser(doc, 0, uint64(OptionValidateString))
			pSca.isEface = true
			pSca.nodes = make([]node, capInit)
			pSca.nbuf.init(pSca.nodes)
			codeSca := parseRunsGrowingMode(pSca, true)

			if codeAsm != SONIC_OK || codeSca != SONIC_OK || pAsm.Pos() != len(doc) || pSca.Pos() != len(doc) || pAsm.nbuf.nlen != pSca.nbuf.nlen {
				t.Fatalf("sz=%d cap=%d: asm=(%v,%d,%d) sca=(%v,%d,%d)",
					sz, capInit, codeAsm, pAsm.Pos(), pAsm.nbuf.nlen, codeSca, pSca.Pos(), pSca.nbuf.nlen)
			}
			for i := 0; i < pAsm.nbuf.nlen; i++ {
				if pAsm.nodes[i] != pSca.nodes[i] {
					t.Fatalf("sz=%d cap=%d node[%d]: asm=%+v sca=%+v", sz, capInit, i, pAsm.nodes[i], pSca.nodes[i])
				}
			}
			pAsm.free()
			pSca.free()
		}
	}

	// >100 KiB plain string, escaped string at tail, >100 KiB whitespace, and >100 KiB decimal number.
	longCases := []string{
		`{"s":"` + strings.Repeat("a", 120<<10) + `","ok":true}`,
		`{"s":"` + strings.Repeat("a", 120<<10) + `\u0041\n","ok":true}`,
		strings.Repeat(" \n\t\r", 30<<10) + `[1, 2, 3]`,
		`{"num":0.` + strings.Repeat("0", 110<<10) + `125,"tail":[false,null]}`,
		`{"dec":9.` + strings.Repeat("9", 110<<10) + `,"tail":1}`,
	}
	for idx, doc := range longCases {
		for _, extraOpt := range []uint64{0, uint64(OptionUseNumber)} {
			opts := uint64(OptionValidateString) | extraOpt
			var outAsm any
			posA := 0
			if err := Decode(&doc, &posA, opts, &outAsm); err != nil {
				t.Fatalf("longCase[%d] opts=%d asm Decode failed: %v", idx, opts, err)
			}
			if _, endS, err := decodeViaScalarRunsPosWithInit[any](doc, opts, nil); err != nil || endS != posA {
				t.Fatalf("longCase[%d] opts=%d scalar Decode failed: err=%v end=%d want=%d", idx, opts, err, endS, posA)
			}
		}
	}
	// >100 KiB integer under OptionUseNumber.
	bigIntDoc := `{"int":` + strings.Repeat("9", 110<<10) + `,"tail":1}`
	var outBig any
	posBig := 0
	if err := Decode(&bigIntDoc, &posBig, uint64(OptionValidateString|OptionUseNumber), &outBig); err != nil || posBig != len(bigIntDoc) {
		t.Fatalf("bigIntDoc asm failed: err=%v pos=%d", err, posBig)
	}
	if _, endBigS, err := decodeViaScalarRunsPosWithInit[any](bigIntDoc, uint64(OptionValidateString|OptionUseNumber), nil); err != nil || endBigS != posBig {
		t.Fatalf("bigIntDoc scalar failed: err=%v end=%d", err, endBigS)
	}
}
