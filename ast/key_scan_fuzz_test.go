//go:build go1.18
// +build go1.18

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

import "testing"

func FuzzKeyScanSearch(f *testing.F) {
	for _, src := range []string{`{"a":1}`, `{"a\u0062":1,"b":2}`, `{"x":{},"b":[1,2]}`, `{123:1}`, `{"b`, "{\"a\":1,\n \"b\":2}"} {
		f.Add(src, "b", false)
	}
	f.Fuzz(func(t *testing.T, src, match string, decodeNumbers bool) {
		compareKeyScanSearch(t, src, 0, match, decodeNumbers)
	})
}
