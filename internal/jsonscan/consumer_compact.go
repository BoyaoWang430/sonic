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
	"encoding/binary"
	"math/bits"
	"unsafe"
)

func (m *Machine) recordCompactSpace(pos int) {
	if m.CompactObs.FirstDirtyPos < 0 {
		m.CompactObs.FirstDirtyPos = pos
	}
	m.CompactObs.HasSpace = true
}

func (m *Machine) observeCompactString(src string, tokStart, rawStart, rawContentEnd int, escapeHTML, validateUTF8 bool) {
	if m.CompactObs.NeedsRewrite || (m.Grammar.Profile.CompactFirstDirtyOnly && m.CompactObs.FirstDirtyPos >= 0) {
		return
	}
	if observeCompactStringBody(src[rawStart:rawContentEnd], escapeHTML, validateUTF8) {
		if m.CompactObs.FirstDirtyPos < 0 {
			m.CompactObs.FirstDirtyPos = tokStart
		}
		m.CompactObs.NeedsRewrite = true
	}
}

var compactPlainDirtyHTML = func() [256]bool {
	var t [256]bool
	t['<'] = true
	t['>'] = true
	t['&'] = true
	for i := 0x80; i < 256; i++ {
		t[i] = true
	}
	return t
}()

// observeCompactValidatedStringBody checks an already-validated JSON string body
// (plain or with valid RFC 8259 escapes, since every valid JSON escape consists
// exclusively of ASCII bytes other than '<', '>', '&') for raw '<', '>', '&',
// U+2028/U+2029 (when escapeHTML is true) or invalid raw UTF-8 (when validateUTF8
// is true).
func observeCompactValidatedStringBody(body string, escapeHTML, validateUTF8 bool) bool {
	n := len(body)
	if n == 0 {
		return false
	}
	b := unsafe.Slice(unsafe.StringData(body), n)
	i := 0
	if !escapeHTML {
		if !validateUTF8 {
			return false
		}
		for i+8 <= n {
			w := binary.LittleEndian.Uint64(b[i : i+8 : i+8])
			if (w & 0x8080808080808080) != 0 {
				return !kernValidateUTF8(body[i:])
			}
			i += 8
		}
		for i < n {
			if b[i] >= 0x80 {
				return !kernValidateUTF8(body[i:])
			}
			i++
		}
		return false
	}
	for i+8 <= n {
		w := binary.LittleEndian.Uint64(b[i : i+8 : i+8])
		lt := w ^ 0x3c3c3c3c3c3c3c3c
		gt := w ^ 0x3e3e3e3e3e3e3e3e
		amp := w ^ 0x2626262626262626
		mask := (w | ((lt - 0x0101010101010101) &^ lt) | ((gt - 0x0101010101010101) &^ gt) | ((amp - 0x0101010101010101) &^ amp)) & 0x8080808080808080
		if mask != 0 {
			return observeCompactStringBodySlow(body, i+(bits.TrailingZeros64(mask)>>3), validateUTF8)
		}
		i += 8
	}
	for i < n {
		if compactPlainDirtyHTML[b[i]] {
			return observeCompactStringBodySlow(body, i, validateUTF8)
		}
		i++
	}
	return false
}

//go:noinline
func observeCompactStringBodySlow(body string, i int, validateUTF8 bool) bool {
	return observeCompactStringBody(body[i:], true, validateUTF8)
}

// observeCompactStringBody inspects already-validated string content
// src[rawStart:rawEnd] for raw '<', '>', '&', U+2028/U+2029 (when escapeHTML
// is true) or invalid raw UTF-8 (when validateUTF8 is true). Valid RFC 8259
// escape spellings contain only ASCII bytes outside '<', '>', '&', so backslash
// bytes are never re-parsed here (which would otherwise desynchronize when an
// 8-byte SWAR chunk splits a valid "\\" pair).
func observeCompactStringBody(body string, escapeHTML, validateUTF8 bool) bool {
	if !escapeHTML {
		return validateUTF8 && !kernValidateUTF8(body)
	}
	n := len(body)
	b := unsafe.Slice(unsafe.StringData(body), n)
	i := 0
	for i < n {
		for i+8 <= n {
			w := binary.LittleEndian.Uint64(b[i : i+8 : i+8])
			lt := w ^ 0x3c3c3c3c3c3c3c3c
			gt := w ^ 0x3e3e3e3e3e3e3e3e
			amp := w ^ 0x2626262626262626
			mask := (w | ((lt - 0x0101010101010101) & ^lt) | ((gt - 0x0101010101010101) & ^gt) | ((amp - 0x0101010101010101) & ^amp)) & 0x8080808080808080
			if mask != 0 {
				i += bits.TrailingZeros64(mask) >> 3
				break
			}
			i += 8
		}
		if i >= n {
			return false
		}
		c := body[i]
		if c < 0x80 {
			if c == '<' || c == '>' || c == '&' {
				return true
			}
			i++
			continue
		}
		if c >= 0xe0 && c <= 0xef && i+2 < n {
			b1, b2 := body[i+1], body[i+2]
			if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (c != 0xe0 || b1 >= 0xa0) && (c != 0xed || b1 < 0xa0) {
				if c == 0xe2 && b1 == 0x80 && (b2&^1) == 0xa8 {
					return true
				}
				i += 3
				continue
			}
		} else if c >= 0xc2 && c <= 0xdf && i+1 < n {
			if (body[i+1] & 0xc0) == 0x80 {
				i += 2
				continue
			}
		} else if c >= 0xf0 && c <= 0xf4 && i+3 < n {
			b1, b2, b3 := body[i+1], body[i+2], body[i+3]
			if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (b3&0xc0) == 0x80 && (c != 0xf0 || b1 >= 0x90) && (c != 0xf4 || b1 < 0x90) {
				i += 4
				continue
			}
		}
		if validateUTF8 {
			return true
		}
		i++
	}
	return false
}
