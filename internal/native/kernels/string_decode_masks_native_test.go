//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestStringDecodeMaskChunks(t *testing.T) {
	vl := stringDecodeVectorBytes()
	gaps := []int{0, 63, 64, 65, vl - 1, vl, vl + 1}
	for gap := 1; gap <= 16; gap++ {
		gaps = append(gaps, gap)
	}
	// Escapes cross blocks; event offsets must survive output contraction.
	for prefix := 0; prefix <= 2*vl+1; prefix++ {
		for _, gap := range gaps {
			body := strings.Repeat("p", prefix) +
				strings.Repeat(`\n\"\\\u0041\ud800\udc00\ud800\u0042`, 3) +
				strings.Repeat("x", gap) + `\tend`
			want := stringDecodeOracle(t, body)
			checkUnquote(t, body, want, types.F_UNICODE_REPLACE, -1, 0)
			double, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			checkUnquote(t, string(double[1:len(double)-1]), want,
				types.F_DOUBLE_UNQUOTE|types.F_UNICODE_REPLACE, -1, 0)

			quoted := `"` + body + `"`
			input := []byte(quoted + `, "untouched":123}`)
			end, written, escaped, pos, code := ParseString(input, 0, true, false)
			if end != len(quoted) || written != len(want) || !escaped || pos != -1 || code != 0 ||
				string(input[1:1+written]) != want || string(input[end-1:]) != `", "untouched":123}` {
				t.Fatalf("mask chunks prefix=%d gap=%d: end=%d written=%d escaped=%v pos=%d code=%d",
					prefix, gap, end, written, escaped, pos, code)
			}
		}
	}
}

func TestStringDecodeShortSpansInPlace(t *testing.T) {
	const plain = "0123456789abcdef"
	for gap := 0; gap <= len(plain); gap++ {
		for contraction := 1; contraction <= 9; contraction++ {
			// Each initial escape moves the output one byte behind the input.
			// Short copies must not overwrite bytes that remain to be loaded.
			body := strings.Repeat(`\n`, contraction) + plain[:gap] + `\t` + plain[:gap]
			want := stringDecodeOracle(t, body)
			for offset := 0; offset < 8; offset++ {
				storage, input, ptr := decodeAlignedRegion(len(body), offset)
				copy(input, body)
				written, pos, code := UnquoteBuffer(ptr, len(input), ptr, 0)
				if written != len(want) || pos != -1 || code != 0 || string(input[:written]) != want ||
					string(input[written:]) != body[written:] {
					t.Fatalf("short span gap=%d contraction=%d offset=%d: written=%d pos=%d code=%d output=%q",
						gap, contraction, offset, written, pos, code, input)
				}
				decodeCheckCanaries(t, storage, ptr, len(input))
			}
		}
	}
}

func TestStringDecodeMaskChunksErrors(t *testing.T) {
	vl := stringDecodeVectorBytes()
	for prefix := 0; prefix <= 2*vl+1; prefix++ {
		body := strings.Repeat("p", prefix) + `\n\"\\\u0041`
		for _, tc := range []struct {
			tail string
			pos  int
			code KernelStatus
		}{
			{"\x00\"", 0, StatusControlChar},
			{`\x"`, 0, StatusInvalidEscape},
			{`\u12x4"`, 0, StatusInvalidUnicode},
			{`\ud800\u12x4"`, 6, StatusInvalidUnicode},
			{`\u123`, 5, StatusEOF},
		} {
			input := []byte(`"` + body + tc.tail)
			end, _, escaped, pos, code := ParseString(input, 0, true, false)
			wantPos := 1 + len(body) + tc.pos
			if end != wantPos || pos != wantPos || code != tc.code || !escaped {
				t.Fatalf("mask error prefix=%d tail=%q: end=%d escaped=%v pos=%d code=%d, want pos=%d code=%d",
					prefix, tc.tail, end, escaped, pos, code, wantPos, tc.code)
			}
		}
	}
}
