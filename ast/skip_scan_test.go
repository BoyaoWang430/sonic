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
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// The references use checked byte indexing and intentionally preserve the
// scanners' permissive syntax. They are correctness oracles, not benchmarks.
func referenceSkipString(src string, pos int) (int, int) {
	if len(src)-pos < 2 {
		return -int(types.ERR_EOF), -1
	}
	if src[pos] != '"' {
		return -int(types.ERR_INVALID_CHAR), -1
	}
	firstEscape := -1
	for i := pos + 1; i < len(src); i++ {
		switch src[i] {
		case '"':
			return i + 1, firstEscape
		case '\\':
			if firstEscape < 0 {
				firstEscape = i
			}
			i++
		}
	}
	return -int(types.ERR_EOF), -1
}

func referenceSkipPair(src string, pos int, left, right byte) int {
	if len(src)-pos < 2 {
		return -int(types.ERR_EOF)
	}
	if src[pos] != left {
		return -int(types.ERR_INVALID_CHAR)
	}
	depth, quoted := 1, false
	for i := pos + 1; i < len(src); i++ {
		switch c := src[i]; c {
		case '\\':
			// Fast skipping also ignores the byte after a backslash outside quotes.
			i++
		case '"':
			quoted = !quoted
		default:
			if quoted {
				continue
			}
			if c == left {
				depth++
			} else if c == right {
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
	}
	return -int(types.ERR_INVALID_CHAR)
}

func checkSkipString(t *testing.T, src string, pos int) {
	t.Helper()
	got, escape := skipString(src, pos)
	want, wantEscape := referenceSkipString(src, pos)
	if got != want || escape != wantEscape {
		t.Fatalf("skipString(%q, %d) = (%d, %d), want (%d, %d)", src, pos, got, escape, want, wantEscape)
	}
}

func checkSkipPair(t *testing.T, src string, pos int, left, right byte) {
	t.Helper()
	got := skipPair(src, pos, left, right)
	want := referenceSkipPair(src, pos, left, right)
	if got != want {
		t.Fatalf("skipPair(%q, %d, %q, %q) = %d, want %d", src, pos, left, right, got, want)
	}
}

func TestSkipString(t *testing.T) {
	for _, tc := range []struct {
		src          string
		pos, end, ep int
	}{
		{`""`, 0, 2, -1},
		{`xx"a"tail`, 2, 5, -1},
		{`"a\"b"`, 0, 6, 2},
		{`"\q"`, 0, 4, 1},          // Skipping does not validate escape syntax.
		{"\"\x00\xff\"", 0, 4, -1}, // Nor controls or UTF-8.
		{"", 0, -int(types.ERR_EOF), -1},
		{`""`, 2, -int(types.ERR_EOF), -1},
		{`"`, 0, -int(types.ERR_EOF), -1},
		{`"a\q`, 0, -int(types.ERR_EOF), -1}, // EOF discards the escape position.
		{`x"a"`, 0, -int(types.ERR_INVALID_CHAR), -1},
	} {
		got, ep := skipString(tc.src, tc.pos)
		if got != tc.end || ep != tc.ep {
			t.Fatalf("skipString(%q, %d) = (%d, %d), want (%d, %d)", tc.src, tc.pos, got, ep, tc.end, tc.ep)
		}
		checkSkipString(t, tc.src, tc.pos)
	}
}

func TestSkipPair(t *testing.T) {
	for _, tc := range []struct {
		src      string
		pos, end int
	}{
		{`{}`, 0, 2},
		{`x{}tail`, 1, 3},
		{`{{}}`, 0, 4},
		{`{"}"}`, 0, 5},
		{`{\}}`, 0, 4}, // Backslash also skips a closing brace outside quotes.
		{`{]}`, 0, 3},  // Fast skipping only balances the requested delimiter.
		{"", 0, -int(types.ERR_EOF)},
		{`{}`, 2, -int(types.ERR_EOF)},
		{`{`, 0, -int(types.ERR_EOF)},
		{`{\}`, 0, -int(types.ERR_INVALID_CHAR)},
		{`{"}`, 0, -int(types.ERR_INVALID_CHAR)},
		{`{abc`, 0, -int(types.ERR_INVALID_CHAR)},
		{`x{}`, 0, -int(types.ERR_INVALID_CHAR)},
	} {
		if got := skipPair(tc.src, tc.pos, '{', '}'); got != tc.end {
			t.Fatalf("skipPair(%q, %d) = %d, want %d", tc.src, tc.pos, got, tc.end)
		}
		checkSkipPair(t, tc.src, tc.pos, '{', '}')
	}
	for _, src := range []string{`[]`, `[[[]]]`, `["[",{"a":[1,2]}]tail`, `["\"]",2]`, `[1,2`, `[\]`, `["\u1234"]`} {
		checkSkipPair(t, src, 0, '[', ']')
	}
}

// These prefixes leave either zero or one open quote, at depth one or two.
// The first four consume one complete word; the last two consume an escaped
// string before returning to the scanner with only the requested short tail.
func skipPairShortTailInputs(left, right byte) []string {
	l, r := string(left), string(right)
	prefixes := []string{
		l + strings.Repeat("a", 8),
		l + `"` + strings.Repeat("a", 7),
		l + l + strings.Repeat("a", 7),
		l + l + `"` + strings.Repeat("a", 6),
		l + `"\n` + strings.Repeat("a", 8) + `"`,
		l + l + `"\n` + strings.Repeat("a", 8) + `"`,
	}
	var inputs []string
	for remaining := 0; remaining < 8; remaining++ {
		tails := []string{strings.Repeat("b", remaining)}
		if remaining > 0 {
			plain := strings.Repeat("b", remaining-1)
			tails = append(tails, plain+`\`, plain+`"`, plain+r)
		}
		if remaining > 1 {
			plain := strings.Repeat("b", remaining-2)
			tails = append(tails, plain+`"`+r, plain+`\`+r, plain+r+r)
		}
		for _, prefix := range prefixes {
			for _, tail := range tails {
				inputs = append(inputs, prefix+tail)
			}
		}
	}
	return inputs
}

func TestSkipPairShortTail(t *testing.T) {
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		for _, src := range skipPairShortTailInputs(pair[0], pair[1]) {
			for _, offset := range []int{0, 1, 7} {
				checkSkipPair(t, strings.Repeat("p", offset)+src, offset, pair[0], pair[1])
			}
		}
	}
}

func TestSkipScanBoundaries(t *testing.T) {
	for offset := 0; offset < 16; offset++ {
		prefix := strings.Repeat("p", offset)
		for n := 0; n <= 80; n++ {
			body := strings.Repeat("a", n)
			for _, tail := range []string{`"tail`, "", `\`, `\"`, `\\"`, `\\\"`} {
				checkSkipString(t, prefix+`"`+body+tail, offset)
				for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
					checkSkipPair(t, prefix+string(pair[0])+`"`+body+tail+string(pair[1]), offset, pair[0], pair[1])
				}
			}
		}
		// Place events on both sides of every word boundary. Adjacent '#' and ']'
		// exercise borrow-generated matches after quote and backslash XORs.
		for _, n := range []int{15, 16, 17, 31, 32, 33, 63, 64, 65} {
			body := strings.Repeat("a", n)
			for at := 0; at <= n; at++ {
				for _, event := range []string{`"#`, `\]`, `\"`, `\\"`, `\\\"`, `\\\\"`, "\x00\x01", "\x80\xff"} {
					inside := body[:at] + event + body[at:]
					checkSkipString(t, prefix+`"`+inside+`"tail`, offset)
					for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
						left, right := string(pair[0]), string(pair[1])
						checkSkipPair(t, prefix+left+`"`+inside+`"`+right+"tail", offset, pair[0], pair[1])
						checkSkipPair(t, prefix+left+inside+right+"tail", offset, pair[0], pair[1])
					}
				}
				for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
					left, right := string(pair[0]), string(pair[1])
					for _, event := range []string{left, right, left + right, left + string(pair[0]^1), right + string(pair[1]^1)} {
						inside := body[:at] + event + body[at:]
						checkSkipPair(t, prefix+left+inside+right+"tail", offset, pair[0], pair[1])
						checkSkipPair(t, prefix+left+`"`+inside+`"`+right+"tail", offset, pair[0], pair[1])
					}
				}
			}
		}
		for _, body := range []string{strings.Repeat("a", 4096), strings.Repeat(`a\"b\\c\n`, 512), strings.Repeat("\xff\x00", 2048)} {
			checkSkipString(t, prefix+`"`+body+`"`, offset)
			checkSkipString(t, prefix+`"`+body+`\`, offset)
			checkSkipPair(t, prefix+`{"k":"`+body+`","v":[1,2]}`, offset, '{', '}')
			checkSkipPair(t, prefix+`[{"k":"`+body+`"}`, offset, '[', ']')
		}
	}
}

// An escaped string should not affect the interpretation of later values.
// Vary the preceding string's length so its closing quote and the next string's
// backslash can share a word, before the scanner has consumed that backslash.
func TestSkipPairResumeAfterEscapedString(t *testing.T) {
	long := strings.Repeat("a", 4096)
	array := `[` + strings.Repeat(`"abcdefgh",`, 127) + `"last"]`
	for offset := 0; offset < 8; offset++ {
		prefix := strings.Repeat("p", offset)
		for padding := 0; padding < 16; padding++ {
			first := strings.Repeat("b", padding)
			for _, src := range []string{
				`{"early":"` + first + `\n","late":"` + long + `"}`,
				`{"early":"` + first + `\n","late":` + array + `}`,
				`{"early":"` + first + `","next":"\n","late":"` + long + `"}`,
				`{"early":"` + first + `\n","late":"` + long + `\`,
				`{"early":"` + first + `\n",\}}`, // Preserve escapes outside quotes.
				`{"early":"` + first + `\n",\}`,  // The only closing brace is escaped.
			} {
				checkSkipPair(t, prefix+src, offset, '{', '}')
			}
			for _, src := range []string{
				`["` + first + `","\n","` + long + `"]`,
				`["` + first + `",` + strings.Repeat(`"a\n",`, 63) + `"b\t"]`,
				`["` + first + `",` + strings.Repeat(`"\n","plain","\\","plain",`, 31) + `null]`,
				`["` + first + `",` + strings.Repeat(`"\n","a",`, 127) + `null]`,
				`["` + first + `","\n","` + long + `\`,
			} {
				checkSkipPair(t, prefix+src, offset, '[', ']')
			}
			for count := 1; count <= 8; count++ {
				escapes := strings.Repeat(`\`, count)
				content := first + escapes
				if count&1 != 0 {
					content += `"x` // The quote after an odd run is escaped.
				}
				checkSkipPair(t, prefix+`{"early":"`+content+`","late":"`+long+`"}`, offset, '{', '}')
				checkSkipPair(t, prefix+`{"early":"`+content+`","late":"`+escapes, offset, '{', '}')
			}
		}
	}
}

func TestSkipScanRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5a6262))
	alphabet := []byte{'"', '#', '\\', ']', '[', '{', '}', 'a', 0, 1, 0x1f, 0x7f, 0x80, 0xff}
	for trial := 0; trial < 20000; trial++ {
		data := make([]byte, rng.Intn(512))
		for i := range data {
			if trial&1 == 0 {
				data[i] = alphabet[rng.Intn(len(alphabet))]
			} else {
				data[i] = byte(rng.Intn(256))
			}
		}
		pos := rng.Intn(len(data) + 1)
		checkSkipString(t, string(data), pos)
		if pos < len(data) {
			data[pos] = '"'
			checkSkipString(t, string(data), pos)
		}
		for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
			checkSkipPair(t, string(data), pos, pair[0], pair[1])
			if pos < len(data) {
				data[pos] = pair[0]
				checkSkipPair(t, string(data), pos, pair[0], pair[1])
			}
		}
	}
}

// Search past both scalar strings and composite values containing escaped quotes
// and apparent JSON structure, with the escapes straddling different word lanes.
func TestSearcherSkipScanBoundaries(t *testing.T) {
	for padding := 0; padding < 16; padding++ {
		t.Run(strconv.Itoa(padding), func(t *testing.T) {
			escaped := `"` + strings.Repeat("a", padding) + `\"target\":999,{}[]\\` + strings.Repeat("b", 33) + `"`
			src := strings.Repeat(" ", padding%8) + `{"beforeString":` + escaped +
				`,"beforeObject":{"items":[` + escaped + `,{"nested":[{},[],{"s":` + escaped + `}]}]}` +
				`,"beforeArray":[{"s":` + escaped + `},[{},[]]],"target":{"value":42},"after":true}`
			if !json.Valid([]byte(src)) {
				t.Fatal("invalid test JSON")
			}
			for _, validate := range []bool{false, true} {
				searcher := NewSearcher(src)
				searcher.ValidateJSON = validate
				node, err := searcher.GetByPath("target", "value")
				if err != nil {
					t.Fatalf("ValidateJSON=%t: finding target: %v", validate, err)
				}
				if got, err := node.Int64(); err != nil || got != 42 {
					t.Fatalf("ValidateJSON=%t: target = (%d, %v), want (42, nil)", validate, got, err)
				}
				for _, path := range [][]interface{}{{"missing"}, {"target", "missing"}} {
					if _, err := searcher.GetByPath(path...); err != ErrNotExist {
						t.Fatalf("ValidateJSON=%t: path %v: got %v, want ErrNotExist", validate, path, err)
					}
				}
			}
		})
	}
}

func FuzzSkipString(f *testing.F) {
	for _, src := range []string{"", `""`, `"abc"tail`, `"aaaaaaa\"#"`, `"aaaaaaaa\\"`, `"\q`, "\"\x00\x80\xff\"", `prefix"a\"b"`} {
		f.Add(src, 0)
	}
	f.Add(`prefix"a\"b"`, 6)
	f.Fuzz(func(t *testing.T, src string, pos int) {
		pos %= len(src) + 1
		if pos < 0 {
			pos = -pos
		}
		checkSkipString(t, src, pos)
	})
}

func FuzzSkipPair(f *testing.F) {
	for _, src := range []string{"", `{}`, `{"k":"aaaaaaa\"#","v":[1,2]}`, `{{}}`, `{\}}`, `{"x":"\`, `[{"k":"\\"},[]]`, `["\"]",2]`, "{\"\x00\x80\xff\"}"} {
		f.Add(src, 0, false)
		f.Add(src, 0, true)
	}
	f.Add(`prefix{"k":[1,2]}`, 6, false)
	f.Fuzz(func(t *testing.T, src string, pos int, array bool) {
		pos %= len(src) + 1
		if pos < 0 {
			pos = -pos
		}
		if array {
			checkSkipPair(t, src, pos, '[', ']')
		} else {
			checkSkipPair(t, src, pos, '{', '}')
		}
	})
}

var skipScanEndSink, skipScanEscapeSink int

func BenchmarkSkipString(b *testing.B) {
	type benchmarkCase struct{ name, src string }
	cases := []benchmarkCase{{"empty", `""`}}
	for _, n := range []int{4, 16, 64, 256, 4096} {
		cases = append(cases, benchmarkCase{"ascii" + strconv.Itoa(n), `"` + strings.Repeat("a", n) + `"`})
	}
	cases = append(cases,
		benchmarkCase{"escapedSparse", `"` + strings.Repeat(strings.Repeat("a", 127)+`\n`, 32) + `"`},
		benchmarkCase{"escapedDense", `"` + strings.Repeat(`a\"b\\c\n`, 128) + `"`},
		benchmarkCase{"escapedEarlyLongTail", `"\n\t\\` + strings.Repeat("a", 4096) + `"`},
	)
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			end, escape := 0, 0
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				end, escape = skipString(tc.src, 0)
			}
			skipScanEndSink, skipScanEscapeSink = end, escape
		})
	}
}

func BenchmarkSkipPair(b *testing.B) {
	type benchmarkCase struct {
		name, src   string
		left, right byte
	}
	cases := []benchmarkCase{{"emptyObject", `{}`, '{', '}'}, {"emptyArray", `[]`, '[', ']'}}
	for _, n := range []int{4, 16, 64, 256, 4096} {
		cases = append(cases, benchmarkCase{"string" + strconv.Itoa(n), `{"k":"` + strings.Repeat("a", n) + `"}`, '{', '}'})
	}
	cases = append(cases,
		benchmarkCase{"numberArray", `[` + strings.Repeat("12345,", 255) + `0]`, '[', ']'},
		benchmarkCase{"escapedSparse", `{"k":"` + strings.Repeat(strings.Repeat("a", 127)+`\n`, 32) + `"}`, '{', '}'},
		benchmarkCase{"escapedDense", `{"k":"` + strings.Repeat(`a\"b\\c\n`, 128) + `"}`, '{', '}'},
		benchmarkCase{"nestedObjects", `[` + strings.Repeat(`{"k":"`+strings.Repeat("a", 64)+`","a":[1,2,3]},`, 31) + `null]`, '[', ']'},
		benchmarkCase{"escapedEarlyLongTail", `{"k":"\n\t\\` + strings.Repeat("a", 4096) + `"}`, '{', '}'},
		benchmarkCase{"structuralCharsInString", `{"k":"` + strings.Repeat(`{}[]`, 1024) + `"}`, '{', '}'},
		benchmarkCase{"quotedStrings", `[` + strings.Repeat(`"{}[]",`, 255) + `"{}[]"]`, '[', ']'},
		benchmarkCase{"earlyEscapedFieldThenLongValue", `{"early":"\n","late":"` + strings.Repeat("a", 4096) + `"}`, '{', '}'},
		benchmarkCase{"earlyEscapedFieldThenArray", `{"early":"\n","late":[` + strings.Repeat(`"abcdefgh",`, 255) + `"last"]}`, '{', '}'},
		benchmarkCase{"manyEscapedStringsShort", `[` + strings.Repeat(`"a\n",`, 255) + `"b\t"]`, '[', ']'},
		benchmarkCase{"manyEscapedStringsLong", `[` + strings.Repeat(`"`+strings.Repeat("a", 128)+`\n",`, 31) + `null]`, '[', ']'},
		benchmarkCase{"alternatingEscapes", `[` + strings.Repeat(`"\n","plain","\\","plain",`, 63) + `null]`, '[', ']'},
		benchmarkCase{"alternatingShortEscapes", `[` + strings.Repeat(`"\n","a",`, 127) + `null]`, '[', ']'},
	)
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			end := 0
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				end = skipPair(tc.src, 0, tc.left, tc.right)
			}
			skipScanEndSink = end
		})
	}
}
