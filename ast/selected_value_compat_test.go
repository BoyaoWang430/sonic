//go:build ((!amd64 && !arm64) || go1.28 || !go1.17 || (arm64 && !go1.20)) && (!riscv64 || !go1.26 || go1.28 || !riscv64.rva22u64)

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
package ast

import (
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// Compare fallback cursor/error behavior without imposing native or encoding/json
// validation rules on inputs intentionally accepted by the scalar scanners.
func TestSkipSelectedValueParserCursor(t *testing.T) {
	for _, body := range selectedValueCorpus() {
		for _, space := range []string{"", " \t\r\n"} {
			src := "prefix" + space + body
			const pos = 6
			for _, fast := range []bool{false, true} {
				wantEnd, wantStart := selectedReferenceValue(src, pos)
				if fast {
					wantEnd, wantStart = selectedReferenceFast(src, pos)
				}
				p := NewParserObj(src)
				p.p = pos
				var start int
				var err types.ParsingError
				if fast {
					start, err = p.skipFast()
				} else {
					start, err = p.skip()
				}
				if wantEnd < 0 {
					if err != types.ParsingError(-wantEnd) || start != pos || p.p != pos {
						t.Fatalf("failed cursor fast=%v src=%q got=(%d,%d,%v) want=(%d,%d,%v)", fast, src, start, p.p, err, pos, pos, -wantEnd)
					}
				} else if err != 0 || start != wantStart || p.p != wantEnd {
					t.Fatalf("success cursor fast=%v src=%q got=(%d,%d,%v) want=(%d,%d,0)", fast, src, start, p.p, err, wantStart, wantEnd)
				}
			}
		}
	}
}
func TestGetSelectedValuePermissive(t *testing.T) {
	const prefix = `{"target":`
	for _, body := range selectedValueCorpus() {
		for _, tail := range []string{"", `,"after":0}`} {
			// No automatic closing delimiter is appended: EOF placement is observable.
			src := prefix + " \t" + body + tail
			for _, validate := range []bool{false, true} {
				wantEnd, wantStart := selectedReferenceFast(src, len(prefix))
				if validate {
					wantEnd, wantStart = selectedReferenceValue(src, len(prefix))
				}
				p := NewParserObj(src)
				start, code := p.getByPath(validate, "target")
				searcher := NewSearcher(src)
				searcher.ValidateJSON = validate
				node, err := searcher.GetByPathCopy("target")
				if wantEnd < 0 {
					if code != types.ParsingError(-wantEnd) || start != len(prefix) || p.p != len(prefix) || err == nil || searcher.parser.p != len(prefix) {
						t.Fatalf("query failure validate=%v src=%q got=(start=%d,pos=%d,code=%v,err=%v) want code=%v,pos=%d", validate, src, start, p.p, code, err, -wantEnd, len(prefix))
					}
				} else {
					if code != 0 || start != wantStart || p.p != wantEnd || err != nil {
						t.Fatalf("query success validate=%v src=%q got=(%d,%d,%v,%v) want=(%d,%d)", validate, src, start, p.p, code, err, wantStart, wantEnd)
					}
					raw, rawErr := node.Raw()
					if rawErr != nil || raw != src[wantStart:wantEnd] {
						t.Fatalf("raw mismatch: %q, %v", raw, rawErr)
					}
				}
			}
		}
	}
	// A complete selected value does not validate unrelated trailing document bytes.
	src := prefix + `[1,2]} trailing junk`
	node, err := NewSearcher(src).GetByPathCopy("target")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := node.Raw()
	if raw != `[1,2]` {
		t.Fatal(raw)
	}
}
