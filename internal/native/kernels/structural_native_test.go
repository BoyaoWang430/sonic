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
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

func checkContainer(t *testing.T, src string, start, wantEnd int, wantErr types.ParsingError) {
	t.Helper()
	end, err := SkipContainer(src, start)
	if end != wantEnd || err != wantErr {
		t.Fatalf("SkipContainer(%q, %d) = (%d, %v), want (%d, %v)", src, start, end, err, wantEnd, wantErr)
	}
}

func TestSkipContainer(t *testing.T) {
	t.Logf("scan width=%d bits", stringScanVectorBytes()*8)
	for _, tt := range []struct {
		src        string
		start, end int
		err        types.ParsingError
	}{
		{`[] trailing`, 0, 2, 0},
		{`{} trailing`, 0, 2, 0},
		{`prefix [1,[2],3] suffix`, 7, 16, 0},
		{`{"a":{"b":1},"c":[2,3]}`, 0, 23, 0},
		{`[}`, 0, 2, types.ERR_EOF},
		{`{]`, 0, 2, types.ERR_EOF},
		{`[}] suffix`, 0, 3, 0},
		{`{]} suffix`, 0, 3, 0},
		{`[{] ignored }`, 0, 3, 0},
		{`{[} ignored ]`, 0, 3, 0},
		{`["[]{}"]`, 0, 8, 0},
		{`["\x\uQQQQ"]`, 0, 12, 0},
		{"[\"\x00\xff\"]", 0, 6, 0},
		{`[\"] trailing`, 0, 4, 0},
		{`[\\"]`, 0, 5, types.ERR_EOF},
		{`[\[]]`, 0, 5, 0}, // a backslash does not escape a bracket
		{`[`, 0, 1, types.ERR_EOF},
		{`["`, 0, 2, types.ERR_EOF},
		{`["\`, 0, 3, types.ERR_EOF},
		{`[[]`, 0, 3, types.ERR_EOF},
	} {
		checkContainer(t, tt.src, tt.start, tt.end, tt.err)
	}
	end, err := skipContainer(nil, 0, '[')
	if end != 0 || err != types.ERR_EOF {
		t.Fatalf("empty private scan = (%d, %v)", end, err)
	}
}

func TestSkipEmptyContainerOffsets(t *testing.T) {
	for offset := 0; offset <= 64; offset++ {
		prefix := strings.Repeat("x", offset)
		for _, empty := range []string{"[]", "{}"} {
			for _, suffix := range []string{"", `"unterminated`, `\\"[}{]`} {
				src := prefix + empty + suffix
				checkContainer(t, src, offset, offset+2, 0)
			}
			checkContainer(t, prefix+empty[:1], offset, offset+1, types.ERR_EOF)
		}
	}
}

func TestSkipContainerVectorBoundaries(t *testing.T) {
	const vl = 64 // the scalar-mask width and maximum requested VL
	for pad := 0; pad <= 3*vl+1; pad++ {
		prefix := strings.Repeat("x", pad)
		for run := 0; run <= 2*vl+1; run++ {
			slashes := strings.Repeat(`\`, run)
			// Outside a string, an odd run escapes the first quote and lets
			// the immediately following bracket finish the container.
			src := `[` + prefix + slashes + `"]"]`
			end := len(src)
			if run%2 != 0 {
				end -= 2
			}
			checkContainer(t, src, 0, end, 0)
			// Inside a string, the parity has the opposite effect.
			src = `[` + prefix + `"` + slashes + `"]"]`
			end = len(src)
			if run%2 == 0 {
				end -= 2
			}
			checkContainer(t, src, 0, end, 0)
		}
		for _, brackets := range [][2]string{{"[", "]"}, {"{", "}"}} {
			open, close := brackets[0], brackets[1]
			for depth := 1; depth <= 2*vl+1; depth++ {
				src := strings.Repeat(open, depth) + prefix + `"[}\\\"{]"` + strings.Repeat(close, depth)
				checkContainer(t, src+open, 0, len(src), 0)
				checkContainer(t, src[:len(src)-1], 0, len(src)-1, types.ERR_EOF)
			}
		}
	}
	// Fixed backing storage exercises every source alignment independently
	// of the payload length and checks a nonzero opening-bracket offset.
	storage := make([]byte, 6*vl+32)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			input := `[{"x":"` + strings.Repeat("a", n) + `\\\"[]"}]`
			copy(storage[align:], input)
			src := rt.String(rt.SliceData(storage), align+len(input))
			checkContainer(t, src, align, align+len(input), 0)
		}
	}
	// The relaxed contract deliberately has no JSON nesting-depth limit.
	src := strings.Repeat("[", 10000) + strings.Repeat("]", 10000)
	checkContainer(t, src, 0, len(src), 0)
}

func TestSkipContainerCrossing64AndSibling(t *testing.T) {
	// Verify full-64B blocks transitioning into <64B tails with crossing
	// backslashes, quoted/unquoted brackets, and an immediately following sibling.
	pats := []string{
		`""`, `"\""`, `"\\"`, `"\\\""`, `"{}"`, `"}"`, `{"a":1}`, `{"a":"\"}"}`, `{"a":"\\"}`,
	}
	for _, n := range []int{0, 1, 15, 16, 31, 32, 48, 60, 61, 62, 63, 64, 65, 66, 126, 127, 128, 129} {
		pad := strings.Repeat(" ", n)
		for _, pat := range pats {
			src := `{"k":` + pad + pat + `},"sibling":{"keep":true}`
			e1, err1 := SkipContainer(src, 0)
			e2, err2 := skipContainerScalar(rt.StringData(src[1:]), len(src)-1, '{')
			if err2 == 0 {
				e2++
			}
			if e1 != e2 || err1 != err2 || src[e1:] != `,"sibling":{"keep":true}` {
				t.Fatalf("n=%d pat=%q: got (%d,%v) want (%d,%v) rem=%q", n, pat, e1, err1, e2, err2, src[e1:])
			}
		}
	}
}
