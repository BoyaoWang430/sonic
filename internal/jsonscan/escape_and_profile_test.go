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

package jsonscan

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/cpu"
)

func TestG3EscapeErrorOffsetAuthorityAndISAParity(t *testing.T) {
	// Exercise every escape error class across vector/alignment boundaries (0..97 prefix bytes),
	// verifying 100% offset & error-code parity between:
	//   1) UnquoteInto (hardware SIMD kernel + NormalizeUnquoteError)
	//   2) UnquoteInto under forced cpu.UseScalar=true (scalar reference kernel + NormalizeUnquoteError)
	//   3) StringValidateState.advanceWindow (G2 zero-output validator)
	tails := []string{
		`\q`,
		`\'`,
		`\u`,
		`\u1`,
		`\u12`,
		`\u123`,
		`\uxxxx`,
		`\u1xxx`,
		`\u12xx`,
		`\u123x`,
		`\ud800`,
		`\ud800qwer`,
		`\ud800\n`,
		`\ud800\q`,
		`\ud800\u0041`,
		`\ud800\ud800`,
		`\ud800\ud800\udc00`,
		`\ud800\u12`,
		`\ud800\u12x4`,
		`\udc00`,
		`\udc00\udc00`,
		`\u0041\udc00`,
		`\\ud800\udc00`,
		`\\n\q`,
		`\ud83d\ude00\ud800xyz`,
	}

	origScalar := cpu.UseScalar
	defer func() { cpu.UseScalar = origScalar }()

	for _, prefixLen := range []int{0, 1, 7, 15, 16, 17, 31, 32, 33, 63, 64, 65, 97} {
		pfx := strings.Repeat("x", prefixLen)
		for _, tail := range tails {
			// Ensure escTail starts at an escape backslash just like decodeEscapedStringToUnesc.
			escTail := `\n` + pfx + tail
			dstHW := make([]byte, len(escTail)+8)
			dstSc := make([]byte, len(escTail)+8)

			for _, strictSurr := range []bool{false, true} {
				cpu.UseScalar = false
				wHW, offHW, codeHW := UnquoteInto(escTail, dstHW, strictSurr)

				cpu.UseScalar = true
				wSc, offSc, codeSc := UnquoteInto(escTail, dstSc, strictSurr)

				cpu.UseScalar = origScalar

				if codeHW != codeSc || offHW != offSc {
					t.Fatalf("HW vs Scalar UnquoteInto mismatch (strictSurr=%v, pfx=%d, tail=%q): hw=(%d,%v) sc=(%d,%v)",
						strictSurr, prefixLen, tail, offHW, codeHW, offSc, codeSc)
				}
				if codeHW == ErrNone && string(dstHW[:wHW]) != string(dstSc[:wSc]) {
					t.Fatalf("HW vs Scalar decoded output mismatch (strictSurr=%v, pfx=%d, tail=%q)",
						strictSurr, prefixLen, tail)
				}

				// Compare against G2 zero-output StringValidateState!
				var st StringValidateState
				st.reset()
				vOff, vCode := st.advanceWindow(escTail, 0, len(escTail), true, StringPolicy{
					ValidateEscapes:     true,
					RejectLoneSurrogate: strictSurr,
				}, false)
				if vCode != codeHW || (codeHW != ErrNone && vOff != offHW) {
					t.Fatalf("UnquoteInto vs StringValidateState mismatch (strictSurr=%v, pfx=%d, tail=%q): unquote=(%d,%v) validate=(%d,%v)",
						strictSurr, prefixLen, tail, offHW, codeHW, vOff, vCode)
				}
			}
		}
	}
}

func TestG3UnclosedStringEarlierEscapeAuthority(t *testing.T) {
	cases := []struct {
		tail       string
		strictSurr bool
		wantFound  bool
		wantOff    int
		wantCode   ScanError
	}{
		{`\`, true, false, 0, ErrNone},
		{`\u12`, true, false, 0, ErrNone},
		{`\ud800\u12`, true, false, 0, ErrNone},
		{`\q`, true, true, 0, ErrInvalidEscape},
		{`abc\u1x`, true, true, 3, ErrInvalidEscapeUTF},
		{`abc\ud800`, false, false, 0, ErrNone},
		{`abc\ud800`, true, true, 3, ErrInvalidEscapeUTF},
		{`abc\ud800\`, true, true, 3, ErrInvalidEscapeUTF},
		{`abc\ud800x`, true, true, 3, ErrInvalidEscapeUTF},
		{`abc\ud800\u1x`, true, true, 9, ErrInvalidEscapeUTF},
		{`abc\ud800\ud800`, true, true, 3, ErrInvalidEscapeUTF},
		{`abc\udc00`, true, true, 3, ErrInvalidEscapeUTF},
	}
	for _, tc := range cases {
		off, code, found := CheckEarlierEscapeBeforeEOF(tc.tail, tc.strictSurr)
		if found != tc.wantFound || (found && (off != tc.wantOff || code != tc.wantCode)) {
			t.Fatalf("CheckEarlierEscapeBeforeEOF(%q, strict=%v) = (%d, %v, %v), want (%d, %v, %v)",
				tc.tail, tc.strictSurr, off, code, found, tc.wantOff, tc.wantCode, tc.wantFound)
		}
	}
}

func TestG3AuthoritativeProfileMatrixInvariants(t *testing.T) {
	matrix := AuthoritativeProfileMatrix()
	if len(matrix) < 10 {
		t.Fatalf("expected comprehensive profile matrix, got %d entries", len(matrix))
	}
	sawCompact10000 := false
	sawLazyAST := false
	sawSharedEventAST := 0
	for _, row := range matrix {
		if row.Name == "" || row.Entrypoint == "" || row.CallChainNote == "" {
			t.Fatalf("incomplete profile matrix row: %+v", row)
		}
		if row.Name == "Compact_Sanitize" {
			sawCompact10000 = row.MaxDepth == CompactMaxContainerDepth && row.NumberConsumer == NumberConsumeValidateOnly
		}
		if row.Name == "AST_LazyTree_LoadAll_Patch" {
			sawLazyAST = row.ASTScope == ASTScopeLazyIncremental
		}
		if row.ASTScope == ASTScopeSharedEventSink && row.Output == ModeEventSink {
			sawSharedEventAST++
		}
	}
	if !sawCompact10000 {
		t.Fatal("Compact_Sanitize row missing 10000 depth / validate-only number contract")
	}
	if !sawLazyAST {
		t.Fatal("AST_LazyTree_LoadAll_Patch row missing ASTScopeLazyIncremental")
	}
	if sawSharedEventAST != 2 {
		t.Fatalf("expected 2 ASTScopeSharedEventSink rows (Loads/rawInterface and Preorder), got %d", sawSharedEventAST)
	}

	// Verify ASTValidateProfile and CompactProfile never enable UseNumber (G3.2 / G3.4).
	if ASTValidateProfile(true, 0).UseNumber {
		t.Fatal("ASTValidateProfile must not enable UseNumber")
	}
	if CompactProfile(true, true).UseNumber {
		t.Fatal("CompactProfile must not enable UseNumber")
	}
}
