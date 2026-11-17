//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import (
	"math/bits"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

const (
	swarOnes      uint64 = 0x0101010101010101
	swarHigh      uint64 = 0x8080808080808080
	swarQuote     uint64 = uint64('"') * swarOnes
	swarBS        uint64 = uint64('\\') * swarOnes
	swarCtrl      uint64 = uint64(0x20) * swarOnes
	swarSpaces    uint64 = uint64(' ') * swarOnes
	spaceMask     uint64 = (uint64(1) << ' ') | (uint64(1) << '\n') | (uint64(1) << '\r') | (uint64(1) << '\t')
	numContMaskLo uint64 = (uint64(0x3ff) << '0') | (uint64(1) << '.') | (uint64(1) << '+') | (uint64(1) << '-')
)

func isNumberContByte(b byte) bool {
	return (b < 64 && (numContMaskLo&(uint64(1)<<b)) != 0) || (b|0x20) == 'e'
}

type schemaScanner struct {
	p              *Parser
	jsonStr        string
	win            paddedInputWindow
	pos            int
	end            int
	nIdx           int
	depth          uint64
	opts           uint64
	nbkOffPlus64   uint64
	nbkBits        uint64
	validateStr    bool
	noValidateJSON bool
	caseSensitive  bool
	disUnknown     bool
	mutatedPadded  bool
}

const errCodeLargeReadOnlyScan ErrorCode = -1

func parseWithSchemaOrFull(p *Parser, plan *projPlan) ErrorCode {
	if plan == nil || plan.kind == projFull || plan.kind == projScalar || plan.kind == projBytes {
		if p.end+len(padding) > largeReadOnlyScanBytes {
			return errCodeLargeReadOnlyScan
		}
		return parseImpl(p)
	}
	ok, mutated := parseSchemaRoot(p, plan)
	if ok {
		runtime.KeepAlive(p)
		return SONIC_OK
	}
	if p.end+len(padding) > largeReadOnlyScanBytes {
		return errCodeLargeReadOnlyScan
	}
	return resetAndRunFullParse(p, mutated)
}

func resetAndRunFullParse(p *Parser, mutated bool) ErrorCode {
	atomic.AddUint64(&schemaFallbackCount, 1)
	wasLazy := p.lazyPadded
	if mutated && !wasLazy {
		if p.sessionPadded != nil {
			needed := len(p.Json) + len(padding)
			if cap(p.padded) < needed {
				p.padded = make([]byte, 0, needed)
			}
			p.padded = append(p.padded[:0], p.Json...)
			p.padded = append(p.padded, padding...)
			p.sessionPadded = nil
		} else {
			p.padded = append(p.padded[:0], p.Json...)
			p.padded = append(p.padded, padding...)
		}
	}
	p.unesc = nil
	if p.unescBuf != nil {
		p.unescBuf = p.unescBuf[:0]
	}
	p.pos = 0
	p.end = len(p.Json)
	p.nbk = cParityNonspaceBlock{}
	p.nbuf = nodeBuf{}
	p.nbuf.init(p.nodes)
	if wasLazy && p.singleDoc && len(p.Json) > presizeSingleDocBytes && p.sessionPadded == nil {
		p.presizeNodesOne()
	}
	return parseImpl(p)
}

func parseSchemaRoot(p *Parser, plan *projPlan) (bool, bool) {
	if len(p.nodes) == 0 {
		n := cap(p.nodes)
		if n == 0 {
			n = defaultNodesCount
			p.nodes = make([]node, n, n)
		} else {
			p.nodes = p.nodes[:n]
		}
		p.nbuf.ncap = len(p.nodes)
	}
	var win paddedInputWindow
	if p.lazyPadded && p.sessionPadded == nil {
		win = newPaddedWindowLazy(p.Json, &p.tailBuf)
	} else {
		var ok bool
		win, ok = newPaddedWindowPadded(p.JsonBytes(), p.end)
		if !ok {
			return false, false
		}
	}
	oldOpts := p.options
	if !p.isEface && !p.keepUseNumber {
		p.options &^= 1 << _F_use_number
	}
	sc := schemaScanner{
		p:              p,
		jsonStr:        p.Json,
		win:            win,
		pos:            0,
		end:            p.end,
		nIdx:           0,
		depth:          0,
		opts:           p.options,
		validateStr:    p.options&(1<<_F_validate_string) != 0,
		noValidateJSON: p.options&(1<<_F_no_validate_json) != 0,
		caseSensitive:  p.options&uint64(consts.OptionCaseSensitive) != 0,
		disUnknown:     Options(p.options)&OptionDisableUnknown != 0,
	}
	c, ok := sc.skipSpace()
	if !ok {
		p.options = oldOpts
		return false, sc.mutatedPadded
	}
	valStart := sc.pos - 1
	ok = sc.scanByPlan(plan, c, valStart)
	p.options = oldOpts
	if !ok || sc.pos > sc.end {
		return false, sc.mutatedPadded
	}
	p.hasProjHints = true
	p.pos = sc.pos
	p.nbuf.nlen = sc.nIdx
	p.nbuf.ncap = cap(p.nodes)
	p.nbuf.parent = -1
	p.nbuf.depth = 0
	return true, sc.mutatedPadded
}

func (sc *schemaScanner) ensureNodes(extra int) {
	if sc.nIdx+extra <= len(sc.p.nodes) {
		return
	}
	sc.growTape(extra)
}

//go:noinline
func (sc *schemaScanner) growTape(extra int) {
	p := sc.p
	curLen := sc.nIdx
	nextCap := len(p.nodes) * 4
	if nextCap < 64 {
		nextCap = 64
	}
	for nextCap < curLen+extra {
		nextCap *= 2
	}
	retainInPool := nextCap <= maxPooledNodesCount
	grown := make([]node, nextCap)
	copy(grown[:curLen], p.nodes[:curLen])
	if !retainInPool && p.backup == nil && cap(p.nodes) <= maxPooledNodesCount {
		p.backup = p.nodes
	}
	p.nodes = grown
	p.nbuf.ncap = cap(p.nodes)
}

func (sc *schemaScanner) skipSpace() (byte, bool) {
	pos := sc.pos
	c := sc.win.byteAt(pos)
	if c > ' ' {
		sc.pos = pos + 1
		return c, pos < sc.end
	}
	if (spaceMask & (uint64(1) << c)) == 0 {
		sc.pos = pos + 1
		return c, pos < sc.end
	}
	c2 := sc.win.byteAt(pos + 1)
	if c2 > ' ' {
		sc.pos = pos + 2
		return c2, pos+1 < sc.end
	}
	return sc.skipSpaceSlow(pos + 1)
}

func (sc *schemaScanner) skipSpaceSlow(pos int) (byte, bool) {
	win := sc.win
	end := sc.end
	if pos > end {
		return 'x', false
	}
	if b, nextPos, hit := skipIndentSWAR(win, pos); hit {
		sc.pos = nextPos
		return b, true
	} else {
		pos = nextPos
	}
	c := win.byteAt(pos)
	if c > ' ' || (spaceMask&(uint64(1)<<c)) == 0 {
		sc.pos = pos + 1
		return c, pos < end
	}
	pos++
	if sc.nbkOffPlus64 != 0 {
		blockOff := int(sc.nbkOffPlus64) - 64
		rel := pos - blockOff
		if uint(rel) < 64 {
			mask := sc.nbkBits >> uint(rel)
			if mask != 0 {
				at := pos + bits.TrailingZeros64(mask)
				sc.pos = at + 1
				return win.byteAt(at), at < end
			}
			pos = blockOff + 64
		}
	}
	for {
		if pos > end {
			return 'x', false
		}
		mask := win.nonspace64(pos)
		if mask != 0 {
			sc.nbkBits = mask
			sc.nbkOffPlus64 = uint64(pos + 64)
			at := pos + bits.TrailingZeros64(mask)
			sc.pos = at + 1
			return win.byteAt(at), at < end
		}
		pos += 64
	}
}

func (sc *schemaScanner) scanNonEmitting(c byte, mode subtreeOutputMode, opts uint64) bool {
	nextPos, nbkOff, nbkBits, ok := sharedScanNonEmittingSubtree(
		sc.p, sc.jsonStr, sc.win, c, sc.pos, sc.depth, mode, opts, sc.nbkOffPlus64, sc.nbkBits,
	)
	if !ok {
		return false
	}
	sc.pos = nextPos
	sc.nbkOffPlus64 = nbkOff
	sc.nbkBits = nbkBits
	return true
}

// scanByPlan drives subtree output selection (Emit vs RawSpan) using shared
// scanning primitives rather than owning a parallel JSON grammar.
func (sc *schemaScanner) scanByPlan(plan *projPlan, c byte, valStart int) bool {
	if planOutputMode(plan) == subtreeOutRawSpan {
		return sc.scanRawSpan(c, valStart)
	}
	if plan == nil || plan.kind == projFull {
		return sc.scanFullSubtree(c, valStart)
	}
	switch plan.kind {
	case projScalar:
		return sc.scanScalar(c, valStart)
	case projBytes:
		if c == '[' {
			return sc.scanSlice(plan, c, valStart)
		}
		return sc.scanScalar(c, valStart)
	case projStruct:
		return sc.scanStruct(plan, c, valStart)
	case projSlice:
		return sc.scanSlice(plan, c, valStart)
	case projMap:
		return sc.scanMap(plan, c, valStart)
	default:
		return sc.scanFullSubtree(c, valStart)
	}
}

func (sc *schemaScanner) scanRawSpan(c byte, valStart int) bool {
	if c == 'n' {
		nextPos, ok := matchLiteralTail(sc.win, 'n', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	}
	// Consumed raw subtrees (json.RawMessage, Unmarshaler) always enforce strict
	// validation (subtreeOutRawSpan) even when OptionNoValidateJSONSkip is set.
	rawOpts := (sc.opts &^ (1 << _F_no_validate_json)) | (1 << _F_use_number)
	if !sc.scanNonEmitting(c, subtreeOutRawSpan, rawOpts) {
		return false
	}
	valEnd := sc.pos
	sc.ensureNodes(2)
	sc.p.nodes[sc.nIdx] = node{
		typ: KRaw | uint64(valStart)<<PosBits,
		val: uint64(valEnd - valStart),
	}
	sc.nIdx++
	return true
}

func (sc *schemaScanner) scanStruct(plan *projPlan, c byte, valStart int) bool {
	if c == 'n' {
		nextPos, ok := matchLiteralTail(sc.win, 'n', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	}
	if c != '{' {
		return false
	}
	sc.depth++
	if sc.depth > consts.MaxStack {
		return false
	}
	sc.ensureNodes(2)
	objIdx := sc.nIdx
	sc.p.nodes[objIdx] = node{typ: KObject | uint64(valStart)<<PosBits}
	sc.nIdx++

	nc, ok := sc.skipSpace()
	if !ok {
		return false
	}
	if nc == '}' {
		sc.p.nodes[objIdx].val = 1 << ConLenBits
		sc.depth--
		return true
	}

	sd := plan.sd
	fields := sd.fields
	nfields := len(fields)
	fplans := plan.fields
	validateStr := sc.validateStr
	caseSensitive := sc.caseSensitive
	disUnknown := sc.disUnknown
	discardMode := unknownFieldOutputMode(sc.noValidateJSON)
	expectedIdx := 0
	var count uint64
	win := sc.win

	for {
		if nc != '"' {
			return false
		}
		keyStart := sc.pos
		var klen int
		var keyTyp uint64
		var keyVal uint64
		hasEsc := false

		// 64-byte register SWAR fast path for keys (handles >99.9% of JSON keys with zero ABI calls).
		kp := win.blockPtr(keyStart)
		w0 := *(*uint64)(kp)
		q0 := w0 ^ swarQuote
		qm0 := (q0 - swarOnes) &^ q0 & swarHigh
		b0 := w0 ^ swarBS
		em0 := (b0 - swarOnes) &^ b0 & swarHigh
		if validateStr {
			em0 |= (w0 - swarCtrl) &^ w0 & swarHigh
		}
		if qm0 != 0 && (qm0-1)&em0 == 0 {
			klen = bits.TrailingZeros64(qm0) >> 3
			afterQuote := keyStart + klen + 1
			if afterQuote > sc.end {
				return false
			}
			sc.pos = afterQuote
			keyTyp = KStringCommon | uint64(keyStart)<<PosBits
			keyVal = uint64(klen)
		} else if qm0 == 0 && em0 == 0 {
			found := false
			for off := 8; off < 64; off += 8 {
				w := *(*uint64)(unsafe.Add(kp, off))
				q := w ^ swarQuote
				qm := (q - swarOnes) &^ q & swarHigh
				b := w ^ swarBS
				em := (b - swarOnes) &^ b & swarHigh
				if validateStr {
					em |= (w - swarCtrl) &^ w & swarHigh
				}
				if qm != 0 {
					if (qm-1)&em != 0 {
						break
					}
					klen = off + (bits.TrailingZeros64(qm) >> 3)
					afterQuote := keyStart + klen + 1
					if afterQuote > sc.end {
						return false
					}
					sc.pos = afterQuote
					keyTyp = KStringCommon | uint64(keyStart)<<PosBits
					keyVal = uint64(klen)
					found = true
					break
				}
				if em != 0 {
					break
				}
			}
			if !found {
				goto slowKey
			}
		} else {
			goto slowKey
		}
		goto afterKeyScan

	slowKey:
		{
			var afterQuote int
			keyTyp, keyVal, afterQuote, hasEsc, ok = scanSharedStringEmit(sc.p, sc.jsonStr, win, keyStart, sc.opts)
			if !ok {
				return false
			}
			sc.pos = afterQuote
			if hasEsc {
				klen = int(uint32(keyVal))
			} else {
				klen = int(keyVal)
			}
		}

	afterKeyScan:
		if win.byteAt(sc.pos) == ':' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
		} else {
			colon, ok := sc.skipSpace()
			if !ok || colon != ':' {
				return false
			}
		}

		// Authoritative field resolution via read-only sd.fieldMap.Get.
		idx := -1
		{
			var key string
			if hasEsc {
				b := sc.p.bytesEscaped(keyStart, keyVal)
				key = unsafe.String(unsafe.SliceData(b), len(b))
			} else {
				key = sc.jsonStr[keyStart : keyStart+klen]
			}
			if expectedIdx < nfields && fields[expectedIdx].name == key {
				idx = expectedIdx
				expectedIdx++
			} else {
				idx = sd.fieldMap.Get(key, caseSensitive)
				if idx >= 0 {
					expectedIdx = idx + 1
				}
			}
		}

		var vc byte
		pVal := sc.pos
		cVal := win.byteAt(pVal)
		if cVal > ' ' {
			vc = cVal
			sc.pos = pVal + 1
			if sc.pos > sc.end {
				return false
			}
		} else if cVal == ' ' {
			cVal2 := win.byteAt(pVal + 1)
			if cVal2 > ' ' {
				vc = cVal2
				sc.pos = pVal + 2
				if sc.pos > sc.end {
					return false
				}
			} else {
				vc, ok = sc.skipSpace()
				if !ok {
					return false
				}
			}
		} else {
			vc, ok = sc.skipSpace()
			if !ok {
				return false
			}
		}
		vStart := sc.pos - 1

		if idx < 0 {
			if !disUnknown {
				if !sc.scanNonEmitting(vc, discardMode, sc.opts) {
					return false
				}
			} else {
				if !sc.scanNonEmitting(vc, subtreeOutValidateOnly, sc.opts&^(1<<_F_no_validate_json)) {
					return false
				}
				sc.ensureNodes(3)
				sc.p.nodes[sc.nIdx] = node{typ: keyTyp, val: keyVal}
				sc.p.nodes[sc.nIdx+1] = node{typ: KNull | uint64(vStart)<<PosBits}
				sc.nIdx += 2
				count++
			}
		} else {
			hint := uint64(0)
			if idx < 0xFFFFFE {
				hint = uint64(idx+1) << 8
			}
			sc.ensureNodes(2)
			sc.p.nodes[sc.nIdx] = node{typ: keyTyp | hint, val: keyVal}
			sc.nIdx++
			if !sc.scanByPlan(fplans[idx], vc, vStart) {
				return false
			}
			count++
		}

		delim := win.byteAt(sc.pos)
		if delim == ',' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			cNext := win.byteAt(sc.pos)
			if cNext == '"' {
				nc = '"'
				sc.pos++
				if sc.pos > sc.end {
					return false
				}
				continue
			}
			if cNext == '\n' {
				if b, nextPos, hit := skipIndentSWAR(win, sc.pos+1); hit {
					nc = b
					sc.pos = nextPos
					continue
				}
			}
			nc, ok = sc.skipSpace()
			if !ok {
				return false
			}
			continue
		}
		if delim == '}' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			break
		}
		if delim == '\n' {
			if b, nextPos, hit := skipIndentSWAR(win, sc.pos+1); hit {
				delim = b
				sc.pos = nextPos
				if delim == '}' {
					break
				}
				if delim == ',' {
					nc, ok = sc.skipSpace()
					if !ok {
						return false
					}
					continue
				}
				return false
			}
		}
		delim, ok = sc.skipSpace()
		if !ok {
			return false
		}
		if delim == ',' {
			nc, ok = sc.skipSpace()
			if !ok {
				return false
			}
			continue
		}
		if delim == '}' {
			break
		}
		return false
	}

	span := uint64(sc.nIdx - objIdx)
	sc.p.nodes[objIdx].val = (span << ConLenBits) | count
	sc.depth--
	return true
}

func (sc *schemaScanner) scanSlice(plan *projPlan, c byte, valStart int) bool {
	if c == 'n' {
		nextPos, ok := matchLiteralTail(sc.win, 'n', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	}
	if c != '[' {
		return false
	}
	sc.depth++
	if sc.depth > consts.MaxStack {
		return false
	}
	sc.ensureNodes(2)
	arrIdx := sc.nIdx
	sc.p.nodes[arrIdx] = node{typ: KArray | uint64(valStart)<<PosBits}
	sc.nIdx++

	ec, ok := sc.skipSpace()
	if !ok {
		return false
	}
	if ec == ']' {
		sc.p.nodes[arrIdx].val = 1 << ConLenBits
		sc.depth--
		return true
	}

	elemPlan := plan.elem
	useFullElem := elemPlan == nil || elemPlan.kind == projFull
	var count uint64
	win := sc.win

	for {
		eStart := sc.pos - 1
		if useFullElem {
			if !sc.scanFullSubtree(ec, eStart) {
				return false
			}
		} else {
			if !sc.scanByPlan(elemPlan, ec, eStart) {
				return false
			}
		}
		count++

		delim := win.byteAt(sc.pos)
		if delim == ',' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			cNext := win.byteAt(sc.pos)
			if cNext > ' ' {
				ec = cNext
				sc.pos++
				if sc.pos > sc.end {
					return false
				}
				continue
			}
			if cNext == '\n' {
				if b, nextPos, hit := skipIndentSWAR(win, sc.pos+1); hit {
					ec = b
					sc.pos = nextPos
					continue
				}
			}
			ec, ok = sc.skipSpace()
			if !ok {
				return false
			}
			continue
		}
		if delim == ']' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			break
		}
		delim, ok = sc.skipSpace()
		if !ok {
			return false
		}
		if delim == ',' {
			ec, ok = sc.skipSpace()
			if !ok {
				return false
			}
			continue
		}
		if delim == ']' {
			break
		}
		return false
	}

	span := uint64(sc.nIdx - arrIdx)
	sc.p.nodes[arrIdx].val = (span << ConLenBits) | count
	sc.depth--
	return true
}

func (sc *schemaScanner) scanMap(plan *projPlan, c byte, valStart int) bool {
	if c == 'n' {
		nextPos, ok := matchLiteralTail(sc.win, 'n', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	}
	if c != '{' {
		return false
	}
	sc.depth++
	if sc.depth > consts.MaxStack {
		return false
	}
	sc.ensureNodes(2)
	objIdx := sc.nIdx
	sc.p.nodes[objIdx] = node{typ: KObject | uint64(valStart)<<PosBits}
	sc.nIdx++

	nc, ok := sc.skipSpace()
	if !ok {
		return false
	}
	if nc == '}' {
		sc.p.nodes[objIdx].val = 1 << ConLenBits
		sc.depth--
		return true
	}

	elemPlan := plan.elem
	var count uint64
	win := sc.win

	for {
		if nc != '"' {
			return false
		}
		if !sc.scanStringScalar(sc.pos - 1) {
			return false
		}
		if win.byteAt(sc.pos) == ':' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
		} else {
			colon, ok := sc.skipSpace()
			if !ok || colon != ':' {
				return false
			}
		}
		vc, ok := sc.skipSpace()
		if !ok {
			return false
		}
		if !sc.scanByPlan(elemPlan, vc, sc.pos-1) {
			return false
		}
		count++

		delim, ok := sc.skipSpace()
		if !ok {
			return false
		}
		if delim == ',' {
			nc, ok = sc.skipSpace()
			if !ok {
				return false
			}
			continue
		}
		if delim == '}' {
			break
		}
		return false
	}

	span := uint64(sc.nIdx - objIdx)
	sc.p.nodes[objIdx].val = (span << ConLenBits) | count
	sc.depth--
	return true
}

func (sc *schemaScanner) scanFullSubtree(c byte, valStart int) bool {
	if c != '{' && c != '[' {
		return sc.scanScalar(c, valStart)
	}
	savedPos := sc.pos
	savedNbkOff := sc.nbkOffPlus64
	savedNbkBits := sc.nbkBits
	if firstTok, ok := sc.skipSpace(); ok {
		if c == '[' && firstTok == ']' {
			if sc.depth+1 > consts.MaxStack {
				return false
			}
			sc.ensureNodes(2)
			sc.p.nodes[sc.nIdx] = node{typ: KArray | uint64(valStart)<<PosBits, val: 1 << ConLenBits}
			sc.nIdx++
			return true
		}
		if c == '{' && firstTok == '}' {
			if sc.depth+1 > consts.MaxStack {
				return false
			}
			sc.ensureNodes(2)
			sc.p.nodes[sc.nIdx] = node{typ: KObject | uint64(valStart)<<PosBits, val: 1 << ConLenBits}
			sc.nIdx++
			return true
		}
	}
	sc.pos = savedPos
	sc.nbkOffPlus64 = savedNbkOff
	sc.nbkBits = savedNbkBits

	sc.ensureNodes(16)
	p := sc.p
	p.ensurePadded()
	nbk := &p.nbk
	nbk.offPlus64 = sc.nbkOffPlus64
	nbk.bits = sc.nbkBits
	p.pos = valStart
	p.nbuf.nlen = sc.nIdx
	p.nbuf.ncap = len(p.nodes)
	savedParent := p.nbuf.parent
	savedDepth := p.nbuf.depth
	savedMaxDepth := p.nbuf.stat.max_depth
	p.nbuf.parent = -1
	p.nbuf.depth = 0
	p.nbuf.stat.max_depth = 0
	sc.mutatedPadded = true

	err := parseRunsGrowing(p)

	subMaxDepth := uint64(p.nbuf.stat.max_depth)
	if uint64(savedMaxDepth) > subMaxDepth {
		p.nbuf.stat.max_depth = savedMaxDepth
	}
	p.nbuf.parent = savedParent
	p.nbuf.depth = savedDepth
	sc.nbkOffPlus64 = nbk.offPlus64
	sc.nbkBits = nbk.bits

	if err != SONIC_OK || sc.depth+subMaxDepth > consts.MaxStack || p.pos > sc.end {
		return false
	}
	sc.pos = p.pos
	sc.nIdx = p.nbuf.nlen
	return true
}

func (sc *schemaScanner) scanStringScalar(valStart int) bool {
	typ, val, afterQuote, _, ok := scanSharedStringEmit(sc.p, sc.jsonStr, sc.win, valStart+1, sc.opts)
	if !ok {
		return false
	}
	sc.pos = afterQuote
	sc.ensureNodes(2)
	sc.p.nodes[sc.nIdx] = node{typ: typ, val: val}
	sc.nIdx++
	return true
}

func (sc *schemaScanner) scanScalar(c byte, valStart int) bool {
	switch c {
	case '"':
		return sc.scanStringScalar(valStart)
	case 't':
		nextPos, ok := matchLiteralTail(sc.win, 't', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KTrue | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	case 'f':
		nextPos, ok := matchLiteralTail(sc.win, 'f', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KFalse | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	case 'n':
		nextPos, ok := matchLiteralTail(sc.win, 'n', sc.pos)
		if !ok {
			return false
		}
		sc.pos = nextPos
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	default:
		if (c >= '0' && c <= '9') || c == '-' {
			nd, numEnd, ok := scanSharedNumberEmit(sc.p, sc.jsonStr, sc.win, c, valStart, sc.pos, sc.opts)
			if !ok {
				return false
			}
			sc.pos = numEnd
			sc.ensureNodes(2)
			sc.p.nodes[sc.nIdx] = nd
			sc.nIdx++
			return true
		}
		return false
	}
}
