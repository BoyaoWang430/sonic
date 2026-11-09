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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type scanArrayQueryCase struct {
	name string
	src  string
}

func scanArrayQueryCases(tb testing.TB) []scanArrayQueryCase {
	tb.Helper()
	dense := `"` + strings.Repeat(`a\"b\\c\n`, 8) + `"`
	var cases []scanArrayQueryCase
	for _, fixture := range []struct {
		name   string
		values []string
	}{
		{"mixed", []string{`null`, `true`, `false`, `-12.5e2`, `"ordinary text"`, `{}`, `[]`, `{"id":1,"items":[null,"a\\b"]}`, `[1,{"ok":true}]`}},
		{"emptyContainers", []string{`{}`, `[]`}},
		{"denseStrings", []string{dense}},
	} {
		values := make([]string, 64)
		for i := 0; i < len(values)-1; i++ {
			values[i] = fixture.values[i%len(fixture.values)]
		}
		// A small final value keeps the complete-query benchmark focused on
		// locating an index rather than validating or copying a large result.
		values[len(values)-1] = `73`
		compact := `[` + strings.Join(values, ",") + `]`
		var spaced bytes.Buffer
		if err := json.Indent(&spaced, []byte(compact), "", " \t"); err != nil {
			tb.Fatal(err)
		}
		cases = append(cases,
			scanArrayQueryCase{fixture.name + "/compact", compact},
			scanArrayQueryCase{fixture.name + "/spaced", " \t\r\n" + spaced.String() + "\r\n"},
		)
	}
	return cases
}

func TestGetScanArrayIndex(t *testing.T) {
	for _, tc := range scanArrayQueryCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			// These fixtures are valid JSON, so RawMessage provides an
			// independent index-to-source-value oracle without scanner logic.
			var want []json.RawMessage
			if err := json.Unmarshal([]byte(tc.src), &want); err != nil {
				t.Fatal(err)
			}
			for _, nested := range []bool{false, true} {
				src := tc.src
				if nested {
					src = `{"before":false,"items":` + src + `,"after":0}`
				}
				for _, validate := range []bool{false, true} {
					for _, copyValue := range []bool{false, true} {
						searcher := NewSearcher(src)
						searcher.ValidateJSON = validate
						// Repeat the first index after a missing lookup to verify
						// that the reused Searcher resets its cursor on every query.
						for _, index := range []int{0, len(want) / 2, len(want) - 1, len(want), 0} {
							path := []interface{}{index}
							if nested {
								path = []interface{}{"items", index}
							}
							var node Node
							var err error
							if copyValue {
								node, err = searcher.GetByPathCopy(path...)
							} else {
								node, err = searcher.GetByPath(path...)
							}
							if index == len(want) {
								if err != ErrNotExist || node.Exists() {
									t.Fatalf("path=%v validate=%v copy=%v: missing query returned (%v,%v)", path, validate, copyValue, node.Exists(), err)
								}
								continue
							}
							if err != nil {
								t.Fatalf("path=%v validate=%v copy=%v: %v", path, validate, copyValue, err)
							}
							raw, rawErr := node.Raw()
							if rawErr != nil || raw != string(want[index]) {
								t.Fatalf("path=%v validate=%v copy=%v: Raw()=(%q,%v), want %q", path, validate, copyValue, raw, rawErr, want[index])
							}
						}
					}
				}
			}
		})
	}
}

var scanArrayQuerySink Node

func BenchmarkGetScanArrayIndex(b *testing.B) {
	for _, tc := range scanArrayQueryCases(b) {
		var values []json.RawMessage
		if err := json.Unmarshal([]byte(tc.src), &values); err != nil {
			b.Fatal(err)
		}
		for _, position := range []struct {
			name  string
			index int
		}{
			{"last", len(values) - 1},
			{"missing", len(values)},
		} {
			b.Run(tc.name+"/"+position.name, func(b *testing.B) {
				path := []interface{}{position.index}
				var wantErr error
				if position.index == len(values) {
					wantErr = ErrNotExist
				}
				node, err := NewSearcher(tc.src).GetByPathCopy(path...)
				if err != wantErr {
					b.Fatalf("query error = %v, want %v", err, wantErr)
				}
				if wantErr == nil {
					raw, rawErr := node.Raw()
					if rawErr != nil || raw != string(values[position.index]) {
						b.Fatalf("Raw()=(%q,%v), want %q", raw, rawErr, values[position.index])
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					node, err = NewSearcher(tc.src).GetByPathCopy(path...)
					if err != wantErr {
						b.Fatalf("query error = %v, want %v", err, wantErr)
					}
				}
				scanArrayQuerySink = node
			})
		}
	}
}
