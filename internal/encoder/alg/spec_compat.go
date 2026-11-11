//go:build ((!amd64 && !arm64) || go1.28 || !go1.16 || (arm64 && !go1.20)) && (!riscv64 || !go1.26 || go1.28 || !riscv64.rva22u64)

/**
 * Copyright 2024 ByteDance Inc.
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

package alg

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

// Valid validates json and returns first non-blank character position,
// if it is only one valid json value.
// Otherwise returns invalid character position using start.
//
// Note: it does not check for the invalid UTF-8 characters.
func Valid(data []byte) (ok bool, start int) {
	ok = json.Valid(data)
	return ok, 0
}

func Quote(e []byte, s string, double bool) []byte {
	if len(s) == 0 {
		if double {
			return append(e, `"\"\""`...)
		}
		return append(e, `""`...)
	}

	b := e
	ss := len(e)
	e = append(e, '"')
	start := 0

	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if rt.SafeSet[b] {
				i++
				continue
			}
			if start < i {
				e = append(e, s[start:i]...)
			}
			e = append(e, '\\')
			switch b {
			case '\\', '"':
				e = append(e, b)
			case '\n':
				e = append(e, 'n')
			case '\r':
				e = append(e, 'r')
			case '\t':
				e = append(e, 't')
			default:
				// This encodes bytes < 0x20 except for \t, \n and \r.
				// If escapeHTML is set, it also escapes <, >, and &
				// because they can lead to security holes when
				// user-controlled strings are rendered into JSON
				// and served to some browsers.
				e = append(e, `u00`...)
				e = append(e, rt.Hex[b>>4])
				e = append(e, rt.Hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		// if correct && c == utf8.RuneError && size == 1 {
		// 	if start < i {
		// 		e = append(e, s[start:i]...)
		// 	}
		// 	e = append(e, `\ufffd`...)
		// 	i += size
		// 	start = i
		// 	continue
		// }
		if c == '\u2028' || c == '\u2029' {
			if start < i {
				e = append(e, s[start:i]...)
			}
			e = append(e, `\u202`...)
			e = append(e, rt.Hex[c&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}

	if start < len(s) {
		e = append(e, s[start:]...)
	}
	e = append(e, '"')

	if double {
		return strconv.AppendQuote(b, string(e[ss:]))
	} else {
		return e
	}
}

func HtmlEscape(dst []byte, src []byte) []byte {
	buf := bytes.NewBuffer(dst)
	json.HTMLEscape(buf, src)
	return buf.Bytes()
}

func F64toa(buf []byte, v float64) []byte {
	bs := bytes.NewBuffer(buf)
	_ = json.NewEncoder(bs).Encode(v)
	return bs.Bytes()
}

func F32toa(buf []byte, v float32) []byte {
	bs := bytes.NewBuffer(buf)
	_ = json.NewEncoder(bs).Encode(v)
	return bs.Bytes()
}

func I64toa(buf []byte, v int64) []byte {
	return strconv.AppendInt(buf, int64(v), 10)
}

func U64toa(buf []byte, v uint64) []byte {
	return strconv.AppendUint(buf, v, 10)
}

func QuoteKey(e []byte, s string) []byte {
	e = Quote(e, s, false)
	return append(e, ':')
}

func QuoteWithPaddedKey(e []byte, keyPtr *byte, keyLen int, val string) []byte {
	e = append(e, unsafe.Slice(keyPtr, keyLen)...)
	return Quote(e, val, false)
}

func AppendPaddedKeyI64(e []byte, keyPtr *byte, keyLen int, v int64) []byte {
	if keyLen > 0 {
		e = append(e, unsafe.Slice(keyPtr, keyLen)...)
	}
	return I64toa(e, v)
}

func AppendPaddedKeyU64(e []byte, keyPtr *byte, keyLen int, v uint64) []byte {
	if keyLen > 0 {
		e = append(e, unsafe.Slice(keyPtr, keyLen)...)
	}
	return U64toa(e, v)
}

func QuoteWithPaddedKeyFlags(buf []byte, keyPtr *byte, keyLen int, val string, sanitize uint64) []byte {
	if sanitize == 0 {
		return QuoteWithPaddedKey(buf, keyPtr, keyLen, val)
	}
	nb := len(val)
	if cap(buf)-len(buf) >= 34+nb && keyLen <= 32 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dk := (*[4]uint64)(dp)
		dk[0], dk[1] = sk[0], sk[1]
		if keyLen > 16 {
			dk[2], dk[3] = sk[2], sk[3]
		}
		dp = unsafe.Add(dp, keyLen)
		*(*byte)(dp) = '"'
		if nb == 0 {
			*(*byte)(unsafe.Add(dp, 1)) = '"'
			return buf[:len(buf)+keyLen+2]
		}
		sp := unsafe.Pointer(unsafe.StringData(val))
		copied := copySafeHTMLPrefix(sp, nb, unsafe.Add(dp, 1), sanitize)
		if copied == nb {
			*(*byte)(unsafe.Add(dp, nb+1)) = '"'
			return buf[:len(buf)+keyLen+nb+2]
		}
		buf = buf[:len(buf)+keyLen+1+copied]
		return appendQuoteSanitizedTail(buf, val[copied:], sanitize, 0)
	}
	buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
	buf = append(buf, '"')
	if nb == 0 {
		return append(buf, '"')
	}
	return appendQuoteSanitizedTail(buf, val, sanitize, 0)
}

func QuoteFlags(buf []byte, val string, double bool, sanitize uint64) []byte {
	if sanitize == 0 {
		return Quote(buf, val, double)
	}
	if !double {
		nb := len(val)
		if cap(buf)-len(buf) < nb+2 {
			buf = rt.GuardSlice2(buf, nb+2)
		}
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		*(*byte)(dp) = '"'
		if nb == 0 {
			*(*byte)(unsafe.Add(dp, 1)) = '"'
			return buf[:len(buf)+2]
		}
		sp := unsafe.Pointer(unsafe.StringData(val))
		copied := copySafeHTMLPrefix(sp, nb, unsafe.Add(dp, 1), sanitize)
		if copied == nb {
			*(*byte)(unsafe.Add(dp, nb+1)) = '"'
			return buf[:len(buf)+nb+2]
		}
		buf = buf[:len(buf)+1+copied]
		return appendQuoteSanitizedTail(buf, val[copied:], sanitize, 0)
	}
	mark := len(buf)
	buf = Quote(buf, val, true)
	return SanitizeAppended(buf, mark, sanitize)
}

func QuoteKeyFlags(buf []byte, val string, sanitize uint64) []byte {
	if sanitize == 0 {
		return QuoteKey(buf, val)
	}
	nb := len(val)
	if cap(buf)-len(buf) < nb+3 {
		buf = rt.GuardSlice2(buf, nb+3)
	}
	dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
	*(*byte)(dp) = '"'
	if nb == 0 {
		*(*byte)(unsafe.Add(dp, 1)) = '"'
		*(*byte)(unsafe.Add(dp, 2)) = ':'
		return buf[:len(buf)+3]
	}
	sp := unsafe.Pointer(unsafe.StringData(val))
	copied := copySafeHTMLPrefix(sp, nb, unsafe.Add(dp, 1), sanitize)
	if copied == nb {
		*(*byte)(unsafe.Add(dp, nb+1)) = '"'
		*(*byte)(unsafe.Add(dp, nb+2)) = ':'
		return buf[:len(buf)+nb+3]
	}
	buf = buf[:len(buf)+1+copied]
	return appendQuoteSanitizedTail(buf, val[copied:], sanitize, ':')
}
