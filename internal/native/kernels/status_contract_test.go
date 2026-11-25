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

import "testing"

func TestBoundedKernelsZeroLengthNilAndNeutralStatus(t *testing.T) {
	// 1. Nil pointer + zero length must never dereference memory.
	if c, w := QuoteBuffer(nil, 0, nil, 0, 0); c != 0 || w != 0 {
		t.Fatalf("QuoteBuffer(nil,0) = (%d,%d)", c, w)
	}
	if c, w := QuoteHTMLBuffer(nil, 0, nil, 0); c != 0 || w != 0 {
		t.Fatalf("QuoteHTMLBuffer(nil,0) = (%d,%d)", c, w)
	}
	if c, w := HTMLEscapeBuffer(nil, 0, nil, 0); c != 0 || w != 0 {
		t.Fatalf("HTMLEscapeBuffer(nil,0) = (%d,%d)", c, w)
	}
	if w, pos, err := UnquoteBuffer(nil, 0, nil, 0); w != 0 || pos != -1 || err != 0 {
		t.Fatalf("UnquoteBuffer(nil,0) = (%d,%d,%v)", w, pos, err)
	}
	if w, pos, st := UnquoteStatus("", nil, 0); w != 0 || pos != -1 || st != StatusOK {
		t.Fatalf("UnquoteStatus(\"\",nil) = (%d,%d,%v)", w, pos, st)
	}
	if end, w, esc, pos, st := DecodeQuoted(nil, 0, 4); end != 0 || w != 0 || esc != 0 || pos != 0 || st != StatusEOF {
		t.Fatalf("DecodeQuoted(nil,0) = (%d,%d,%d,%d,%v)", end, w, esc, pos, st)
	}
	if !ValidUTF8("") {
		t.Fatal("ValidUTF8(\"\") = false")
	}
	if ev, ns, width := TraversalMasks("abc", 3, true); ev != 0 || ns != 0 || width != 0 {
		t.Fatalf("TraversalMasks empty tail = (%#x,%#x,%d)", ev, ns, width)
	}

	// 2. Neutral UnquoteStatus classification across escape, unicode, and EOF errors.
	dst := make([]byte, 32)
	if _, pos, st := UnquoteStatus(`a\x`, dst, 0); st != StatusInvalidEscape || pos != 2 {
		t.Fatalf("UnquoteStatus invalid escape = (pos=%d, st=%v)", pos, st)
	}
	if _, pos, st := UnquoteStatus(`a\u12x4`, dst, 0); st != StatusInvalidUnicode || pos != 5 {
		t.Fatalf("UnquoteStatus invalid unicode = (pos=%d, st=%v)", pos, st)
	}
	if _, pos, st := UnquoteStatus(`a\`, dst, 0); st != StatusEOF || pos != 2 {
		t.Fatalf("UnquoteStatus EOF = (pos=%d, st=%v)", pos, st)
	}
}
