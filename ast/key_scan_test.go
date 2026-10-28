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
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/unquote"
)

// Keep the original key-search path as an oracle for cursor and error behavior.
func (self *Parser) searchKeyBeforeKeyScan(match string) types.ParsingError {
	ns := len(self.s)
	if err := self.object(); err != 0 {
		return err
	}

	/* check for EOF */
	if self.p = self.lspace(self.p); self.p >= ns {
		return types.ERR_EOF
	}

	/* check for empty object */
	if self.s[self.p] == '}' {
		self.p++
		return _ERR_NOT_FOUND
	}

	var njs types.JsonState
	var err types.ParsingError
	/* decode each pair */
	for {

		/* decode the key */
		if njs = self.decodeValue(); njs.Vt != types.V_STRING {
			return types.ERR_INVALID_CHAR
		}

		/* extract the key */
		idx := self.p - 1
		key := self.s[njs.Iv:idx]

		/* check for escape sequence */
		if njs.Ep != -1 {
			if key, err = unquote.String(key); err != 0 {
				return err
			}
		}

		/* expect a ':' delimiter */
		if err = self.delim(); err != 0 {
			return err
		}

		/* skip value */
		if key != match {
			if _, err = self.skipFast(); err != 0 {
				return err
			}
		} else {
			return 0
		}

		/* check for EOF */
		self.p = self.lspace(self.p)
		if self.p >= ns {
			return types.ERR_EOF
		}

		/* check for the next character */
		switch self.s[self.p] {
		case ',':
			self.p++
		case '}':
			self.p++
			return _ERR_NOT_FOUND
		default:
			return types.ERR_INVALID_CHAR
		}
	}
}

func compareKeyScanSearch(t *testing.T, src string, pos int, match string, decodeNumbers bool) {
	t.Helper()
	want := Parser{s: src, p: pos}
	var scratch [types.MaxDigitNums]byte
	if decodeNumbers {
		want.dbuf = &scratch[0]
	}
	got := want
	wantErr := want.searchKeyBeforeKeyScan(match)
	gotErr := got.searchKey(match)
	if gotErr != wantErr || got.p != want.p {
		t.Fatalf("src=%q pos=%d match=%q decodeNumbers=%v: got (%d, %v), want (%d, %v)", src, pos, match, decodeNumbers, got.p, gotErr, want.p, wantErr)
	}
}

func TestKeyScanSearchDifferential(t *testing.T) {
	inputs := []string{
		`{}`, ` { } `, `{"a":1}`, `{"a":1,"b":2}`, `{"a":[{},1],"b":{"a":2}}`,
		`{"a":1,"a":2}`, `{"a\u0062":3}`, `{"a\\b":4}`, `{"a\"b":5}`, `{"":6}`, `{"你好":7}`,
		`{"\uD83D\uDE00":8}`, `{"\uD800":9}`, `{"\q":10}`, `{"\u00X0":11}`, `{"\":12}`,
		"{\"a\x01b\":13}", "{\"a\xffb\":14}", "{\"a\": 1,\n\t\r \"b\": 2}",
		`{"a":`, `{"a"`, `{"a`, `{"`, `{`, ``, ` `, `{"a":1,}`, `{"a":1, `,
		`{123:1}`, `{null:1}`, `{true:1}`, `{false:1}`, `{[]:1}`, `{{}:1}`, `{1e500:1}`, `{x:1}`,
		`{"a" 1}`, `{"a":1 "b":2}`, `{"a":1,"b":}`, `{"a":1,"b":{broken}}`,
		`{"a":1,"b":1e}`, `{"a":1,"b":"unterminated}`, `[]`, `[1,2]`, `null`,
	}
	matches := []string{"a", "b", "missing", "", "ab", "a\\b", "a\"b", "你好", "😀"}
	for _, src := range inputs {
		for _, match := range matches {
			for _, decodeNumbers := range []bool{false, true} {
				compareKeyScanSearch(t, src, 0, match, decodeNumbers)
				compareKeyScanSearch(t, "prefix"+src, 6, match, decodeNumbers)
			}
		}
	}
}

func TestKeyScanSearchRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(726198))
	alphabet := []byte("abcdef012389 +-eEtnrufals\"\\/:,{}[]\t\n\r\x00\x1f\x7f\x80\xff")
	for i := 0; i < 100000; i++ {
		buf := make([]byte, 2+rng.Intn(160))
		buf[0] = '{'
		buf[1] = '"'
		for j := 2; j < len(buf); j++ {
			buf[j] = alphabet[rng.Intn(len(alphabet))]
		}
		if i%3 == 0 {
			buf = append(buf, []byte(`":0,"b":2}`)...)
		}
		if i%5 == 0 {
			buf = append([]byte(`{"x":0, `), buf[1:]...)
		}
		compareKeyScanSearch(t, string(buf), 0, "b", i%2 == 0)
	}
}

func TestKeyScanObjectParsing(t *testing.T) {
	const src = ` { "plain":1, "a\u0062":2, "a\\b":3, "a\"b":4, "":5, "你好":6, "\uD83D\uDE00":7 } `
	keys := []string{"plain", "ab", "a\\b", "a\"b", "", "你好", "😀"}
	for _, eager := range []bool{false, true} {
		for _, skipValues := range []bool{false, true} {
			t.Run(fmt.Sprintf("eager=%v/skip=%v", eager, skipValues), func(t *testing.T) {
				parser := NewParser(src)
				parser.noLazy = eager
				parser.skipValue = skipValues
				node, err := parser.Parse()
				if err != 0 {
					t.Fatal(err)
				}
				// Lazy lookup walks skipNextPair; eager parsing walks decodeObject.
				for i, key := range keys {
					value, err := node.Get(key).Int64()
					if err != nil || value != int64(i+1) {
						t.Fatalf("key %q: got %d, %v", key, value, err)
					}
				}
				if err := node.LoadAll(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func BenchmarkSearchKeyScan(b *testing.B) {
	var many, escaped, spaced strings.Builder
	many.WriteByte('{')
	escaped.WriteByte('{')
	spaced.WriteByte('{')
	for i := 0; i < 64; i++ {
		if i != 0 {
			many.WriteByte(',')
			escaped.WriteByte(',')
			spaced.WriteByte(',')
		}
		fmt.Fprintf(&many, `"key%03d":%d`, i, i)
		fmt.Fprintf(&escaped, `"key\u0030%02d":%d`, i, i)
		fmt.Fprintf(&spaced, "\n\t \"key%03d\" : %d ", i, i)
	}
	many.WriteByte('}')
	escaped.WriteByte('}')
	spaced.WriteByte('}')
	cases := []struct {
		name, src, key string
		err            types.ParsingError
	}{
		{"short", `{"first":0,"middle":1,"target":2}`, "target", 0},
		{"many", many.String(), "key063", 0},
		{"escaped", escaped.String(), "key063", 0},
		{"spaced", spaced.String(), "key063", 0},
		{"missing", many.String(), "missing", _ERR_NOT_FOUND},
		{"long", `{"` + strings.Repeat("a", 128) + `":0,"target":2}`, "target", 0},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			parser := Parser{s: tc.src}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				parser.p = 0
				if err := parser.searchKey(tc.key); err != tc.err {
					b.Fatal(err)
				}
			}
		})
	}
}
