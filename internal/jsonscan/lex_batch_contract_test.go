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

package jsonscan

import (
	"strings"
	"testing"
	"unsafe"
)

func TestLexBatchLayoutAndDisjointSafety(t *testing.T) {
	if unsafe.Sizeof(LexBlockMask{}) != 24 {
		t.Fatalf("LexBlockMask size = %d, want 24", unsafe.Sizeof(LexBlockMask{}))
	}
	if unsafe.Sizeof(LexBlockCarry{}) != 24 {
		t.Fatalf("LexBlockCarry size = %d, want 24", unsafe.Sizeof(LexBlockCarry{}))
	}
	if unsafe.Sizeof(LexBatchOut{}) != 16 {
		t.Fatalf("LexBatchOut size = %d, want 16", unsafe.Sizeof(LexBatchOut{}))
	}

	// Verify CheckedWindowSpan overflow and range checks.
	if s, e, ok := CheckedWindowSpan(100, 4, 9, 120); !ok || s != 104 || e != 109 {
		t.Fatalf("CheckedWindowSpan valid failed: (%d,%d,%v)", s, e, ok)
	}
	if _, _, ok := CheckedWindowSpan(100, 9, 4, 120); ok {
		t.Fatal("CheckedWindowSpan accepted start > end")
	}
	if _, _, ok := CheckedWindowSpan(100, 0, 21, 120); ok {
		t.Fatal("CheckedWindowSpan accepted end beyond srcLen")
	}

	// Verify overlapping window & block slice panics cleanly.
	var blkBuf [2]LexBlockMask
	rawAlias := unsafe.String((*byte)(unsafe.Pointer(&blkBuf[0])), 32)
	didPanic := false
	func() {
		defer func() {
			if recover() != nil {
				didPanic = true
			}
		}()
		var carry LexBlockCarry
		_, _ = LexBatchWindow(rawAlias, 0, blkBuf[:], LexFlagNone, &carry)
	}()
	if !didPanic {
		t.Fatal("expected LexBatchWindow to reject overlapping window and block buffer")
	}
}

func TestLexBatchContextFreeAndCarryEquivalence(t *testing.T) {
	// LexBatch classifies purely lexical 64B blocks and NEVER invents structural errors:
	// `{]`, `[}`, `{,}`, `[1,]`, `::,,` all produce exact structural EventMask bits.
	for _, puncDoc := range []string{`{]`, `[}`, `{,}`, `[1,]`, `::,,`} {
		var blk [2]LexBlockMask
		var carry LexBlockCarry
		_, out := LexBatchWindow(puncDoc, 0, blk[:], LexFlagRejectControl, &carry)
		if out.BlocksProduced != 1 || int(out.BytesScanned) != len(puncDoc) {
			t.Fatalf("punc %q out = %+v", puncDoc, out)
		}
		wantMask := (uint64(1) << uint(len(puncDoc))) - 1
		if blk[0].EventMask != wantMask || blk[0].CloseQuote != 0 || blk[0].InsideSpecial != 0 {
			t.Fatalf("punc %q blk[0] = %+v, want EventMask=0x%x", puncDoc, blk[0], wantMask)
		}
	}

	// Batch capacities 1, 2, 3, 7, 16 produce bit-identical LexBlockMask sequences
	// across multi-block strings, trailing backslashes, numbers, and UTF-8!
	doc := `  {"id": 9999999999999999999, "neg": -9223372036854775808, "z": -0, "ok": true, "no": false, "nil": null, ` +
		`"esc": "` + strings.Repeat("x", 55) + `\"\\` + strings.Repeat("y", 70) + `", "arr": [0, 1, "plain", "<b>", "中文"]}   `

	collectAll := func(capBlk int, forceScalar bool) ([]LexBlockMask, LexBlockCarry) {
		scratch := make([]LexBlockMask, capBlk)
		var out []LexBlockMask
		var carry LexBlockCarry
		pos := 0
		for pos < len(doc) {
			_, r := LexBatchWindowMode(doc, pos, scratch, LexFlagRejectControl, &carry, forceScalar)
			if r.BlocksProduced == 0 || r.BytesScanned == 0 {
				t.Fatalf("cap=%d made zero progress at pos=%d", capBlk, pos)
			}
			out = append(out, scratch[:r.BlocksProduced]...)
			pos += int(r.BytesScanned)
		}
		return out, carry
	}

	ref, refCarry := collectAll(16, true)
	for _, scalar := range []bool{false, true} {
		for _, capBlk := range []int{1, 2, 3, 7, 16} {
			got, gotCarry := collectAll(capBlk, scalar)
			if gotCarry != refCarry || len(got) != len(ref) {
				t.Fatalf("scalar=%v cap=%d blocks=%d carry=%+v, want %d %+v",
					scalar, capBlk, len(got), gotCarry, len(ref), refCarry)
			}
			for i := range ref {
				if got[i] != ref[i] {
					t.Fatalf("scalar=%v cap=%d blk[%d] = %+v, want %+v", scalar, capBlk, i, got[i], ref[i])
				}
			}
		}
	}

	// Capacity 0 returns zero blocks cleanly without panic.
	var c0 LexBlockCarry
	_, r0 := LexBatchWindow(`[1,2,3]`, 0, nil, LexFlagRejectControl, &c0)
	if r0.BlocksProduced != 0 || r0.BytesScanned != 0 {
		t.Fatalf("cap=0 out = %+v", r0)
	}
}

func TestLexBatchLargeTokenAndVectorBoundaries(t *testing.T) {
	// Sweep vector/block boundary widths AND >64 KiB / >4 MiB strings & numbers through Machine!
	for _, n := range []int{0, 1, 7, 8, 9, 15, 16, 17, 29, 31, 32, 33, 47, 48, 49, 63, 64, 65, 96<<10 + 13} {
		s := strings.Repeat(" ", n%128) + `"` + strings.Repeat("a", n) + `"`
		var m Machine
		m.Nodes = make([]Node, 8)
		m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
		res := m.Step(s, true)
		if res.Reason != StopDone || m.NodeLen != 1 || int(m.Nodes[0].Val) != n {
			t.Fatalf("n=%d res=%+v nodeLen=%d val=%d", n, res, m.NodeLen, m.Nodes[0].Val)
		}
	}

	// > 4 MiB plain string through ModeEmit and ModeValidateOnly (zero Unesc growth!).
	giant4M := `{"msg":"` + strings.Repeat("x", (4<<20)+37) + `"}`
	var mEmit Machine
	mEmit.Nodes = make([]Node, 8)
	mEmit.Reset(StrictProfile(true, false, true, false, true, true), ModeEmit)
	rEmit := mEmit.Step(giant4M, true)
	if rEmit.Reason != StopDone || mEmit.NodeLen != 3 || len(mEmit.Unesc) != 0 {
		t.Fatalf("giant4M ModeEmit failed: res=%+v nodes=%d unesc=%d", rEmit, mEmit.NodeLen, len(mEmit.Unesc))
	}
}
