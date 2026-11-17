//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import (
	"math/bits"
	"unsafe"
)

func cParityNonspace64Scalar(src unsafe.Pointer) (mask uint64) {
	b := (*[64]byte)(src)
	for i := 0; i < 64; i++ {
		c := b[i]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			mask |= uint64(1) << uint(i)
		}
	}
	return mask
}

func cParityStringMasks32Scalar(src unsafe.Pointer, opts uint64) (bs, quote, control uint32) {
	b := (*[32]byte)(src)
	checkCtrl := opts&scanKernelFlagRejectControl != 0
	for i := 0; i < 32; i++ {
		c := b[i]
		bit := uint32(1) << uint(i)
		if c == '\\' {
			bs |= bit
		} else if c == '"' {
			quote |= bit
		} else if checkCtrl && c < 0x20 {
			control |= bit
		}
	}
	return bs, quote, control
}

func cParityNumberMasks16Scalar(src unsafe.Pointer) (decimal, exponent, sign, valid uint32) {
	b := (*[16]byte)(src)
	for i := 0; i < 16; i++ {
		c := b[i]
		bit := uint32(1) << uint(i)
		switch {
		case c >= '0' && c <= '9':
			valid |= bit
		case c == '.':
			decimal |= bit
			valid |= bit
		case c == 'e' || c == 'E':
			exponent |= bit
			valid |= bit
		case c == '+' || c == '-':
			sign |= bit
			valid |= bit
		}
	}
	return decimal, exponent, sign, valid
}

func runsSpaceScalar(raw []byte, pos *int, nbk *cParityNonspaceBlock) byte {
	p := *pos
	c0 := raw[p]
	p++
	if c0 > ' ' || (c0 != ' ' && c0 != '\t' && c0 != '\n' && c0 != '\r') {
		*pos = p
		return c0
	}
	c1 := raw[p]
	p++
	if c1 > ' ' || (c1 != ' ' && c1 != '\t' && c1 != '\n' && c1 != '\r') {
		*pos = p
		return c1
	}
	if nbk.offPlus64 >= 64 {
		blkStart := int(nbk.offPlus64 - 64)
		rel := p - blkStart
		if uint(rel) < 64 {
			masked := nbk.bits & (^uint64(0) << uint(rel))
			if masked != 0 {
				idx := blkStart + bits.TrailingZeros64(masked)
				*pos = idx + 1
				return raw[idx]
			}
			p = blkStart + 64
		}
	}
	for {
		bits64 := cParityNonspace64Scalar(unsafe.Pointer(&raw[p]))
		if bits64 != 0 {
			nbk.offPlus64 = uint64(p + 64)
			nbk.bits = bits64
			idx := p + bits.TrailingZeros64(bits64)
			*pos = idx + 1
			return raw[idx]
		}
		p += 64
	}
}

func runsStringScalar(raw []byte, pos *int, flags uint64) (strLenOrNegErr int, hasEsc uint64, unicodeDst int) {
	start := *pos
	p := start
	for {
		bs, quote, control := cParityStringMasks32Scalar(unsafe.Pointer(&raw[p]), flags)
		if cParityAtFirst(quote, bs|control) {
			p += bits.TrailingZeros32(quote) + 1
			*pos = p
			return p - start - 1, 0, 0
		}
		if flags&scanKernelFlagRejectControl != 0 && cParityAtFirst(control, bs|quote) {
			*pos = p + bits.TrailingZeros32(control)
			return -SONIC_CONTROL_CHAR, 0, 0
		}
		if cParityAtFirst(bs, quote) {
			p += bits.TrailingZeros32(bs)
			return runsStringContinueScalar(raw, pos, p, p, start, flags)
		}
		p += 32
	}
}

func runsStringContinueScalar(raw []byte, pos *int, p, dst, start int, flags uint64) (strLenOrNegErr int, hasEsc uint64, unicodeDst int) {
	if raw[p] != '\\' {
		goto move
	}

escape:
	{
		esc := raw[p+1]
		if esc == 'u' {
			*pos = p
			return -cParityRunsStringYield, 1, dst
		}
		mapped := cParityEscaped[esc]
		if mapped == 0 {
			*pos = p
			return -SONIC_INVALID_ESCAPED, 1, 0
		}
		raw[dst] = mapped
		dst++
		p += 2
		if raw[p] == '\\' {
			goto escape
		}
	}

move:
	bs, quote, control := cParityStringMasks32Scalar(unsafe.Pointer(&raw[p]), flags)
	if cParityAtFirst(quote, bs|control) {
		adv := bits.TrailingZeros32(quote)
		copy(raw[dst:dst+adv], raw[p:p+adv])
		dst += adv
		p += adv + 1
		*pos = p
		return dst - start, 1, 0
	}
	if flags&scanKernelFlagRejectControl != 0 && cParityAtFirst(control, quote) {
		*pos = p + bits.TrailingZeros32(control)
		return -SONIC_CONTROL_CHAR, 1, 0
	}
	if cParityAtFirst(bs, quote) {
		adv := bits.TrailingZeros32(bs)
		copy(raw[dst:dst+adv], raw[p:p+adv])
		dst += adv
		p += adv
		goto escape
	}
	copy(raw[dst:dst+32], raw[p:p+32])
	dst += 32
	p += 32
	goto move
}

// cParityParseRunsScalar is the pure-Go scalar reference implementation of the
// Runs kernel protocol (cParityParseRunsAsm). It operates on the exact same
// ScanKernelState, cParityRunsState, padded input buffer, and node tape so
// SONIC_MODE=scalar and pure-Go scalar builds share 100% of Sonic's Go core.
func cParityParseRunsScalar(kern *ScanKernelState, state *cParityRunsState, raw []byte, nodes []node) int {
	pos := kern.pos
	end := kern.end
	flags := kern.scanFlags
	nlen := kern.nbuf.nlen
	lastIdx := kern.nbuf.ncap - 1
	parent := kern.nbuf.parent

	finish := func(ev int) int {
		kern.pos = pos
		kern.nbuf.nlen = nlen
		kern.nbuf.parent = parent
		return ev
	}
	yield := func(tokPos int, ev int) int {
		state.pos = tokPos
		return finish(ev)
	}

	var (
		c          byte
		tokPos     int
		context    uint64
		strRes     int
		strEsc     uint64
		unicodeDst int
		neg        bool
	)

	switch state.phase {
	case cParityRunsObjectStart:
		goto openObject
	case cParityRunsArrayStart:
		goto openArray
	case cParityRunsObjectValue:
		goto objectValue
	case cParityRunsParentContinue:
		goto parentContinue
	case cParityRunsStringResume:
		tokPos = state.pos
		context = state.context
		if state.stringErr != 0 {
			strRes = -state.stringErr
			strEsc = 1
		} else {
			strRes, strEsc, unicodeDst = runsStringContinueScalar(raw, &pos, pos, state.stringDst, tokPos+1, flags)
		}
		if context == cParityRunsContextKey {
			goto keyStringResult
		}
		goto valueStringResult
	default:
		context = cParityRunsContextRoot
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
		tokPos = pos - 1
		goto valueDispatch
	}

parentContinue:
	c = raw[pos]
	if c <= ' ' {
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
	} else {
		pos++
	}
	if nodes[parent].typ&0xff == KObject {
		goto objectContinue
	}
	goto arrayContinue

objectKey:
	if c != '"' {
		return finish(SONIC_EXPECT_KEY)
	}
	tokPos = pos - 1
	context = cParityRunsContextKey
	strRes, strEsc, unicodeDst = runsStringScalar(raw, &pos, flags)

keyStringResult:
	if strRes == -cParityRunsStringYield {
		state.stringDst = unicodeDst
		state.context = context
		return yield(tokPos, cParityRunsUnicode)
	}
	if strRes < 0 {
		return finish(-strRes)
	}
	c = raw[pos]
	if c == ':' {
		pos++
	} else {
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
		if c != ':' {
			return finish(SONIC_EXPECT_COLON)
		}
	}
	nodes[nlen] = node{
		typ: (uint64(tokPos+1) << PosBits) | (strEsc << 3) | KStringCommon,
		val: uint64(strRes),
	}
	nlen++
	if nlen > lastIdx {
		kern.nbuf.iskey = true
		return finish(cParityRunsNodeCapFull)
	}

objectValue:
	context = cParityRunsContextObject
	c = raw[pos]
	if c > ' ' {
		tokPos = pos
		pos++
		goto valueDispatch
	}
	if c == ' ' {
		c1 := raw[pos+1]
		if c1 > ' ' {
			tokPos = pos + 1
			pos += 2
			c = c1
			goto valueDispatch
		}
	}
	c = runsSpaceScalar(raw, &pos, &kern.nbk)
	tokPos = pos - 1
	goto valueDispatch

arrayValue:
	context = cParityRunsContextArray
	tokPos = pos - 1

valueDispatch:
	switch {
	case c == '"':
		strRes, strEsc, unicodeDst = runsStringScalar(raw, &pos, flags)
		goto valueStringResult
	case c >= '0' && c <= '9':
		pos--
		neg = false
		goto valueNumber
	case c == '{':
		nodes[nlen] = node{
			typ: (uint64(tokPos) << PosBits) | KObject,
			val: uint64(parent),
		}
		parent = int64(nlen)
		nlen++
		kern.nbuf.depth++
		if nlen > lastIdx {
			return finish(cParityRunsNodeCapFull)
		}
		goto openObject
	case c == '[':
		nodes[nlen] = node{
			typ: (uint64(tokPos) << PosBits) | KArray,
			val: uint64(parent),
		}
		parent = int64(nlen)
		nlen++
		kern.nbuf.depth++
		if nlen > lastIdx {
			return finish(cParityRunsNodeCapFull)
		}
		goto openArray
	case c == '-':
		neg = true
		goto valueNumber
	case c == 't':
		if raw[pos] != 'r' || raw[pos+1] != 'u' || raw[pos+2] != 'e' {
			return yield(tokPos, cParityRunsLiteral)
		}
		pos += 3
		nodes[nlen] = node{typ: (uint64(tokPos) << PosBits) | KTrue}
		nlen++
		goto afterScalar
	case c == 'f':
		if raw[pos] != 'a' || raw[pos+1] != 'l' || raw[pos+2] != 's' || raw[pos+3] != 'e' {
			return yield(tokPos, cParityRunsLiteral)
		}
		pos += 4
		nodes[nlen] = node{typ: (uint64(tokPos) << PosBits) | KFalse}
		nlen++
		goto afterScalar
	case c == 'n':
		if raw[pos] != 'u' || raw[pos+1] != 'l' || raw[pos+2] != 'l' {
			return yield(tokPos, cParityRunsLiteral)
		}
		pos += 3
		nodes[nlen] = node{typ: (uint64(tokPos) << PosBits) | KNull}
		nlen++
		goto afterScalar
	default:
		return finish(SONIC_INVALID_CHAR)
	}

openObject:
	c = raw[pos]
	if c <= ' ' {
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
	} else {
		pos++
	}
	if c == '}' {
		goto closeContainer
	}
	goto objectKey

openArray:
	c = raw[pos]
	if c <= ' ' {
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
	} else {
		pos++
	}
	if c == ']' {
		goto closeContainer
	}
	goto arrayValue

valueNumber:
	if flags&scanKernelFlagRawNumber != 0 {
		return yield(tokPos, cParityRunsNumber)
	}
	{
		bodyStart := pos
		d0 := raw[pos]
		if d0 == '0' {
			next := raw[pos+1]
			if next == '.' || next == 'e' || next == 'E' {
				return yield(tokPos, cParityRunsNumber)
			}
			pos++
			kind := uint64(KUint)
			if neg {
				kind = KSint
			}
			nodes[nlen] = node{typ: (uint64(tokPos) << PosBits) | kind, val: 0}
			nlen++
			kern.nbuf.stat.number++
			goto afterScalar
		}
		if d0 < '1' || d0 > '9' {
			return finish(SONIC_INVALID_NUM)
		}
		acc := uint64(d0 - '0')
		pos++
		digits := 1
		for {
			dc := raw[pos]
			if dc < '0' || dc > '9' {
				break
			}
			digits++
			if digits > 19 {
				pos = bodyStart
				return yield(tokPos, cParityRunsNumber)
			}
			acc = acc*10 + uint64(dc-'0')
			pos++
		}
		next := raw[pos]
		if next == '.' || next == 'e' || next == 'E' {
			pos = bodyStart
			return yield(tokPos, cParityRunsNumber)
		}
		if !neg {
			nodes[nlen] = node{typ: (uint64(tokPos) << PosBits) | KUint, val: acc}
		} else {
			if acc > 0x8000000000000000 {
				pos = bodyStart
				return yield(tokPos, cParityRunsNumber)
			}
			nodes[nlen] = node{typ: (uint64(tokPos) << PosBits) | KSint, val: -acc}
		}
		nlen++
		kern.nbuf.stat.number++
		goto afterScalar
	}

valueStringResult:
	if strRes == -cParityRunsStringYield {
		state.stringDst = unicodeDst
		state.context = context
		return yield(tokPos, cParityRunsUnicode)
	}
	{
		errCode := 0
		if strRes < 0 {
			errCode = -strRes
		}
		if context == cParityRunsContextRoot && pos > end && errCode == 0 {
			errCode = SONIC_EOF
		}
		if errCode != 0 {
			return finish(errCode)
		}
		nodes[nlen] = node{
			typ: (uint64(tokPos+1) << PosBits) | (strEsc << 3) | KStringCommon,
			val: uint64(strRes),
		}
		nlen++
		kern.nbuf.stat.str++
	}

afterScalar:
	if context == cParityRunsContextRoot {
		return finish(SONIC_OK)
	}
	if nlen > lastIdx {
		return finish(cParityRunsNodeCapFull)
	}
	c = raw[pos]
	if c <= ' ' {
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
	} else {
		pos++
	}
	if context == cParityRunsContextObject {
		goto objectContinue
	}
	goto arrayContinue

objectContinue:
	nodes[parent].typ += 1 << TypeBits
	if c == ',' {
		c = raw[pos]
		if c <= ' ' {
			c = runsSpaceScalar(raw, &pos, &kern.nbk)
		} else {
			pos++
		}
		goto objectKey
	}
	if c != '}' {
		return finish(SONIC_EXPECT_OBJ_COMMA_OR_END)
	}
	goto closeContainer

arrayContinue:
	nodes[parent].typ += 1 << TypeBits
	if c == ',' {
		c = raw[pos]
		if c > ' ' {
			pos++
			goto arrayValue
		}
		if c == ' ' {
			c1 := raw[pos+1]
			if c1 > ' ' {
				pos += 2
				c = c1
				goto arrayValue
			}
		}
		c = runsSpaceScalar(raw, &pos, &kern.nbk)
		goto arrayValue
	}
	if c != ']' {
		return finish(SONIC_EXPECT_ARR_COMMA_OR_END)
	}

closeContainer:
	{
		cnode := &nodes[parent]
		childCount := uint32(cnode.typ>>TypeBits) & 0xffffff
		kind := cnode.typ & 0xff
		prevParent := int64(cnode.val)
		if kind == KObject {
			kern.nbuf.stat.object++
			kern.nbuf.stat.object_keys += childCount
		} else {
			kern.nbuf.stat.array++
			kern.nbuf.stat.array_elems += childCount
		}
		span := uint64(nlen - int(parent))
		cnode.typ &= 0xffffffff000000ff
		cnode.val = (span << 32) | uint64(childCount)
		parent = prevParent

		d := kern.nbuf.depth
		if uint32(d) > kern.nbuf.stat.max_depth {
			kern.nbuf.stat.max_depth = uint32(d)
			if d > 4096 {
				return finish(SONIC_STACK_OVERFLOW)
			}
		}
		kern.nbuf.depth = d - 1
		if parent == -1 {
			return finish(SONIC_OK)
		}
		if (kern.nbuf.stat.object+kern.nbuf.stat.array)&0xfff == 0 {
			return finish(cParityRunsCloseContainer)
		}
		goto parentContinue
	}
}
