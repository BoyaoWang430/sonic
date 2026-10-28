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
	"strings"
	"testing"
)

type scanStringQueryCase struct {
	name   string
	src    string
	object bool
}

func scanStringQueryCases() []scanStringQueryCase {
	const textBytes = 4096
	repeatText := func(segment string) string {
		return strings.Repeat(segment, textBytes/len(segment)) + strings.Repeat("a", textBytes%len(segment))
	}
	texts := []struct{ name, text string }{
		{"plain", strings.Repeat("a", textBytes)},
		{"sparse31", repeatText(strings.Repeat("a", 31) + `\n`)},
		{"sparse127", repeatText(strings.Repeat("a", 127) + `\n`)},
		{"dense", repeatText(`a\"b\\c\n`)},
		{"shortEscape", `a\nb\\c\"d`},
	}
	cases := make([]scanStringQueryCase, 0, 7)
	for _, tc := range texts {
		quoted := `"` + tc.text + `"`
		cases = append(cases, scanStringQueryCase{
			name: "returnString/" + tc.name,
			src:  `{"before":0,"target":` + quoted + `,"after":"tail"}`,
		})
		// Existing object workloads cover plain, dense, and short escaped
		// text. Add only the two spacing cases for the object return path.
		if tc.name == "sparse31" || tc.name == "sparse127" {
			cases = append(cases, scanStringQueryCase{
				name:   "returnObject/" + tc.name,
				src:    `{"before":0,"target":{"text":` + quoted + `,"flags":[1,true]},"after":"tail"}`,
				object: true,
			})
		}
	}
	return cases
}

func checkScanStringQuery(t testing.TB, tc scanStringQueryCase, validate bool) Node {
	t.Helper()
	// RawMessage preserves the exact selected JSON, including its escape
	// spelling. Decode its string separately to also check the returned value.
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tc.src), &document); err != nil {
		t.Fatal(err)
	}
	wantRaw := document["target"]
	stringRaw := wantRaw
	if tc.object {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wantRaw, &fields); err != nil {
			t.Fatal(err)
		}
		stringRaw = fields["text"]
	}
	var wantText string
	if err := json.Unmarshal(stringRaw, &wantText); err != nil {
		t.Fatal(err)
	}

	searcher := NewSearcher(tc.src)
	searcher.ValidateJSON = validate
	node, err := searcher.GetByPathCopy("target")
	if err != nil {
		t.Fatalf("validate=%v: %v", validate, err)
	}
	raw, err := node.Raw()
	if err != nil || raw != string(wantRaw) {
		t.Fatalf("validate=%v: Raw() = (%q,%v), want %q", validate, raw, err, wantRaw)
	}
	value := &node
	if tc.object {
		value = node.Get("text")
	}
	text, err := value.String()
	if err != nil || text != wantText {
		t.Fatalf("validate=%v: String() = (%q,%v), want %q", validate, text, err, wantText)
	}
	return node
}

func TestGetScanStringQuery(t *testing.T) {
	for _, tc := range scanStringQueryCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, validate := range []bool{false, true} {
				checkScanStringQuery(t, tc, validate)
			}
		})
	}
}

var scanStringQuerySink Node

func BenchmarkGetScanStringQuery(b *testing.B) {
	for _, tc := range scanStringQueryCases() {
		b.Run(tc.name, func(b *testing.B) {
			node := checkScanStringQuery(b, tc, true)
			var err error
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				node, err = NewSearcher(tc.src).GetByPathCopy("target")
				if err != nil {
					b.Fatal(err)
				}
			}
			scanStringQuerySink = node
		})
	}
}
