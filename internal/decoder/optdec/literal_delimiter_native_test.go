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

import "testing"

func TestNativeParserLiteralContainerDelimiter(t *testing.T) {
	// A complete literal followed by another token fails at the enclosing
	// container's delimiter check, not inside the literal parser.
	for _, word := range []string{"null", "true", "false"} {
		input := "[" + word + "x]"
		p := newParser(input, 0, 0)
		code, pos, used := p.parse(), p.Pos(), nativeParserUsed(p)
		p.free()
		if code != SONIC_EXPECT_ARR_COMMA_OR_END || pos != len(word)+2 || used != 2 {
			t.Fatalf("parse(%q)=(%v,%d,%d), want delimiter error at %d after two nodes", input, code, pos, used, len(word)+2)
		}
	}
}
