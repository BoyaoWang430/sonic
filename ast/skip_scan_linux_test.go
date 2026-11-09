//go:build linux
// +build linux

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
	"strings"
	"syscall"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

// newGuardPageInput places inputs directly before an inaccessible page.
// Each returned string remains valid until the next input or test cleanup.
func newGuardPageInput(t *testing.T) func(string) string {
	t.Helper()
	pageSize := syscall.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 2*pageSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Munmap(mapping); err != nil {
			t.Error(err)
		}
	})
	if err := syscall.Mprotect(mapping[pageSize:], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	return func(input string) string {
		t.Helper()
		if len(input) > pageSize {
			t.Fatalf("input length %d exceeds page size %d", len(input), pageSize)
		}
		window := mapping[pageSize-len(input) : pageSize : pageSize]
		copy(window, input)
		// A normal []byte-to-string conversion would copy away from the guard.
		return rt.Mem2Str(window)
	}
}

// Every input ends immediately before an inaccessible page. In particular,
// reading a full word when fewer than eight bytes remain must fault here.
func TestSkipScanGuardPage(t *testing.T) {
	atPageEnd := newGuardPageInput(t)

	checkSkipString(t, atPageEnd(""), 0)
	checkSkipString(t, atPageEnd("prefix"), len("prefix"))
	checkSkipPair(t, atPageEnd(""), 0, '{', '}')
	checkSkipPair(t, atPageEnd("prefix"), len("prefix"), '{', '}')
	for _, offset := range []int{0, 1, 3, 7} {
		prefix := strings.Repeat("p", offset)
		for _, n := range []int{0, 1, 6, 7, 8, 9, 14, 15, 16, 17, 31, 32, 63, 64, 65} {
			body := strings.Repeat("a", n)
			for _, tail := range []string{`"`, "", `\`, `\"`, `\\"`} {
				checkSkipString(t, atPageEnd(prefix+`"`+body+tail), offset)
			}
			for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
				left, right := string(pair[0]), string(pair[1])
				for _, input := range []string{
					prefix + left + body + right,
					prefix + left + body,
					prefix + left + body + `\`,
					prefix + left + `"` + body + `"` + right,
					prefix + left + `"` + body + `\`,
				} {
					checkSkipPair(t, atPageEnd(input), offset, pair[0], pair[1])
				}
			}
		}
	}

	// Returning to word scanning after an escaped string must still respect the
	// final short block, including a trailing escape that advances past the end.
	for offset := 0; offset < 8; offset++ {
		prefix := strings.Repeat("p", offset)
		for padding := 0; padding < 16; padding++ {
			first := strings.Repeat("a", padding)
			for _, tail := range []string{`"}`, `\`, `\"`, `\\"}`} {
				src := prefix + `{"early":"` + first + `","next":"\n","late":"` + strings.Repeat("b", 31) + tail
				checkSkipPair(t, atPageEnd(src), offset, '{', '}')
			}
			for _, tail := range []string{`"]`, `\`, `\"`, `\\"]`} {
				src := prefix + `["` + first + `","\n","\\","` + strings.Repeat("b", 31) + tail
				checkSkipPair(t, atPageEnd(src), offset, '[', ']')
			}
			checkSkipPair(t, atPageEnd(prefix+`{"early":"`+first+`\n",\}`), offset, '{', '}')
			checkSkipPair(t, atPageEnd(prefix+`{"early":"`+first+`\n",\}}`), offset, '{', '}')
		}
	}

	// Cover each terminal remainder with both quote states and nested depth,
	// including a backslash in the final byte that advances the cursor to end+1.
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		for _, src := range skipPairShortTailInputs(pair[0], pair[1]) {
			for _, offset := range []int{0, 1, 7} {
				checkSkipPair(t, atPageEnd(strings.Repeat("p", offset)+src), offset, pair[0], pair[1])
			}
		}
	}

	// Recovery probes begin after the closing quote. Seven remaining bytes must
	// not be loaded as a word; eight fit exactly; nine leave a one-byte tail.
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		for _, remaining := range []int{7, 8, 9} {
			for _, tail := range []string{
				strings.Repeat(" ", remaining-1) + string(pair[1]),
				strings.Repeat(" ", remaining-1) + `\`,
				`"` + strings.Repeat("a", remaining-2) + `\`,
			} {
				for _, offset := range []int{0, 1, 7} {
					src := strings.Repeat("p", offset) + string(pair[0]) + `"\n` + strings.Repeat("a", 8) + `"` + tail
					checkSkipPair(t, atPageEnd(src), offset, pair[0], pair[1])
				}
			}
		}
	}
}

func TestScanResumeStringProbeGuardPage(t *testing.T) {
	atPageEnd := newGuardPageInput(t)

	// The first scalar window consumes eight bytes, or nine when its last
	// byte is a backslash. Leave exactly 7/8/9 bytes before the guard page
	// to check the recovery probe and its EOF handling after an early escape.
	for _, first := range []string{`\n` + strings.Repeat("a", 6), strings.Repeat("a", 7) + `\n`} {
		for _, remaining := range []int{7, 8, 9} {
			for _, tail := range []string{
				strings.Repeat("b", remaining-1) + `"`,
				strings.Repeat("b", remaining),
				strings.Repeat("b", remaining-1) + `\`,
			} {
				src := `"` + first + tail
				checkSkipString(t, atPageEnd(src), 0)
			}
		}
	}
}
