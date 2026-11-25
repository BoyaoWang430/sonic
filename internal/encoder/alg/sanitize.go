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

package alg

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/bits"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

const (
	SanitizeMask = (1 << BitEscapeHTML) | (1 << BitValidateString)

	saniLSB64     = 0x0101010101010101
	saniMSB64     = 0x8080808080808080
	saniSp64      = 0x2020202020202020
	saniGtSp64    = 0x2121212121212121
	saniQuot64    = 0x2222222222222222
	saniAmp64     = 0x2626262626262626
	saniLt64      = 0x3c3c3c3c3c3c3c3c
	saniBsl64     = 0x5c5c5c5c5c5c5c5c
	saniE264      = 0xe2e2e2e2e2e2e2e2
	saniAmpQuot64 = 0x0404040404040404
	saniLtGt64    = 0x0202020202020202
	hexDigits     = "0123456789abcdef"
)

func quoteSafeHTMLBits64(w uint64) uint64 {
	aq := (w &^ saniAmpQuot64) ^ saniQuot64
	lg := (w &^ saniLtGt64) ^ saniLt64
	bs := w ^ saniBsl64
	return w | (w - saniSp64) | (aq - saniLSB64) | (lg - saniLSB64) | (bs - saniLSB64)
}

func quoteSafeHTMLNoValBits64(w uint64) uint64 {
	ctrl := (w - saniSp64) & ^w
	aq := (w &^ saniAmpQuot64) ^ saniQuot64
	lg := (w &^ saniLtGt64) ^ saniLt64
	bs := w ^ saniBsl64
	e2 := w ^ saniE264
	return ctrl | ((aq - saniLSB64) & ^aq) | ((lg - saniLSB64) & ^lg) | ((bs - saniLSB64) & ^bs) | ((e2 - saniLSB64) & ^e2)
}

func isSafeHTMLASCIIByte(b byte) bool {
	return b >= 0x20 && b < 0x80 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&'
}

func isSafeHTMLNoValByte(b byte) bool {
	return b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' && b != 0xe2
}

func copySafeHTMLNoValPrefix(sp unsafe.Pointer, nb int, dp unsafe.Pointer) int {
	if nb >= 16 {
		off := 0
		for off+16 <= nb {
			w0 := *(*uint64)(unsafe.Add(sp, off))
			w1 := *(*uint64)(unsafe.Add(sp, off+8))
			if ((quoteSafeHTMLNoValBits64(w0) | quoteSafeHTMLNoValBits64(w1)) & saniMSB64) != 0 {
				if (quoteSafeHTMLNoValBits64(w0) & saniMSB64) == 0 {
					*(*uint64)(unsafe.Add(dp, off)) = w0
					return off + 8
				}
				return off
			}
			*(*uint64)(unsafe.Add(dp, off)) = w0
			*(*uint64)(unsafe.Add(dp, off+8)) = w1
			off += 16
		}
		if off == nb {
			return nb
		}
		w0 := *(*uint64)(unsafe.Add(sp, nb-16))
		w1 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteSafeHTMLNoValBits64(w0) | quoteSafeHTMLNoValBits64(w1)) & saniMSB64) == 0 {
			*(*uint64)(unsafe.Add(dp, nb-16)) = w0
			*(*uint64)(unsafe.Add(dp, nb-8)) = w1
			return nb
		}
		return off
	}
	if nb >= 8 {
		w0 := *(*uint64)(sp)
		w1 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteSafeHTMLNoValBits64(w0) | quoteSafeHTMLNoValBits64(w1)) & saniMSB64) == 0 {
			*(*uint64)(dp) = w0
			*(*uint64)(unsafe.Add(dp, nb-8)) = w1
			return nb
		}
		return 0
	}
	if nb >= 4 {
		u0 := *(*uint32)(sp)
		u1 := *(*uint32)(unsafe.Add(sp, nb-4))
		w := uint64(u0) | (uint64(u1) << 32)
		if (quoteSafeHTMLNoValBits64(w) & saniMSB64) == 0 {
			*(*uint32)(dp) = u0
			*(*uint32)(unsafe.Add(dp, nb-4)) = u1
			return nb
		}
		return 0
	}
	b0 := *(*byte)(sp)
	b1 := *(*byte)(unsafe.Add(sp, nb>>1))
	b2 := *(*byte)(unsafe.Add(sp, nb-1))
	if isSafeHTMLNoValByte(b0) && isSafeHTMLNoValByte(b1) && isSafeHTMLNoValByte(b2) {
		*(*byte)(dp) = b0
		*(*byte)(unsafe.Add(dp, nb>>1)) = b1
		*(*byte)(unsafe.Add(dp, nb-1)) = b2
		return nb
	}
	return 0
}

func copySafeHTMLPrefix(sp unsafe.Pointer, nb int, dp unsafe.Pointer, sanitize uint64) int {
	if sanitize&(1<<BitValidateString) == 0 {
		return copySafeHTMLNoValPrefix(sp, nb, dp)
	}
	return copySafeHTMLASCIIPrefix(sp, nb, dp)
}

// copySafeHTMLASCIIPrefix copies as many 8/16-byte safe ASCII chunks as possible and returns
// nb when the entire string is copied, or the safe prefix length (< nb) where a slow byte sits.
func copySafeHTMLASCIIPrefix(sp unsafe.Pointer, nb int, dp unsafe.Pointer) int {
	if nb >= 16 {
		off := 0
		for off+16 <= nb {
			w0 := *(*uint64)(unsafe.Add(sp, off))
			w1 := *(*uint64)(unsafe.Add(sp, off+8))
			if ((quoteSafeHTMLBits64(w0) | quoteSafeHTMLBits64(w1)) & saniMSB64) != 0 {
				if (quoteSafeHTMLBits64(w0) & saniMSB64) == 0 {
					*(*uint64)(unsafe.Add(dp, off)) = w0
					return off + 8
				}
				return off
			}
			*(*uint64)(unsafe.Add(dp, off)) = w0
			*(*uint64)(unsafe.Add(dp, off+8)) = w1
			off += 16
		}
		if off == nb {
			return nb
		}
		w0 := *(*uint64)(unsafe.Add(sp, nb-16))
		w1 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteSafeHTMLBits64(w0) | quoteSafeHTMLBits64(w1)) & saniMSB64) == 0 {
			*(*uint64)(unsafe.Add(dp, nb-16)) = w0
			*(*uint64)(unsafe.Add(dp, nb-8)) = w1
			return nb
		}
		return off
	}
	if nb >= 8 {
		w0 := *(*uint64)(sp)
		w1 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteSafeHTMLBits64(w0) | quoteSafeHTMLBits64(w1)) & saniMSB64) == 0 {
			*(*uint64)(dp) = w0
			*(*uint64)(unsafe.Add(dp, nb-8)) = w1
			return nb
		}
		return 0
	}
	if nb >= 4 {
		u0 := *(*uint32)(sp)
		u1 := *(*uint32)(unsafe.Add(sp, nb-4))
		w := uint64(u0) | (uint64(u1) << 32)
		if (quoteSafeHTMLBits64(w) & saniMSB64) == 0 {
			*(*uint32)(dp) = u0
			*(*uint32)(unsafe.Add(dp, nb-4)) = u1
			return nb
		}
		return 0
	}
	b0 := *(*byte)(sp)
	b1 := *(*byte)(unsafe.Add(sp, nb>>1))
	b2 := *(*byte)(unsafe.Add(sp, nb-1))
	if isSafeHTMLASCIIByte(b0) && isSafeHTMLASCIIByte(b1) && isSafeHTMLASCIIByte(b2) {
		*(*byte)(dp) = b0
		*(*byte)(unsafe.Add(dp, nb>>1)) = b1
		*(*byte)(unsafe.Add(dp, nb-1)) = b2
		return nb
	}
	return 0
}

func appendQuoteSanitizedTail(buf []byte, val string, sanitize uint64, suffix byte) []byte {
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0
	nb := len(val)
	if cap(buf)-len(buf) < nb+nb>>2+32 {
		buf = rt.GuardSlice2(buf, nb+nb>>2+32)
	}
	bPtr := unsafe.Pointer(unsafe.SliceData(buf))
	bLen := len(buf)
	bCap := cap(buf)
	sp := unsafe.Pointer(unsafe.StringData(val))
	i := 0
	for i < nb {
		if bCap-bLen < 24 {
			buf = buf[:bLen]
			buf = rt.GuardSlice2(buf, (nb-i)*2+32)
			bPtr = unsafe.Pointer(unsafe.SliceData(buf))
			bCap = cap(buf)
		}
		if validateUTF8 {
			for i+8 <= nb && bCap-bLen >= 16 {
				w := *(*uint64)(unsafe.Add(sp, i))
				if (quoteSafeHTMLBits64(w) & saniMSB64) != 0 {
					break
				}
				*(*uint64)(unsafe.Add(bPtr, bLen)) = w
				bLen += 8
				i += 8
			}
		} else {
			for i+8 <= nb && bCap-bLen >= 16 {
				w := *(*uint64)(unsafe.Add(sp, i))
				if (quoteSafeHTMLNoValBits64(w) & saniMSB64) != 0 {
					break
				}
				*(*uint64)(unsafe.Add(bPtr, bLen)) = w
				bLen += 8
				i += 8
			}
		}
		if i >= nb {
			break
		}
		if bCap-bLen < 16 {
			continue
		}
		dp := unsafe.Add(bPtr, bLen)
		c := *(*byte)(unsafe.Add(sp, i))
		if c < 0x80 {
			if c >= 0x20 && c != '"' && c != '\\' && (!escapeHTML || (c != '<' && c != '>' && c != '&')) {
				*(*byte)(dp) = c
				bLen++
				i++
				continue
			}
			switch c {
			case '"', '\\':
				*(*byte)(dp) = '\\'
				*(*byte)(unsafe.Add(dp, 1)) = c
				bLen += 2
			case '\t':
				*(*[2]byte)(dp) = [2]byte{'\\', 't'}
				bLen += 2
			case '\n':
				*(*[2]byte)(dp) = [2]byte{'\\', 'n'}
				bLen += 2
			case '\r':
				*(*[2]byte)(dp) = [2]byte{'\\', 'r'}
				bLen += 2
			default:
				*(*[4]byte)(dp) = [4]byte{'\\', 'u', '0', '0'}
				*(*byte)(unsafe.Add(dp, 4)) = hexDigits[c>>4]
				*(*byte)(unsafe.Add(dp, 5)) = hexDigits[c&0x0f]
				bLen += 6
			}
			i++
			continue
		}
		if !validateUTF8 {
			if escapeHTML && c == 0xe2 && i+2 < nb && *(*byte)(unsafe.Add(sp, i+1)) == 0x80 {
				b2 := *(*byte)(unsafe.Add(sp, i+2))
				if (b2 &^ 1) == 0xa8 {
					*(*[5]byte)(dp) = [5]byte{'\\', 'u', '2', '0', '2'}
					*(*byte)(unsafe.Add(dp, 5)) = hexDigits[b2&0x0f]
					bLen += 6
					i += 3
					continue
				}
			}
			*(*byte)(dp) = c
			bLen++
			i++
			continue
		}
		if c >= 0xe0 && c <= 0xef && i+2 < nb {
			b1 := *(*byte)(unsafe.Add(sp, i+1))
			b2 := *(*byte)(unsafe.Add(sp, i+2))
			if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (c != 0xe0 || b1 >= 0xa0) && (c != 0xed || b1 < 0xa0) {
				if escapeHTML && c == 0xe2 && b1 == 0x80 && (b2&^1) == 0xa8 {
					*(*[5]byte)(dp) = [5]byte{'\\', 'u', '2', '0', '2'}
					*(*byte)(unsafe.Add(dp, 5)) = hexDigits[b2&0x0f]
					bLen += 6
				} else {
					*(*byte)(dp) = c
					*(*byte)(unsafe.Add(dp, 1)) = b1
					*(*byte)(unsafe.Add(dp, 2)) = b2
					bLen += 3
				}
				i += 3
				continue
			}
		} else if c >= 0xc2 && c <= 0xdf && i+1 < nb {
			b1 := *(*byte)(unsafe.Add(sp, i+1))
			if (b1 & 0xc0) == 0x80 {
				*(*byte)(dp) = c
				*(*byte)(unsafe.Add(dp, 1)) = b1
				bLen += 2
				i += 2
				continue
			}
		} else if c >= 0xf0 && c <= 0xf4 && i+3 < nb {
			b1 := *(*byte)(unsafe.Add(sp, i+1))
			b2 := *(*byte)(unsafe.Add(sp, i+2))
			b3 := *(*byte)(unsafe.Add(sp, i+3))
			if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (b3&0xc0) == 0x80 && (c != 0xf0 || b1 >= 0x90) && (c != 0xf4 || b1 < 0x90) {
				*(*byte)(dp) = c
				*(*byte)(unsafe.Add(dp, 1)) = b1
				*(*byte)(unsafe.Add(dp, 2)) = b2
				*(*byte)(unsafe.Add(dp, 3)) = b3
				bLen += 4
				i += 4
				continue
			}
		}
		if validateUTF8 {
			*(*[6]byte)(dp) = [6]byte{'\\', 'u', 'f', 'f', 'f', 'd'}
			bLen += 6
		} else {
			*(*byte)(dp) = c
			bLen++
		}
		i++
	}
	if suffix != 0 {
		if bCap-bLen >= 2 {
			dp := unsafe.Add(bPtr, bLen)
			*(*byte)(dp) = '"'
			*(*byte)(unsafe.Add(dp, 1)) = suffix
			return buf[:bLen+2]
		}
		return append(buf[:bLen], '"', suffix)
	}
	if bCap-bLen >= 1 {
		*(*byte)(unsafe.Add(bPtr, bLen)) = '"'
		return buf[:bLen+1]
	}
	return append(buf[:bLen], '"')
}

func hasHTMLChars(b []byte) bool {
	n := len(b)
	if n == 0 {
		return false
	}
	ptr := unsafe.Pointer(rt.SliceData(b))
	i := 0
	for i+8 <= n {
		w := *(*uint64)(unsafe.Add(ptr, i))
		lg := (w &^ saniLtGt64) ^ saniLt64
		amp := w ^ saniAmp64
		e2 := w ^ saniE264
		if ((((lg - saniLSB64) & ^lg) | ((amp - saniLSB64) & ^amp) | ((e2 - saniLSB64) & ^e2)) & saniMSB64) == 0 {
			i += 8
			continue
		}
		for j := i; j < i+8; j++ {
			c := b[j]
			if c == '<' || c == '>' || c == '&' || (c == 0xe2 && j+2 < n && b[j+1] == 0x80 && (b[j+2]&^1) == 0xa8) {
				return true
			}
		}
		i += 8
	}
	for ; i < n; i++ {
		c := b[i]
		if c == '<' || c == '>' || c == '&' || (c == 0xe2 && i+2 < n && b[i+1] == 0x80 && (b[i+2]&^1) == 0xa8) {
			return true
		}
	}
	return false
}

func NeedsSanitize(s string) bool {
	if !utf8.ValidateString(s) {
		return true
	}
	return hasHTMLChars(rt.Str2Mem(s))
}

func SanitizeAppended(buf []byte, mark int, sanitize uint64) []byte {
	seg := buf[mark:]
	if len(seg) == 0 {
		return buf
	}
	if sanitize&(1<<BitValidateString) != 0 && !utf8.Validate(seg) {
		tmp := vars.NewBytes()
		*tmp = utf8.CorrectWith((*tmp)[:0], seg, `\ufffd`)
		buf = append(buf[:mark], (*tmp)...)
		vars.FreeBytes(tmp)
		seg = buf[mark:]
	}
	if sanitize&(1<<BitEscapeHTML) != 0 && hasHTMLChars(seg) {
		tmp := vars.NewBytes()
		*tmp = HtmlEscape((*tmp)[:0], seg)
		buf = append(buf[:mark], (*tmp)...)
		vars.FreeBytes(tmp)
	}
	return buf
}

func fragmentSafeHTMLASCIIBits64(w uint64) uint64 {
	lg := (w &^ saniLtGt64) ^ saniLt64
	amp := w ^ saniAmp64
	return w | ((lg - saniLSB64) & ^lg) | ((amp - saniLSB64) & ^amp)
}

func appendUTF8OrInvalidByte(bPtr unsafe.Pointer, bLen int, sp unsafe.Pointer, i int, nb int, escapeHTML bool, validateUTF8 bool) (int, int) {
	dp := unsafe.Add(bPtr, bLen)
	c := *(*byte)(unsafe.Add(sp, i))
	if c >= 0xe0 && c <= 0xef && i+2 < nb {
		b1 := *(*byte)(unsafe.Add(sp, i+1))
		b2 := *(*byte)(unsafe.Add(sp, i+2))
		if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (c != 0xe0 || b1 >= 0xa0) && (c != 0xed || b1 < 0xa0) {
			if escapeHTML && c == 0xe2 && b1 == 0x80 && (b2&^1) == 0xa8 {
				*(*[5]byte)(dp) = [5]byte{'\\', 'u', '2', '0', '2'}
				*(*byte)(unsafe.Add(dp, 5)) = hexDigits[b2&0x0f]
				bLen += 6
			} else {
				*(*byte)(dp) = c
				*(*byte)(unsafe.Add(dp, 1)) = b1
				*(*byte)(unsafe.Add(dp, 2)) = b2
				bLen += 3
			}
			return bLen, i + 3
		}
	} else if c >= 0xc2 && c <= 0xdf && i+1 < nb {
		b1 := *(*byte)(unsafe.Add(sp, i+1))
		if (b1 & 0xc0) == 0x80 {
			*(*byte)(dp) = c
			*(*byte)(unsafe.Add(dp, 1)) = b1
			bLen += 2
			return bLen, i + 2
		}
	} else if c >= 0xf0 && c <= 0xf4 && i+3 < nb {
		b1 := *(*byte)(unsafe.Add(sp, i+1))
		b2 := *(*byte)(unsafe.Add(sp, i+2))
		b3 := *(*byte)(unsafe.Add(sp, i+3))
		if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (b3&0xc0) == 0x80 && (c != 0xf0 || b1 >= 0x90) && (c != 0xf4 || b1 < 0x90) {
			*(*byte)(dp) = c
			*(*byte)(unsafe.Add(dp, 1)) = b1
			*(*byte)(unsafe.Add(dp, 2)) = b2
			*(*byte)(unsafe.Add(dp, 3)) = b3
			bLen += 4
			return bLen, i + 4
		}
	}
	if validateUTF8 {
		*(*[6]byte)(dp) = [6]byte{'\\', 'u', 'f', 'f', 'f', 'd'}
		bLen += 6
	} else {
		*(*byte)(dp) = c
		bLen++
	}
	return bLen, i + 1
}

func overlapsSpare(dst []byte, src []byte) bool {
	if cap(dst)-len(dst) == 0 || len(src) == 0 {
		return false
	}
	dPtr := uintptr(unsafe.Pointer(unsafe.SliceData(dst)))
	dstStart := dPtr + uintptr(len(dst))
	dstEnd := dPtr + uintptr(cap(dst))
	srcStart := uintptr(unsafe.Pointer(unsafe.SliceData(src)))
	srcEnd := srcStart + uintptr(len(src))
	return srcStart < dstEnd && dstStart < srcEnd
}

func isExactSpareSuffix(dst []byte, src []byte) bool {
	if len(src) == 0 {
		return true
	}
	if cap(dst)-len(dst) < len(src) || cap(dst) == 0 {
		return false
	}
	dstBase := unsafe.Pointer(unsafe.SliceData(dst))
	return unsafe.Pointer(unsafe.SliceData(src)) == unsafe.Add(dstBase, len(dst))
}

func DetachIfOverlappingSpare(dst []byte, src []byte) []byte {
	if overlapsSpare(dst, src) {
		cp := make([]byte, len(src))
		copy(cp, src)
		return cp
	}
	return src
}

// NeedsSanitizeBytes reports whether src contains any byte that would be expanded
// or replaced under sanitize (HTML characters <, >, &, U+2028, U+2029 when
// BitEscapeHTML is set, or invalid UTF-8 when BitValidateString is set).
func NeedsSanitizeBytes(src []byte, sanitize uint64) bool {
	nb := len(src)
	if nb == 0 || sanitize == 0 {
		return false
	}
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0
	sp := unsafe.Pointer(rt.SliceData(src))
	i := 0
	for i+8 <= nb {
		w := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(sp, i))[:])
		if (fragmentSafeHTMLASCIIBits64(w) & saniMSB64) == 0 {
			i += 8
			continue
		}
		break
	}
	for i < nb {
		c := src[i]
		if c < 0x80 {
			if escapeHTML && (c == '<' || c == '>' || c == '&') {
				return true
			}
			i++
			continue
		}
		if c >= 0xe0 && c <= 0xef && i+2 < nb {
			b1 := src[i+1]
			b2 := src[i+2]
			if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (c != 0xe0 || b1 >= 0xa0) && (c != 0xed || b1 < 0xa0) {
				if escapeHTML && c == 0xe2 && b1 == 0x80 && (b2&^1) == 0xa8 {
					return true
				}
				i += 3
				continue
			}
		} else if c >= 0xc2 && c <= 0xdf && i+1 < nb {
			if (src[i+1] & 0xc0) == 0x80 {
				i += 2
				continue
			}
		} else if c >= 0xf0 && c <= 0xf4 && i+3 < nb {
			b1 := src[i+1]
			b2 := src[i+2]
			b3 := src[i+3]
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

// AppendSanitizedBytes appends src to dst in a single streaming pass while applying
// optional HTML escaping (<, >, &, U+2028, U+2029) and UTF-8 replacement (\ufffd).
func AppendSanitizedBytes(dst []byte, src []byte, sanitize uint64) []byte {
	nb := len(src)
	if nb == 0 {
		return dst
	}
	if sanitize == 0 || !NeedsSanitizeBytes(src, sanitize) {
		if isExactSpareSuffix(dst, src) {
			return dst[:len(dst)+nb]
		}
		if !overlapsSpare(dst, src) {
			return append(dst, src...)
		}
	}
	src = DetachIfOverlappingSpare(dst, src)
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0
	if cap(dst)-len(dst) < nb+24 {
		dst = rt.GuardSlice2(dst, nb+nb>>3+32)
	}
	bPtr := unsafe.Pointer(unsafe.SliceData(dst))
	bLen := len(dst)
	bCap := cap(dst)
	sp := unsafe.Pointer(rt.SliceData(src))
	i := 0
	for i < nb {
		if bCap-bLen < 24 {
			dst = dst[:bLen]
			dst = rt.GuardSlice2(dst, (nb-i)*2+32)
			bPtr = unsafe.Pointer(unsafe.SliceData(dst))
			bCap = cap(dst)
		}
		for i+8 <= nb && bCap-bLen >= 16 {
			w := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(sp, i))[:])
			mask := fragmentSafeHTMLASCIIBits64(w) & saniMSB64
			binary.LittleEndian.PutUint64((*[8]byte)(unsafe.Add(bPtr, bLen))[:], w)
			if mask != 0 {
				k := bits.TrailingZeros64(mask) >> 3
				bLen += k
				i += k
				break
			}
			bLen += 8
			i += 8
		}
		if i >= nb {
			break
		}
		if bCap-bLen < 16 {
			continue
		}
		c := *(*byte)(unsafe.Add(sp, i))
		if c < 0x80 {
			dp := unsafe.Add(bPtr, bLen)
			if escapeHTML && (c == '<' || c == '>' || c == '&') {
				*(*[4]byte)(dp) = [4]byte{'\\', 'u', '0', '0'}
				*(*byte)(unsafe.Add(dp, 4)) = hexDigits[c>>4]
				*(*byte)(unsafe.Add(dp, 5)) = hexDigits[c&0x0f]
				bLen += 6
			} else {
				*(*byte)(dp) = c
				bLen++
			}
			i++
			continue
		}
		bLen, i = appendUTF8OrInvalidByte(bPtr, bLen, sp, i, nb, escapeHTML, validateUTF8)
	}
	return dst[:bLen]
}

func quoteEscapeBits64Strict(w uint64) uint64 {
	q := w ^ saniQuot64
	bs := w ^ saniBsl64
	return ((w - saniSp64) & ^w) | ((q - saniLSB64) & ^q) | ((bs - saniLSB64) & ^bs)
}

var compactMachinePool = sync.Pool{
	New: func() interface{} {
		return &jsonscan.Machine{}
	},
}

var (
	errCompactMaxDepth    = errors.New("sonic: exceeded max container depth")
	errCompactSpanLimit   = errors.New("sonic: json source span overflow")
	errCompactUnsupported = errors.New("sonic: json compact validation rejected input")
)

type compactScanResult struct {
	valid         bool
	hasSpace      bool
	needsRewrite  bool
	firstDirtyPos int
	code          jsonscan.ScanError
}

// scanCompactJSON validates src as a complete RFC 8259 document via the shared
// jsonscan.Machine (CompactFirstDirtyProfile + ModeCompactObserve),
// reporting whether src is valid JSON and the byte offset of the first dirty
// byte (whitespace or HTML/UTF-8 rewrite). Because AppendCompactedJSON always
// applies full sanitize flags from firstDirtyPos onward, rewrite-need scans
// stop once firstDirtyPos >= 0 while full syntax/escape validation still runs.
func scanCompactJSON(src []byte, sanitize uint64) compactScanResult {
	if len(src) == 0 {
		return compactScanResult{code: jsonscan.ErrEOF}
	}
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0

	m := compactMachinePool.Get().(*jsonscan.Machine)
	m.Reset(jsonscan.CompactFirstDirtyProfile(escapeHTML, validateUTF8), jsonscan.ModeCompactObserve)
	res := m.Step(rt.Mem2Str(src), true)
	obs := m.CompactObs
	compactMachinePool.Put(m)

	if res.Reason != jsonscan.StopDone {
		return compactScanResult{code: res.Code}
	}
	return compactScanResult{
		valid:         true,
		hasSpace:      obs.HasSpace,
		needsRewrite:  obs.NeedsRewrite,
		firstDirtyPos: obs.FirstDirtyPos,
	}
}

// compactStdSyntaxError is the single cold stdlib error adapter used when
// scanCompactJSON rejects invalid JSON, preserving encoding/json.Compact's exact
// error type and message without re-running stdlib on valid fast paths. If Sonic
// rejected input due to an explicit resource limit (e.g. container depth or span
// limit) that stdlib happened to accept, it returns an explicit non-empty
// resource error rather than constructing an empty &json.SyntaxError{}.
func compactStdSyntaxError(src []byte, code jsonscan.ScanError) error {
	var errBuf bytes.Buffer
	if err := json.Compact(&errBuf, src); err != nil {
		return err
	}
	switch code {
	case jsonscan.ErrDepthOverflow:
		return errCompactMaxDepth
	case jsonscan.ErrSpanOverflow:
		return errCompactSpanLimit
	default:
		return errCompactUnsupported
	}
}

// rewriteValidatedCompactJSON compacts src[start:] into dst after scanCompactJSON
// has already proven src is syntactically valid and src[:start] is clean. It
// focuses solely on dropping inter-token whitespace, copying non-string tokens
// (including raw number and literal spellings) verbatim, and rewriting strings
// in a single pointer-resident loop without per-token append or call overhead.
func rewriteValidatedCompactJSON(dst []byte, src []byte, start int, sanitize uint64) []byte {
	nb := len(src)
	i := start
	if i >= nb {
		return dst
	}
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0
	bPtr := unsafe.Pointer(unsafe.SliceData(dst))
	bLen := len(dst)
	bCap := cap(dst)
	sp := unsafe.Pointer(rt.SliceData(src))

	for i < nb {
		c := *(*byte)(unsafe.Add(sp, i))
		if c <= ' ' {
			i++
			for i < nb && *(*byte)(unsafe.Add(sp, i)) <= ' ' {
				i++
			}
			continue
		}
		if bCap-bLen < 32 {
			dst = dst[:bLen]
			dst = rt.GuardSlice2(dst, (nb-i)*2+32)
			bPtr = unsafe.Pointer(unsafe.SliceData(dst))
			bCap = cap(dst)
		}
		if c == '"' {
			*(*byte)(unsafe.Add(bPtr, bLen)) = '"'
			bLen++
			i++
			for i < nb {
				if bCap-bLen < 24 {
					dst = dst[:bLen]
					dst = rt.GuardSlice2(dst, (nb-i)*2+32)
					bPtr = unsafe.Pointer(unsafe.SliceData(dst))
					bCap = cap(dst)
				}
				if sanitize == 0 {
					for i+8 <= nb && bCap-bLen >= 16 {
						w := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(sp, i))[:])
						mask := quoteEscapeBits64Strict(w) & saniMSB64
						binary.LittleEndian.PutUint64((*[8]byte)(unsafe.Add(bPtr, bLen))[:], w)
						if mask != 0 {
							k := bits.TrailingZeros64(mask) >> 3
							bLen += k
							i += k
							break
						}
						bLen += 8
						i += 8
					}
				} else {
					for i+8 <= nb && bCap-bLen >= 16 {
						w := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(sp, i))[:])
						mask := quoteSafeHTMLBits64(w) & saniMSB64
						binary.LittleEndian.PutUint64((*[8]byte)(unsafe.Add(bPtr, bLen))[:], w)
						if mask != 0 {
							k := bits.TrailingZeros64(mask) >> 3
							bLen += k
							i += k
							break
						}
						bLen += 8
						i += 8
					}
				}
				if i >= nb {
					break
				}
				if bCap-bLen < 16 {
					continue
				}
				sc := *(*byte)(unsafe.Add(sp, i))
				dp := unsafe.Add(bPtr, bLen)
				if sc == '"' {
					*(*byte)(dp) = '"'
					bLen++
					i++
					break
				}
				if sc == '\\' {
					esc := *(*byte)(unsafe.Add(sp, i+1))
					if esc == 'u' {
						*(*[4]byte)(dp) = *(*[4]byte)(unsafe.Add(sp, i))
						*(*[2]byte)(unsafe.Add(dp, 4)) = *(*[2]byte)(unsafe.Add(sp, i+4))
						bLen += 6
						i += 6
					} else {
						*(*byte)(dp) = '\\'
						*(*byte)(unsafe.Add(dp, 1)) = esc
						bLen += 2
						i += 2
					}
					continue
				}
				if sc < 0x80 {
					if escapeHTML && (sc == '<' || sc == '>' || sc == '&') {
						*(*[4]byte)(dp) = [4]byte{'\\', 'u', '0', '0'}
						*(*byte)(unsafe.Add(dp, 4)) = hexDigits[sc>>4]
						*(*byte)(unsafe.Add(dp, 5)) = hexDigits[sc&0x0f]
						bLen += 6
					} else {
						*(*byte)(dp) = sc
						bLen++
					}
					i++
					continue
				}
				bLen, i = appendUTF8OrInvalidByte(bPtr, bLen, sp, i, nb, escapeHTML, validateUTF8)
			}
			continue
		}
		*(*byte)(unsafe.Add(bPtr, bLen)) = c
		bLen++
		i++
		for i < nb && bCap-bLen >= 16 {
			nc := *(*byte)(unsafe.Add(sp, i))
			if nc <= ' ' || nc == '"' {
				break
			}
			*(*byte)(unsafe.Add(bPtr, bLen)) = nc
			bLen++
			i++
		}
	}
	return dst[:bLen]
}

// ValidStrictFullDoc validates data as a complete JSON document using the
// shared jsonscan.Machine ModeValidateOnly + StrictProfile (zero Unesc growth).
func ValidStrictFullDoc(data []byte, validateString, rejectLoneSurrogate bool) bool {
	if len(data) == 0 {
		return false
	}
	m := compactMachinePool.Get().(*jsonscan.Machine)
	m.Reset(jsonscan.StrictProfile(validateString, validateString, rejectLoneSurrogate, false, false, true), jsonscan.ModeValidateOnly)
	res := m.Step(rt.Mem2Str(data), true)
	compactMachinePool.Put(m)
	return res.Reason == jsonscan.StopDone
}

// AppendCompactedJSON validates src as a single JSON value via scanCompactJSON
// and appends its compacted (and optionally HTML-escaped / UTF-8-sanitized)
// representation to dst. Clean, already-compact inputs finalize in place (or via
// a single copy), dirty valid inputs copy their clean prefix and run linear token
// compaction without a second grammar state machine, and invalid inputs preserve
// dst and delegate to compactStdSyntaxError.
func AppendCompactedJSON(dst []byte, src []byte, sanitize uint64) ([]byte, error) {
	res := scanCompactJSON(src, sanitize)
	if !res.valid {
		return dst, compactStdSyntaxError(src, res.code)
	}
	if res.firstDirtyPos < 0 {
		if isExactSpareSuffix(dst, src) {
			return dst[:len(dst)+len(src)], nil
		}
		src = DetachIfOverlappingSpare(dst, src)
		return append(dst, src...), nil
	}

	src = DetachIfOverlappingSpare(dst, src)
	nb := len(src)
	if cap(dst)-len(dst) < nb+24 {
		dst = rt.GuardSlice2(dst, nb+nb>>3+32)
	}
	if res.firstDirtyPos > 0 {
		dst = append(dst, src[:res.firstDirtyPos]...)
	}
	return rewriteValidatedCompactJSON(dst, src, res.firstDirtyPos, sanitize), nil
}

func AppendSanitizedLiteral(buf []byte, s string, sanitize uint64) []byte {
	return AppendSanitizedBytes(buf, rt.Str2Mem(s), sanitize)
}
