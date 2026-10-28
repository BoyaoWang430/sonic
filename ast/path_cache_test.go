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
	"strings"
	"testing"
)

func pathCacheLongCases() []struct {
	name, src, key string
	missing        bool
} {
	key := strings.Repeat("k", 4096)
	return []struct {
		name, src, key string
		missing        bool
	}{
		{"skipKey128", `{"` + strings.Repeat("k", 128) + `":0,"target":17}`, "target", false},
		{"skipKey4096", `{"` + key + `":0,"target":17}`, "target", false},
		{"matchKey4096", `{"` + key + `":17}`, key, false},
		{"skipString4096", `{"noise":"` + strings.Repeat("v", 4096) + `","target":17}`, "target", false},
		{"skipStringSparse4096", `{"noise":"` + strings.Repeat(strings.Repeat("v", 126)+`\n`, 32) + `","target":17}`, "target", false},
		{"skipStringDense4096", `{"noise":"` + strings.Repeat(`v\"x\\y\n`, 512) + `","target":17}`, "target", false},
		{"skipNumber4096", `{"noise":` + strings.Repeat("7", 4096) + `,"target":17}`, "target", false},
		{"missingAfterLongKey4096", `{"` + key + `":0}`, "target", true},
	}
}

func TestPathCacheLongQueries(t *testing.T) {
	for _, tc := range pathCacheLongCases() {
		t.Run(tc.name, func(t *testing.T) {
			n, err := NewSearcher(tc.src).GetByPathCopy(tc.key)
			if tc.missing {
				if err != ErrNotExist {
					t.Fatalf("missing path error = %v, want %v", err, ErrNotExist)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := n.Raw()
			if err != nil || raw != "17" {
				t.Fatal(raw, err)
			}
		})
	}
}

func BenchmarkPathCacheLongQueries(b *testing.B) {
	for _, tc := range pathCacheLongCases() {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				n, err := NewSearcher(tc.src).GetByPathCopy(tc.key)
				if (err != nil) != tc.missing {
					b.Fatal(err)
				}
				scanStringQuerySink = n
			}
		})
	}
}
