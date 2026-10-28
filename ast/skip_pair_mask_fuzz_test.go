//go:build go1.18
// +build go1.18

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

func FuzzSkipPairQuoteMasks(f *testing.F) {
	for _, seed := range []string{"", `"#"{}[]`, `"aaaaaaa"#`, `"{}[]""#`, `{z}|`, `[Z]`, `\"#`, `"\n","plain"`, "\x00\xff", `}"#"{{`} {
		f.Add(seed, uint8(0), false)
		f.Add(seed, uint8(7), true)
	}
	f.Fuzz(func(t *testing.T, src string, offset uint8, array bool) {
		left, right := byte('{'), byte('}')
		if array {
			left, right = '[', ']'
		}
		pos := int(offset)
		if pos > len(src) {
			pos = len(src)
		}
		checkSkipPair(t, src, pos, left, right)
		prefix := strings.Repeat("p", int(offset&7))
		wrapped := prefix + string(left) + src + string(right)
		checkSkipPair(t, wrapped, len(prefix), left, right)
	})
}
