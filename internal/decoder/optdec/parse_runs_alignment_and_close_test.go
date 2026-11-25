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
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/jsonscan"
)

// Use unpadded input at every byte alignment without relying on the pool's
// allocation alignment (CL12 read-only zero-copy window contract).
func cParityRunsTuneParser(src string, offset, capacity int, options uint64) *Parser {
	storage := make([]byte, offset+len(src))
	copy(storage[offset:], src)
	aligned := unsafe.String(unsafe.SliceData(storage[offset:]), len(src))
	if len(src) == 0 {
		aligned = ""
	}
	p := &Parser{
		ScanContext: ScanContext{
			options: options,
			isEface: true,
		},
		Document: Document{Json: aligned, nodes: make([]node, capacity)},
	}
	p.pos, p.end = 0, len(src)
	p.nbuf.init(p.nodes)
	return p
}

// Compare cursor, escape status, partial node and all decoded string bytes
// across all 32 byte alignments and prefix lengths between hardware LexBatch,
// scalar LexBatch, and encoding/json (where RFC 8259 strict), including
// strict left-to-right control-character vs escape error precedence.
func TestCParityRunsStringProbeBoundary(t *testing.T) {
	suffixes := []string{
		`"`, `中🚀"`, `\n"`, `\u4e2d\ud800\u0041"`, `\q"`, `\u00xx"`,
		"\x01\"", "\x01" + strings.Repeat("b", 15) + "\\n\"", "\\n\x01\"",
		`\n` + strings.Repeat("b", 97) + `\t"`,
		`\na\q` + "\x01\"", `\n\q` + "\x01\"",
		`\ud800\u00xx"`,
		`\u0022\u005c\u0000` + strings.Repeat("b", 97) + `\n"`,
	}
	lengths := make([]int, 34)
	for i := range lengths {
		lengths[i] = i
	}
	lengths = append(lengths, 63, 64, 65, 127, 128, 191, 192, 193, 255, 256, 257, 320)
	for _, n := range lengths {
		for _, suffix := range suffixes {
			body := strings.Repeat("a", n) + suffix
			for _, options := range []uint64{0, uint64(OptionValidateString)} {
				for _, priorEscaped := range []bool{false, true} {
					prefix := `["seed","`
					if priorEscaped {
						prefix = `["s\n","`
					}
					src := prefix + body + "]"

					ref := cParityRunsTuneParser(src, 0, 8, options)
					wantCode := parseRunsGrowingMode(ref, true)
					wantPos := ref.Pos()
					wantUsed := ref.nbuf.nlen
					wantNode := ref.nodes[2]
					var wantUnesc []byte
					if wantCode == SONIC_OK && (wantNode.typ&TypeMask) == KStringEscaped {
						wantUnesc = append([]byte(nil), ref.bytesEscaped(len(prefix), wantNode.val)...)
					}

					// Cross-check strict OptionValidateString valid/invalid decisions against encoding/json.
					if (options & uint64(OptionValidateString)) != 0 {
						var stdArr []string
						stdErr := json.Unmarshal([]byte(src), &stdArr)
						if (wantCode == SONIC_OK) != (stdErr == nil) {
							t.Fatalf("prefix=%d suffix=%q: Sonic code=%d vs stdlib err=%v", n, suffix, wantCode, stdErr)
						}
						if wantCode == SONIC_OK {
							var gotStr string
							if (wantNode.typ & TypeMask) == KStringEscaped {
								uoff, ulen, ok := jsonscan.UnpackEscapedSpan(wantNode.val)
								if !ok {
									t.Fatalf("invalid packed escaped span: %#x", wantNode.val)
								}
								gotStr = string(ref.unesc[uoff : uoff+ulen])
							} else {
								gotStr = ref.strRefCommon(len(prefix), int(wantNode.val))
							}
							if gotStr != stdArr[1] {
								t.Fatalf("prefix=%d suffix=%q: decoded %q != stdlib %q", n, suffix, gotStr, stdArr[1])
							}
						}
					}

					for offset := 0; offset < 32; offset++ {
						p := cParityRunsTuneParser(src, offset, 8, options)
						code := p.parse()
						used := p.nbuf.nlen
						nodeOK := wantCode != SONIC_OK || p.nodes[2] == wantNode
						if wantCode != SONIC_OK && p.nodes[2] != (node{}) {
							nodeOK = false
						}
						unescOK := true
						if wantCode == SONIC_OK && (wantNode.typ&TypeMask) == KStringEscaped {
							unescOK = bytes.Equal(p.bytesEscaped(len(prefix), p.nodes[2].val), wantUnesc)
						}
						if code != wantCode || p.Pos() != wantPos || used != wantUsed || !nodeOK || !unescOK ||
							p.Json[len(prefix):len(src)] != src[len(prefix):len(src)] {
							t.Fatalf("prefix=%d offset=%d options=%d priorEscaped=%v suffix=%q: code=%d pos=%d used=%d node=%+v unescOK=%v, want %d/%d/%d/%+v",
								n, offset, options, priorEscaped, suffix, code, p.Pos(), used, p.nodes[2], unescOK, wantCode, wantPos, wantUsed, wantNode)
						}
					}
				}
			}
		}
	}
}

func cParityRunsTuneStats(value interface{}, depth uint32, stat *jsonStat) {
	switch v := value.(type) {
	case map[string]interface{}:
		stat.object++
		stat.object_keys += uint32(len(v))
		depth++
		if depth > stat.max_depth {
			stat.max_depth = depth
		}
		for _, child := range v {
			cParityRunsTuneStats(child, depth, stat)
		}
	case []interface{}:
		stat.array++
		stat.array_elems += uint32(len(v))
		depth++
		if depth > stat.max_depth {
			stat.max_depth = depth
		}
		for _, child := range v {
			cParityRunsTuneStats(child, depth, stat)
		}
	case string:
		stat.str++
	case float64:
		stat.number++
	}
}

// Adjacent closers can share a Go continuation, but closing a child must still
// update the parent's element count and restore the right object/array phase.
// Semantic values and aggregate statistics come from encoding/json, not from
// a snapshot of this parser's output.
func TestCParityRunsCloseChainsGrowth(t *testing.T) {
	dec, err := findOrCompile(reflect.TypeOf((*interface{})(nil)).Elem())
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{
		`{"a":[{"b":[1,2]}],"c":[[],{}]}`,
		`[[[{"a":[],"b":{}}]],{"last":[true,false,null]}]`,
		`{"outer":{"child":{"leaf":1}},"tail":"x\u4e2d"}`,
		strings.Repeat("[", 12) + `{"u":"x\u4e2d","n":1.25}` + strings.Repeat("]", 12),
	} {
		for _, n := range []int{0, 1, 2, 63, 64, 65} {
			white := strings.Repeat(" \t\r\n", (n+3)/4)[:n]
			src := strings.NewReplacer("]", white+"]", "}", white+"}").Replace(base)
			var want interface{}
			if err := json.Unmarshal([]byte(src), &want); err != nil {
				t.Fatal(err)
			}
			var stat jsonStat
			cParityRunsTuneStats(want, 0, &stat)
			for capacity := 1; capacity <= 24; capacity++ {
				p := cParityRunsTuneParser(src, 0, capacity, uint64(OptionCopyString|OptionValidateString))
				code := p.parse()
				if code != SONIC_OK || p.Pos() != len(src) || p.nbuf.depth != 0 || p.nbuf.parent != -1 || p.nbuf.stat != stat {
					t.Fatalf("capacity=%d whitespace=%d input=%q: code=%d pos=%d depth=%d parent=%d stat=%+v want=%+v",
						capacity, n, base, code, p.Pos(), p.nbuf.depth, p.nbuf.parent, p.nbuf.stat, stat)
				}
				var got interface{}
				ctx := Context{Doc: p.snapshotDocument(), options: p.options, isEface: p.isEface}
				if err := dec.FromDom(unsafe.Pointer(&got), ctx.Root(), &ctx); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("capacity=%d whitespace=%d input=%q: decoded=%#v want=%#v err=%v", capacity, n, base, got, want, err)
				}
			}
		}
	}
}

func TestCParityRunsCloseChainErrors(t *testing.T) {
	for _, tc := range []struct {
		prefix, bad, tail string
		code              ErrorCode
	}{
		{"[[0]", "}", "]", SONIC_EXPECT_ARR_COMMA_OR_END},
		{`{"k":[0]`, "]", "}", SONIC_EXPECT_OBJ_COMMA_OR_END},
		{`[{"k":0}`, "}", "]", SONIC_EXPECT_ARR_COMMA_OR_END},
		{`{"k":{"j":0}`, "]", "}", SONIC_EXPECT_OBJ_COMMA_OR_END},
		{"[[0]", "\x00", "]", SONIC_EXPECT_ARR_COMMA_OR_END},
	} {
		for _, n := range []int{0, 1, 2, 63, 64, 65} {
			white := strings.Repeat(" \t\r\n", (n+3)/4)[:n]
			src := tc.prefix + white + tc.bad + tc.tail
			for capacity := 1; capacity <= 24; capacity++ {
				p := cParityRunsTuneParser(src, 0, capacity, 0)
				code := p.parse()
				// C's enclosing-container check consumes the unexpected byte.
				if code != tc.code || p.Pos() != len(tc.prefix)+len(white)+1 {
					t.Fatalf("capacity=%d input=%q: code=%d pos=%d want=%d/%d", capacity, src, code, p.Pos(), tc.code, len(tc.prefix)+len(white)+1)
				}
			}
		}
	}
}

// CL05 behavioral migration: container overflow is detected immediately upon
// encountering the (MAX_RECURSE+1)-th opening '['/'{' without publishing an
// overflow node onto the tape or waiting for a closing bracket/capacity check.
func TestCParityRunsCloseChainDepth(t *testing.T) {
	for _, depth := range []int{4096, 4097} {
		for _, closed := range []bool{true, false} {
			if depth == 4096 && !closed {
				continue
			}
			src := strings.Repeat("[", depth) + "0"
			if closed {
				src += strings.Repeat("]", depth)
			}
			for _, forceScalar := range []bool{false, true} {
				for _, capacity := range []int{1, 2, 8, 24, 4096, depth + 2} {
					p := cParityRunsTuneParser(src, 0, capacity, 0)
					code := parseRunsGrowingMode(p, forceScalar)
					if p.nbuf.stat.max_depth != uint32(depth) {
						t.Fatalf("depth=%d closed=%v scalar=%v capacity=%d max_depth=%d", depth, closed, forceScalar, capacity, p.nbuf.stat.max_depth)
					}
					if depth == 4096 {
						if code != SONIC_OK || p.Pos() != len(src) || p.nbuf.depth != 0 || p.nbuf.parent != -1 || p.nbuf.stat.array != uint32(depth) {
							t.Fatalf("depth=%d scalar=%v capacity=%d code=%d pos=%d state=%+v", depth, forceScalar, capacity, code, p.Pos(), p.nbuf)
						}
					} else {
						// Overflow stops immediately right after the 4097th '[' (at tokPos=4096 -> Pos()=4097, SyntaxError.Pos=4096)
						// and publishes only the 4096 valid outer container nodes.
						if code != SONIC_STACK_OVERFLOW || p.Pos() != 4097 || p.nbuf.depth != 4096 || p.nbuf.nlen != 4096 {
							t.Fatalf("depth=%d closed=%v scalar=%v capacity=%d code=%d pos=%d state=%+v", depth, closed, forceScalar, capacity, code, p.Pos(), p.nbuf)
						}
						serr := p.fixError(code).(SyntaxError)
						if serr.Pos != 4096 {
							t.Fatalf("expected SyntaxError.Pos=4096, got %d", serr.Pos)
						}
					}
				}
			}
		}
	}
}
