/*
 * Copyright 2022 ByteDance Inc.
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
	"encoding/base64"
	"encoding/binary"
	"math/bits"
	"runtime"
	"strconv"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/internal/utils"
	"github.com/bytedance/sonic/unquote"
)

var bytesNull = []byte("null")

const (
	strNull     = "null"
	bytesTrue   = "true"
	bytesFalse  = "false"
	bytesObject = "{}"
	bytesArray  = "[]"
)

//go:nocheckptr
func skipBlank(src string, pos int) int {
	// Keep scan addresses as integers. A pointer at or past the end of the
	// allocation may be shared across calls after inlining and scanned by GC.
	base := uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr)
	sp := base + uintptr(pos)
	se := base + uintptr(len(src))

	for sp < se {
		if !utils.IsSpace(*(*byte)(unsafe.Pointer(sp))) {
			break
		}
		sp += 1
	}
	if sp >= se {
		runtime.KeepAlive(src)
		return -int(types.ERR_EOF)
	}
	ret := int(sp - base)
	runtime.KeepAlive(src)
	return ret
}

func decodeNull(src string, pos int) (ret int) {
	ret = pos + 4
	if ret > len(src) {
		return -int(types.ERR_EOF)
	}
	if src[pos:ret] == strNull {
		return ret
	} else {
		return -int(types.ERR_INVALID_CHAR)
	}
}

func decodeTrue(src string, pos int) (ret int) {
	ret = pos + 4
	if ret > len(src) {
		return -int(types.ERR_EOF)
	}
	if src[pos:ret] == bytesTrue {
		return ret
	} else {
		return -int(types.ERR_INVALID_CHAR)
	}

}

func decodeFalse(src string, pos int) (ret int) {
	ret = pos + 5
	if ret > len(src) {
		return -int(types.ERR_EOF)
	}
	if src[pos:ret] == bytesFalse {
		return ret
	}
	return -int(types.ERR_INVALID_CHAR)
}

//go:nocheckptr
func decodeString(src string, pos int) (ret int, v string) {
	ret, ep := skipString(src, pos)
	if ep == -1 {
		(*rt.GoString)(unsafe.Pointer(&v)).Ptr = rt.IndexChar(src, pos+1)
		(*rt.GoString)(unsafe.Pointer(&v)).Len = ret - pos - 2
		return ret, v
	}

	result, err := unquote.String(src[pos:ret])
	if err != 0 {
		return -int(types.ERR_INVALID_CHAR), ""
	}

	runtime.KeepAlive(src)
	return ret, result
}

func decodeBinary(src string, pos int) (ret int, v []byte) {
	var vv string
	ret, vv = decodeString(src, pos)
	if ret < 0 {
		return ret, nil
	}
	var err error
	v, err = base64.StdEncoding.DecodeString(vv)
	if err != nil {
		return -int(types.ERR_INVALID_CHAR), nil
	}
	return ret, v
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

//go:nocheckptr
func decodeInt64(src string, pos int) (ret int, v int64, err error) {
	base := uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr)
	sp := base + uintptr(pos)
	ss := sp
	se := base + uintptr(len(src))
	if sp >= se {
		return -int(types.ERR_EOF), 0, nil
	}

	if c := *(*byte)(unsafe.Pointer(sp)); c == '-' {
		sp += 1
	}
	if sp == se {
		return -int(types.ERR_EOF), 0, nil
	}

	for ; sp < se; sp += uintptr(1) {
		if !isDigit(*(*byte)(unsafe.Pointer(sp))) {
			break
		}
	}

	if sp < se {
		if c := *(*byte)(unsafe.Pointer(sp)); c == '.' || c == 'e' || c == 'E' {
			return -int(types.ERR_INVALID_NUMBER_FMT), 0, nil
		}
	}

	var vv string
	ret = int(uintptr(sp) - uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr))
	(*rt.GoString)(unsafe.Pointer(&vv)).Ptr = unsafe.Pointer(ss)
	(*rt.GoString)(unsafe.Pointer(&vv)).Len = ret - pos

	v, err = strconv.ParseInt(vv, 10, 64)
	if err != nil {
		//NOTICE: allow overflow here
		if err.(*strconv.NumError).Err == strconv.ErrRange {
			return ret, 0, err
		}
		return -int(types.ERR_INVALID_CHAR), 0, err
	}

	runtime.KeepAlive(src)
	return ret, v, nil
}

func isNumberChars(c byte) bool {
	return (c >= '0' && c <= '9') || c == '+' || c == '-' || c == 'e' || c == 'E' || c == '.'
}

//go:nocheckptr
func decodeFloat64(src string, pos int) (ret int, v float64, err error) {
	base := uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr)
	sp := base + uintptr(pos)
	ss := sp
	se := base + uintptr(len(src))
	if sp >= se {
		return -int(types.ERR_EOF), 0, nil
	}

	if c := *(*byte)(unsafe.Pointer(sp)); c == '-' {
		sp += 1
	}
	if sp == se {
		return -int(types.ERR_EOF), 0, nil
	}

	for ; sp < se; sp += uintptr(1) {
		if !isNumberChars(*(*byte)(unsafe.Pointer(sp))) {
			break
		}
	}

	var vv string
	ret = int(uintptr(sp) - uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr))
	(*rt.GoString)(unsafe.Pointer(&vv)).Ptr = unsafe.Pointer(ss)
	(*rt.GoString)(unsafe.Pointer(&vv)).Len = ret - pos

	v, err = strconv.ParseFloat(vv, 64)
	if err != nil {
		//NOTICE: allow overflow here
		if err.(*strconv.NumError).Err == strconv.ErrRange {
			return ret, 0, err
		}
		return -int(types.ERR_INVALID_CHAR), 0, err
	}

	runtime.KeepAlive(src)
	return ret, v, nil
}

func decodeValue(src string, pos int, skipnum bool) (ret int, v types.JsonState) {
	pos = skipBlank(src, pos)
	if pos < 0 {
		return pos, types.JsonState{Vt: types.ValueType(pos)}
	}
	switch c := src[pos]; c {
	case 'n':
		ret = decodeNull(src, pos)
		if ret < 0 {
			return ret, types.JsonState{Vt: types.ValueType(ret)}
		}
		return ret, types.JsonState{Vt: types.V_NULL}
	case '"':
		var ep int
		ret, ep = skipString(src, pos)
		if ret < 0 {
			return ret, types.JsonState{Vt: types.ValueType(ret)}
		}
		return ret, types.JsonState{Vt: types.V_STRING, Iv: int64(pos + 1), Ep: ep}
	case '{':
		return pos + 1, types.JsonState{Vt: types.V_OBJECT}
	case '[':
		return pos + 1, types.JsonState{Vt: types.V_ARRAY}
	case 't':
		ret = decodeTrue(src, pos)
		if ret < 0 {
			return ret, types.JsonState{Vt: types.ValueType(ret)}
		}
		return ret, types.JsonState{Vt: types.V_TRUE}
	case 'f':
		ret = decodeFalse(src, pos)
		if ret < 0 {
			return ret, types.JsonState{Vt: types.ValueType(ret)}
		}
		return ret, types.JsonState{Vt: types.V_FALSE}
	case '-', '+', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if skipnum {
			ret = skipNumber(src, pos)
			if ret >= 0 {
				return ret, types.JsonState{Vt: types.V_DOUBLE, Iv: 0, Ep: pos}
			} else {
				return ret, types.JsonState{Vt: types.ValueType(ret)}
			}
		} else {
			var iv int64
			ret, iv, _ = decodeInt64(src, pos)
			if ret >= 0 {
				return ret, types.JsonState{Vt: types.V_INTEGER, Iv: iv, Ep: pos}
			} else if ret != -int(types.ERR_INVALID_NUMBER_FMT) {
				return ret, types.JsonState{Vt: types.ValueType(ret)}
			}
			var fv float64
			ret, fv, _ = decodeFloat64(src, pos)
			if ret >= 0 {
				return ret, types.JsonState{Vt: types.V_DOUBLE, Dv: fv, Ep: pos}
			} else {
				return ret, types.JsonState{Vt: types.ValueType(ret)}
			}
		}

	default:
		return -int(types.ERR_INVALID_CHAR), types.JsonState{Vt: -types.ValueType(types.ERR_INVALID_CHAR)}
	}
}

//go:nocheckptr
func skipNumber(src string, pos int) (ret int) {
	return utils.SkipNumber(src, pos)
}

const (
	scanByteOnes     uint64 = 0x0101010101010101
	scanByteHighBits uint64 = 0x8080808080808080
)

//go:nocheckptr
func skipString(src string, pos int) (ret int, ep int) {
	if pos+1 >= len(src) {
		return -int(types.ERR_EOF), -1
	}

	base := uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr)
	sp := base + uintptr(pos)
	se := base + uintptr(len(src))

	// not start with quote
	if *(*byte)(unsafe.Pointer(sp)) != '"' {
		runtime.KeepAlive(src)
		return -int(types.ERR_INVALID_CHAR), -1
	}
	sp += 1

	ep = -1

	// Scan escape-free words in bulk. After an escape, periodically probe for
	// another escape-free word and resume here. Back off between failed probes
	// so dense escapes stay in the scalar loop. All loads remain within src.
stringWords:
	for sp < se && se-sp >= 8 {
		// Little-endian order makes the lowest candidate bit the earliest byte,
		// including on big-endian machines. The load may be unaligned.
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
		b := x ^ ('\\' * scanByteOnes)
		if (b-scanByteOnes)&^b&scanByteHighBits == 0 {
			q := x ^ ('"' * scanByteOnes)
			mask := (q - scanByteOnes) &^ q & scanByteHighBits
			// Borrowing may mark later bytes too, but the first match is exact.
			if mask != 0 {
				ret = int(sp + uintptr(bits.TrailingZeros64(mask)>>3) + 1 - base)
				runtime.KeepAlive(src)
				return ret, ep
			}
			sp += 8
			continue
		}
		goto stringEscaped
	}
	for sp < se {
		c := *(*byte)(unsafe.Pointer(sp))
		if c == '\\' {
			if ep == -1 {
				ep = int(sp - base)
			}
			sp += 2
			continue
		}
		sp += 1
		if c == '"' {
			ret = int(sp - base)
			runtime.KeepAlive(src)
			return ret, ep
		}
	}

	goto stringEOF
stringEscaped:
	{
		// Retry after 8 bytes, then 16, 32, 64 and at most 128 on dense escapes.
		step := uintptr(8)
		for sp < se {
			stop := se
			if se-sp > step {
				stop = sp + step
			}
			for sp < stop {
				c := *(*byte)(unsafe.Pointer(sp))
				if c == '\\' {
					if ep == -1 {
						ep = int(sp - base)
					}
					sp += 2
					continue
				}
				sp++
				if c == '"' {
					ret = int(sp - base)
					runtime.KeepAlive(src)
					return ret, ep
				}
			}
			if sp < se && se-sp >= 8 {
				x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
				b := x ^ ('\\' * scanByteOnes)
				if (b-scanByteOnes)&^b&scanByteHighBits == 0 {
					goto stringWords
				}
			}
			if step < 128 {
				step *= 2
			}
		}
	}
stringEOF:

	runtime.KeepAlive(src)
	// not found the closed quote until EOF
	return -int(types.ERR_EOF), -1
}

//go:nocheckptr
func skipPair(src string, pos int, lchar byte, rchar byte) (ret int) {
	if pos+1 >= len(src) {
		return -int(types.ERR_EOF)
	}

	base := uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr)
	sp := base + uintptr(pos)
	se := base + uintptr(len(src))

	if *(*byte)(unsafe.Pointer(sp)) != lchar {
		runtime.KeepAlive(src)
		return -int(types.ERR_INVALID_CHAR)
	}

	sp += 1
	nbrace := 1
	// Keep the 0/1 scan state in machine words to avoid byte-width
	// normalization in the hot loops.
	inquote := uint(0)
	// firstSlash belongs to the word that entered scalar recovery. Do not
	// retry at an earlier closing quote before that backslash is consumed.
	var firstSlash uintptr

	lword, rword := uint64(lchar)*scanByteOnes, uint64(rchar)*scanByteOnes
	// Scan whole words until a block contains a backslash. Escape-heavy strings
	// use the scalar loop, with spaced probes to resume bulk scanning inside
	// long strings as well as after their closing quote.
scanWords:
	for sp < se && se-sp >= 8 {
		// Little-endian order makes the lowest candidate bit the earliest byte,
		// including on big-endian machines. The load may be unaligned.
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
		b := x ^ ('\\' * scanByteOnes)
		if (b-scanByteOnes)&^b&scanByteHighBits == 0 {
			q := x ^ ('"' * scanByteOnes)
			// Only existence matters here: do not enumerate quote candidates.
			quotes := (q - scanByteOnes) &^ q & scanByteHighBits
			if inquote != 0 && quotes == 0 {
				sp += 8
				continue
			}
			l, r := x^lword, x^rword
			mask := ((l-scanByteOnes)&^l | (r-scanByteOnes)&^r) & scanByteHighBits
			// Without delimiters, only the parity of the quote count matters.
			// Keep the common no-event path free of exact-mask work.
			if mask == 0 {
				if quotes != 0 {
					// Unlike the subtract-and-mask idiom, this exact mask has
					// no borrow-generated hits that could change quote parity.
					const lo uint64 = 0x7f7f7f7f7f7f7f7f
					exact := ^(((q & lo) + lo) | q | lo)
					inquote ^= uint(bits.OnesCount64(exact)) & 1
				}
				sp += 8
				continue
			}
			if quotes != 0 {
				const lo uint64 = 0x7f7f7f7f7f7f7f7f
				quotes = ^(((q & lo) + lo) | q | lo)
				// Prefix parity marks the bytes inside a string after each
				// quote. Carry the last byte's state into the next word.
				inside := quotes
				inside ^= inside << 8
				inside ^= inside << 16
				inside ^= inside << 32
				inside ^= 0 - uint64(inquote)
				inquote = uint(inside >> 63)
				// Ignore delimiters inside strings and the quote bytes.
				mask &^= inside | quotes
			}
			// Delimiter masks can still contain borrow-generated hits.
			// Verify each byte and stop at the first balanced close.
			for mask != 0 {
				shift := uint(bits.TrailingZeros64(mask)) & 56
				c := byte(x >> shift)
				mask &= mask - 1
				if c == lchar {
					nbrace++
				} else if c == rchar {
					nbrace--
					if nbrace == 0 {
						ret = int(sp + uintptr(shift>>3) + 1 - base)
						goto done
					}
				}
			}
			sp += 8
			continue
		}
		// Locate the first slash only after the existence test succeeds. Use
		// an exact mask here so the escape-free test stays an existence test.
		const lo uint64 = 0x7f7f7f7f7f7f7f7f
		slashes := ^(((b & lo) + lo) | b | lo)
		firstSlash = sp + uintptr(bits.TrailingZeros64(slashes)>>3)
		goto scanEscaped
	}
	// A partial word or a final recovery window uses the terminal scalar
	// loop, which does not carry the escape-recovery state.
pairTail:
	for sp < se {
		c := *(*byte)(unsafe.Pointer(sp))
		if c == '\\' {
			sp += 2
			continue
		} else if c == '"' {
			inquote ^= 1
		} else if inquote == 0 {
			if c == lchar {
				nbrace++
			} else if c == rchar {
				nbrace--
				if nbrace == 0 {
					ret = int(sp + 1 - base)
					goto done
				}
			}
		}
		sp += 1
	}

	ret = -int(types.ERR_INVALID_CHAR)
	goto done

scanEscaped:
	{
		if inquote != 0 {
			goto escapedString
		}
	escapedOutside:
		for sp < se {
			c := *(*byte)(unsafe.Pointer(sp))
			sp++
			if c == '\\' {
				sp++
				continue
			}
			if c == '"' {
				inquote = 1
				goto escapedString
			}
			if c == lchar {
				nbrace++
			} else if c == rchar {
				nbrace--
				if nbrace == 0 {
					ret = int(sp - base)
					goto done
				}
			}
		}
		goto escapedEOF

	escapedString:
		{
			// Scan one word before retrying so a slash in its last lane is
			// consumed. Back off when escapes remain dense.
			step := uintptr(8)
			// Keep eight probe bytes even if an escape crosses stop.
			for sp < se && se-sp > step+8 {
				stop := sp + step
				for sp < stop {
					c := *(*byte)(unsafe.Pointer(sp))
					sp++
					if c == '\\' {
						sp++
						continue
					}
					if c == '"' {
						goto escapedStringClose
					}
				}
				x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
				b := x ^ ('\\' * scanByteOnes)
				if (b-scanByteOnes)&^b&scanByteHighBits == 0 {
					// Consume the already-loaded probe word before resuming.
					q := x ^ ('"' * scanByteOnes)
					if (q-scanByteOnes)&^q&scanByteHighBits != 0 {
						// Form the position mask only after finding a quote.
						const lo uint64 = 0x7f7f7f7f7f7f7f7f
						mask := ^(((q & lo) + lo) | q | lo)
						sp += uintptr(bits.TrailingZeros64(mask)>>3) + 1
						goto escapedStringClose
					}
					sp += 8
					goto escapedStringWords
				}
				if step < 128 {
					step *= 2
				}
			}
			goto pairTail
		}
	escapedStringWords:
		for sp < se && se-sp >= 8 {
			x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
			b := x ^ ('\\' * scanByteOnes)
			if (b-scanByteOnes)&^b&scanByteHighBits != 0 {
				goto escapedString
			}
			q := x ^ ('"' * scanByteOnes)
			if (q-scanByteOnes)&^q&scanByteHighBits != 0 {
				// Form the position mask only after finding a quote.
				const lo uint64 = 0x7f7f7f7f7f7f7f7f
				mask := ^(((q & lo) + lo) | q | lo)
				sp += uintptr(bits.TrailingZeros64(mask)>>3) + 1
				goto escapedStringClose
			}
			sp += 8
		}
		goto escapedString
	escapedStringClose:
		inquote = 0
		// Only retry after consuming the backslash that triggered recovery.
		if sp > firstSlash && sp < se && se-sp >= 8 {
			x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
			b := x ^ ('\\' * scanByteOnes)
			if (b-scanByteOnes)&^b&scanByteHighBits == 0 {
				// A transition may reread this word; structural parsing stays
				// in one place for both normal scanning and recovery.
				goto scanWords
			}
		}
		goto escapedOutside
	escapedEOF:
	}

	ret = -int(types.ERR_INVALID_CHAR)
done:
	runtime.KeepAlive(src)
	return ret
}

func skipValueFast(src string, pos int) (ret int, start int) {
	pos = skipBlank(src, pos)
	if pos < 0 {
		return pos, -1
	}
	switch c := src[pos]; c {
	case 'n':
		ret = decodeNull(src, pos)
	case '"':
		ret, _ = skipString(src, pos)
	case '{':
		ret = skipPair(src, pos, '{', '}')
	case '[':
		ret = skipPair(src, pos, '[', ']')
	case 't':
		ret = decodeTrue(src, pos)
	case 'f':
		ret = decodeFalse(src, pos)
	case '-', '+', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		ret = skipNumber(src, pos)
	default:
		ret = -int(types.ERR_INVALID_CHAR)
	}
	return ret, pos
}

func skipValue(src string, pos int) (ret int, start int) {
	if uint(pos) >= uint(len(src)) {
		return -int(types.ERR_EOF), -1
	}
	if utils.IsSpace(src[pos]) {
		pos = skipBlank(src, pos+1)
		if pos < 0 {
			return pos, -1
		}
	}
	switch c := src[pos]; c {
	case 'n':
		ret = decodeNull(src, pos)
	case '"':
		ret, _ = skipString(src, pos)
	case '{':
		ret = skipObject(src, pos)
	case '[':
		ret = skipArray(src, pos)
	case 't':
		ret = decodeTrue(src, pos)
	case 'f':
		ret = decodeFalse(src, pos)
	case '-', '+', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		ret = skipNumber(src, pos)
	default:
		ret = -int(types.ERR_INVALID_CHAR)
	}
	return ret, pos
}

// skipObject starts at the opening { already checked by skipValue.
func skipObject(src string, pos int) int {
	pos++
	if uint(pos) >= uint(len(src)) {
		return -int(types.ERR_EOF)
	}
	if utils.IsSpace(src[pos]) {
		pos = skipBlank(src, pos+1)
		if pos < 0 {
			return pos
		}
	}
	if src[pos] == '}' {
		return pos + 1
	}

	for {
		pos, _ = skipString(src, pos)
		if pos < 0 {
			return pos
		}

		pos = skipBlank(src, pos)
		if pos < 0 {
			return pos
		}
		if src[pos] != ':' {
			return -int(types.ERR_INVALID_CHAR)
		}

		pos++
		pos, _ = skipValue(src, pos)
		if pos < 0 {
			return pos
		}

		pos = skipBlank(src, pos)
		if pos < 0 {
			return pos
		}
		if src[pos] == '}' {
			return pos + 1
		}
		if src[pos] != ',' {
			return -int(types.ERR_INVALID_CHAR)
		}

		pos++
		pos = skipBlank(src, pos)
		if pos < 0 {
			return pos
		}

	}
}

// skipArray starts at the opening [ already checked by skipValue.
func skipArray(src string, pos int) int {
	pos++
	if uint(pos) >= uint(len(src)) {
		return -int(types.ERR_EOF)
	}
	if utils.IsSpace(src[pos]) {
		pos = skipBlank(src, pos+1)
		if pos < 0 {
			return pos
		}
	}
	if src[pos] == ']' {
		return pos + 1
	}

	for {
		pos, _ = skipValue(src, pos)
		if pos < 0 {
			return pos
		}

		pos = skipBlank(src, pos)
		if pos < 0 {
			return pos
		}
		if src[pos] == ']' {
			return pos + 1
		}
		if src[pos] != ',' {
			return -int(types.ERR_INVALID_CHAR)
		}
		pos++
	}
}

// DecodeString decodes a JSON string from pos and return golang string.
//   - needEsc indicates if to unescaped escaping chars
//   - hasEsc tells if the returned string has escaping chars
//   - validStr enables validating UTF8 charset
func _DecodeString(src string, pos int, needEsc bool, validStr bool) (v string, ret int, hasEsc bool) {
	p := NewParserObj(src)
	p.p = pos
	switch val := p.decodeValue(); val.Vt {
	case types.V_STRING:
		str := p.s[val.Iv : p.p-1]
		if validStr && !validate_utf8(str) {
			return "", -int(types.ERR_INVALID_UTF8), false
		}
		/* fast path: no escape sequence */
		if val.Ep == -1 {
			return str, p.p, false
		} else if !needEsc {
			return str, p.p, true
		}
		/* unquote the string */
		out, err := unquote.String(str)
		/* check for errors */
		if err != 0 {
			return "", -int(err), true
		} else {
			return out, p.p, true
		}
	default:
		return "", -int(_ERR_UNSUPPORT_TYPE), false
	}
}
