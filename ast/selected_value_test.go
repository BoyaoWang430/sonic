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
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// Reference scanners preserve the original cursor, error, and permissive syntax behavior.

func selectedReferenceBlank(s string, p int) int {
	for p < len(s) {
		switch s[p] {
		case ' ', '\t', '\r', '\n':
			p++
		default:
			return p
		}
	}
	return -int(types.ERR_EOF)
}
func selectedReferenceLiteral(s string, p int, literal string) int {
	end := p + len(literal)
	if end > len(s) {
		return -int(types.ERR_EOF)
	}
	if s[p:end] != literal {
		return -int(types.ERR_INVALID_CHAR)
	}
	return end
}
func selectedReferenceNull(s string, p int) int  { return selectedReferenceLiteral(s, p, "null") }
func selectedReferenceTrue(s string, p int) int  { return selectedReferenceLiteral(s, p, "true") }
func selectedReferenceFalse(s string, p int) int { return selectedReferenceLiteral(s, p, "false") }

// Preserve leading zeroes and the original exponent error positions.
func selectedReferenceNumber(s string, p int) int {
	if p >= len(s) {
		return -int(types.ERR_EOF)
	}
	if s[p] == '-' {
		p++
	}
	start := p
	point, exponent, lastDigit, needDigit := false, false, false, true
	for ; p < len(s); p++ {
		c := s[p]
		switch {
		case c >= '0' && c <= '9':
			lastDigit = true
			needDigit = false
		case needDigit:
			return -int(types.ERR_INVALID_CHAR)
		case c == '.':
			if !lastDigit || point || exponent || p == start {
				return -int(types.ERR_INVALID_CHAR)
			}
			point = true
			lastDigit = false
			needDigit = true
		case c == 'e' || c == 'E':
			if !lastDigit || exponent {
				return -int(types.ERR_INVALID_CHAR)
			}
			if p == len(s)-1 {
				return -int(types.ERR_EOF)
			}
			exponent = true
			lastDigit = false
			needDigit = false
		case c == '-' || c == '+':
			if s[p-1] != 'e' && s[p-1] != 'E' {
				return -int(types.ERR_INVALID_CHAR)
			}
			lastDigit = false
			needDigit = true
		default:
			return p
		}
	}
	if needDigit {
		return -int(types.ERR_EOF)
	}
	return p
}

func selectedValueCorpus() []string {
	return []string{"", " \t\r\n", "\v", "\f", "\xc2\xa0", "{", "[", "{]", "{]x", "[}",
		`{}`, `[]`, `{"a":1,}`, `[1,]`, `{"a":}`, `{"a"}`, `{"a":`, `{"a":1`,
		`{"a":1 "b":2}`, `{"a":1,,"b":2}`, `[1 2]`, `[,1]`, `[1,,2]`,
		`{"a":{"b":[{},[],null,true,false]}}tail`, ` [ { }, [ ], { "a" : [ 1, 2 ] } ] tail`,
		`{"a":"\q","b":"\uXX"}`, "[\"\x00\xff\"]", `[01,1e]`, `01`, `1e`, `1e]`, `1e+`, `1e-]`, `1.`, `1..0`, `+1`, `-`,
		`truex`, `false`, `nul`, `nall`, `nulltail`, `"abc"tail`, `"abc\`, `"\""`,
		`{bad}`, `[}]`, `{\}}`, `["\q",{"x":01}]`}
}
func checkSelectedValue(t testing.TB, s string, p int) {
	t.Helper()
	for _, fast := range []bool{false, true} {
		wantEnd, wantStart := selectedReferenceValue(s, p)
		gotEnd, gotStart := skipValue(s, p)
		if fast {
			wantEnd, wantStart = selectedReferenceFast(s, p)
			gotEnd, gotStart = skipValueFast(s, p)
		}
		if gotEnd != wantEnd || gotStart != wantStart {
			t.Fatalf("fast=%v src=%q pos=%d got=(%d,%d) want=(%d,%d)", fast, s, p, gotEnd, gotStart, wantEnd, wantStart)
		}
	}
}
func TestSkipSelectedValueBaseline(t *testing.T) {
	for _, body := range selectedValueCorpus() {
		for _, offset := range []int{0, 1, 7} {
			prefix := strings.Repeat("x", offset)
			for cut := 0; cut <= len(body); cut++ {
				checkSelectedValue(t, prefix+body[:cut], offset)
			}
			for _, space := range []string{" ", "\t\r\n", strings.Repeat(" ", 32)} {
				checkSelectedValue(t, prefix+space+body, offset)
			}
		}
	}
	// Preserve permissive inputs and error positions.
	for _, tc := range []struct {
		s          string
		end, start int
	}{
		{"{]", -int(types.ERR_EOF), 0}, {"{]x", -int(types.ERR_INVALID_CHAR), 0},
		{`{"a":1,}`, -int(types.ERR_EOF), 0}, {`[1,]`, -int(types.ERR_INVALID_CHAR), 0},
		{`[01,1e]`, 7, 0}, {`"\q"`, 4, 0}, {" \t{]", -int(types.ERR_EOF), 2},
	} {
		end, start := skipValue(tc.s, 0)
		if end != tc.end || start != tc.start {
			t.Fatalf("pinned %q got=(%d,%d) want=(%d,%d)", tc.s, end, start, tc.end, tc.start)
		}
	}
	rng := rand.New(rand.NewSource(801))
	alphabet := []byte("{}[],:\"\\ ntfruae0129+-.\t\r\n\x00\x80\xff")
	for n := 0; n < 3000; n++ {
		b := make([]byte, rng.Intn(96))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		checkSelectedValue(t, string(b), rng.Intn(len(b)+1))
	}
}

type selectedContainerCase struct{ name, value string }

func selectedContainerCases() []selectedContainerCase {
	numbers := `[` + strings.Repeat(`123,-45,6.25,1e3,`, 32) + `0]`
	record := `{"id":123,"active":true,"items":[null,false,{}],"text":"a\nb"}`
	objects := `[` + strings.Repeat(record+`,`, 15) + record + `]`
	nested := `[1,2,3]`
	for depth := 0; depth < 12; depth++ {
		nested = `{"child":[` + nested + `],"ok":true}`
	}
	empty := `[` + strings.Repeat(`{},[],`, 31) + `{},[]]`
	plain64 := `"` + strings.Repeat("a", 64) + `"`
	stringArray := `[` + strings.Repeat(plain64+`,`, 63) + plain64 + `]`
	spaced := func(s string) string {
		// Fixtures have no structural punctuation inside quoted text.
		return strings.NewReplacer("{", "{ \n\t ", "}", " \r\n }", "[", "[ \n ", "]", " \t ]", ":", " \t : \n ", ",", " \n , \t ").Replace(s)
	}
	var cases []selectedContainerCase
	for _, tc := range []selectedContainerCase{{"numberArray", numbers}, {"objectArray", objects}, {"nestedContainers", nested}, {"emptyContainers", empty}, {"stringArray64", stringArray}} {
		cases = append(cases, selectedContainerCase{tc.name + "/compact", tc.value}, selectedContainerCase{tc.name + "/spaced", spaced(tc.value)})
	}
	return cases
}
func TestGetSelectedContainers(t *testing.T) {
	for _, tc := range selectedContainerCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, query := range []struct {
				src  string
				path []interface{}
			}{
				{" \t" + tc.value + " \n", nil},
				{`{"before":0,"target": ` + tc.value + `,"after":false}`, []interface{}{"target"}},
				{`[0, ` + tc.value + `,false]`, []interface{}{1}},
			} {
				for _, validate := range []bool{false, true} {
					for _, copyValue := range []bool{false, true} {
						s := NewSearcher(query.src)
						s.ValidateJSON = validate
						var node Node
						var err error
						if copyValue {
							node, err = s.GetByPathCopy(query.path...)
						} else {
							node, err = s.GetByPath(query.path...)
						}
						if err != nil {
							t.Fatalf("validate=%v copy=%v path=%v: %v", validate, copyValue, query.path, err)
						}
						raw, err := node.Raw()
						if err != nil || raw != tc.value {
							t.Fatalf("raw mismatch validate=%v copy=%v: err=%v got=%q want=%q", validate, copyValue, err, raw, tc.value)
						}
					}
				}
			}
		})
	}
}

var selectedContainerSink Node

func BenchmarkGetSelectedContainers(b *testing.B) {
	for _, tc := range selectedContainerCases() {
		for _, validate := range []bool{false, true} {
			b.Run(tc.name+"/validate="+strconv.FormatBool(validate), func(b *testing.B) {
				src := `{"before":0,"target": ` + tc.value + `,"after":false}`
				s := NewSearcher(src)
				s.ValidateJSON = validate
				node, err := s.GetByPathCopy("target")
				if err != nil {
					b.Fatal(err)
				}
				raw, err := node.Raw()
				if err != nil || raw != tc.value {
					b.Fatal("fixture mismatch", err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					searcher := NewSearcher(src)
					searcher.ValidateJSON = validate
					node, err = searcher.GetByPathCopy("target")
					if err != nil {
						b.Fatal(err)
					}
				}
				selectedContainerSink = node
			})
		}
	}
}

func selectedReferenceValue(src string, pos int) (ret int, start int) {
	pos = selectedReferenceBlank(src, pos)
	if pos < 0 {
		return pos, -1
	}
	switch c := src[pos]; c {
	case 'n':
		ret = selectedReferenceNull(src, pos)
	case '"':
		ret, _ = referenceSkipString(src, pos)
	case '{':
		ret, _ = selectedReferenceObject(src, pos)
	case '[':
		ret, _ = selectedReferenceArray(src, pos)
	case 't':
		ret = selectedReferenceTrue(src, pos)
	case 'f':
		ret = selectedReferenceFalse(src, pos)
	case '-', '+', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		ret = selectedReferenceNumber(src, pos)
	default:
		ret = -int(types.ERR_INVALID_CHAR)
	}
	return ret, pos
}

func selectedReferenceFast(src string, pos int) (ret int, start int) {
	pos = selectedReferenceBlank(src, pos)
	if pos < 0 {
		return pos, -1
	}
	switch c := src[pos]; c {
	case 'n':
		ret = selectedReferenceNull(src, pos)
	case '"':
		ret, _ = referenceSkipString(src, pos)
	case '{':
		ret = referenceSkipPair(src, pos, '{', '}')
	case '[':
		ret = referenceSkipPair(src, pos, '[', ']')
	case 't':
		ret = selectedReferenceTrue(src, pos)
	case 'f':
		ret = selectedReferenceFalse(src, pos)
	case '-', '+', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		ret = selectedReferenceNumber(src, pos)
	default:
		ret = -int(types.ERR_INVALID_CHAR)
	}
	return ret, pos
}

func selectedReferenceObject(src string, pos int) (ret int, start int) {
	start = selectedReferenceBlank(src, pos)
	if start < 0 {
		return start, -1
	}

	if src[start] != '{' {
		return -int(types.ERR_INVALID_CHAR), -1
	}

	pos = start + 1
	pos = selectedReferenceBlank(src, pos)
	if pos < 0 {
		return pos, -1
	}
	if src[pos] == '}' {
		return pos + 1, start
	}

	for {
		pos, _ = referenceSkipString(src, pos)
		if pos < 0 {
			return pos, -1
		}

		pos = selectedReferenceBlank(src, pos)
		if pos < 0 {
			return pos, -1
		}
		if src[pos] != ':' {
			return -int(types.ERR_INVALID_CHAR), -1
		}

		pos++
		pos, _ = selectedReferenceValue(src, pos)
		if pos < 0 {
			return pos, -1
		}

		pos = selectedReferenceBlank(src, pos)
		if pos < 0 {
			return pos, -1
		}
		if src[pos] == '}' {
			return pos + 1, start
		}
		if src[pos] != ',' {
			return -int(types.ERR_INVALID_CHAR), -1
		}

		pos++
		pos = selectedReferenceBlank(src, pos)
		if pos < 0 {
			return pos, -1
		}

	}
}

func selectedReferenceArray(src string, pos int) (ret int, start int) {
	start = selectedReferenceBlank(src, pos)
	if start < 0 {
		return start, -1
	}

	if src[start] != '[' {
		return -int(types.ERR_INVALID_CHAR), -1
	}

	pos = start + 1
	pos = selectedReferenceBlank(src, pos)
	if pos < 0 {
		return pos, -1
	}
	if src[pos] == ']' {
		return pos + 1, start
	}

	for {
		pos, _ = selectedReferenceValue(src, pos)
		if pos < 0 {
			return pos, -1
		}

		pos = selectedReferenceBlank(src, pos)
		if pos < 0 {
			return pos, -1
		}
		if src[pos] == ']' {
			return pos + 1, start
		}
		if src[pos] != ',' {
			return -int(types.ERR_INVALID_CHAR), -1
		}
		pos++
	}
}
