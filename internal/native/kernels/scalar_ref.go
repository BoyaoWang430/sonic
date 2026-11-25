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
	"unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

var scalarSimpleEscapes = [128]byte{
	'"':  '"',
	'/':  '/',
	'\\': '\\',
	'b':  '\b',
	'f':  '\f',
	'n':  '\n',
	'r':  '\r',
	't':  '\t',
}

var scalarHexDigits = [256]uint8{
	'0': 0x10, '1': 0x11, '2': 0x12, '3': 0x13, '4': 0x14,
	'5': 0x15, '6': 0x16, '7': 0x17, '8': 0x18, '9': 0x19,
	'a': 0x1a, 'b': 0x1b, 'c': 0x1c, 'd': 0x1d, 'e': 0x1e, 'f': 0x1f,
	'A': 0x1a, 'B': 0x1b, 'C': 0x1c, 'D': 0x1d, 'E': 0x1e, 'F': 0x1f,
}

var scalarQuoteSingle = [256]string{
	0x00: `\u0000`, 0x01: `\u0001`, 0x02: `\u0002`, 0x03: `\u0003`,
	0x04: `\u0004`, 0x05: `\u0005`, 0x06: `\u0006`, 0x07: `\u0007`,
	0x08: `\u0008`, 0x09: `\t`, 0x0a: `\n`, 0x0b: `\u000b`,
	0x0c: `\u000c`, 0x0d: `\r`, 0x0e: `\u000e`, 0x0f: `\u000f`,
	0x10: `\u0010`, 0x11: `\u0011`, 0x12: `\u0012`, 0x13: `\u0013`,
	0x14: `\u0014`, 0x15: `\u0015`, 0x16: `\u0016`, 0x17: `\u0017`,
	0x18: `\u0018`, 0x19: `\u0019`, 0x1a: `\u001a`, 0x1b: `\u001b`,
	0x1c: `\u001c`, 0x1d: `\u001d`, 0x1e: `\u001e`, 0x1f: `\u001f`,
	'"': `\"`, '\\': `\\`,
}

var scalarQuoteDouble = [256]string{
	0x00: `\\u0000`, 0x01: `\\u0001`, 0x02: `\\u0002`, 0x03: `\\u0003`,
	0x04: `\\u0004`, 0x05: `\\u0005`, 0x06: `\\u0006`, 0x07: `\\u0007`,
	0x08: `\\u0008`, 0x09: `\\t`, 0x0a: `\\n`, 0x0b: `\\u000b`,
	0x0c: `\\u000c`, 0x0d: `\\r`, 0x0e: `\\u000e`, 0x0f: `\\u000f`,
	0x10: `\\u0010`, 0x11: `\\u0011`, 0x12: `\\u0012`, 0x13: `\\u0013`,
	0x14: `\\u0014`, 0x15: `\\u0015`, 0x16: `\\u0016`, 0x17: `\\u0017`,
	0x18: `\\u0018`, 0x19: `\\u0019`, 0x1a: `\\u001a`, 0x1b: `\\u001b`,
	0x1c: `\\u001c`, 0x1d: `\\u001d`, 0x1e: `\\u001e`, 0x1f: `\\u001f`,
	'"': `\\\"`, '\\': `\\\\`,
}

func unsafeBytes(ptr *byte, size int) []byte {
	if size <= 0 {
		return nil
	}
	return unsafe.Slice(ptr, size)
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func spaceScalar(src *byte, size int) int {
	b := unsafeBytes(src, size)
	for i, c := range b {
		if !isJSONSpace(c) {
			return i
		}
	}
	return len(b)
}

func nonSpaceMaskScalar(src *byte, size int) (mask uint64) {
	b := unsafeBytes(src, minInt(size, 64))
	for i, c := range b {
		if !isJSONSpace(c) {
			mask |= uint64(1) << uint(i)
		}
	}
	return mask
}

func traversalMasksScalar(src *byte, size int, validate uint64) (events, nonspace uint64) {
	b := unsafeBytes(src, minInt(size, 64))
	strict := validate != 0
	for i, c := range b {
		bit := uint64(1) << uint(i)
		if c == '"' || c == '\\' || (strict && c < 0x20) {
			events |= bit
		}
		if !isJSONSpace(c) {
			nonspace |= bit
		}
	}
	return events, nonspace
}

func pathMasksScalar(src *byte, size int) (events, nonspace, boundaries uint64) {
	b := unsafeBytes(src, minInt(size, 64))
	for i, c := range b {
		bit := uint64(1) << uint(i)
		if c == '"' || c == '\\' {
			events |= bit
		}
		sp := isJSONSpace(c)
		if !sp {
			nonspace |= bit
		}
		if sp || c == ',' || c == '}' || c == ']' {
			boundaries |= bit
		}
	}
	return events, nonspace, boundaries
}

func scanStringScalar(src *byte, size int, strict bool) (end, escape, errorPos int, err types.ParsingError) {
	b := unsafeBytes(src, size)
	first := -1
	for i := 0; i < len(b); {
		c := b[i]
		if c == '"' {
			return i + 1, first, -1, 0
		}
		if c != '\\' {
			if strict && c < 0x20 {
				return i, first, i, types.ERR_INVALID_CHAR
			}
			i++
			continue
		}
		if first < 0 {
			first = i
		}
		if i+1 == len(b) {
			break
		}
		if !strict {
			i += 2
			continue
		}
		switch b[i+1] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			i += 2
		case 'u':
			if len(b)-i < 6 {
				return len(b), first, len(b), types.ERR_EOF
			}
			for j := i + 2; j < i+6; j++ {
				if scalarHexDigits[b[j]] == 0 {
					return i, first, i, types.ERR_INVALID_CHAR
				}
			}
			i += 6
		default:
			return i, first, i, types.ERR_INVALID_CHAR
		}
	}
	return len(b), first, len(b), types.ERR_EOF
}

func scanStringRelaxedScalar(src *byte, size int) (end, escape, errorPos int, err types.ParsingError) {
	return scanStringScalar(src, size, false)
}

func scanStringStrictScalar(src *byte, size int) (end, escape, errorPos int, err types.ParsingError) {
	return scanStringScalar(src, size, true)
}

func scanStringBlockScalar(src *byte, size int, strict bool) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	end, escape, errorPos, err = scanStringScalar(src, size, strict)
	if err != 0 || end >= size {
		return end, escape, errorPos, err, 0, 0
	}
	tailWidth = minInt(63, size-end)
	tail := unsafeBytes(src, size)[end : end+tailWidth]
	for i, c := range tail {
		if c == '"' || c == '\\' {
			tailEvents |= uint64(1) << uint(i)
		}
	}
	return end, escape, errorPos, 0, tailEvents, tailWidth
}

func scanStringRelaxedBlockScalar(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	return scanStringBlockScalar(src, size, false)
}

func scanStringStrictBlockScalar(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	return scanStringBlockScalar(src, size, true)
}

func skipContainerScalar(src *byte, size int, open uint64) (end int, err types.ParsingError) {
	b := unsafeBytes(src, size)
	openB := byte(open)
	closeB := openB + 2
	depth := 1
	inside := false
	escaped := false
	for i, c := range b {
		if c == '\\' {
			escaped = !escaped
			continue
		}
		if c == '"' && !escaped {
			inside = !inside
		}
		escaped = false
		if inside {
			continue
		}
		if c == openB {
			depth++
		} else if c == closeB {
			depth--
			if depth == 0 {
				return i + 1, 0
			}
		}
	}
	return len(b), types.ERR_EOF
}

func numberScanScalar(src *byte, size int, mode uint64) (end int, err types.ParsingError) {
	b := unsafeBytes(src, size)
	if mode == 2 {
		for i, c := range b {
			switch c {
			case ' ', '\t', '\n', '\r', '}', ']', ',':
				return i, 0
			}
		}
		return len(b), 0
	}

	const invalid, eof = types.ERR_INVALID_CHAR, types.ERR_EOF
	i := 0
	if len(b) != 0 && b[0] == '-' {
		i++
	}
	if i == len(b) {
		return i, eof
	}
	if b[i] < '0' || b[i] > '9' {
		return i, invalid
	}
	if mode == 1 {
		if b[i] == '0' && (i+1 == len(b) || (b[i+1] != '.' && b[i+1] != 'e' && b[i+1] != 'E')) {
			return i + 1, 0
		}
		dotPos, expPos, signPos := -1, -1, -1
		for ; i < len(b); i++ {
			c := b[i]
			if c >= '0' && c <= '9' {
				continue
			}
			switch c {
			case '.':
				if dotPos >= 0 {
					return i, invalid
				}
				dotPos = i
			case 'e', 'E':
				if expPos >= 0 {
					return i, invalid
				}
				expPos = i
			case '+', '-':
				if signPos >= 0 {
					return i, invalid
				}
				signPos = i
			default:
				goto checkStrict
			}
		}
	checkStrict:
		switch b[i-1] {
		case '.', 'e', 'E', '+', '-':
			return i - 1, invalid
		}
		if signPos >= 0 && (expPos < 0 || expPos != signPos-1) {
			return signPos, invalid
		}
		if dotPos >= 0 && expPos >= 0 {
			if dotPos > expPos {
				return dotPos, invalid
			}
			if dotPos+1 == expPos {
				return expPos, invalid
			}
		}
		return i, 0
	}

	if b[i] == '0' {
		i++
	} else {
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i == len(b) {
			return i, eof
		}
		if b[i] < '0' || b[i] > '9' {
			return i, invalid
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i == len(b) {
			return i, eof
		}
		if b[i] < '0' || b[i] > '9' {
			return i, invalid
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	return i, 0
}

func parseHex4Scalar(b []byte) (cp uint32, badIdx int, ok bool) {
	d0 := uint32(scalarHexDigits[b[0]])
	if d0 == 0 {
		return 0, 0, false
	}
	d1 := uint32(scalarHexDigits[b[1]])
	if d1 == 0 {
		return 0, 1, false
	}
	d2 := uint32(scalarHexDigits[b[2]])
	if d2 == 0 {
		return 0, 2, false
	}
	d3 := uint32(scalarHexDigits[b[3]])
	if d3 == 0 {
		return 0, 3, false
	}
	return ((d0 & 0x0f) << 12) | ((d1 & 0x0f) << 8) | ((d2 & 0x0f) << 4) | (d3 & 0x0f), -1, true
}

func emitUTF8Codepoint(out []byte, w int, cp uint32) int {
	switch {
	case cp < 0x80:
		out[w] = byte(cp)
		return w + 1
	case cp < 0x800:
		out[w] = byte(0xc0 | (cp >> 6))
		out[w+1] = byte(0x80 | (cp & 0x3f))
		return w + 2
	case cp < 0x10000:
		out[w] = byte(0xe0 | (cp >> 12))
		out[w+1] = byte(0x80 | ((cp >> 6) & 0x3f))
		out[w+2] = byte(0x80 | (cp & 0x3f))
		return w + 3
	default:
		out[w] = byte(0xf0 | (cp >> 18))
		out[w+1] = byte(0x80 | ((cp >> 12) & 0x3f))
		out[w+2] = byte(0x80 | ((cp >> 6) & 0x3f))
		out[w+3] = byte(0x80 | (cp & 0x3f))
		return w + 4
	}
}

func stringDecodeScalar(src *byte, size int, dst *byte, mode uint64) (end, written, escaped, errorPos, code int) {
	doubleUnquote := (mode & 1) != 0
	unicodeReplace := (mode & 2) != 0
	quotedInput := (mode & 4) != 0
	rejectControl := (mode & 8) != 0

	eofCode := 1
	if quotedInput {
		eofCode = 5
	}
	if size <= 0 {
		if quotedInput {
			return 0, 0, 0, 0, eofCode
		}
		return 0, 0, 0, -1, 0
	}

	in := unsafeBytes(src, size)
	out := unsafeBytes(dst, size)
	inPlace := src == dst
	r, w := 0, 0

	for r < size {
		c := in[r]
		if c != '\\' {
			if quotedInput {
				if c == '"' {
					return r + 1, w, escaped, -1, 0
				}
				if rejectControl && c < 0x20 {
					return r, w, escaped, r, 1
				}
			}
			if !inPlace || w != r {
				out[w] = c
			}
			r++
			w++
			continue
		}

		escaped = 1
		escPos := r
		if size-r < 2 {
			return size, w, escaped, size, eofCode
		}
		r += 2
		if doubleUnquote {
			if r >= size {
				return size, w, escaped, size, eofCode
			}
			if in[r-1] == '\\' {
				if in[r] == '\\' {
					if size-r < 2 {
						return size, w, escaped, size, eofCode
					}
					if in[r+1] != '\\' && in[r+1] != '"' {
						return r + 1, w, escaped, r + 1, 2
					}
					r++
				}
				r++
			}
		}

		letter := in[r-1]
		if letter != 'u' {
			var mapped byte
			if letter < 128 {
				mapped = scalarSimpleEscapes[letter]
			}
			if mapped == 0 {
				if quotedInput {
					return escPos, w, escaped, escPos, 2
				}
				return r - 1, w, escaped, r - 1, 3
			}
			out[w] = mapped
			w++
			continue
		}

		if size-r < 4 {
			return size, w, escaped, size, eofCode
		}
		cp, badIdx, ok := parseHex4Scalar(in[r : r+4])
		if !ok {
			if quotedInput {
				return escPos, w, escaped, escPos, 12
			}
			pos := r + badIdx
			return pos, w, escaped, pos, 2
		}
		r += 4

	codepoint:
		if cp < 0xd800 || cp >= 0xe000 {
			w = emitUTF8Codepoint(out, w, cp)
			continue
		}

		if doubleUnquote {
			if r >= size {
				if !unicodeReplace {
					return size, w, escaped, size, eofCode
				}
				w = emitUTF8Codepoint(out, w, 0xfffd)
				continue
			}
			if in[r] == '\\' {
				r++
			}
		}

		if size-r < 6 || cp >= 0xdc00 || in[r] != '\\' || in[r+1] != 'u' {
			if unicodeReplace {
				w = emitUTF8Codepoint(out, w, 0xfffd)
				continue
			}
			if quotedInput {
				return escPos, w, escaped, escPos, 12
			}
			pos := r - 4
			if doubleUnquote {
				pos--
			}
			return pos, w, escaped, pos, 4
		}

		escPos = r
		cp2, badIdx2, ok2 := parseHex4Scalar(in[r+2 : r+6])
		if !ok2 {
			if quotedInput {
				return escPos, w, escaped, escPos, 12
			}
			pos := r + 2 + badIdx2
			return pos, w, escaped, pos, 2
		}
		r += 6
		if cp2 >= 0xdc00 && cp2 < 0xe000 {
			combined := ((cp - 0xd800) << 10) + (cp2 - 0xdc00) + 0x10000
			w = emitUTF8Codepoint(out, w, combined)
			continue
		}
		if unicodeReplace {
			w = emitUTF8Codepoint(out, w, 0xfffd)
			cp = cp2
			goto codepoint
		}
		if quotedInput {
			return escPos, w, escaped, escPos, 12
		}
		pos := r - 4
		return pos, w, escaped, pos, 4
	}

	if quotedInput {
		return size, w, escaped, size, eofCode
	}
	return size, w, escaped, -1, 0
}

func utf8ScanScalar(src *byte, size, base int, positions *int, limit int) (next, count int, full bool) {
	if size <= 0 {
		return base, 0, false
	}
	b := unsafeBytes(src, size)
	var out []int
	if limit > 0 {
		out = unsafe.Slice(positions, limit)
	}
	i := 0
	for i < len(b) {
		if b[i] < 0x80 {
			i++
			continue
		}
		r, width := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && width == 1 {
			if count >= limit {
				return base + i, count, true
			}
			out[count] = base + i
			count++
			i++
			continue
		}
		i += width
	}
	return base + len(b), count, false
}

func quoteScalar(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int) {
	if size <= 0 {
		return 0, 0
	}
	if capacity < 0 {
		capacity = 0
	}
	in := unsafeBytes(src, size)
	out := unsafeBytes(dst, capacity)
	table := &scalarQuoteSingle
	if flags&types.F_DOUBLE_UNQUOTE != 0 {
		table = &scalarQuoteDouble
	}
	for i := 0; i < len(in); i++ {
		c := in[i]
		esc := table[c]
		if esc == "" {
			if written >= len(out) {
				return ^i, written
			}
			out[written] = c
			written++
			continue
		}
		if len(out)-written < len(esc) {
			return ^i, written
		}
		copy(out[written:], esc)
		written += len(esc)
	}
	return len(in), written
}

func quoteHTMLScalar(src *byte, size int, dst *byte, capacity int) (consumed, written int) {
	if size <= 0 {
		return 0, 0
	}
	if capacity < 0 {
		capacity = 0
	}
	in := unsafeBytes(src, size)
	out := unsafeBytes(dst, capacity)
	for i := 0; i < len(in); {
		c := in[i]
		var esc string
		step := 1
		switch c {
		case '<':
			esc = `\u003c`
		case '>':
			esc = `\u003e`
		case '&':
			esc = `\u0026`
		case 0xe2:
			if i+2 < len(in) && in[i+1] == 0x80 {
				switch in[i+2] {
				case 0xa8:
					esc = `\u2028`
					step = 3
				case 0xa9:
					esc = `\u2029`
					step = 3
				}
			}
		default:
			esc = scalarQuoteSingle[c]
		}
		if esc == "" {
			if written >= len(out) {
				return ^i, written
			}
			out[written] = c
			written++
			i++
			continue
		}
		if len(out)-written < len(esc) {
			return ^i, written
		}
		copy(out[written:], esc)
		written += len(esc)
		i += step
	}
	return len(in), written
}

func htmlEscapeScalar(src *byte, size int, dst *byte, capacity int) (consumed, written int) {
	if size <= 0 {
		return 0, 0
	}
	if capacity < 0 {
		capacity = 0
	}
	in := unsafeBytes(src, size)
	out := unsafeBytes(dst, capacity)
	for i := 0; i < len(in); {
		c := in[i]
		var esc string
		step := 1
		switch c {
		case '<':
			esc = `\u003c`
		case '>':
			esc = `\u003e`
		case '&':
			esc = `\u0026`
		case 0xe2:
			if i+2 < len(in) && in[i+1] == 0x80 {
				switch in[i+2] {
				case 0xa8:
					esc = `\u2028`
					step = 3
				case 0xa9:
					esc = `\u2029`
					step = 3
				}
			}
		}
		if esc == "" {
			if written >= len(out) {
				return ^i, written
			}
			out[written] = c
			written++
			i++
			continue
		}
		if len(out)-written < len(esc) {
			return ^i, written
		}
		copy(out[written:], esc)
		written += len(esc)
		i += step
	}
	return len(in), written
}
