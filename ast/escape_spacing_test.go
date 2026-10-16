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

var escapeSpacingSink Node

func BenchmarkGetEscapeSpacing(b *testing.B) {
	const textBytes = 4096
	for _, spacing := range []int{7, 15, 31, 63, 127, 255} {
		segment := strings.Repeat("a", spacing) + `\n`
		for _, offset := range []int{0, 3} {
			// Shift the escape positions within the text without changing its
			// total encoded length. End with ordinary text to keep JSON valid.
			text := strings.Repeat("p", offset) + strings.Repeat(segment, (textBytes-offset)/len(segment))
			text += strings.Repeat("a", textBytes-len(text))
			src := `{"noise":{"text":"` + text + `"},"target":7}`
			b.Run(fmt.Sprintf("spacing%d/offset%d", spacing, offset), func(b *testing.B) {
				if !json.Valid([]byte(src)) {
					b.Fatal("invalid workload JSON")
				}
				node, err := NewSearcher(src).GetByPathCopy("target")
				if err != nil {
					b.Fatal(err)
				}
				raw, err := node.Raw()
				if err != nil || raw != "7" {
					b.Fatalf("Raw() = (%q,%v), want 7", raw, err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					node, err = NewSearcher(src).GetByPathCopy("target")
					if err != nil {
						b.Fatal(err)
					}
				}
				escapeSpacingSink = node
			})
		}
	}
}
