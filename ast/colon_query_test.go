/*
 * Copyright 2021 ByteDance Inc.
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
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

type scanColonSpacingCase struct {
	name, src, key string
}

func scanColonSpacingCases() []scanColonSpacingCase {
	var cases []scanColonSpacingCase
	for _, spacing := range []struct{ name, text string }{
		{"compact", ""},
		{"space1", " "},
		{"space8", strings.Repeat(" \t\r\n", 2)},
		{"space32", strings.Repeat(" \t\r\n", 8)},
	} {
		var src strings.Builder
		src.WriteByte('{')
		for i := 0; i < 64; i++ {
			if i != 0 {
				src.WriteByte(',')
			}
			fmt.Fprintf(&src, `"key%03d"%s:7`, i, spacing.text)
		}
		src.WriteByte('}')
		for _, target := range []struct{ name, key string }{
			{"first", "key000"}, {"last", "key063"},
		} {
			cases = append(cases, scanColonSpacingCase{
				name: spacing.name + "/" + target.name,
				src:  src.String(),
				key:  target.key,
			})
		}
	}
	return cases
}

func checkScanColonSpacing(t testing.TB, tc scanColonSpacingCase) {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tc.src), &document); err != nil {
		t.Fatal(err)
	}
	want := string(document[tc.key])
	if want != "7" {
		t.Fatalf("fixture key %q = %q, want 7", tc.key, want)
	}
	node, err := NewSearcher(tc.src).GetByPathCopy(tc.key)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := node.Raw(); err != nil || raw != want {
		t.Fatalf("Raw() = (%q, %v), want %q", raw, err, want)
	}
}

func TestKeyScanColonSpacing(t *testing.T) {
	for _, tc := range scanColonSpacingCases() {
		t.Run(tc.name, func(t *testing.T) {
			checkScanColonSpacing(t, tc)
		})
	}

	// Preserve the original delimiter contract: failures do not advance p,
	// even when whitespace was inspected before the offending byte or EOF.
	before := func(parser *Parser) types.ParsingError {
		p := parser.lspace(parser.p)
		if p >= len(parser.s) {
			return types.ERR_EOF
		}
		if parser.s[p] != ':' {
			return types.ERR_INVALID_CHAR
		}
		parser.p = p + 1
		return 0
	}
	compare := func(src string, pos int) {
		t.Helper()
		want, got := Parser{s: src, p: pos}, Parser{s: src, p: pos}
		wantErr, gotErr := before(&want), got.delim()
		if gotErr != wantErr || got.p != want.p {
			t.Fatalf("delim(%q, %d) = (%v, %d), want (%v, %d)", src, pos, gotErr, got.p, wantErr, want.p)
		}
	}
	for _, prefix := range []string{"", `{"key"`} {
		for _, space := range []string{"", " ", "\t", "\r", "\n", strings.Repeat(" \t\r\n", 2), strings.Repeat(" \t\r\n", 8)} {
			for _, tail := range []string{":", ":0", ": :", "", "x", "\x00", "\v:", "\xff"} {
				compare(prefix+space+tail, len(prefix))
			}
		}
		compare(prefix, len(prefix))
		compare(prefix, len(prefix)+1)
	}
}

var scanColonSpacingSink Node

func BenchmarkGetScanColonSpacing(b *testing.B) {
	for _, tc := range scanColonSpacingCases() {
		b.Run(tc.name, func(b *testing.B) {
			checkScanColonSpacing(b, tc)
			b.ReportAllocs()
			b.ResetTimer()
			var node Node
			var err error
			for i := 0; i < b.N; i++ {
				node, err = NewSearcher(tc.src).GetByPathCopy(tc.key)
				if err != nil {
					b.Fatal(err)
				}
			}
			scanColonSpacingSink = node
		})
	}
}
