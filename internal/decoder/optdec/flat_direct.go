//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"math/bits"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/utf8"
)

type flatStagedField struct {
	val    uint64
	strOff uint32
	strLen uint32
}

const (
	flatMaxInputLen      int    = 1 << 20
	flatBackoffThreshold uint32 = 8
	flatUnsupportedOpts  uint64 = (1 << _F_use_number) | (1 << _F_use_int64)
)

// tryDecodeFlatStruct scans s[startPos:] directly into a flat struct (1..32 scalar/string fields)
// without borrowing a pooled Parser, copying input into p.padded, or materializing a []node tape.
// Field writes are staged on the stack and committed atomically only after matching the closing '}',
// so any bailout (escape sequence, type mismatch, overflow, invalid UTF-8, or syntax error) leaves vp
// completely untouched for the standard decoder fallback.
func tryDecodeFlatStruct(s string, startPos int, f uint64, vp unsafe.Pointer, sd *structDecoder, utf8Prechecked, checkTrailing bool) (int, bool) {
	end := len(s)
	if startPos < 0 || startPos >= end || end-startPos > flatMaxInputLen || f&flatUnsupportedOpts != 0 {
		return 0, false
	}
	streak := atomic.LoadUint32(&sd.flatFailStreak)
	if streak >= flatBackoffThreshold {
		if (atomic.AddUint32(&sd.flatProbeTick, 1) & 255) != 0 {
			return 0, false
		}
	}
	nextPos, ok := tryDecodeFlatStructBody(s, startPos, end, f, vp, sd, utf8Prechecked, checkTrailing)
	if ok {
		if streak != 0 {
			atomic.StoreUint32(&sd.flatFailStreak, 0)
		}
		return nextPos, true
	}
	if streak < 16 {
		atomic.AddUint32(&sd.flatFailStreak, 1)
	}
	return 0, false
}

func tryDecodeFlatStructBody(s string, startPos, end int, f uint64, vp unsafe.Pointer, sd *structDecoder, utf8Prechecked, checkTrailing bool) (int, bool) {

	strPtr := unsafe.Pointer(unsafe.StringData(s))
	validateStr := f&(1<<_F_validate_string) != 0
	copyStr := f&(1<<_F_copy_string) != 0
	caseSensitive := f&uint64(consts.OptionCaseSensitive) != 0
	disUnknown := Options(f)&OptionDisableUnknown != 0
	strictURC := f&(1<<_F_disable_urc) != 0
	pureASCIIKeys := sd.flatPureASCIIKeys

	pos, c, ok := flatSkipWS(strPtr, startPos, end)
	if !ok {
		return 0, false
	}
	if c == 'n' {
		if pos+3 > end || s[pos:pos+3] != "ull" {
			return 0, false
		}
		pos += 3
		if checkTrailing && validateStr && pos < end {
			if !flatTrailingUTF8OK(s, strPtr, pos, end) {
				return 0, false
			}
		}
		return pos, true
	}
	if c != '{' {
		return 0, false
	}

	if pos >= end {
		return 0, false
	}
	c = *(*byte)(unsafe.Add(strPtr, pos))
	if c == '}' {
		pos++
		if checkTrailing && validateStr && pos < end {
			if !flatTrailingUTF8OK(s, strPtr, pos, end) {
				return 0, false
			}
		}
		return pos, true
	}
	if c == '"' {
		pos++
	} else {
		pos, c, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if c == '}' {
			if checkTrailing && validateStr && pos < end {
				if !flatTrailingUTF8OK(s, strPtr, pos, end) {
					return 0, false
				}
			}
			return pos, true
		}
		if c != '"' {
			return 0, false
		}
	}

	var staged [32]flatStagedField
	var presentMask uint32
	var strMask uint32
	var nonASCII uint64

	fields := sd.fields
	nfields := len(fields)
	expectedIdx := 0

	for {
		var idx int
		if pureASCIIKeys && uint(expectedIdx) < uint(nfields) {
			fe := &fields[expectedIdx]
			n := len(fe.name)
			if pos+n+2 <= end && *(*uint16)(unsafe.Add(strPtr, pos+n)) == 0x3a22 &&
				unsafe.String((*byte)(unsafe.Add(strPtr, pos)), n) == fe.name {
				pos += n + 2
				idx = expectedIdx
				expectedIdx++
				goto parseFieldValue
			}
		}

		{
			keyStart := pos
			keyEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, &nonASCII)
			if !ok {
				return 0, false
			}
			key := unsafe.String((*byte)(unsafe.Add(strPtr, keyStart)), keyEnd-keyStart)
			pos = keyEnd + 1
			if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == ':' {
				pos++
			} else {
				var colon byte
				pos, colon, ok = flatSkipWS(strPtr, pos, end)
				if !ok || colon != ':' {
					return 0, false
				}
			}

			if uint(expectedIdx) < uint(nfields) && fields[expectedIdx].name == key {
				idx = expectedIdx
				expectedIdx++
			} else if caseSensitive {
				idx = sd.fieldMap.Get(key, true)
				if idx != -1 {
					expectedIdx = idx + 1
				}
			} else {
				idx = sd.fieldMap.GetInsensitive(key)
				if idx != -1 {
					expectedIdx = idx + 1
				}
			}

			if idx == -1 {
				if disUnknown {
					return 0, false
				}
				var vc byte
				if pos < end {
					vc = *(*byte)(unsafe.Add(strPtr, pos))
					if vc > ' ' {
						pos++
					} else {
						pos, vc, ok = flatSkipWS(strPtr, pos, end)
						if !ok {
							return 0, false
						}
					}
				} else {
					return 0, false
				}
				pos, ok = flatSkipValue(strPtr, vc, pos, end, validateStr, strictURC, &nonASCII)
				if !ok {
					return 0, false
				}
				goto afterFieldValue
			}
		}

	parseFieldValue:
		{
			if pos >= end {
				return 0, false
			}
			vc := *(*byte)(unsafe.Add(strPtr, pos))
			if vc > ' ' {
				pos++
			} else if vc == ' ' && pos+1 < end && *(*byte)(unsafe.Add(strPtr, pos+1)) > ' ' {
				vc = *(*byte)(unsafe.Add(strPtr, pos+1))
				pos += 2
			} else {
				pos, vc, ok = flatSkipWS(strPtr, pos, end)
				if !ok {
					return 0, false
				}
			}

			if vc == 'n' {
				if pos+3 > end || s[pos:pos+3] != "ull" {
					return 0, false
				}
				pos += 3
				goto afterFieldValue
			}

			bit := uint32(1) << uint(idx)
			switch fields[idx].kind {
			case fieldKindString:
				if vc != '"' {
					return 0, false
				}
				strStart := pos
				strEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, &nonASCII)
				if !ok {
					return 0, false
				}
				pos = strEnd + 1
				staged[idx].strOff = uint32(strStart)
				staged[idx].strLen = uint32(strEnd - strStart)
				presentMask |= bit
				strMask |= bit

			case fieldKindI64:
				val, nextPos, ok := flatParseSigned(strPtr, vc, pos, end, math.MaxInt64, uint64(math.MaxInt64)+1)
				if !ok {
					return 0, false
				}
				pos = nextPos
				staged[idx].val = val
				presentMask |= bit

			case fieldKindI32:
				val, nextPos, ok := flatParseSigned(strPtr, vc, pos, end, math.MaxInt32, uint64(math.MaxInt32)+1)
				if !ok {
					return 0, false
				}
				pos = nextPos
				staged[idx].val = uint64(uint32(val))
				presentMask |= bit

			case fieldKindU64:
				val, nextPos, ok := flatParseUnsigned(strPtr, vc, pos, end, math.MaxUint64)
				if !ok {
					return 0, false
				}
				pos = nextPos
				staged[idx].val = val
				presentMask |= bit

			case fieldKindU32:
				val, nextPos, ok := flatParseUnsigned(strPtr, vc, pos, end, math.MaxUint32)
				if !ok {
					return 0, false
				}
				pos = nextPos
				staged[idx].val = val
				presentMask |= bit

			case fieldKindBool:
				if vc == 't' {
					if pos+3 > end || s[pos:pos+3] != "rue" {
						return 0, false
					}
					pos += 3
					staged[idx].val = 1
					presentMask |= bit
				} else if vc == 'f' {
					if pos+4 > end || s[pos:pos+4] != "alse" {
						return 0, false
					}
					pos += 4
					staged[idx].val = 0
					presentMask |= bit
				} else {
					return 0, false
				}

			case fieldKindF64:
				fv, nextPos, ok := flatParseFloat(strPtr, vc, pos, end)
				if !ok {
					return 0, false
				}
				pos = nextPos
				staged[idx].val = math.Float64bits(fv)
				presentMask |= bit

			case fieldKindF32:
				fv, nextPos, ok := flatParseFloat(strPtr, vc, pos, end)
				if !ok || fv < -math.MaxFloat32 || fv > math.MaxFloat32 {
					return 0, false
				}
				pos = nextPos
				staged[idx].val = uint64(math.Float32bits(float32(fv)))
				presentMask |= bit

			default:
				return 0, false
			}
		}

	afterFieldValue:
		if pos >= end {
			return 0, false
		}
		delim := *(*byte)(unsafe.Add(strPtr, pos))
		if delim == ',' {
			pos++
			if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == '"' {
				pos++
				continue
			}
			var nc byte
			pos, nc, ok = flatSkipWS(strPtr, pos, end)
			if !ok || nc != '"' {
				return 0, false
			}
			continue
		}
		if delim == '}' {
			pos++
			break
		}
		pos, delim, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if delim == '}' {
			break
		}
		if delim != ',' {
			return 0, false
		}
		var nc byte
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok || nc != '"' {
			return 0, false
		}
	}

	if validateStr && !utf8Prechecked && nonASCII != 0 {
		if !utf8.ValidateString(s[startPos:pos]) {
			return 0, false
		}
	}
	if checkTrailing && validateStr && pos < end {
		if !flatTrailingUTF8OK(s, strPtr, pos, end) {
			return 0, false
		}
	}

	if presentMask == 0 {
		return pos, true
	}

	var copySlab []byte
	copyOff := 0
	if copyStr && strMask != 0 {
		totalBytes := 0
		for m := strMask; m != 0; m &= m - 1 {
			fi := bits.TrailingZeros32(m)
			totalBytes += int(staged[fi].strLen)
		}
		if totalBytes > 0 {
			copySlab = make([]byte, totalBytes)
		}
	}

	for m := presentMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		fe := &fields[fi]
		elem := unsafe.Add(vp, uintptr(fe.offset))
		st := &staged[fi]
		switch fe.kind {
		case fieldKindString:
			n := int(st.strLen)
			if n == 0 {
				*(*string)(elem) = ""
			} else if copyStr {
				dst := copySlab[copyOff : copyOff+n]
				copyOff += n
				copy(dst, s[st.strOff:int(st.strOff)+n])
				*(*string)(elem) = unsafe.String(&dst[0], n)
			} else {
				*(*string)(elem) = s[st.strOff : int(st.strOff)+n]
			}
		case fieldKindI64, fieldKindU64, fieldKindF64:
			*(*uint64)(elem) = st.val
		case fieldKindI32, fieldKindU32, fieldKindF32:
			*(*uint32)(elem) = uint32(st.val)
		case fieldKindBool:
			*(*bool)(elem) = st.val != 0
		}
	}

	return pos, true
}

func flatTrailingUTF8OK(s string, strPtr unsafe.Pointer, pos, end int) bool {
	for pos < end {
		b := *(*byte)(unsafe.Add(strPtr, pos))
		if b > ' ' || (spaceMask&(uint64(1)<<b)) == 0 {
			return utf8.ValidateString(s[pos:])
		}
		pos++
	}
	return true
}

func flatSkipWS(strPtr unsafe.Pointer, pos, end int) (int, byte, bool) {
	for pos < end {
		b := *(*byte)(unsafe.Add(strPtr, pos))
		if b > ' ' {
			return pos + 1, b, true
		}
		if (spaceMask & (uint64(1) << b)) == 0 {
			return pos + 1, b, true
		}
		pos++
		for pos+8 <= end {
			w := *(*uint64)(unsafe.Add(strPtr, pos)) ^ swarSpaces
			if w == 0 {
				pos += 8
				continue
			}
			rel := bits.TrailingZeros64(w) >> 3
			at := pos + rel
			c := *(*byte)(unsafe.Add(strPtr, at))
			if c > ' ' || (spaceMask&(uint64(1)<<c)) == 0 {
				return at + 1, c, true
			}
			pos = at + 1
			break
		}
	}
	return end, 0, false
}

// flatScanSimpleString scans an unescaped string starting at pos (right after opening '"')
// and returns (quotePos, true), bailing out cleanly on '\', '< 0x20' (when validateStr), or EOF.
func flatScanSimpleString(strPtr unsafe.Pointer, pos, end int, validateStr bool, nonASCII *uint64) (int, bool) {
	for pos+8 <= end {
		w := *(*uint64)(unsafe.Add(strPtr, pos))
		*nonASCII |= w & swarHigh
		q := w ^ swarQuote
		qm := (q - swarOnes) &^ q & swarHigh
		b := w ^ swarBS
		em := (b - swarOnes) &^ b & swarHigh
		if validateStr {
			em |= (w - swarCtrl) &^ w & swarHigh
		}
		if qm != 0 {
			if (qm-1)&em != 0 {
				return 0, false
			}
			return pos + (bits.TrailingZeros64(qm) >> 3), true
		}
		if em != 0 {
			return 0, false
		}
		pos += 8
	}
	for pos < end {
		c := *(*byte)(unsafe.Add(strPtr, pos))
		*nonASCII |= uint64(c & 0x80)
		if c == '"' {
			return pos, true
		}
		if c == '\\' || (validateStr && c < 0x20) {
			return 0, false
		}
		pos++
	}
	return 0, false
}

func flatParseSigned(strPtr unsafe.Pointer, c byte, pos, end int, maxPos, maxNeg uint64) (uint64, int, bool) {
	neg := false
	if c == '-' {
		neg = true
		if pos >= end {
			return 0, 0, false
		}
		c = *(*byte)(unsafe.Add(strPtr, pos))
		pos++
	}
	var u uint64
	if c == '0' {
		d := schemaByteAt(strPtr, pos, end)
		if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' {
			return 0, 0, false
		}
	} else if c >= '1' && c <= '9' {
		u = uint64(c - '0')
		digits := 1
		for digits < 19 {
			d := schemaByteAt(strPtr, pos, end)
			if d < '0' || d > '9' {
				if d == '.' || (d|0x20) == 'e' {
					return 0, 0, false
				}
				goto checkRange
			}
			u = u*10 + uint64(d-'0')
			pos++
			digits++
		}
		d := schemaByteAt(strPtr, pos, end)
		if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' {
			return 0, 0, false
		}
	} else {
		return 0, 0, false
	}

checkRange:
	if neg {
		if u > maxNeg {
			return 0, 0, false
		}
		return -u, pos, true
	}
	if u > maxPos {
		return 0, 0, false
	}
	return u, pos, true
}

func flatParseUnsigned(strPtr unsafe.Pointer, c byte, pos, end int, maxVal uint64) (uint64, int, bool) {
	var u uint64
	if c == '0' {
		d := schemaByteAt(strPtr, pos, end)
		if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' {
			return 0, 0, false
		}
		return 0, pos, true
	}
	if c < '1' || c > '9' {
		return 0, 0, false
	}
	u = uint64(c - '0')
	digits := 1
	for digits < 19 {
		d := schemaByteAt(strPtr, pos, end)
		if d < '0' || d > '9' {
			if d == '.' || (d|0x20) == 'e' {
				return 0, 0, false
			}
			if u > maxVal {
				return 0, 0, false
			}
			return u, pos, true
		}
		u = u*10 + uint64(d-'0')
		pos++
		digits++
	}
	d := schemaByteAt(strPtr, pos, end)
	if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' || u > maxVal {
		return 0, 0, false
	}
	return u, pos, true
}

func flatParseFloat(strPtr unsafe.Pointer, c byte, pos, end int) (float64, int, bool) {
	neg := false
	if c == '-' {
		neg = true
		if pos >= end {
			return 0, 0, false
		}
		c = *(*byte)(unsafe.Add(strPtr, pos))
		pos++
	}

	var mant uint64
	var exp int32
	sigDigits := 0
	var d byte

	if c == '0' {
		d = schemaByteAt(strPtr, pos, end)
		if d >= '0' && d <= '9' {
			return 0, 0, false
		}
		if d == '.' {
			pos++
			f0 := schemaByteAt(strPtr, pos, end)
			if f0 < '0' || f0 > '9' {
				return 0, 0, false
			}
			for schemaByteAt(strPtr, pos, end) == '0' {
				pos++
				exp--
			}
			for {
				fd := schemaByteAt(strPtr, pos, end)
				if fd < '0' || fd > '9' {
					d = fd
					break
				}
				if sigDigits >= 19 {
					return 0, 0, false
				}
				mant = mant*10 + uint64(fd-'0')
				exp--
				sigDigits++
				pos++
			}
		} else if (d | 0x20) != 'e' {
			if neg {
				return math.Copysign(0, -1), pos, true
			}
			return 0, pos, true
		}
	} else if c >= '1' && c <= '9' {
		mant = uint64(c - '0')
		sigDigits = 1
		for {
			id := schemaByteAt(strPtr, pos, end)
			if id < '0' || id > '9' {
				d = id
				break
			}
			if sigDigits >= 19 {
				return 0, 0, false
			}
			mant = mant*10 + uint64(id-'0')
			sigDigits++
			pos++
		}
		if d == '.' {
			pos++
			f0 := schemaByteAt(strPtr, pos, end)
			if f0 < '0' || f0 > '9' {
				return 0, 0, false
			}
			for {
				fd := schemaByteAt(strPtr, pos, end)
				if fd < '0' || fd > '9' {
					d = fd
					break
				}
				if sigDigits >= 19 {
					return 0, 0, false
				}
				mant = mant*10 + uint64(fd-'0')
				exp--
				sigDigits++
				pos++
			}
		}
	} else {
		return 0, 0, false
	}

	if d == 'e' || d == 'E' {
		pos++
		expSign := int32(1)
		ed := schemaByteAt(strPtr, pos, end)
		if ed == '+' {
			pos++
			ed = schemaByteAt(strPtr, pos, end)
		} else if ed == '-' {
			expSign = -1
			pos++
			ed = schemaByteAt(strPtr, pos, end)
		}
		if ed < '0' || ed > '9' {
			return 0, 0, false
		}
		expVal := int32(ed - '0')
		pos++
		expCount := 1
		for {
			ed = schemaByteAt(strPtr, pos, end)
			if ed < '0' || ed > '9' {
				break
			}
			if expCount >= 3 {
				return 0, 0, false
			}
			expVal = expVal*10 + int32(ed-'0')
			expCount++
			pos++
		}
		exp += expSign * expVal
	}

	fv, ok := cParityAtofFast(mant, exp, neg, false)
	if !ok || math.IsInf(fv, 0) {
		return 0, 0, false
	}
	return fv, pos, true
}

func flatSkipString(strPtr unsafe.Pointer, pos, end int, validateStr, strictURC bool, nonASCII *uint64) (int, bool) {
	for {
		for pos+8 <= end {
			w := *(*uint64)(unsafe.Add(strPtr, pos))
			*nonASCII |= w & swarHigh
			q := w ^ swarQuote
			qm := (q - swarOnes) &^ q & swarHigh
			b := w ^ swarBS
			em := (b - swarOnes) &^ b & swarHigh
			if validateStr {
				em |= (w - swarCtrl) &^ w & swarHigh
			}
			if qm != 0 {
				if (qm-1)&em != 0 {
					pos += bits.TrailingZeros64(em) >> 3
					break
				}
				return pos + (bits.TrailingZeros64(qm) >> 3) + 1, true
			}
			if em != 0 {
				pos += bits.TrailingZeros64(em) >> 3
				break
			}
			pos += 8
		}
		if pos >= end {
			return 0, false
		}
		c := *(*byte)(unsafe.Add(strPtr, pos))
		*nonASCII |= uint64(c & 0x80)
		if c == '"' {
			return pos + 1, true
		}
		if validateStr && c < 0x20 {
			return 0, false
		}
		if c != '\\' {
			pos++
			continue
		}
		if pos+2 > end {
			return 0, false
		}
		esc := *(*byte)(unsafe.Add(strPtr, pos+1))
		if esc != 'u' {
			if cParityEscaped[esc] == 0 {
				return 0, false
			}
			pos += 2
			continue
		}
		if pos+6 > end {
			return 0, false
		}
		u, ok := cParityReadHex16(unsafe.Add(strPtr, pos+2))
		if !ok {
			return 0, false
		}
		pos += 6
		if strictURC && u >= 0xd800 && u <= 0xdfff {
			if u > 0xdbff || pos+6 > end || *(*byte)(unsafe.Add(strPtr, pos)) != '\\' || *(*byte)(unsafe.Add(strPtr, pos+1)) != 'u' {
				return 0, false
			}
			u2, ok2 := cParityReadHex16(unsafe.Add(strPtr, pos+2))
			if !ok2 || u2 < 0xdc00 || u2 > 0xdfff {
				return 0, false
			}
			pos += 6
		}
	}
}

func flatSkipValue(strPtr unsafe.Pointer, c byte, pos, end int, validateStr, strictURC bool, nonASCII *uint64) (int, bool) {
	var frameBits uint64
	sp := 0
	var ok bool
	var nc, delim, colon, vc byte

valueEntry:
	switch c {
	case '"':
		pos, ok = flatSkipString(strPtr, pos, end, validateStr, strictURC, nonASCII)
		if !ok {
			return 0, false
		}
	case '{':
		if sp >= 64 {
			return 0, false
		}
		frameBits <<= 1
		sp++
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if nc == '}' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		goto objKey
	case '[':
		if sp >= 64 {
			return 0, false
		}
		frameBits = (frameBits << 1) | 1
		sp++
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if nc == ']' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		c = nc
		goto valueEntry
	case 't':
		if pos+3 > end || unsafe.String((*byte)(unsafe.Add(strPtr, pos)), 3) != "rue" {
			return 0, false
		}
		pos += 3
	case 'f':
		if pos+4 > end || unsafe.String((*byte)(unsafe.Add(strPtr, pos)), 4) != "alse" {
			return 0, false
		}
		pos += 4
	case 'n':
		if pos+3 > end || unsafe.String((*byte)(unsafe.Add(strPtr, pos)), 3) != "ull" {
			return 0, false
		}
		pos += 3
	default:
		pos, ok = skipNumberAt(strPtr, c, pos, end, true)
		if !ok {
			return 0, false
		}
	}

afterValue:
	if sp == 0 {
		return pos, true
	}
	if (frameBits & 1) != 0 {
		pos, delim, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if delim == ',' {
			pos, vc, ok = flatSkipWS(strPtr, pos, end)
			if !ok {
				return 0, false
			}
			c = vc
			goto valueEntry
		}
		if delim == ']' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		return 0, false
	}
	pos, delim, ok = flatSkipWS(strPtr, pos, end)
	if !ok {
		return 0, false
	}
	if delim == ',' {
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		goto objKey
	}
	if delim == '}' {
		frameBits >>= 1
		sp--
		goto afterValue
	}
	return 0, false

objKey:
	if nc != '"' {
		return 0, false
	}
	pos, ok = flatSkipString(strPtr, pos, end, validateStr, strictURC, nonASCII)
	if !ok {
		return 0, false
	}
	pos, colon, ok = flatSkipWS(strPtr, pos, end)
	if !ok || colon != ':' {
		return 0, false
	}
	pos, vc, ok = flatSkipWS(strPtr, pos, end)
	if !ok {
		return 0, false
	}
	c = vc
	goto valueEntry
}
