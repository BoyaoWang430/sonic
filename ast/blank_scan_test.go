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

	"github.com/bytedance/sonic/internal/native/types"
)

func blankScanOracle(src string, pos int) int {
	for pos < len(src) {
		switch src[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			return pos
		}
	}
	return -int(types.ERR_EOF)
}

func TestSkipBlankBoundaries(t *testing.T) {
	for c := 0; c < 256; c++ {
		for _, spaces := range []string{"", " ", "\t\r\n", strings.Repeat(" \t\r\n", 32)} {
			for _, offset := range []int{0, 1, 7, 15} {
				src := strings.Repeat("p", offset) + spaces + string([]byte{byte(c)}) + "\t "
				for _, pos := range []int{offset, len(src) - 2, len(src) - 1, len(src), len(src) + 1} {
					want := blankScanOracle(src, pos)
					if got := skipBlank(src, pos); got != want {
						t.Fatalf("skipBlank(%q, %d) = %d, want %d", src, pos, got, want)
					}
				}
			}
		}
	}
	for _, src := range []string{"", " ", "\t\r\n", strings.Repeat(" \t\r\n", 32)} {
		for pos := 0; pos <= len(src)+1; pos++ {
			if got := skipBlank(src, pos); got != blankScanOracle(src, pos) {
				t.Fatalf("whitespace EOF: src=%q pos=%d got=%d", src, pos, got)
			}
		}
	}
}

var blankScanSink int

func BenchmarkSkipBlank(b *testing.B) {
	for _, tc := range []struct{ name, src string }{
		{"empty", ""},
		{"key", `"key"`},
		{"number", "123"},
		{"oneSpace", ` "key"`},
		{"mixed", "\t\n\r \"key\""},
		{"long", strings.Repeat(" \t\r\n", 64) + `"key"`},
		{"allSpace", strings.Repeat(" \t\r\n", 64)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				blankScanSink = skipBlank(tc.src, 0)
			}
		})
	}
}
