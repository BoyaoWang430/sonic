//go:build !sonic_compat && linux && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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
	"syscall"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestHTMLEscapeGuardPages(t *testing.T) {
	sourcePage, destPage := newEscapingGuardPage(t), newEscapingGuardPage(t)
	// Cover scalar bulk loops independently of the quoting scan width.
	maxPrefix := maxTestInt(129, 3*quoteVectorBytes()+1)
	if maxPrefix > len(sourcePage)-16 {
		maxPrefix = len(sourcePage) - 16
	}
	for n := 0; n <= maxPrefix; n++ {
		prefix := strings.Repeat("a", n)
		checkEscapingGuarded(t, sourcePage, destPage, prefix, n, HTMLEscape, n, prefix)
		for _, tt := range htmlEscapeCases {
			input := prefix + tt.src
			for capacity := n; capacity < n+6; capacity++ {
				checkEscapingGuarded(t, sourcePage, destPage, input, capacity, HTMLEscape, ^n, prefix)
			}
			checkEscapingGuarded(t, sourcePage, destPage, input, n+6, HTMLEscape, len(input), prefix+tt.output)
		}
		// The e2 lookahead must not read past the source, and an unrecognized
		// sequence remains ordinary bytes even when the destination fills.
		for _, tail := range []string{"\xe2", "\xe2\x80", "\xe2\x80\xa7", "\xe2\x81\xa8"} {
			input := prefix + tail
			for capacity := n; capacity < len(input); capacity++ {
				checkEscapingGuarded(t, sourcePage, destPage, input, capacity, HTMLEscape, ^capacity, input[:capacity])
			}
			checkEscapingGuarded(t, sourcePage, destPage, input, len(input), HTMLEscape, len(input), input)
		}
	}
	src := "<&>\u2028\u2029\xe2"
	want := `\u003c\u0026\u003e\u2028\u2029` + "\xe2"
	input := sourcePage[len(sourcePage)-len(src):]
	copy(input, src)
	if err := syscall.Mprotect(sourcePage, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	dst := destPage[len(destPage)-len(want):]
	consumed, written := HTMLEscape(rt.String(rt.SliceData(input), len(input)), dst)
	if consumed != len(src) || written != len(want) || string(dst) != want {
		t.Fatalf("read-only HTMLEscape: (%d,%d) %q", consumed, written, dst)
	}
}
