//go:build !sonic_compat && !sonic_scalar && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

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

package kernels

import (
	"bytes"
	"encoding/json"

	"github.com/bytedance/sonic/internal/rt"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

type stringDecodeTailResult struct{ end, written, escaped, pos, code int }

func callStringDecodeTail(src, dst []byte, mode uint64) stringDecodeTailResult {
	a, b, c, d, e := stringDecode(rt.SliceData(src), len(src), rt.SliceData(dst), mode)
	return stringDecodeTailResult{a, b, c, d, e}
}

func stringDecodeTailInputs() []string {
	var inputs []string
	for n := 0; n <= 72; n++ {
		plain := strings.Repeat("p", n)
		inputs = append(inputs, plain, plain+`"`, plain+`",0]`)
	}
	// Contraction can overwrite the discarded part of a backwards tail load.
	// These outputs resemble scan events, while ']' and '#' are one greater
	// than '\\' and '"'. A SWAR borrow from discarded bytes must not turn the
	// unread tail into a false escape, quote, or control character.
	for prefix := 0; prefix < 8; prefix++ {
		for _, escape := range []struct{ source, following string }{
			{`\u005c`, "]"}, {`\u0022`, "#"}, {`\u0001`, " "},
		} {
			for tail := 0; tail < 16; tail++ {
				body := strings.Repeat("p", prefix) + escape.source + strings.Repeat(escape.following, tail)
				inputs = append(inputs, body, body+`"`)
			}
		}
	}
	for prefix := 0; prefix < 16; prefix++ {
		for _, tail := range []string{`\`, `\x`, `\u12`, `\u12x4`, `\ud800`, "\x01", `\n\"x"z`, `\\u005c]`} {
			inputs = append(inputs, strings.Repeat("p", prefix)+tail)
		}
	}
	return inputs
}

// Aligned output is a consistency baseline for arbitrary source/destination
// offsets and aliasing, not an independent decoder. The standard-library
// oracle below and existing fixed error fixtures provide semantic checks.
func stringDecodeAlignedBaseline(input string, mode uint64) (stringDecodeTailResult, []byte, stringDecodeTailResult, []byte) {
	_, source, _ := decodeAlignedRegion(len(input), 0)
	copy(source, input)
	_, output, _ := decodeAlignedRegion(len(input), 0)
	want := callStringDecodeTail(source, output, mode)
	wantAlias := callStringDecodeTail(source, source, mode)
	return want, output, wantAlias, source
}

func TestStringDecodeScalarTailAlignment(t *testing.T) {
	for _, input := range stringDecodeTailInputs() {
		for mode := uint64(0); mode < 16; mode++ {
			want, wantOutput, wantAlias, wantInPlace := stringDecodeAlignedBaseline(input, mode)
			for srcAlign := 0; srcAlign < 8; srcAlign++ {
				srcStorage, source, sp := decodeAlignedRegion(len(input), srcAlign)
				copy(source, input)
				for dstAlign := 0; dstAlign < 8; dstAlign++ {
					dstStorage, output, dp := decodeAlignedRegion(len(input), dstAlign)
					got := callStringDecodeTail(source, output, mode)
					if got != want || !bytes.Equal(output, wantOutput) {
						t.Fatalf("input=%q mode=%d src=%d dst=%d: got (%+v,%x), want (%+v,%x)", input, mode, srcAlign, dstAlign, got, output, want, wantOutput)
					}
					if string(source) != input {
						t.Fatalf("modified separate source: input=%q mode=%d src=%d dst=%d", input, mode, srcAlign, dstAlign)
					}
					decodeCheckCanaries(t, srcStorage, sp, len(source))
					decodeCheckCanaries(t, dstStorage, dp, got.written)
				}
				got := callStringDecodeTail(source, source, mode)
				if got != wantAlias || !bytes.Equal(source, wantInPlace) {
					t.Fatalf("in-place input=%q mode=%d align=%d: got (%+v,%x), want (%+v,%x)", input, mode, srcAlign, got, source, wantAlias, wantInPlace)
				}
				if string(source[got.written:]) != input[got.written:] {
					t.Fatalf("in-place write past output: input=%q mode=%d align=%d result=%+v", input, mode, srcAlign, got)
				}
				decodeCheckCanaries(t, srcStorage, sp, len(source))
			}
		}
	}
}

func TestStringDecodeScalarTailOracle(t *testing.T) {
	for n := 0; n <= 72; n++ {
		for _, prefix := range []string{``, `\n`, `\u005c`, `\u0022`, `\u0001`, `\u4e2d`, `\ud83d\ude00`} {
			body := prefix + strings.Repeat("]#! ", 19)[:n]
			want := stringDecodeOracle(t, body)
			for _, flags := range []uint64{0, types.F_UNICODE_REPLACE} {
				checkUnquote(t, body, want, flags, -1, 0)
				double, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				checkUnquote(t, string(double[1:len(double)-1]), want, flags|types.F_DOUBLE_UNQUOTE, -1, 0)
			}
			for _, trailing := range []string{``, `,0]`} {
				quoted := `"` + body + `"` + trailing
				for align := 0; align < 8; align++ {
					for _, validate := range []bool{false, true} {
						for _, unicodeErrors := range []bool{false, true} {
							storage, input, ptr := decodeAlignedRegion(len(quoted), align)
							copy(input, quoted)
							end, written, escaped, pos, code := ParseString(input, 0, validate, unicodeErrors)
							if end != len(body)+2 || written != len(want) || escaped != (prefix != "") || pos != -1 || code != 0 || string(input[1:1+written]) != want {
								t.Fatalf("body=%q trailing=%q align=%d validate=%v unicodeErrors=%v: result=(%d,%d,%v,%d,%d) output=%q want=%q", body, trailing, align, validate, unicodeErrors, end, written, escaped, pos, code, input[1:1+written], want)
							}
							if input[0] != '"' || string(input[1+written:]) != quoted[1+written:] {
								t.Fatalf("parser modified bytes outside decoded output: %q", quoted)
							}
							decodeCheckCanaries(t, storage, ptr, len(input))
						}
					}
				}
			}
		}
	}
}
