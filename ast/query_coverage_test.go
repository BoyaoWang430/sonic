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
	"fmt"
	"strings"
	"testing"
)

type scanQueryCoverageCase struct {
	name string
	src  string
	path []interface{}
	want string
}

func scanQueryCoverageCases() []scanQueryCoverageCase {
	// These workloads vary the JSON being searched rather than scanner word
	// boundaries: numeric data, nested records, and arrays of escaped strings.
	numbers := `[` + strings.Repeat(`12345,-67,8.5,1e3,`, 64) + `0]`
	record := `{"id":123,"meta":{"active":true,"tags":["alpha","beta"]},"text":"plain {} [] text","values":[1,2,3]}`
	records := `[` + strings.Repeat(record+`,`, 31) + record + `]`
	plainStrings := `[` + strings.Repeat(`"abcdefgh",`, 127) + `"last"]`
	earlyEscape := `{"early":"\n","late":` + plainStrings + `}`
	alternating := `[` + strings.Repeat(`"\n","a","\\","b",`, 64) + `null]`
	const target = `{"value":42}`
	const suffix = `,"after":{"ignored":"after target"}}`

	cases := make([]scanQueryCoverageCase, 0, 6)
	for _, tc := range []struct{ name, skipped string }{
		{"numberArray", numbers},
		{"nestedObjects", records},
		{"earlyEscapedFieldThenArray", earlyEscape},
		{"alternatingShortEscapes", alternating},
	} {
		cases = append(cases, scanQueryCoverageCase{
			name: tc.name,
			src:  `{"noise":` + tc.skipped + `,"target":` + target + suffix,
			path: []interface{}{"target"},
			want: target,
		})
	}

	cases = append(cases, scanQueryCoverageCase{
		name: "nestedPath",
		src: `{"noise":` + numbers + `,"payload":{"noise":` + records +
			`,"target":` + target + `,"after":true}` + suffix,
		path: []interface{}{"payload", "target"},
		want: target,
	})
	selected := `{"items":` + records + `,"meta":{"count":32,"complete":true}}`
	cases = append(cases, scanQueryCoverageCase{
		name: "returnNestedContainer",
		src:  `{"noise":` + numbers + `,"target":` + selected + suffix,
		path: []interface{}{"target"},
		want: selected,
	})
	// Match the escaped and spaced inputs in BenchmarkSearchKeyScan exactly.
	var escaped, spaced strings.Builder
	escaped.WriteByte('{')
	spaced.WriteByte('{')
	for i := 0; i < 64; i++ {
		if i != 0 {
			escaped.WriteByte(',')
			spaced.WriteByte(',')
		}
		fmt.Fprintf(&escaped, `"key\u0030%02d":%d`, i, i)
		fmt.Fprintf(&spaced, "\n\t \"key%03d\" : %d ", i, i)
	}
	escaped.WriteByte('}')
	spaced.WriteByte('}')
	cases = append(cases,
		scanQueryCoverageCase{
			name: "escapedKeys",
			src:  escaped.String(),
			path: []interface{}{"key063"},
			want: "63",
		},
		scanQueryCoverageCase{
			name: "spacedKeys",
			src:  spaced.String(),
			path: []interface{}{"key063"},
			want: "63",
		},
	)
	return cases
}

func TestGetScanQueryCoverage(t *testing.T) {
	for _, tc := range scanQueryCoverageCases() {
		t.Run(tc.name, func(t *testing.T) {
			// RawMessage preserves the selected bytes while the standard
			// decoder independently checks the complete fixture and its path.
			raw := json.RawMessage(tc.src)
			for _, component := range tc.path {
				var object map[string]json.RawMessage
				if err := json.Unmarshal(raw, &object); err != nil {
					t.Fatal(err)
				}
				var ok bool
				raw, ok = object[component.(string)]
				if !ok {
					t.Fatalf("missing fixture path component %q", component)
				}
			}
			if string(raw) != tc.want {
				t.Fatalf("fixture path %v selected %q, want %q", tc.path, raw, tc.want)
			}

			for _, validate := range []bool{false, true} {
				searcher := NewSearcher(tc.src)
				searcher.ValidateJSON = validate
				node, err := searcher.GetByPathCopy(tc.path...)
				if err != nil {
					t.Fatalf("validate=%v: %v", validate, err)
				}
				got, err := node.Raw()
				if err != nil || got != string(raw) {
					t.Fatalf("validate=%v: Raw() = (%q,%v), want %q", validate, got, err, raw)
				}
				if _, err := searcher.GetByPathCopy("missing"); err != ErrNotExist {
					t.Fatalf("validate=%v: missing key error = %v, want ErrNotExist", validate, err)
				}
			}
		})
	}
}

var scanQueryCoverageSink Node

func BenchmarkGetScanQueryCoverage(b *testing.B) {
	for _, tc := range scanQueryCoverageCases() {
		b.Run(tc.name, func(b *testing.B) {
			node, err := NewSearcher(tc.src).GetByPathCopy(tc.path...)
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
				node, err = NewSearcher(tc.src).GetByPathCopy(tc.path...)
				if err != nil {
					b.Fatal(err)
				}
			}
			scanQueryCoverageSink = node
		})
	}
}

func manyShortEscapedFieldsQuery() string {
	var skipped strings.Builder
	skipped.WriteByte('{')
	for i := 0; i < 64; i++ {
		if i != 0 {
			skipped.WriteByte(',')
		}
		fmt.Fprintf(&skipped, `"key%03d":"\n"`, i)
	}
	skipped.WriteByte('}')
	return `{"noise":` + skipped.String() + `,"target":7}`
}

func checkManyShortEscapedFieldsQuery(t testing.TB, src string, validate bool) Node {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(src), &document); err != nil {
		t.Fatal(err)
	}
	want := string(document["target"])
	if want != "7" {
		t.Fatalf("fixture target = %q, want 7", want)
	}
	searcher := NewSearcher(src)
	searcher.ValidateJSON = validate
	node, err := searcher.GetByPathCopy("target")
	if err != nil {
		t.Fatalf("validate=%v: %v", validate, err)
	}
	raw, err := node.Raw()
	if err != nil || raw != want {
		t.Fatalf("validate=%v: Raw() = (%q,%v), want %q", validate, raw, err, want)
	}
	return node
}

func TestGetManyShortEscapedFields(t *testing.T) {
	src := manyShortEscapedFieldsQuery()
	for _, validate := range []bool{false, true} {
		checkManyShortEscapedFieldsQuery(t, src, validate)
	}
}

var manyShortEscapedFieldsSink Node

func BenchmarkGetManyShortEscapedFields(b *testing.B) {
	src := manyShortEscapedFieldsQuery()
	node := checkManyShortEscapedFieldsQuery(b, src, true)
	var err error
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		node, err = NewSearcher(src).GetByPathCopy("target")
		if err != nil {
			b.Fatal(err)
		}
	}
	manyShortEscapedFieldsSink = node
}
