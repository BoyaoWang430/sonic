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

	"github.com/bytedance/sonic/internal/rt"
)

func TestScanStringEveryEscapeByte(t *testing.T) {
	// Exercise the complete byte domain around block boundaries: bytes above
	// ASCII must not alias a valid escape or hexadecimal digit. Both entry
	// shapes are compared with the independent byte-oriented reference.
	check := func(content string) {
		want := stringBlockReference(`"`+content, 0, true)
		checkStringScan(t, `"`+content, 0, true, stringScanResult{
			want.end, want.escape, want.errorPos, want.err,
		})
		checkScanStringBlock(t, rt.StringData(content), len(content), 1)
	}
	for _, prefix := range []int{0, 7, 15, 16, 31, 63} {
		plain := strings.Repeat("a", prefix)
		for c := 0; c < 256; c++ {
			check(plain + "\\" + string(byte(c)) + `1234"#"\`)
			for digit := 0; digit < 4; digit++ {
				hex := []byte(`\u1234"#"\`)
				hex[2+digit] = byte(c)
				check(plain + string(hex))
			}
		}
	}
}

func TestScanStringStrictCrossingBoundaries(t *testing.T) {
	// Exercise raw-event consumption and crossing-escape error positions around
	// 64 B and 128 B boundaries (including consecutive backslashes, quotes,
	// controls, and malformed Unicode escapes).
	lens := []int{0, 1, 7, 8, 9, 15, 16, 60, 61, 62, 63, 64, 65, 66, 126, 127, 128, 129, 130}
	patterns := []string{
		`\"`, `\\`, `\\"`, `\\\"`, `\u0022`, `\u4e2d`, `\u00`, `\x`, "\x01", `\` + "\x01", `\u00` + "\x01" + `a`, `\\\\\"`, `\"\\\"\\`,
	}
	suffixes := []string{`"`, `b"`, `\"c"`, `\\"`}
	for _, n := range lens {
		prefix := strings.Repeat("a", n)
		for _, pat := range patterns {
			for _, suf := range suffixes {
				content := prefix + pat + suf
				want := stringBlockReference(`"`+content, 0, true)
				checkStringScan(t, `"`+content, 0, true, stringScanResult{
					want.end, want.escape, want.errorPos, want.err,
				})
			}
		}
	}
}
