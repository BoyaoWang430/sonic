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

import "testing"

func FuzzSkipValue(f *testing.F) {
	for _, s := range selectedValueCorpus() {
		f.Add(s, uint16(0))
	}
	f.Add("prefix \t{\"a\":[1e,01]}", uint16(6))
	f.Fuzz(func(t *testing.T, s string, offset uint16) { checkSelectedValue(t, s, int(offset)%(len(s)+1)) })
}
