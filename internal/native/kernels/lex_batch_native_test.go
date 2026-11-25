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
	"bufio"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestLexBatchHeaderOffsets(t *testing.T) {
	f, err := os.Open("lex_batch_offsets.h")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	defs := make(map[string]int64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "#define ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		v, err := strconv.ParseInt(fields[2], 0, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		defs[fields[1]] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	want := map[string]int64{
		"LEX_BLK_SIZE":          types.LexBlockMaskSize,
		"LEX_BLK_EVENT_MASK":    types.LexBlockOffEvent,
		"LEX_BLK_CLOSE_QUOTE":   types.LexBlockOffCloseQuote,
		"LEX_BLK_INSIDE_SPEC":   types.LexBlockOffInsideSpec,
		"LEX_CARRY_SIZE":        types.LexCarrySize,
		"LEX_CARRY_ODD_SLASH":   types.LexCarryOffOddSlash,
		"LEX_CARRY_IN_STRING":   types.LexCarryOffInString,
		"LEX_CARRY_PREV_SCALAR": types.LexCarryOffPrevScalar,
		"LEX_OUT_SIZE":          types.LexOutSize,
		"LEX_OUT_BLOCKS":        types.LexOutOffBlocks,
		"LEX_OUT_BYTES":         types.LexOutOffBytes,
		"LEX_FLAG_REJECT_CTRL":  int64(types.LexFlagRejectControl),
	}
	for k, wv := range want {
		gv, ok := defs[k]
		if !ok || gv != wv {
			t.Fatalf("header macro %s = (%d, ok=%v), want %d", k, gv, ok, wv)
		}
	}
}

func assertLexBatchHardwareEqualsScalar(t *testing.T, doc string, pos int, capBlk int, flags types.LexFlags, initCarry types.LexBlockCarry) {
	t.Helper()
	hwDst := make([]types.LexBlockMask, capBlk)
	scDst := make([]types.LexBlockMask, capBlk)
	hwCarry := initCarry
	scCarry := initCarry

	cur := pos
	for {
		hwOut := LexBatchMode(doc, cur, hwDst, flags, &hwCarry, false)
		scOut := LexBatchMode(doc, cur, scDst, flags, &scCarry, true)
		if hwOut != scOut || hwCarry != scCarry {
			t.Fatalf("LexBatch out/carry mismatch (pos=%d cur=%d cap=%d flags=%d len=%d):\n  hw=(%+v, %+v)\n  sc=(%+v, %+v)\n  doc=%q",
				pos, cur, capBlk, flags, len(doc), hwOut, hwCarry, scOut, scCarry, doc)
		}
		for b := 0; b < int(hwOut.BlocksProduced); b++ {
			if hwDst[b] != scDst[b] {
				t.Fatalf("LexBatch block[%d] mismatch (pos=%d cur=%d cap=%d flags=%d len=%d):\n  hw=%+v\n  sc=%+v\n  chunk=%q",
					b, pos, cur, capBlk, flags, len(doc), hwDst[b], scDst[b], doc[cur:])
			}
		}
		if hwOut.BytesScanned == 0 || capBlk == 0 {
			break
		}
		cur += int(hwOut.BytesScanned)
		if cur >= len(doc) {
			break
		}
	}
}

func TestLexBatchAll256ByteClassificationParity(t *testing.T) {
	// Exhaustively verify all 256 byte values (0x00..0xff) both outside strings
	// and inside strings so LUT / SWAR / SIMD nibble tricks have zero false positives!
	raw := make([]byte, 256)
	for i := 0; i < 256; i++ {
		raw[i] = byte(i)
	}
	s := string(raw)
	for _, fl := range []types.LexFlags{types.LexFlagNone, types.LexFlagRejectControl} {
		for _, inStr := range []uint64{0, ^uint64(0)} {
			for _, oddSl := range []uint64{0, 1} {
				for _, prevSc := range []uint64{0, 1} {
					c := types.LexBlockCarry{OddSlash: oddSl, InString: inStr, PrevScalar: prevSc}
					assertLexBatchHardwareEqualsScalar(t, s, 0, 8, fl, c)
				}
			}
		}
	}
}

func TestLexBatchISAParityAndVectorBoundaries(t *testing.T) {
	// Verify overlapping input/output panics cleanly.
	var blkBuf [2]types.LexBlockMask
	alias := unsafe.String((*byte)(unsafe.Pointer(&blkBuf[0])), 32)
	didPanic := false
	func() {
		defer func() {
			if recover() != nil {
				didPanic = true
			}
		}()
		var carry types.LexBlockCarry
		_ = LexBatch(alias, 0, blkBuf[:], types.LexFlagNone, &carry)
	}()
	if !didPanic {
		t.Fatal("expected LexBatch to reject overlapping input and output block buffer")
	}

	corpus := []string{
		"",
		" ",
		"{}",
		strings.Repeat(" ", 65),
		"\t\r\n  {}[],: true false null 0 -0 123 -9223372036854775808 -9223372036854775809 9999999999999999999",
		`{"a" 1, "b":"\q"}`,
		"{\"a\" 1, \"b\":\"\x01\"}",
		"\"\r\n\t\x00\x1f\"",
		`{} "\q"`,
		`{} @@@`,
		`{]`,
		`[1+2, 01, -01, 18446744073709551615, 1.25, 1e5, -x, truX, falsX, nulX]`,
	}
	// Sweep all SIMD / SWAR / block tail widths (0..66, 95, 96, 97, 127, 128, 129)!
	for _, n := range []int{
		0, 1, 2, 3, 4, 7, 8, 9, 14, 15, 16, 17, 29, 31, 32, 33,
		47, 48, 49, 62, 63, 64, 65, 95, 96, 97, 127, 128, 129,
	} {
		p := strings.Repeat("a", n)
		sp := strings.Repeat(" ", n)
		sl := strings.Repeat(`\`, n)
		corpus = append(corpus,
			p,
			sp+`"`+p+`"`,
			sp+`"`+p+`<b>"`,
			sp+`"`+p+`中文"`,
			sp+`"`+sl+`"`,
			sp+`"`+p+"\x01\"",
			sp+`"`+p,
			sp+"12345",
			sp+"true",
		)
	}

	for _, s := range corpus {
		// Unaligned sub-slices (0..15 byte offsets)
		pad := "0123456789abcdef" + s
		for align := 0; align <= 15; align++ {
			win := pad[align:]
			pos := 16 - align
			for _, capBlk := range []int{0, 1, 2, 16} {
				for _, fl := range []types.LexFlags{types.LexFlagNone, types.LexFlagRejectControl} {
					assertLexBatchHardwareEqualsScalar(t, win, pos, capBlk, fl, types.LexBlockCarry{})
				}
			}
		}
		// Also test exact slice len (including < 16B total size!)
		for _, fl := range []types.LexFlags{types.LexFlagNone, types.LexFlagRejectControl} {
			assertLexBatchHardwareEqualsScalar(t, s, 0, 16, fl, types.LexBlockCarry{})
		}
	}

	// Randomized multi-block stress across capacities, carries, and alignments.
	rng := rand.New(rand.NewSource(20261119))
	atoms := []string{
		"{", "}", "[", "]", ":", ",", " ", "   \n\t\r  ",
		`""`, `"abc"`, `"hello_world_1234567890_abcdefghijklmnopqrstuvwxyz_0123456789_long"`,
		`"esc\n\"\\\/end"`, `"trailing_slashes\\\\"`, `"odd_slashes\\\"`, "\"ctrl\x01\r\n\t\"", `"中文🚀<b>&"`,
		"0", "-0", "12345", "12.345", "1e-10", "true", "false", "null", "01", "1+2", "@", "\x1a", "\x0c",
	}
	for iter := 0; iter < 500; iter++ {
		var sb strings.Builder
		k := 1 + rng.Intn(60)
		for j := 0; j < k; j++ {
			sb.WriteString(atoms[rng.Intn(len(atoms))])
		}
		doc := sb.String()
		pos := 0
		if len(doc) > 0 && rng.Intn(3) == 0 {
			pos = rng.Intn(len(doc))
		}
		assertLexBatchHardwareEqualsScalar(t, doc, pos, 1+rng.Intn(16), types.LexFlags(rng.Intn(2)), types.LexBlockCarry{})
	}

	// Exhaustive (size=1..140, pos=0..size) sweep covering every 32B/64B tail subcase.
	const pattern140 = `{"id":123,"k":"v\"\\x","a":[true,false,null,0,-1.5]}   ` +
		"{\"t\":\"\t\r\n\x01\x1f\",\"long_ascii_key_name\":9876543210}   [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15]"
	for sz := 1; sz <= len(pattern140); sz++ {
		sub := pattern140[:sz]
		for p := 0; p <= sz; p += 1 + (p / 8) {
			assertLexBatchHardwareEqualsScalar(t, sub, p, 4, types.LexFlagRejectControl, types.LexBlockCarry{})
			assertLexBatchHardwareEqualsScalar(t, sub, p, 4, types.LexFlagNone, types.LexBlockCarry{})
		}
	}
}

func TestLexBatchAll256ByteParity(t *testing.T) {
	// Verify every byte value 0x00..0xFF at every 64B lane position under both
	// LexFlagNone (VPMAXUB no-control-mask path on AMD64) and LexFlagRejectControl,
	// across non-trivial carry combinations.
	var base [256]byte
	for i := range base {
		base[i] = byte(i)
	}
	carries := []types.LexBlockCarry{
		{},
		{InString: ^uint64(0)},
		{OddSlash: 1},
		{PrevScalar: 1},
		{OddSlash: 1, InString: ^uint64(0), PrevScalar: 1},
	}
	for rot := 0; rot < 64; rot++ {
		buf := make([]byte, 256+rot)
		for i := range buf {
			buf[i] = base[(i+rot)&255]
		}
		src := string(buf)
		for _, flags := range []types.LexFlags{types.LexFlagNone, types.LexFlagRejectControl} {
			for _, carry := range carries {
				assertLexBatchHardwareEqualsScalar(t, src, 0, 16, flags, carry)
			}
		}
	}
}
