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

package optdec

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/jsonscan"
)

func checkUnicodeDecodeParity(t *testing.T, quotedSrc, want string, wantCode ErrorCode) {
	t.Helper()
	for _, forceScalar := range []bool{false, true} {
		p := newParser(quotedSrc, 0, 0)
		p.isEface = true
		code := parseRunsGrowingMode(p, forceScalar)
		if code != wantCode {
			t.Fatalf("scalar=%v src=%q: code=%d want=%d", forceScalar, quotedSrc, code, wantCode)
		}
		if wantCode == SONIC_OK {
			nd := p.nodes[0]
			var got string
			if (nd.typ & TypeMask) == KStringEscaped {
				got = string(p.bytesEscaped(1, nd.val))
			} else {
				got = p.strRefCommon(1, int(nd.val))
			}
			if got != want {
				t.Fatalf("scalar=%v src=%q: got=%q want=%q", forceScalar, quotedSrc, got, want)
			}
		}
		p.free()
	}
}

func TestCParityUnicodeCodeUnits(t *testing.T) {
	// encoding/json supplies an independent UTF-16/UTF-8 reference, including
	// replacement of each isolated surrogate. Check every UTF-16 code unit.
	for value := 0; value <= 0xffff; value++ {
		quoted := fmt.Sprintf(`"\u%04x"`, value)
		var want string
		if err := json.Unmarshal([]byte(quoted), &want); err != nil {
			t.Fatal(err)
		}
		checkUnicodeDecodeParity(t, quoted, want, SONIC_OK)
	}
}

func TestCParityUnicodeRuns(t *testing.T) {
	for _, tc := range []struct {
		body     string
		want     string
		wantOff  int
		wantCode ErrorCode
	}{
		{`\u0041\u007f\u0080\u07ff\u0800\uffff`, "A\x7f\u0080\u07ff\u0800\uffff", 0, SONIC_OK},
		{`\uD83D\uDe00\u4E2d\u6587`, "😀中文", 0, SONIC_OK},
		{`\ud800\ud800\udc00`, "\ufffd\U00010000", 0, SONIC_OK},
		{`\ud800\u0041\udc00`, "\ufffdA\ufffd", 0, SONIC_OK},
		{`\ud800x\u0041`, "\ufffdxA", 0, SONIC_OK},
		{`\u0041\n\u0042`, "A\nB", 0, SONIC_OK},
		{`\u00xx`, "", 0, SONIC_INVALID_ESCAPED_UTF},
		{`\ud800\u00xx`, "", 6, SONIC_INVALID_ESCAPED_UTF},
		{`\u0041\u00xx`, "", 6, SONIC_INVALID_ESCAPED_UTF},
		{`\ud800\u0041\u00xx`, "", 12, SONIC_INVALID_ESCAPED_UTF},
		{`\ud800\ud800\u00xx`, "", 12, SONIC_INVALID_ESCAPED_UTF},
	} {
		quoted := `"` + tc.body + `"`
		checkUnicodeDecodeParity(t, quoted, tc.want, tc.wantCode)
		if tc.wantCode != SONIC_OK {
			dst := make([]byte, len(tc.body)+8)
			_, errOff, ucode := jsonscan.UnquoteInto(tc.body, dst, false)
			if ucode != jsonscan.ErrInvalidEscapeUTF || errOff != tc.wantOff {
				t.Fatalf("body=%q: UnquoteInto=(%d,%v) want=(%d,ErrInvalidEscapeUTF)", tc.body, errOff, ucode, tc.wantOff)
			}
		}
	}

	rng := rand.New(rand.NewSource(2601103))
	for trial := 0; trial < 512; trial++ {
		var body strings.Builder
		for unit := 0; unit < 64; unit++ {
			value := rng.Intn(65536)
			if unit%3 == 0 {
				value = 0xd800 + rng.Intn(2048)
			}
			fmt.Fprintf(&body, `\u%04X`, value)
		}
		quoted := `"` + body.String() + `"`
		var want string
		if err := json.Unmarshal([]byte(quoted), &want); err != nil {
			t.Fatal(err)
		}
		checkUnicodeDecodeParity(t, quoted, want, SONIC_OK)
	}
}
