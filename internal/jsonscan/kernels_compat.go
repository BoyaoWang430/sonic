//go:build sonic_compat || (!(amd64 && go1.20) && !(arm64 && go1.20) && !(riscv64 && go1.26 && riscv64.rva22u64) && !(sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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
	stdutf8 "unicode/utf8"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

func kernTraversalMasks(src string, start int, validate bool) (events, nonspace uint64, width int) {
	width = len(src) - start
	if width > 64 {
		width = 64
	}
	for i := 0; i < width; i++ {
		c := src[start+i]
		if c == '"' || c == '\\' || (validate && c < 0x20) {
			events |= uint64(1) << uint(i)
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			nonspace |= uint64(1) << uint(i)
		}
	}
	return events, nonspace, width
}

func kernScanString(src string, quotePos int, rejectCtrl bool) (end, escape, errPos int, err types.ParsingError) {
	base := quotePos + 1
	first := -1
	for i := base; i < len(src); {
		c := src[i]
		if c == '"' {
			return i + 1, first, -1, 0
		}
		if c != '\\' {
			if rejectCtrl && c < 0x20 {
				return i, first, i, types.ERR_INVALID_CHAR
			}
			i++
			continue
		}
		if first < 0 {
			first = i
		}
		if i+1 >= len(src) {
			break
		}
		if !rejectCtrl {
			i += 2
			continue
		}
		switch src[i+1] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			i += 2
		case 'u':
			if len(src)-i < 6 {
				return len(src), first, len(src), types.ERR_EOF
			}
			for j := i + 2; j < i+6; j++ {
				if hexDigitTable[src[j]] == 0 {
					return i, first, i, types.ERR_INVALID_CHAR
				}
			}
			i += 6
		default:
			return i, first, i, types.ERR_INVALID_CHAR
		}
	}
	return len(src), first, len(src), types.ERR_EOF
}

func kernUnquoteRaw(src string, dst []byte, replaceUnicode bool) (written, rawErrPos int, perr types.ParsingError) {
	w := 0
	for i := 0; i < len(src); {
		c := src[i]
		if c != '\\' {
			dst[w] = c
			w++
			i++
			continue
		}
		if i+1 >= len(src) {
			return w, len(src), types.ERR_EOF
		}
		ch := src[i+1]
		if ch != 'u' {
			mapped := simpleEscapeTable[ch]
			if mapped == 0 {
				return w, i + 1, types.ERR_INVALID_ESCAPE
			}
			dst[w] = mapped
			w++
			i += 2
			continue
		}
		if i+6 > len(src) {
			return w, len(src), types.ERR_EOF
		}
		for k := 0; k < 4; k++ {
			if hexDigitTable[src[i+2+k]] == 0 {
				return w, i + 2 + k, types.ERR_INVALID_CHAR
			}
		}
		r0, _ := parseHex16(src[i+2 : i+6])
		i += 6
		for {
			if r0 < 0xd800 || r0 > 0xdfff {
				w += stdutf8.EncodeRune(dst[w:], rune(r0))
				break
			}
			if r0 > 0xdbff || i+6 > len(src) || src[i] != '\\' || src[i+1] != 'u' {
				if !replaceUnicode {
					return w, i - 4, types.ERR_INVALID_UNICODE
				}
				w += stdutf8.EncodeRune(dst[w:], stdutf8.RuneError)
				break
			}
			for k := 0; k < 4; k++ {
				if hexDigitTable[src[i+2+k]] == 0 {
					return w, i + 2 + k, types.ERR_INVALID_CHAR
				}
			}
			r1, _ := parseHex16(src[i+2 : i+6])
			i += 6
			if r1 >= 0xdc00 && r1 <= 0xdfff {
				cp := ((r0 - 0xd800) << 10) + (r1 - 0xdc00) + 0x10000
				w += stdutf8.EncodeRune(dst[w:], rune(cp))
				break
			}
			if !replaceUnicode {
				return w, i - 4, types.ERR_INVALID_UNICODE
			}
			w += stdutf8.EncodeRune(dst[w:], stdutf8.RuneError)
			r0 = r1
		}
	}
	return w, -1, 0
}

func kernScanNumberPrefix(src string, start int) (end int, err types.ParsingError) {
	return number.Scan(src, start)
}

func kernScanNumberStrict(src string, start int) (end int, err types.ParsingError) {
	end, err = number.Scan(src, start)
	if err != 0 {
		return end, err
	}
	isBareZero := end > start && src[end-1] == '0' && (end == start+1 || (end == start+2 && src[start] == '-'))
	if !isBareZero && end < len(src) {
		switch src[end] {
		case '.', 'e', 'E', '+', '-':
			return end, types.ERR_INVALID_CHAR
		}
	}
	return end, 0
}

func kernSkipContainerRelaxed(src string, openPos int) (end int, err types.ParsingError) {
	openB := src[openPos]
	closeB := openB + 2
	depth := 1
	inside := false
	escaped := false
	for i := openPos + 1; i < len(src); i++ {
		c := src[i]
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
	return len(src), types.ERR_EOF
}

func kernSkipStringRelaxed(content string) (end int, err types.ParsingError) {
	escaped := false
	for i := 0; i < len(content); i++ {
		c := content[i]
		if c == '\\' {
			escaped = !escaped
			continue
		}
		if c == '"' && !escaped {
			return i + 1, 0
		}
		escaped = false
	}
	return len(content), types.ERR_EOF
}

func kernSkipNumberRelaxed(src string, start int) int {
	for i := start; i < len(src); i++ {
		switch src[i] {
		case ' ', '\t', '\n', '\r', '}', ']', ',':
			return i
		}
	}
	return len(src)
}

func kernValidateUTF8(src string) bool {
	return stdutf8.ValidString(src)
}

func kernLexBatchUnobserved(src string, pos int, dst []types.LexBlockMask, flags types.LexFlags, carry *types.LexBlockCarry, _ bool) types.LexBatchOut {
	return types.LexBatchScalar(src, pos, dst, flags, carry)
}
