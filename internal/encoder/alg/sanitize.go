package alg

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/vars"
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

func isSafeHTMLASCIIByte(b byte) bool {
	return b >= 0x20 && b < 0x80 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&'
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
		for i+8 <= nb && bCap-bLen >= 16 {
			w := *(*uint64)(unsafe.Add(sp, i))
			if (quoteSafeHTMLBits64(w) & saniMSB64) != 0 {
				break
			}
			*(*uint64)(unsafe.Add(bPtr, bLen)) = w
			bLen += 8
			i += 8
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

const (
	compactPhaseValue uint8 = iota
	compactPhaseAfterValue
	compactPhaseObjKeyOrEnd
	compactPhaseObjKey
	compactPhaseObjColon

	maxCompactNestingDepth = 10000
)

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isJSONSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func quoteEscapeBits64Strict(w uint64) uint64 {
	q := w ^ saniQuot64
	bs := w ^ saniBsl64
	return ((w - saniSp64) & ^w) | ((q - saniLSB64) & ^q) | ((bs - saniLSB64) & ^bs)
}

func scanStrictJSONNumber(src []byte, pos int) (int, bool) {
	n := len(src)
	i := pos
	if i < n && src[i] == '-' {
		i++
	}
	if i >= n {
		return pos, false
	}
	if src[i] == '0' {
		i++
	} else if src[i] >= '1' && src[i] <= '9' {
		i++
		for i < n && src[i] >= '0' && src[i] <= '9' {
			i++
		}
	} else {
		return pos, false
	}
	if i < n && src[i] == '.' {
		i++
		if i >= n || src[i] < '0' || src[i] > '9' {
			return pos, false
		}
		i++
		for i < n && src[i] >= '0' && src[i] <= '9' {
			i++
		}
	}
	if i < n && (src[i] == 'e' || src[i] == 'E') {
		i++
		if i < n && (src[i] == '+' || src[i] == '-') {
			i++
		}
		if i >= n || src[i] < '0' || src[i] > '9' {
			return pos, false
		}
		i++
		for i < n && src[i] >= '0' && src[i] <= '9' {
			i++
		}
	}
	return i, true
}

func appendCompactedJSONStringContent(dst []byte, src []byte, i int, sanitize uint64) ([]byte, int, bool) {
	nb := len(src)
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0
	bPtr := unsafe.Pointer(unsafe.SliceData(dst))
	bLen := len(dst)
	bCap := cap(dst)
	sp := unsafe.Pointer(rt.SliceData(src))

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
			return dst[:bLen], i, false
		}
		c := *(*byte)(unsafe.Add(sp, i))
		dp := unsafe.Add(bPtr, bLen)
		if c == '"' {
			*(*byte)(dp) = '"'
			return dst[:bLen+1], i + 1, true
		}
		if c < 0x20 {
			return dst[:bLen], i, false
		}
		if c == '\\' {
			if i+1 >= nb {
				return dst[:bLen], i, false
			}
			esc := *(*byte)(unsafe.Add(sp, i+1))
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				*(*byte)(dp) = '\\'
				*(*byte)(unsafe.Add(dp, 1)) = esc
				bLen += 2
				i += 2
			case 'u':
				if i+6 > nb || !isHexByte(src[i+2]) || !isHexByte(src[i+3]) || !isHexByte(src[i+4]) || !isHexByte(src[i+5]) {
					return dst[:bLen], i, false
				}
				*(*[4]byte)(dp) = *(*[4]byte)(unsafe.Add(sp, i))
				*(*[2]byte)(unsafe.Add(dp, 4)) = *(*[2]byte)(unsafe.Add(sp, i+4))
				bLen += 6
				i += 6
			default:
				return dst[:bLen], i, false
			}
			continue
		}
		if c < 0x80 {
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
	return dst[:bLen], i, false
}

func scanCleanJSONStringContent(src []byte, i int, sanitize uint64) (int, bool) {
	nb := len(src)
	escapeHTML := sanitize&(1<<BitEscapeHTML) != 0
	validateUTF8 := sanitize&(1<<BitValidateString) != 0
	sp := unsafe.Pointer(rt.SliceData(src))

	for i < nb {
		if sanitize == 0 {
			for i+8 <= nb {
				w := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(sp, i))[:])
				mask := quoteEscapeBits64Strict(w) & saniMSB64
				if mask != 0 {
					i += bits.TrailingZeros64(mask) >> 3
					break
				}
				i += 8
			}
		} else {
			for i+8 <= nb {
				w := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(sp, i))[:])
				mask := quoteSafeHTMLBits64(w) & saniMSB64
				if mask != 0 {
					i += bits.TrailingZeros64(mask) >> 3
					break
				}
				i += 8
			}
		}
		if i >= nb {
			return i, false
		}
		c := src[i]
		if c == '"' {
			return i + 1, true
		}
		if c < 0x20 {
			return i, false
		}
		if c == '\\' {
			if i+1 >= nb {
				return i, false
			}
			switch src[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > nb || !isHexByte(src[i+2]) || !isHexByte(src[i+3]) || !isHexByte(src[i+4]) || !isHexByte(src[i+5]) {
					return i, false
				}
				i += 6
			default:
				return i, false
			}
			continue
		}
		if c < 0x80 {
			if escapeHTML && (c == '<' || c == '>' || c == '&') {
				return i, false
			}
			i++
			continue
		}
		if c >= 0xe0 && c <= 0xef && i+2 < nb {
			b1 := src[i+1]
			b2 := src[i+2]
			if (b1&0xc0) == 0x80 && (b2&0xc0) == 0x80 && (c != 0xe0 || b1 >= 0xa0) && (c != 0xed || b1 < 0xa0) {
				if escapeHTML && c == 0xe2 && b1 == 0x80 && (b2&^1) == 0xa8 {
					return i, false
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
			return i, false
		}
		i++
	}
	return i, false
}

// validateCompactAndCleanJSON reports whether src is a single valid JSON value
// that already has no outer/inter-token whitespace and requires no HTML or UTF-8
// escaping expansion under sanitize.
func validateCompactAndCleanJSON(src []byte, sanitize uint64) bool {
	nb := len(src)
	if nb == 0 {
		return false
	}
	var stackBuf [64]byte
	stack := stackBuf[:0]
	phase := compactPhaseValue
	i := 0

	for i < nb {
		c := src[i]
		if c <= ' ' {
			return false
		}
		switch phase {
		case compactPhaseValue:
			switch c {
			case '{':
				if len(stack) >= maxCompactNestingDepth {
					return false
				}
				stack = append(stack, '{')
				phase = compactPhaseObjKeyOrEnd
				i++
			case '[':
				if len(stack) >= maxCompactNestingDepth {
					return false
				}
				stack = append(stack, '[')
				i++
				if i < nb && src[i] == ']' {
					stack = stack[:len(stack)-1]
					phase = compactPhaseAfterValue
					i++
				}
			case '"':
				var ok bool
				i, ok = scanCleanJSONStringContent(src, i+1, sanitize)
				if !ok {
					return false
				}
				phase = compactPhaseAfterValue
			case 't':
				if i+4 > nb || src[i+1] != 'r' || src[i+2] != 'u' || src[i+3] != 'e' {
					return false
				}
				i += 4
				phase = compactPhaseAfterValue
			case 'f':
				if i+5 > nb || src[i+1] != 'a' || src[i+2] != 'l' || src[i+3] != 's' || src[i+4] != 'e' {
					return false
				}
				i += 5
				phase = compactPhaseAfterValue
			case 'n':
				if i+4 > nb || src[i+1] != 'u' || src[i+2] != 'l' || src[i+3] != 'l' {
					return false
				}
				i += 4
				phase = compactPhaseAfterValue
			default:
				endNum, ok := scanStrictJSONNumber(src, i)
				if !ok {
					return false
				}
				i = endNum
				phase = compactPhaseAfterValue
			}
		case compactPhaseObjKeyOrEnd:
			if c == '}' {
				stack = stack[:len(stack)-1]
				phase = compactPhaseAfterValue
				i++
				continue
			}
			if c != '"' {
				return false
			}
			var ok bool
			i, ok = scanCleanJSONStringContent(src, i+1, sanitize)
			if !ok {
				return false
			}
			phase = compactPhaseObjColon
		case compactPhaseObjKey:
			if c != '"' {
				return false
			}
			var ok bool
			i, ok = scanCleanJSONStringContent(src, i+1, sanitize)
			if !ok {
				return false
			}
			phase = compactPhaseObjColon
		case compactPhaseObjColon:
			if c != ':' {
				return false
			}
			phase = compactPhaseValue
			i++
		case compactPhaseAfterValue:
			if len(stack) == 0 {
				return false
			}
			top := stack[len(stack)-1]
			if top == '{' {
				if c == ',' {
					phase = compactPhaseObjKey
					i++
				} else if c == '}' {
					stack = stack[:len(stack)-1]
					i++
				} else {
					return false
				}
			} else {
				if c == ',' {
					phase = compactPhaseValue
					i++
				} else if c == ']' {
					stack = stack[:len(stack)-1]
					i++
				} else {
					return false
				}
			}
		}
	}
	return phase == compactPhaseAfterValue && len(stack) == 0
}

// AppendCompactedJSON validates src as a single JSON value and appends its compacted
// (and optionally HTML-escaped / UTF-8-sanitized) representation directly to dst
// in a single pass. On invalid JSON, dst is restored to its entry length and
// encoding/json.Compact's exact error is returned.
func AppendCompactedJSON(dst []byte, src []byte, sanitize uint64) ([]byte, error) {
	if validateCompactAndCleanJSON(src, sanitize) {
		if isExactSpareSuffix(dst, src) {
			return dst[:len(dst)+len(src)], nil
		}
		if !overlapsSpare(dst, src) {
			return append(dst, src...), nil
		}
	}
	origLen := len(dst)
	src = DetachIfOverlappingSpare(dst, src)
	nb := len(src)
	if cap(dst)-len(dst) < nb+24 {
		dst = rt.GuardSlice2(dst, nb+nb>>3+32)
	}

	var stackBuf [64]byte
	stack := stackBuf[:0]
	phase := compactPhaseValue
	i := 0

	fail := func() ([]byte, error) {
		dst = dst[:origLen]
		var errBuf bytes.Buffer
		if err := json.Compact(&errBuf, src); err != nil {
			return dst, err
		}
		return AppendSanitizedBytes(dst, errBuf.Bytes(), sanitize), nil
	}

	for i < nb {
		c := src[i]
		if c <= ' ' {
			if !isJSONSpaceByte(c) {
				return fail()
			}
			i++
			for i < nb && isJSONSpaceByte(src[i]) {
				i++
			}
			continue
		}

		switch phase {
		case compactPhaseValue:
			switch c {
			case '{':
				if len(stack) >= maxCompactNestingDepth {
					return fail()
				}
				dst = append(dst, '{')
				stack = append(stack, '{')
				phase = compactPhaseObjKeyOrEnd
				i++
			case '[':
				if len(stack) >= maxCompactNestingDepth {
					return fail()
				}
				dst = append(dst, '[')
				stack = append(stack, '[')
				i++
				for i < nb && isJSONSpaceByte(src[i]) {
					i++
				}
				if i < nb && src[i] == ']' {
					dst = append(dst, ']')
					stack = stack[:len(stack)-1]
					phase = compactPhaseAfterValue
					i++
				}
			case '"':
				dst = append(dst, '"')
				var ok bool
				dst, i, ok = appendCompactedJSONStringContent(dst, src, i+1, sanitize)
				if !ok {
					return fail()
				}
				phase = compactPhaseAfterValue
			case 't':
				if i+4 > nb || src[i+1] != 'r' || src[i+2] != 'u' || src[i+3] != 'e' {
					return fail()
				}
				dst = append(dst, "true"...)
				i += 4
				phase = compactPhaseAfterValue
			case 'f':
				if i+5 > nb || src[i+1] != 'a' || src[i+2] != 'l' || src[i+3] != 's' || src[i+4] != 'e' {
					return fail()
				}
				dst = append(dst, "false"...)
				i += 5
				phase = compactPhaseAfterValue
			case 'n':
				if i+4 > nb || src[i+1] != 'u' || src[i+2] != 'l' || src[i+3] != 'l' {
					return fail()
				}
				dst = append(dst, "null"...)
				i += 4
				phase = compactPhaseAfterValue
			default:
				endNum, ok := scanStrictJSONNumber(src, i)
				if !ok {
					return fail()
				}
				dst = append(dst, src[i:endNum]...)
				i = endNum
				phase = compactPhaseAfterValue
			}

		case compactPhaseObjKeyOrEnd:
			if c == '}' {
				dst = append(dst, '}')
				stack = stack[:len(stack)-1]
				phase = compactPhaseAfterValue
				i++
				continue
			}
			if c != '"' {
				return fail()
			}
			dst = append(dst, '"')
			var ok bool
			dst, i, ok = appendCompactedJSONStringContent(dst, src, i+1, sanitize)
			if !ok {
				return fail()
			}
			phase = compactPhaseObjColon

		case compactPhaseObjKey:
			if c != '"' {
				return fail()
			}
			dst = append(dst, '"')
			var ok bool
			dst, i, ok = appendCompactedJSONStringContent(dst, src, i+1, sanitize)
			if !ok {
				return fail()
			}
			phase = compactPhaseObjColon

		case compactPhaseObjColon:
			if c != ':' {
				return fail()
			}
			dst = append(dst, ':')
			phase = compactPhaseValue
			i++

		case compactPhaseAfterValue:
			if len(stack) == 0 {
				return fail()
			}
			top := stack[len(stack)-1]
			if top == '{' {
				if c == ',' {
					dst = append(dst, ',')
					phase = compactPhaseObjKey
					i++
				} else if c == '}' {
					dst = append(dst, '}')
					stack = stack[:len(stack)-1]
					i++
				} else {
					return fail()
				}
			} else {
				if c == ',' {
					dst = append(dst, ',')
					phase = compactPhaseValue
					i++
				} else if c == ']' {
					dst = append(dst, ']')
					stack = stack[:len(stack)-1]
					i++
				} else {
					return fail()
				}
			}
		}
	}

	if phase != compactPhaseAfterValue || len(stack) != 0 {
		return fail()
	}
	return dst, nil
}

func AppendSanitizedLiteral(buf []byte, s string, sanitize uint64) []byte {
	return AppendSanitizedBytes(buf, rt.Str2Mem(s), sanitize)
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
		copied := copySafeHTMLASCIIPrefix(sp, nb, unsafe.Add(dp, 1))
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
		copied := copySafeHTMLASCIIPrefix(sp, nb, unsafe.Add(dp, 1))
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
	copied := copySafeHTMLASCIIPrefix(sp, nb, unsafe.Add(dp, 1))
	if copied == nb {
		*(*byte)(unsafe.Add(dp, nb+1)) = '"'
		*(*byte)(unsafe.Add(dp, nb+2)) = ':'
		return buf[:len(buf)+nb+3]
	}
	buf = buf[:len(buf)+1+copied]
	return appendQuoteSanitizedTail(buf, val[copied:], sanitize, ':')
}
