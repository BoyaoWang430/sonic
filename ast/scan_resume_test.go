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
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestScanResumeEscapeRuns(t *testing.T) {
	// Every lane in two adjacent words, with odd/even runs spanning both
	// possible widths. A quote after an odd run is escaped; after an even
	// run it must terminate the string immediately, regardless of its tail.
	for leading := 0; leading < 32; leading++ {
		for run := 1; run <= 17; run++ {
			for _, tail := range []int{0, 1, 7, 8, 15, 16, 31, 32} {
				body := `"` + strings.Repeat("a", leading) + strings.Repeat(`\`, run) +
					`"` + strings.Repeat("b", tail) + `"`
				for _, offset := range []int{0, 1, 7, 8, 15} {
					prefix := strings.Repeat("p", offset)
					checkSkipString(t, prefix+body+`\"after`, offset)
					for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
						src := prefix + string(pair[0]) + body + string(pair[1]) + `"\"after`
						checkSkipPair(t, src, offset, pair[0], pair[1])
					}
				}
			}
		}
	}
}

func TestScanResumeFirstClose(t *testing.T) {
	for length := 0; length < 32; length++ {
		quoted := `"` + strings.Repeat("a", length) + `"`
		for offset := 0; offset < 16; offset++ {
			prefix := strings.Repeat("p", offset)
			// A later backslash can be in the same 16-byte load as the
			// closing quote. It must not become the string's first escape.
			for _, suffix := range []string{"", `\`, `\"`, `"\\`, strings.Repeat("b", 7) + `\n`, strings.Repeat("b", 15) + `\"`} {
				src := prefix + quoted + suffix
				end, ep := skipString(src, offset)
				if end != offset+len(quoted) || ep != -1 {
					t.Fatalf("first close in %q at %d: got (%d,%d), want (%d,-1)", src, offset, end, ep, offset+len(quoted))
				}
			}
		}
	}
}

func TestScanResumeLongTails(t *testing.T) {
	for _, leading := range []int{0, 7, 8, 15, 16, 31} {
		for _, tail := range []int{0, 1, 7, 8, 15, 16, 31, 32, 255, 256, 4096} {
			text := strings.Repeat("a", leading) + `\n` + strings.Repeat("b", tail)
			for _, offset := range []int{0, 7, 15} {
				prefix := strings.Repeat("p", offset)
				quoted := `"` + text + `"`
				src := prefix + quoted + `"\"after`
				checkSkipString(t, src, offset)
				end, ep := skipString(src, offset)
				if end != offset+len(quoted) || ep != offset+1+leading {
					t.Fatalf("leading=%d tail=%d offset=%d: got (%d,%d)", leading, tail, offset, end, ep)
				}
				// A late truncated escape must not return the earlier ep.
				for _, suffix := range []string{"", `\`, `\"`, `\\`} {
					checkSkipString(t, prefix+`"`+text+suffix, offset)
				}
				object := prefix + `{"text":` + quoted + `,"after":[{},1]}"\"after`
				checkSkipPair(t, object, offset, '{', '}')
			}
		}
	}
	// Once a string closes, subsequent escape runs and quote parity must not
	// affect its end. Dense runs also cover repeated attempts to resume.
	for _, text := range []string{
		strings.Repeat(`a\"b\\c\n`, 128),
		strings.Repeat(strings.Repeat("a", 31)+`\n`, 32),
		`\n\t\\` + strings.Repeat("a", 4096),
	} {
		for offset := 0; offset < 16; offset++ {
			prefix := strings.Repeat("p", offset)
			quoted := `"` + text + `"`
			checkSkipString(t, prefix+quoted+`\"unfinished`, offset)
			src := prefix + `[` + quoted + `,"short",{"n":1}]` + `\"unfinished`
			checkSkipPair(t, src, offset, '[', ']')
		}
	}
}

func TestScanResumeDenseToPlain(t *testing.T) {
	// Cross each recovery-window boundary before the escape-free tail. Both
	// a complete value and an escape truncated at EOF must keep scalar semantics.
	for _, runs := range []int{1, 3, 7, 15, 31, 63, 127} {
		for _, tail := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 63, 64, 127, 128, 129, 256} {
			text := strings.Repeat(`a\"b\\c\n`, runs) + strings.Repeat("x", tail)
			for _, offset := range []int{0, 1, 7, 15} {
				prefix := strings.Repeat("p", offset)
				for _, suffix := range []string{`"`, `\"`, `\\"`, `\`, ""} {
					quoted := `"` + text + suffix
					checkSkipString(t, prefix+quoted, offset)
					for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
						src := prefix + string(pair[0]) + quoted + string(pair[1]) + `"\"after`
						checkSkipPair(t, src, offset, pair[0], pair[1])
					}
				}
			}
		}
	}
}

func TestScanResumeStringProbeQuoteLanes(t *testing.T) {
	for _, first := range []struct {
		text   string
		escape int
	}{
		{`\n` + strings.Repeat("a", 6), 0},
		{strings.Repeat("a", 7) + `\n`, 7},
	} {
		// The first escape starts recovery. Its scalar window consumes either
		// eight bytes, or nine when an escape straddles the window boundary.
		for _, offset := range []int{0, 7} {
			for lane := 0; lane < 8; lane++ {
				for _, alphabet := range []string{"a", "\x00\x80\xff"} {
					beforeQuote := strings.Repeat(alphabet, lane)[:lane]
					// The next probe is escape-free and contains the first close.
					// '#' immediately after it can inherit a false mask bit. A
					// later quote and escape must not change the end or first ep.
					src := strings.Repeat("p", offset) + `"` + first.text + beforeQuote +
						`"#` + strings.Repeat("z", 8) + `"\"after`
					wantEnd, wantEP := referenceSkipString(src, offset)
					if wantEnd != offset+1+len(first.text)+lane+1 || wantEP != offset+1+first.escape {
						t.Fatalf("incorrect probe fixture: %q at %d: (%d,%d)", src, offset, wantEnd, wantEP)
					}
					end, ep := skipString(src, offset)
					if end != wantEnd || ep != wantEP {
						t.Fatalf("skipString(%q,%d) = (%d,%d), want (%d,%d)", src, offset, end, ep, wantEnd, wantEP)
					}
				}
			}
		}
	}
}

func TestSkipPairRecoveryProbeLanes(t *testing.T) {
	for _, tc := range []struct {
		left, right byte
		prefix      string
	}{
		{'{', '}', `{"k":"\n`},
		{'[', ']', `["\n`},
	} {
		for lane := 0; lane < 8; lane++ {
			// The probe after the escaped string closes contains the first
			// balanced delimiter. Later events cannot change that endpoint.
			src := tc.prefix + `"` + strings.Repeat(" ", lane) + string(tc.right) + `"#[]{}"#[]{}"`
			checkSkipPair(t, src, 0, tc.left, tc.right)

			// Consume the escape and six ordinary bytes in the first scalar
			// window. The next, string-internal probe has its first quote in
			// the selected lane. '#' can inherit a false mask bit from it.
			// The scanner permits this non-JSON byte outside the closed string.
			src = tc.prefix + strings.Repeat("a", 6+lane) + `"#` + string(tc.right) + `"#[]{}"#[]{}"`
			checkSkipPair(t, src, 0, tc.left, tc.right)
		}
	}
}

type scanGetWorkload struct {
	name string
	src  string
	want string
}

func scanGetWorkloads() []scanGetWorkload {
	// Model an extra field containing an encoded JSON document, with escaped
	// quotes separating longer ordinary-text spans.
	embedded := `{"body":"` + strings.Repeat("ordinary text ", 128) + `","metadata":{"title":"quoted \"title\"","tags":["one","two"]}}`
	encoded := strconv.Quote(embedded)
	texts := []struct{ name, text string }{
		{"short", "ordinarytext"},
		{"encodedJSON", encoded[1 : len(encoded)-1]},
		{"earlyEscape", `\n` + strings.Repeat("a", 4096)},
		{"sparseEscape", strings.Repeat(strings.Repeat("a", 127)+`\n`, 32)},
		{"denseEscape", strings.Repeat(`a\"b\\c\n`, 128)},
		{"shortEscape", `a\nb\\c\"d`},
	}
	cases := make([]scanGetWorkload, 0, 2*len(texts))
	for _, tc := range texts {
		value := `{"text":"` + tc.text + `","items":[1,2,3]}`
		cases = append(cases,
			scanGetWorkload{
				name: "skipContainer/" + tc.name,
				src:  `{"noise":` + value + `,"target":7}`,
				want: `7`,
			},
			scanGetWorkload{
				name: "returnObject/" + tc.name,
				src:  `{"noise":0,"target":` + value + `}`,
				want: value,
			},
		)
	}
	return cases
}

func TestGetScanWorkloads(t *testing.T) {
	for _, tc := range scanGetWorkloads() {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.src)) {
				t.Fatal("invalid workload JSON")
			}
			for _, validate := range []bool{false, true} {
				searcher := NewSearcher(tc.src)
				searcher.ValidateJSON = validate
				got, err := searcher.GetByPathCopy("target")
				if err != nil {
					t.Fatal(err)
				}
				raw, err := got.Raw()
				if err != nil || raw != tc.want {
					t.Fatalf("validate=%v: Raw() = (%q,%v), want %q", validate, raw, err, tc.want)
				}
				// GetByPath resets the cursor; a preceding query must not
				// change either the selected span or its copied contents.
				if _, err := searcher.GetByPathCopy("noise"); err != nil {
					t.Fatal(err)
				}
				got, err = searcher.GetByPathCopy("target")
				if err != nil {
					t.Fatal(err)
				}
				raw, err = got.Raw()
				if err != nil || raw != tc.want {
					t.Fatalf("second query: Raw() = (%q,%v), want %q", raw, err, tc.want)
				}
			}
		})
	}
}

var scanGetWorkloadSink Node

func BenchmarkGetScanWorkload(b *testing.B) {
	for _, tc := range scanGetWorkloads() {
		b.Run(tc.name, func(b *testing.B) {
			searcher := NewSearcher(tc.src)
			searcher.ValidateJSON = true
			// Check the selected span before timing. The measured operation
			// includes finding, validating and copying the returned node.
			node, err := searcher.GetByPathCopy("target")
			if err != nil {
				b.Fatal(err)
			}
			raw, err := node.Raw()
			if err != nil || raw != tc.want {
				b.Fatalf("Raw() = (%q,%v), want %q", raw, err, tc.want)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				node, err = NewSearcher(tc.src).GetByPathCopy("target")
				if err != nil {
					b.Fatal(err)
				}
			}
			scanGetWorkloadSink = node
		})
	}
}
