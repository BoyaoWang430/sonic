//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"math"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
)

// subtreeOutputMode selects how the shared JSON scanner handles a JSON subtree
// without owning separate per-backend grammars for numbers, escapes, or containers:
//   - subtreeOutEmit:         emit full node(s) onto Document tape.
//   - subtreeOutRawSpan:      strictly validate subtree and emit one KRaw span
//                             (used by json.RawMessage / Unmarshaler; never relaxed).
//   - subtreeOutValidateOnly: strictly validate subtree without emitting nodes.
//   - subtreeOutRelaxedSkip:  relaxed boundary skip ONLY for discarded unknown
//                             fields when OptionNoValidateJSONSkip is explicitly set.
type subtreeOutputMode uint8

const (
	subtreeOutEmit subtreeOutputMode = iota
	subtreeOutRawSpan
	subtreeOutValidateOnly
	subtreeOutRelaxedSkip
)

func unknownFieldOutputMode(noValidateSkip bool) subtreeOutputMode {
	if noValidateSkip {
		return subtreeOutRelaxedSkip
	}
	return subtreeOutValidateOnly
}

func planOutputMode(plan *projPlan) subtreeOutputMode {
	if plan != nil && plan.kind == projRaw {
		return subtreeOutRawSpan
	}
	return subtreeOutEmit
}

// skipIndentSWAR skips up to 24 ASCII spaces or tabs starting at pos (typically right after '\n').
// Returns (ch, nextPos, true) if a non-whitespace byte (> ' ') is found within 24 bytes.
func skipIndentSWAR(win paddedInputWindow, pos int) (byte, int, bool) {
	end := win.end
	if pos > end {
		return 0, pos, false
	}
	base := win.blockPtr(pos)
	w0 := *(*uint64)(base) ^ swarSpaces
	if w0 != 0 {
		rel := bits.TrailingZeros64(w0) >> 3
		at := pos + rel
		b := *(*byte)(unsafe.Add(base, rel))
		if b > ' ' {
			return b, at + 1, at < end
		}
		if b == '\t' {
			for at+1 < end {
				at++
				b = win.byteAt(at)
				if b != '\t' && b != ' ' {
					break
				}
			}
			if b == '\t' || b == ' ' {
				return 0, end, false
			}
			if b > ' ' {
				return b, at + 1, at < end
			}
		}
		return 0, at, false
	}
	w1 := *(*uint64)(unsafe.Add(base, 8)) ^ swarSpaces
	if w1 != 0 {
		rel := 8 + (bits.TrailingZeros64(w1) >> 3)
		at := pos + rel
		b := *(*byte)(unsafe.Add(base, rel))
		if b > ' ' {
			return b, at + 1, at < end
		}
		return 0, at, false
	}
	w2 := *(*uint64)(unsafe.Add(base, 16)) ^ swarSpaces
	if w2 != 0 {
		rel := 16 + (bits.TrailingZeros64(w2) >> 3)
		at := pos + rel
		b := *(*byte)(unsafe.Add(base, rel))
		if b > ' ' {
			return b, at + 1, at < end
		}
		return 0, at, false
	}
	if pos+24 > end {
		return 0, end, false
	}
	return 0, pos + 24, false
}

func skipSpaceReg(win paddedInputWindow, pos int, nbkOffPlus64, nbkBits uint64) (byte, int, uint64, uint64, bool) {
	end := win.end
	b := win.byteAt(pos)
	if b > ' ' || (spaceMask&(uint64(1)<<b)) == 0 {
		return b, pos + 1, nbkOffPlus64, nbkBits, pos < end
	}
	if b, np, hit := skipIndentSWAR(win, pos+1); hit {
		return b, np, nbkOffPlus64, nbkBits, true
	} else {
		pos = np
	}
	b = win.byteAt(pos)
	if b > ' ' || (spaceMask&(uint64(1)<<b)) == 0 {
		return b, pos + 1, nbkOffPlus64, nbkBits, pos < end
	}
	pos++
	if nbkOffPlus64 != 0 {
		blockOff := int(nbkOffPlus64) - 64
		rel := pos - blockOff
		if uint(rel) < 64 {
			mask := nbkBits >> uint(rel)
			if mask != 0 {
				at := pos + bits.TrailingZeros64(mask)
				return win.byteAt(at), at + 1, nbkOffPlus64, nbkBits, at < end
			}
			pos = blockOff + 64
		}
	}
	for {
		if pos >= end {
			return 0, pos, nbkOffPlus64, nbkBits, false
		}
		mask := win.nonspace64(pos)
		if mask != 0 {
			nbkBits = mask
			nbkOffPlus64 = uint64(pos + 64)
			at := pos + bits.TrailingZeros64(mask)
			return win.byteAt(at), at + 1, nbkOffPlus64, nbkBits, at < end
		}
		pos += 64
	}
}

func swarSkipString64(win paddedInputWindow, pos int, validateStr bool) (int, bool, bool) {
	end := win.end
	if pos > end {
		return pos, true, false
	}
	base := win.blockPtr(pos)
	for off := 0; off < 64; off += 8 {
		w := *(*uint64)(unsafe.Add(base, off))
		q := w ^ swarQuote
		qm := (q - swarOnes) &^ q & swarHigh
		b := w ^ swarBS
		em := (b - swarOnes) &^ b & swarHigh
		if validateStr {
			em |= (w - swarCtrl) &^ w & swarHigh
		}
		if qm != 0 {
			if (qm-1)&em != 0 {
				return pos + off, false, false
			}
			afterQuote := pos + off + (bits.TrailingZeros64(qm) >> 3) + 1
			return afterQuote, true, afterQuote <= end
		}
		if em != 0 {
			return pos + off, false, false
		}
	}
	if pos+64 > end {
		return end, false, false
	}
	return pos + 64, false, false
}

// scanSharedStringEmit scans a JSON string starting right after the opening quote
// (contentStart) using bounded kernels.ScanString + kernels.UnquoteStatus,
// appending any decoded escaped text to p.unescBuf without mutating source bytes.
func scanSharedStringEmit(p *Parser, jsonStr string, win paddedInputWindow, contentStart int, opts uint64) (typ, val uint64, afterQuote int, hasEsc, ok bool) {
	end := win.end
	validateStr := opts&(1<<_F_validate_string) != 0
	if nextPos, done, valid := swarSkipString64(win, contentStart, validateStr); done {
		if !valid {
			return 0, 0, contentStart, false, false
		}
		return KStringCommon | uint64(contentStart)<<PosBits, uint64(nextPos - contentStart - 1), nextPos, false, true
	}
	if contentStart > end {
		return 0, 0, contentStart, false, false
	}
	afterPos, escPos, _, perr := kernels.ScanString(jsonStr[:end], contentStart-1, validateStr)
	if perr != 0 || afterPos > end {
		return 0, 0, contentStart, false, false
	}
	if escPos < 0 {
		return KStringCommon | uint64(contentStart)<<PosBits, uint64(afterPos - contentStart - 1), afterPos, false, true
	}

	quotePos := afterPos - 1
	uoff := len(p.unescBuf)
	prefixLen := escPos - contentStart
	maxOut := quotePos - contentStart
	if cap(p.unescBuf)-uoff < maxOut {
		newCap := cap(p.unescBuf) * 2
		if newCap < 256 {
			newCap = 256
		}
		if newCap < uoff+maxOut {
			newCap = uoff + maxOut
		}
		grown := make([]byte, uoff, newCap)
		copy(grown, p.unescBuf)
		p.unescBuf = grown
	}
	p.unescBuf = append(p.unescBuf, jsonStr[contentStart:escPos]...)
	escTail := jsonStr[escPos:quotePos]
	dst := p.unescBuf[uoff+prefixLen : uoff+maxOut]
	var unquoteFlags uint64 = types.F_UNICODE_REPLACE
	if opts&(1<<_F_disable_urc) != 0 {
		unquoteFlags = 0
	}
	written, _, ustatus := kernels.UnquoteStatus(escTail, dst, unquoteFlags)
	if ustatus != kernels.StatusOK {
		p.unescBuf = p.unescBuf[:uoff]
		if p.unesc != nil {
			p.unesc = p.unescBuf
		}
		return 0, 0, contentStart, false, false
	}
	ulen := prefixLen + written
	p.unescBuf = p.unescBuf[:uoff+ulen]
	p.unesc = p.unescBuf
	packedVal := escapedInUnescFlag | (uint64(uoff) << 32) | uint64(uint32(ulen))
	return KStringEscaped | uint64(contentStart)<<PosBits, packedVal, afterPos, true, true
}

// scanSharedStringSkipOrValidate advances past a JSON string starting right
// after its opening quote (contentStart) according to mode (RelaxedSkip vs
// ValidateOnly/RawSpan), sharing kernels.ScanString and kernels.UnquoteStatus.
func scanSharedStringSkipOrValidate(p *Parser, jsonStr string, win paddedInputWindow, contentStart int, mode subtreeOutputMode, opts uint64) (int, bool) {
	end := win.end
	if mode == subtreeOutRelaxedSkip {
		if nextPos, done, valid := swarSkipString64(win, contentStart, false); done {
			return nextPos, valid
		}
		if contentStart > end {
			return contentStart, false
		}
		relEnd, err := kernels.SkipStringRelaxed(jsonStr[contentStart:end])
		if err != 0 {
			return contentStart, false
		}
		return contentStart + relEnd, true
	}

	validateStr := opts&(1<<_F_validate_string) != 0
	if nextPos, done, valid := swarSkipString64(win, contentStart, validateStr); done {
		return nextPos, valid
	}
	if contentStart > end {
		return contentStart, false
	}
	afterPos, escPos, _, perr := kernels.ScanString(jsonStr[:end], contentStart-1, validateStr)
	if perr != 0 || afterPos > end {
		return contentStart, false
	}
	if escPos < 0 || (validateStr && opts&(1<<_F_disable_urc) == 0) {
		return afterPos, true
	}

	escTail := jsonStr[escPos : afterPos-1]
	var unquoteFlags uint64 = types.F_UNICODE_REPLACE
	if opts&(1<<_F_disable_urc) != 0 {
		unquoteFlags = 0
	}
	var stackDst [256]byte
	if len(escTail) <= len(stackDst) {
		_, _, ustatus := kernels.UnquoteStatus(escTail, stackDst[:len(escTail)], unquoteFlags)
		return afterPos, ustatus == kernels.StatusOK
	}
	savedLen := len(p.unescBuf)
	if cap(p.unescBuf)-savedLen < len(escTail) {
		newCap := cap(p.unescBuf) * 2
		if newCap < savedLen+len(escTail) {
			newCap = savedLen + len(escTail)
		}
		grown := make([]byte, savedLen, newCap)
		copy(grown, p.unescBuf)
		p.unescBuf = grown
		if p.unesc != nil {
			p.unesc = p.unescBuf
		}
	}
	dst := p.unescBuf[savedLen : savedLen+len(escTail)]
	_, _, ustatus := kernels.UnquoteStatus(escTail, dst, unquoteFlags)
	return afterPos, ustatus == kernels.StatusOK
}

// scanStrictNumberEnd validates a JSON number starting at valStart using the
// authoritative kernels.ScanNumberStrict grammar and ensures no trailing number
// continuation byte immediately follows.
func scanStrictNumberEnd(jsonStr string, win paddedInputWindow, valStart int) (int, bool) {
	end := win.end
	if valStart < 0 || valStart >= end {
		return valStart, false
	}
	numEnd, perr := kernels.ScanNumberStrict(jsonStr[:end], valStart)
	if perr != 0 || numEnd > end || isNumberContByte(win.byteAt(numEnd)) {
		return numEnd, false
	}
	return numEnd, true
}

// scanSharedNumberEmit scans and converts a JSON number starting at valStart
// (where first == jsonStr[valStart] and pos == valStart+1) into a tape node.
func scanSharedNumberEmit(p *Parser, jsonStr string, win paddedInputWindow, first byte, valStart, pos int, opts uint64) (node, int, bool) {
	end := win.end
	if opts&(1<<_F_use_number) != 0 {
		numEnd, ok := scanStrictNumberEnd(jsonStr, win, valStart)
		if !ok {
			return node{}, pos, false
		}
		return node{typ: KRawNumber | uint64(valStart)<<PosBits, val: uint64(numEnd - valStart)}, numEnd, true
	}

	if first >= '1' && first <= '9' {
		uval := uint64(first - '0')
		digits := 1
		for digits < 19 {
			d := win.byteAt(pos)
			if d < '0' || d > '9' {
				if d != '.' && (d|0x20) != 'e' && !isNumberContByte(d) {
					if pos > end {
						return node{}, pos, false
					}
					return node{typ: KUint | uint64(valStart)<<PosBits, val: uval}, pos, true
				}
				break
			}
			uval = uval*10 + uint64(d-'0')
			pos++
			digits++
		}
	} else if first == '0' {
		d := win.byteAt(pos)
		if !isNumberContByte(d) {
			if pos > end {
				return node{}, pos, false
			}
			return node{typ: KUint | uint64(valStart)<<PosBits, val: 0}, pos, true
		}
		if d >= '0' && d <= '9' {
			return node{}, pos, false
		}
	} else if first == '-' {
		d0 := win.byteAt(pos)
		if d0 >= '1' && d0 <= '9' {
			uval := uint64(d0 - '0')
			p2 := pos + 1
			digits := 1
			for digits < 19 {
				d := win.byteAt(p2)
				if d < '0' || d > '9' {
					if d != '.' && (d|0x20) != 'e' && !isNumberContByte(d) {
						if p2 > end {
							return node{}, p2, false
						}
						if uval <= math.MaxInt64+1 {
							if uval == math.MaxInt64+1 {
								return node{typ: KSint | uint64(valStart)<<PosBits, val: uint64(1) << 63}, p2, true
							}
							return node{typ: KSint | uint64(valStart)<<PosBits, val: uint64(-int64(uval))}, p2, true
						}
					}
					break
				}
				uval = uval*10 + uint64(d-'0')
				p2++
				digits++
			}
			pos = p2
		}
	}

	neg := first == '-'
	numBody := valStart
	if neg {
		numBody++
	}
	var zeroNum cParityNum
	var numVal cParityNum
	var numEnd int
	if startPtr, exactEnd, contiguous := win.terminatedNumberPtr(numBody, pos); contiguous {
		nextCur, nv, code := cParityParseNumberValue(startPtr, zeroNum, neg, p.dbuf)
		if code == SONIC_FLOAT_INF && p.deferFloatOverflow {
			endPos, ok := scanStrictNumberEnd(jsonStr, win, valStart)
			if !ok {
				return node{}, pos, false
			}
			return node{typ: KRawNumber | uint64(valStart)<<PosBits, val: uint64(endPos - valStart)}, endPos, true
		}
		if code != SONIC_OK {
			return node{}, pos, false
		}
		parsedLen := int(uintptr(nextCur) - uintptr(startPtr))
		if exactEnd >= 0 {
			if parsedLen != exactEnd-numBody {
				return node{}, pos, false
			}
			numEnd = exactEnd
		} else {
			numEnd = numBody + parsedLen
			if numEnd > end || isNumberContByte(win.byteAt(numEnd)) {
				return node{}, pos, false
			}
		}
		numVal = nv
	} else {
		var ok bool
		numEnd, ok = scanStrictNumberEnd(jsonStr, win, valStart)
		if !ok {
			return node{}, pos, false
		}
		savedLen := len(p.unescBuf)
		p.unescBuf = append(p.unescBuf, jsonStr[numBody:numEnd]...)
		p.unescBuf = append(p.unescBuf, 'x', 0, 0, 0, 0, 0, 0, 0)
		startPtr := unsafe.Pointer(&p.unescBuf[savedLen])
		nextCur, nv, code := cParityParseNumberValue(startPtr, zeroNum, neg, p.dbuf)
		p.unescBuf = p.unescBuf[:savedLen]
		if p.unesc != nil {
			p.unesc = p.unescBuf
		}
		if code == SONIC_FLOAT_INF && p.deferFloatOverflow {
			return node{typ: KRawNumber | uint64(valStart)<<PosBits, val: uint64(numEnd - valStart)}, numEnd, true
		}
		if code != SONIC_OK || int(uintptr(nextCur)-uintptr(startPtr)) != numEnd-numBody {
			return node{}, pos, false
		}
		numVal = nv
	}
	return node{typ: numVal.typ | uint64(valStart)<<PosBits, val: numVal.val}, numEnd, true
}

func matchLiteralTail(win paddedInputWindow, c byte, pos int) (int, bool) {
	end := win.end
	switch c {
	case 't':
		if pos+3 > end || (*(*uint32)(win.blockPtr(pos))&0x00ffffff) != 0x00657572 {
			return pos, false
		}
		return pos + 3, true
	case 'f':
		if pos+4 > end || *(*uint32)(win.blockPtr(pos)) != 0x65736c61 {
			return pos, false
		}
		return pos + 4, true
	case 'n':
		if pos+3 > end || (*(*uint32)(win.blockPtr(pos))&0x00ffffff) != 0x006c6c75 {
			return pos, false
		}
		return pos + 3, true
	default:
		return pos, false
	}
}

func skipOrValidateNumberAt(jsonStr string, win paddedInputWindow, c byte, pos int) (int, bool) {
	end := win.end
	valStart := pos - 1
	if c >= '1' && c <= '9' {
		p2 := pos
		for p2-pos < 20 {
			d := win.byteAt(p2)
			if d < '0' || d > '9' {
				if !isNumberContByte(d) {
					return p2, p2 <= end
				}
				break
			}
			p2++
		}
	} else if c == '0' {
		d := win.byteAt(pos)
		if !isNumberContByte(d) {
			return pos, pos <= end
		}
		if d >= '0' && d <= '9' {
			return pos, false
		}
	} else if c != '-' {
		return pos, false
	}
	return scanStrictNumberEnd(jsonStr, win, valStart)
}

// sharedScanNonEmittingSubtree scans one JSON value starting with byte c at
// valStart = pos-1 under a non-emitting output mode (subtreeOutRawSpan,
// subtreeOutValidateOnly, or subtreeOutRelaxedSkip).
func sharedScanNonEmittingSubtree(p *Parser, jsonStr string, win paddedInputWindow, c byte, pos int, baseDepth uint64, mode subtreeOutputMode, opts uint64, nbkOffPlus64, nbkBits uint64) (int, uint64, uint64, bool) {
	switch c {
	case '"':
		nextPos, ok := scanSharedStringSkipOrValidate(p, jsonStr, win, pos, mode, opts)
		return nextPos, nbkOffPlus64, nbkBits, ok
	case 't', 'f', 'n':
		nextPos, ok := matchLiteralTail(win, c, pos)
		return nextPos, nbkOffPlus64, nbkBits, ok
	case '{', '[':
		if mode == subtreeOutRelaxedSkip {
			endPos, err := kernels.SkipContainer(jsonStr[:win.end], pos-1)
			if err != 0 {
				return pos, nbkOffPlus64, nbkBits, false
			}
			return endPos, nbkOffPlus64, nbkBits, true
		}
		return sharedValidateContainer(p, jsonStr, win, c, pos, baseDepth, opts, nbkOffPlus64, nbkBits)
	default:
		nextPos, ok := skipOrValidateNumberAt(jsonStr, win, c, pos)
		return nextPos, nbkOffPlus64, nbkBits, ok
	}
}

// sharedValidateContainer strictly validates an object/array subtree without
// materializing nodes, using a single bounded paddedInputWindow state machine
// with a 64-level register bit-stack (0 = object, 1 = array).
func sharedValidateContainer(p *Parser, jsonStr string, win paddedInputWindow, c byte, pos int, baseDepth uint64, opts uint64, nbkOffPlus64, nbkBits uint64) (int, uint64, uint64, bool) {
	maxSp := int(types.MAX_RECURSE - baseDepth)
	if maxSp <= 0 {
		return pos, nbkOffPlus64, nbkBits, false
	}
	if maxSp > 64 {
		maxSp = 64
	}

	end := win.end
	var frameBits uint64
	sp := 0
	var nc, vc, delim, colon byte
	var ok bool

valueEntry:
	switch c {
	case '"':
		pos, ok = scanSharedStringSkipOrValidate(p, jsonStr, win, pos, subtreeOutValidateOnly, opts)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
	case '{':
		if sp >= maxSp {
			return pos, nbkOffPlus64, nbkBits, false
		}
		frameBits <<= 1
		sp++
		nc = win.byteAt(pos)
		if nc > ' ' {
			pos++
			if pos > end {
				return pos, nbkOffPlus64, nbkBits, false
			}
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWAR(win, pos+1); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, np, nbkOffPlus64, nbkBits)
				if !ok {
					return pos, nbkOffPlus64, nbkBits, false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
			if !ok {
				return pos, nbkOffPlus64, nbkBits, false
			}
		}
		if nc == '}' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		goto objKey
	case '[':
		if sp >= maxSp {
			return pos, nbkOffPlus64, nbkBits, false
		}
		frameBits = (frameBits << 1) | 1
		sp++
		nc = win.byteAt(pos)
		if nc > ' ' {
			pos++
			if pos > end {
				return pos, nbkOffPlus64, nbkBits, false
			}
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWAR(win, pos+1); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, np, nbkOffPlus64, nbkBits)
				if !ok {
					return pos, nbkOffPlus64, nbkBits, false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
			if !ok {
				return pos, nbkOffPlus64, nbkBits, false
			}
		}
		if nc == ']' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		c = nc
		goto valueEntry
	case 't', 'f', 'n':
		pos, ok = matchLiteralTail(win, c, pos)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
	default:
		pos, ok = skipOrValidateNumberAt(jsonStr, win, c, pos)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
	}

afterValue:
	if sp == 0 {
		return pos, nbkOffPlus64, nbkBits, true
	}
	if (frameBits & 1) != 0 {
		goto arrAfterValue
	}
	delim = win.byteAt(pos)
	if delim == ',' {
		pos++
		if pos > end {
			return pos, nbkOffPlus64, nbkBits, false
		}
		cNext := win.byteAt(pos)
		if cNext == '"' {
			pos++
			if pos > end {
				return pos, nbkOffPlus64, nbkBits, false
			}
			goto objKeyBody
		}
		if cNext == '\n' {
			if b, np, hit := skipIndentSWAR(win, pos+1); hit {
				nc = b
				pos = np
				goto objKey
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, np, nbkOffPlus64, nbkBits)
			}
			if !ok {
				return pos, nbkOffPlus64, nbkBits, false
			}
			goto objKey
		}
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
		goto objKey
	}
	if delim == '}' {
		pos++
		if pos > end {
			return pos, nbkOffPlus64, nbkBits, false
		}
		frameBits >>= 1
		sp--
		goto afterValue
	}
	if delim == '\n' {
		if b, np, hit := skipIndentSWAR(win, pos+1); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, np, nbkOffPlus64, nbkBits)
			if !ok {
				return pos, nbkOffPlus64, nbkBits, false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
	}
	if delim == ',' {
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
		goto objKey
	}
	if delim == '}' {
		frameBits >>= 1
		sp--
		goto afterValue
	}
	return pos, nbkOffPlus64, nbkBits, false

objKey:
	if nc != '"' {
		return pos, nbkOffPlus64, nbkBits, false
	}
objKeyBody:
	pos, ok = scanSharedStringSkipOrValidate(p, jsonStr, win, pos, subtreeOutValidateOnly, opts)
	if !ok {
		return pos, nbkOffPlus64, nbkBits, false
	}
	if win.byteAt(pos) == ':' {
		pos++
		if pos > end {
			return pos, nbkOffPlus64, nbkBits, false
		}
	} else {
		colon, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok || colon != ':' {
			return pos, nbkOffPlus64, nbkBits, false
		}
	}
	vc = win.byteAt(pos)
	if vc > ' ' {
		pos++
		if pos > end {
			return pos, nbkOffPlus64, nbkBits, false
		}
		c = vc
		goto valueEntry
	}
	if vc == ' ' {
		vc2 := win.byteAt(pos + 1)
		if vc2 > ' ' {
			pos += 2
			if pos > end {
				return pos, nbkOffPlus64, nbkBits, false
			}
			c = vc2
			goto valueEntry
		}
	}
	vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
	if !ok {
		return pos, nbkOffPlus64, nbkBits, false
	}
	c = vc
	goto valueEntry

arrAfterValue:
	delim = win.byteAt(pos)
	if delim == ',' {
		pos++
		if pos > end {
			return pos, nbkOffPlus64, nbkBits, false
		}
		vc = win.byteAt(pos)
		if vc > ' ' {
			pos++
			if pos > end {
				return pos, nbkOffPlus64, nbkBits, false
			}
			c = vc
			goto valueEntry
		}
		if vc == '\n' {
			if b, np, hit := skipIndentSWAR(win, pos+1); hit {
				c = b
				pos = np
				goto valueEntry
			} else {
				vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, np, nbkOffPlus64, nbkBits)
			}
			if !ok {
				return pos, nbkOffPlus64, nbkBits, false
			}
			c = vc
			goto valueEntry
		}
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
		c = vc
		goto valueEntry
	}
	if delim == ']' {
		pos++
		if pos > end {
			return pos, nbkOffPlus64, nbkBits, false
		}
		frameBits >>= 1
		sp--
		goto afterValue
	}
	if delim == '\n' {
		if b, np, hit := skipIndentSWAR(win, pos+1); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, np, nbkOffPlus64, nbkBits)
			if !ok {
				return pos, nbkOffPlus64, nbkBits, false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
	}
	if delim == ',' {
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(win, pos, nbkOffPlus64, nbkBits)
		if !ok {
			return pos, nbkOffPlus64, nbkBits, false
		}
		c = vc
		goto valueEntry
	}
	if delim == ']' {
		frameBits >>= 1
		sp--
		goto afterValue
	}
	return pos, nbkOffPlus64, nbkBits, false
}
