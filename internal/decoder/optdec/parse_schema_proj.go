//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"math/bits"
	"runtime"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

const (
	swarOnes   uint64 = 0x0101010101010101
	swarHigh   uint64 = 0x8080808080808080
	swarQuote  uint64 = uint64('"') * swarOnes
	swarBS     uint64 = uint64('\\') * swarOnes
	swarCtrl   uint64 = uint64(0x20) * swarOnes
	swarSpaces uint64 = uint64(' ') * swarOnes
	spaceMask  uint64 = (uint64(1) << ' ') | (uint64(1) << '\n') | (uint64(1) << '\r') | (uint64(1) << '\t')
)

type schemaScanner struct {
	p             *Parser
	jsonStr       string
	rawPtr        *byte
	pos           int
	end           int
	nIdx          int
	depth         uint64
	opts          uint64
	nbkOffPlus64  uint64
	nbkBits       uint64
	validateStr   bool
	caseSensitive bool
	disUnknown    bool
	mutatedPadded bool
	denseBailout  bool
	skippedKeys   int
	matchedKeys   int
	num           cParityNum
}

func parseWithSchemaOrFull(p *Parser, plan *projPlan) ErrorCode {
	if !plan.shouldUse(p.end) {
		return parseImpl(p)
	}
	ok, mutated := parseSchemaRoot(p, plan)
	if ok {
		runtime.KeepAlive(p)
		return SONIC_OK
	}
	return resetAndRunFullParse(p, mutated)
}

func resetAndRunFullParse(p *Parser, mutated bool) ErrorCode {
	if mutated {
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
	p.pos = 0
	p.end = len(p.Json)
	p._nbk = _nospaceBlock{}
	p.nbuf = nodeBuf{}
	p.nbuf.init(p.nodes)
	return parseImpl(p)
}

func parseSchemaRoot(p *Parser, plan *projPlan) (bool, bool) {
	if len(p.nodes) == 0 {
		n := cap(p.nodes)
		if n == 0 {
			n = int(defaultNodesCap)
			p.nodes = make([]node, n, n)
		} else {
			p.nodes = p.nodes[:n]
		}
		p.nbuf.ncap = len(p.nodes)
	}
	raw := p.JsonBytes()
	if len(raw) < p.end+len(padding) {
		return false, false
	}
	oldOpts := p.options
	if !p.isEface {
		p.options &^= 1 << _F_use_number
	}
	sc := schemaScanner{
		p:             p,
		jsonStr:       p.Json,
		rawPtr:        unsafe.SliceData(raw),
		pos:           0,
		end:           p.end,
		nIdx:          0,
		depth:         0,
		opts:          p.options,
		validateStr:   p.options&(1<<5) != 0,
		caseSensitive: p.options&uint64(consts.OptionCaseSensitive) != 0,
		disUnknown:    Options(p.options)&OptionDisableUnknown != 0,
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
		if sc.denseBailout && !plan.hasRawSubtree {
			plan.recordDense()
		}
		return false, sc.mutatedPadded
	}
	if !plan.hasRawSubtree {
		if sc.skippedKeys*4 < sc.matchedKeys {
			plan.recordDense()
		} else {
			plan.recordSparse()
		}
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
	retainInPool := uintptr(nextCap) <= maxPooledNodesCap
	grown := make([]node, nextCap)
	copy(grown[:curLen], p.nodes[:curLen])
	if !retainInPool && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
		p.backup = p.nodes
	}
	p.nodes = grown
	p.nbuf.ncap = cap(p.nodes)
}

// skipIndentSWAR skips up to 24 ASCII spaces or tabs starting at pos (typically right after '\n').
// Returns (ch, nextPos, true) if a non-whitespace byte (> ' ') is found within 24 bytes.
func skipIndentSWAR(rawPtr unsafe.Pointer, pos, end int) (byte, int, bool) {
	w0 := *(*uint64)(unsafe.Add(rawPtr, pos)) ^ swarSpaces
	if w0 != 0 {
		at := pos + (bits.TrailingZeros64(w0) >> 3)
		b := *(*byte)(unsafe.Add(rawPtr, at))
		if b > ' ' {
			return b, at + 1, at < end
		}
		if b == '\t' {
			for {
				at++
				b = *(*byte)(unsafe.Add(rawPtr, at))
				if b != '\t' && b != ' ' {
					break
				}
			}
			if b > ' ' {
				return b, at + 1, at < end
			}
		}
		return 0, at, false
	}
	w1 := *(*uint64)(unsafe.Add(rawPtr, pos+8)) ^ swarSpaces
	if w1 != 0 {
		at := pos + 8 + (bits.TrailingZeros64(w1) >> 3)
		b := *(*byte)(unsafe.Add(rawPtr, at))
		if b > ' ' {
			return b, at + 1, at < end
		}
		return 0, at, false
	}
	w2 := *(*uint64)(unsafe.Add(rawPtr, pos+16)) ^ swarSpaces
	if w2 != 0 {
		at := pos + 16 + (bits.TrailingZeros64(w2) >> 3)
		b := *(*byte)(unsafe.Add(rawPtr, at))
		if b > ' ' {
			return b, at + 1, at < end
		}
		return 0, at, false
	}
	return 0, pos + 24, false
}

func (sc *schemaScanner) skipSpace() (byte, bool) {
	pos := sc.pos
	rawPtr := unsafe.Pointer(sc.rawPtr)
	c := *(*byte)(unsafe.Add(rawPtr, pos))
	if c > ' ' {
		sc.pos = pos + 1
		return c, pos < sc.end
	}
	if (spaceMask & (uint64(1) << c)) == 0 {
		sc.pos = pos + 1
		return c, pos < sc.end
	}
	c2 := *(*byte)(unsafe.Add(rawPtr, pos+1))
	if c2 > ' ' {
		sc.pos = pos + 2
		return c2, pos+1 < sc.end
	}
	return sc.skipSpaceSlow(pos + 1)
}

func (sc *schemaScanner) skipSpaceSlow(pos int) (byte, bool) {
	rawPtr := unsafe.Pointer(sc.rawPtr)
	end := sc.end
	if b, nextPos, hit := skipIndentSWAR(rawPtr, pos, end); hit {
		sc.pos = nextPos
		return b, true
	} else {
		pos = nextPos
	}
	c := *(*byte)(unsafe.Add(rawPtr, pos))
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
				return *(*byte)(unsafe.Add(rawPtr, at)), at < end
			}
			pos = blockOff + 64
		}
	}
	for {
		mask := cParityNonspace64(unsafe.Add(rawPtr, pos))
		if mask != 0 {
			sc.nbkBits = mask
			sc.nbkOffPlus64 = uint64(pos + 64)
			at := pos + bits.TrailingZeros64(mask)
			sc.pos = at + 1
			return *(*byte)(unsafe.Add(rawPtr, at)), at < end
		}
		pos += 64
	}
}

func (sc *schemaScanner) scanByPlan(plan *projPlan, c byte, valStart int) bool {
	if plan == nil {
		return sc.scanFullSubtree(c, valStart)
	}
	switch plan.kind {
	case projScalar:
		return sc.scanScalar(c, valStart)
	case projRaw:
		return sc.scanRaw(c, valStart)
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

func (sc *schemaScanner) scanRaw(c byte, valStart int) bool {
	if c == 'n' {
		if !sc.matchNullTail() {
			return false
		}
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	}
	if !sc.skipValidateValue(c) {
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
		if !sc.matchNullTail() {
			return false
		}
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
	allASCIIKeys := plan.allASCIIKeys
	hasLongKey := plan.hasLongKey
	keyLenMask := plan.keyLenMask
	firstLo := plan.firstLo
	firstHi := plan.firstHi
	validateStr := sc.validateStr
	caseSensitive := sc.caseSensitive
	disUnknown := sc.disUnknown
	expectedIdx := 0
	var count uint64
	rawPtr := unsafe.Pointer(sc.rawPtr)

	for {
		if nc != '"' {
			return false
		}
		keyStart := sc.pos
		var klen int
		var keyTyp uint64
		hasEsc := false
		isASCIIKey := false

		// 64-byte register SWAR fast path for keys (handles >99.9% of JSON keys with zero ABI calls).
		w0 := *(*uint64)(unsafe.Add(rawPtr, keyStart))
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
			isASCIIKey = (w0 & (qm0 - 1) & swarHigh) == 0
		} else if qm0 == 0 && em0 == 0 {
			highAcc := w0
			found := false
			for off := 8; off < 64; off += 8 {
				w := *(*uint64)(unsafe.Add(rawPtr, keyStart+off))
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
					isASCIIKey = ((highAcc | (w & (qm - 1))) & swarHigh) == 0
					found = true
					break
				}
				if em != 0 {
					break
				}
				highAcc |= w
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
			cur := unsafe.Add(rawPtr, keyStart)
			dlen := cParityParseString(&cur, &hasEsc, sc.opts)
			if dlen < 0 {
				return false
			}
			afterQuote := int(uintptr(cur) - uintptr(rawPtr))
			if afterQuote > sc.end {
				return false
			}
			sc.pos = afterQuote
			klen = dlen
			if hasEsc {
				sc.mutatedPadded = true
				keyTyp = KStringEscaped | uint64(keyStart)<<PosBits
			} else {
				keyTyp = KStringCommon | uint64(keyStart)<<PosBits
			}
		}

	afterKeyScan:
		if *(*byte)(unsafe.Add(rawPtr, sc.pos)) == ':' {
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

		// O(1) bloom-filter rejection for unknown keys before constructing string or hashing.
		idx := -1
		bloomRejected := false
		if !disUnknown && !hasEsc && allASCIIKeys {
			if klen > 0 {
				c0 := byte(w0)
				if c0 < 128 {
					var bit uint64
					if c0 < 64 {
						bit = firstLo >> c0
					} else {
						bit = firstHi >> (c0 - 64)
					}
					if bit&1 == 0 {
						bloomRejected = true
					}
				}
			} else if (keyLenMask & 1) == 0 {
				bloomRejected = true
			}
			if !bloomRejected && isASCIIKey {
				if klen < 64 {
					if (keyLenMask & (uint64(1) << uint(klen))) == 0 {
						bloomRejected = true
					}
				} else if !hasLongKey {
					bloomRejected = true
				}
			}
		}

		if !bloomRejected {
			var key string
			if hasEsc {
				b := unsafe.Slice((*byte)(unsafe.Add(rawPtr, keyStart)), klen)
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
		cVal := *(*byte)(unsafe.Add(rawPtr, pVal))
		if cVal > ' ' {
			vc = cVal
			sc.pos = pVal + 1
			if sc.pos > sc.end {
				return false
			}
		} else if cVal == ' ' {
			cVal2 := *(*byte)(unsafe.Add(rawPtr, pVal+1))
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
				sc.skippedKeys++
				if !sc.skipValidateValue(vc) {
					return false
				}
			} else {
				if !sc.skipValidateValue(vc) {
					return false
				}
				sc.ensureNodes(3)
				sc.p.nodes[sc.nIdx] = node{typ: keyTyp, val: uint64(klen)}
				sc.p.nodes[sc.nIdx+1] = node{typ: KNull | uint64(vStart)<<PosBits}
				sc.nIdx += 2
				count++
			}
		} else {
			sc.matchedKeys++
			hint := uint64(0)
			if idx < 0xFFFFFE {
				hint = uint64(idx+1) << 8
			}
			sc.ensureNodes(2)
			sc.p.nodes[sc.nIdx] = node{typ: keyTyp | hint, val: uint64(klen)}
			sc.nIdx++
			if !sc.scanByPlan(fplans[idx], vc, vStart) {
				return false
			}
			count++
		}

		delim := *(*byte)(unsafe.Add(rawPtr, sc.pos))
		if delim == ',' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			cNext := *(*byte)(unsafe.Add(rawPtr, sc.pos))
			if cNext == '"' {
				nc = '"'
				sc.pos++
				if sc.pos > sc.end {
					return false
				}
				continue
			}
			if cNext == '\n' {
				if b, nextPos, hit := skipIndentSWAR(rawPtr, sc.pos+1, sc.end); hit {
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
			if b, nextPos, hit := skipIndentSWAR(rawPtr, sc.pos+1, sc.end); hit {
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
		if !sc.matchNullTail() {
			return false
		}
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
	checkAdaptive := elemPlan != nil && elemPlan.kind == projStruct && !elemPlan.hasRawSubtree
	var count uint64
	rawPtr := unsafe.Pointer(sc.rawPtr)

	for {
		eStart := sc.pos - 1
		if useFullElem {
			if !sc.scanFullSubtree(ec, eStart) {
				return false
			}
		} else if checkAdaptive && count == 0 {
			beforeSkip := sc.skippedKeys
			if !sc.scanByPlan(elemPlan, ec, eStart) {
				return false
			}
			if sc.skippedKeys == beforeSkip {
				if arrIdx == 0 && !sc.mutatedPadded {
					sc.denseBailout = true
					return false
				}
				useFullElem = true
			}
		} else {
			if !sc.scanByPlan(elemPlan, ec, eStart) {
				return false
			}
		}
		count++

		delim := *(*byte)(unsafe.Add(rawPtr, sc.pos))
		if delim == ',' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			cNext := *(*byte)(unsafe.Add(rawPtr, sc.pos))
			if cNext > ' ' {
				ec = cNext
				sc.pos++
				if sc.pos > sc.end {
					return false
				}
				continue
			}
			if cNext == '\n' {
				if b, nextPos, hit := skipIndentSWAR(rawPtr, sc.pos+1, sc.end); hit {
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
		if !sc.matchNullTail() {
			return false
		}
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
	rawPtr := unsafe.Pointer(sc.rawPtr)

	for {
		if nc != '"' {
			return false
		}
		if !sc.scanStringScalar(sc.pos - 1) {
			return false
		}
		if *(*byte)(unsafe.Add(rawPtr, sc.pos)) == ':' {
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
	sc.ensureNodes(16)
	p := sc.p
	nbk := (*cParityNonspaceBlock)(unsafe.Pointer(&p._nbk))
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

	err := cParityParseWithPadding(p)
	for classifyScanStop(err) == scanStopNodeCapExhausted {
		p.growNodeTape()
		err = cParityParseWithPadding(p)
	}

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

func (sc *schemaScanner) scanScalar(c byte, valStart int) bool {
	switch c {
	case '"':
		return sc.scanStringScalar(valStart)
	case 't':
		if !sc.matchTrueTail() {
			return false
		}
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KTrue | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	case 'f':
		if !sc.matchFalseTail() {
			return false
		}
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KFalse | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	case 'n':
		if !sc.matchNullTail() {
			return false
		}
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KNull | uint64(valStart)<<PosBits}
		sc.nIdx++
		return true
	default:
		if (c >= '0' && c <= '9') || c == '-' {
			return sc.scanNumberScalar(c, valStart)
		}
		return false
	}
}

func swarSkipString64(rawPtr unsafe.Pointer, pos, end int, validateStr bool) (int, bool, bool) {
	for off := 0; off < 64; off += 8 {
		w := *(*uint64)(unsafe.Add(rawPtr, pos+off))
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
	return pos + 64, false, false
}

func (sc *schemaScanner) scanStringScalar(valStart int) bool {
	contentStart := valStart + 1
	rawPtr := unsafe.Pointer(sc.rawPtr)
	if afterQuote, done, valid := swarSkipString64(rawPtr, contentStart, sc.end, sc.validateStr); done {
		if !valid {
			return false
		}
		slen := afterQuote - contentStart - 1
		sc.pos = afterQuote
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{
			typ: KStringCommon | uint64(contentStart)<<PosBits,
			val: uint64(slen),
		}
		sc.nIdx++
		return true
	}

	cur := unsafe.Add(rawPtr, contentStart)
	var hasEsc bool
	dlen := cParityParseString(&cur, &hasEsc, sc.opts)
	if dlen < 0 {
		return false
	}
	afterQuote := int(uintptr(cur) - uintptr(rawPtr))
	if afterQuote > sc.end {
		return false
	}
	sc.pos = afterQuote
	typ := uint64(KStringCommon)
	if hasEsc {
		sc.mutatedPadded = true
		typ = KStringEscaped
	}
	sc.ensureNodes(2)
	sc.p.nodes[sc.nIdx] = node{
		typ: typ | uint64(contentStart)<<PosBits,
		val: uint64(dlen),
	}
	sc.nIdx++
	return true
}

func (sc *schemaScanner) scanNumberScalar(first byte, valStart int) bool {
	rawPtr := unsafe.Pointer(sc.rawPtr)
	pos := sc.pos
	end := sc.end

	if first >= '1' && first <= '9' {
		uval := uint64(first - '0')
		digits := 1
		for digits < 19 {
			d := *(*byte)(unsafe.Add(rawPtr, pos))
			if d < '0' || d > '9' {
				if d != '.' && (d|0x20) != 'e' {
					if pos > end {
						return false
					}
					sc.pos = pos
					sc.ensureNodes(2)
					sc.p.nodes[sc.nIdx] = node{typ: KUint | uint64(valStart)<<PosBits, val: uval}
					sc.nIdx++
					return true
				}
				break
			}
			uval = uval*10 + uint64(d-'0')
			pos++
			digits++
		}
	} else if first == '0' {
		d := *(*byte)(unsafe.Add(rawPtr, pos))
		if d != '.' && (d|0x20) != 'e' {
			if pos > end {
				return false
			}
			sc.ensureNodes(2)
			sc.p.nodes[sc.nIdx] = node{typ: KUint | uint64(valStart)<<PosBits, val: 0}
			sc.nIdx++
			return true
		}
	} else if first == '-' {
		d0 := *(*byte)(unsafe.Add(rawPtr, pos))
		if d0 >= '1' && d0 <= '9' {
			uval := uint64(d0 - '0')
			p2 := pos + 1
			digits := 1
			for digits < 19 {
				d := *(*byte)(unsafe.Add(rawPtr, p2))
				if d < '0' || d > '9' {
					if d != '.' && (d|0x20) != 'e' {
						if p2 > end {
							return false
						}
						if uval <= math.MaxInt64+1 {
							sc.pos = p2
							sc.ensureNodes(2)
							if uval == math.MaxInt64+1 {
								sc.p.nodes[sc.nIdx] = node{typ: KSint | uint64(valStart)<<PosBits, val: uint64(1) << 63}
							} else {
								sc.p.nodes[sc.nIdx] = node{typ: KSint | uint64(valStart)<<PosBits, val: uint64(-int64(uval))}
							}
							sc.nIdx++
							return true
						}
					}
					break
				}
				uval = uval*10 + uint64(d-'0')
				p2++
				digits++
			}
		}
	}

	neg := first == '-'
	numBody := valStart
	if neg {
		numBody++
	}
	nextCur, numVal, code := cParityParseNumberValue(unsafe.Add(rawPtr, numBody), sc.num, neg, sc.p.dbuf)
	if code != SONIC_OK {
		return false
	}
	nextPos := int(uintptr(nextCur) - uintptr(rawPtr))
	if nextPos > end {
		return false
	}
	sc.num = numVal
	sc.pos = nextPos
	sc.ensureNodes(2)
	sc.p.nodes[sc.nIdx] = node{typ: numVal.typ | uint64(valStart)<<PosBits, val: numVal.val}
	sc.nIdx++
	return true
}

func (sc *schemaScanner) matchTrueTail() bool {
	if sc.pos+3 > sc.end {
		return false
	}
	p := unsafe.Add(unsafe.Pointer(sc.rawPtr), sc.pos)
	if (*(*uint32)(p) & 0x00ffffff) != 0x00657572 {
		return false
	}
	sc.pos += 3
	return true
}

func (sc *schemaScanner) matchFalseTail() bool {
	if sc.pos+4 > sc.end {
		return false
	}
	p := unsafe.Add(unsafe.Pointer(sc.rawPtr), sc.pos)
	if *(*uint32)(p) != 0x65736c61 {
		return false
	}
	sc.pos += 4
	return true
}

func (sc *schemaScanner) matchNullTail() bool {
	if sc.pos+3 > sc.end {
		return false
	}
	p := unsafe.Add(unsafe.Pointer(sc.rawPtr), sc.pos)
	if (*(*uint32)(p) & 0x00ffffff) != 0x006c6c75 {
		return false
	}
	sc.pos += 3
	return true
}

// skipValidateValue handles scalar values immediately without touching a container stack,
// and dispatches objects/arrays to skipValidateContainer (64-level register bit-stack).
func (sc *schemaScanner) skipValidateValue(c byte) bool {
	rawPtr := unsafe.Pointer(sc.rawPtr)
	pos := sc.pos
	end := sc.end
	switch c {
	case '"':
		if nextPos, done, valid := swarSkipString64(rawPtr, pos, end, sc.validateStr); done {
			if !valid {
				return false
			}
			sc.pos = nextPos
			return true
		} else {
			var ok bool
			pos, ok = skipStringBodyAt(rawPtr, nextPos, end, sc.opts)
			if !ok {
				return false
			}
			sc.pos = pos
			return true
		}
	case 't':
		if pos+3 > end || (*(*uint32)(unsafe.Add(rawPtr, pos))&0x00ffffff) != 0x00657572 {
			return false
		}
		sc.pos = pos + 3
		return true
	case 'f':
		if pos+4 > end || *(*uint32)(unsafe.Add(rawPtr, pos)) != 0x65736c61 {
			return false
		}
		sc.pos = pos + 4
		return true
	case 'n':
		if pos+3 > end || (*(*uint32)(unsafe.Add(rawPtr, pos))&0x00ffffff) != 0x006c6c75 {
			return false
		}
		sc.pos = pos + 3
		return true
	case '{', '[':
		return sc.skipValidateContainer(c)
	default:
		if c >= '1' && c <= '9' {
			p2 := pos
			for {
				d := *(*byte)(unsafe.Add(rawPtr, p2))
				if d < '0' || d > '9' {
					if d != '.' && (d|0x20) != 'e' && p2-pos < 199 {
						if p2 > end {
							return false
						}
						sc.pos = p2
						return true
					}
					break
				}
				p2++
			}
		} else if c == '0' {
			d := *(*byte)(unsafe.Add(rawPtr, pos))
			if d != '.' && (d|0x20) != 'e' {
				if pos > end {
					return false
				}
				return true
			}
		} else if c != '-' {
			return false
		}
		nextPos, ok := skipNumberAt(rawPtr, c, pos, end, sc.opts&(1<<_F_use_number) == 0)
		if !ok {
			return false
		}
		sc.pos = nextPos
		return true
	}
}

// skipValidateContainer validates and skips a '{' or '[' subtree with a 64-level
// bit-stack in a single uint64 register (0 = object, 1 = array), zero closures,
// zero stack-array zeroing, inline SWAR indentation & string scanning.
func (sc *schemaScanner) skipValidateContainer(c byte) bool {
	var frameBits uint64
	sp := 0
	maxSp := int(consts.MaxStack - sc.depth)
	if maxSp > 64 {
		maxSp = 64
	}

	rawPtr := unsafe.Pointer(sc.rawPtr)
	pos := sc.pos
	end := sc.end
	opts := sc.opts
	validateStr := sc.validateStr
	checkFloatInf := opts&(1<<_F_use_number) == 0
	nbkOffPlus64 := sc.nbkOffPlus64
	nbkBits := sc.nbkBits

	var nc, vc, delim, colon byte
	var nextPos int
	var done, valid, ok bool

valueEntry:
	switch c {
	case '"':
		if nextPos, done, valid = swarSkipString64(rawPtr, pos, end, validateStr); done {
			if !valid {
				return false
			}
			pos = nextPos
			goto afterValue
		}
		pos, ok = skipStringBodyAt(rawPtr, nextPos, end, opts)
		if !ok {
			return false
		}
	case '{':
		if sp >= maxSp {
			return false
		}
		frameBits <<= 1
		sp++
		nc = *(*byte)(unsafe.Add(rawPtr, pos))
		if nc > ' ' {
			pos++
			if pos > end {
				return false
			}
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, np, end, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
			if !ok {
				return false
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
			return false
		}
		frameBits = (frameBits << 1) | 1
		sp++
		nc = *(*byte)(unsafe.Add(rawPtr, pos))
		if nc > ' ' {
			pos++
			if pos > end {
				return false
			}
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, np, end, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
		if nc == ']' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		c = nc
		goto valueEntry
	case 't':
		if pos+3 > end || (*(*uint32)(unsafe.Add(rawPtr, pos))&0x00ffffff) != 0x00657572 {
			return false
		}
		pos += 3
	case 'f':
		if pos+4 > end || *(*uint32)(unsafe.Add(rawPtr, pos)) != 0x65736c61 {
			return false
		}
		pos += 4
	case 'n':
		if pos+3 > end || (*(*uint32)(unsafe.Add(rawPtr, pos))&0x00ffffff) != 0x006c6c75 {
			return false
		}
		pos += 3
	default:
		if c >= '1' && c <= '9' {
			p2 := pos
			for {
				d := *(*byte)(unsafe.Add(rawPtr, p2))
				if d < '0' || d > '9' {
					if d != '.' && (d|0x20) != 'e' && p2-pos < 199 {
						if p2 > end {
							return false
						}
						pos = p2
						goto afterValue
					}
					break
				}
				p2++
			}
		} else if c == '0' {
			d := *(*byte)(unsafe.Add(rawPtr, pos))
			if d != '.' && (d|0x20) != 'e' {
				if pos > end {
					return false
				}
				goto afterValue
			}
		} else if c != '-' {
			return false
		}
		pos, ok = skipNumberAt(rawPtr, c, pos, end, checkFloatInf)
		if !ok {
			return false
		}
	}

afterValue:
	if sp == 0 {
		sc.pos = pos
		sc.nbkOffPlus64 = nbkOffPlus64
		sc.nbkBits = nbkBits
		return true
	}
	if (frameBits & 1) != 0 {
		goto arrAfterValue
	}
	delim = *(*byte)(unsafe.Add(rawPtr, pos))
	if delim == ',' {
		pos++
		if pos > end {
			return false
		}
		cNext := *(*byte)(unsafe.Add(rawPtr, pos))
		if cNext == '"' {
			pos++
			if pos > end {
				return false
			}
			goto objKeyBody
		}
		if cNext == '\n' {
			if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
				nc = b
				pos = np
				goto objKey
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, np, end, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
				goto objKey
			}
		}
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
		goto objKey
	}
	if delim == '}' {
		pos++
		if pos > end {
			return false
		}
		frameBits >>= 1
		sp--
		goto afterValue
	}
	if delim == '\n' {
		if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, np, end, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
	}
	if delim == ',' {
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
		goto objKey
	}
	if delim == '}' {
		frameBits >>= 1
		sp--
		goto afterValue
	}
	return false

objKey:
	if nc != '"' {
		return false
	}
objKeyBody:
	if nextPos, done, valid = swarSkipString64(rawPtr, pos, end, validateStr); done {
		if !valid {
			return false
		}
		pos = nextPos
		goto objAfterKey
	}
	pos, ok = skipStringBodyAt(rawPtr, nextPos, end, opts)
	if !ok {
		return false
	}
objAfterKey:
	if *(*byte)(unsafe.Add(rawPtr, pos)) == ':' {
		pos++
		if pos > end {
			return false
		}
	} else {
		colon, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok || colon != ':' {
			return false
		}
	}
	vc = *(*byte)(unsafe.Add(rawPtr, pos))
	if vc > ' ' {
		pos++
		if pos > end {
			return false
		}
		c = vc
		goto valueEntry
	}
	if vc == ' ' {
		vc2 := *(*byte)(unsafe.Add(rawPtr, pos+1))
		if vc2 > ' ' {
			pos += 2
			if pos > end {
				return false
			}
			c = vc2
			goto valueEntry
		}
	}
	vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
	if !ok {
		return false
	}
	c = vc
	goto valueEntry

arrAfterValue:
	delim = *(*byte)(unsafe.Add(rawPtr, pos))
	if delim == ',' {
		pos++
		if pos > end {
			return false
		}
		vc = *(*byte)(unsafe.Add(rawPtr, pos))
		if vc > ' ' {
			pos++
			if pos > end {
				return false
			}
			c = vc
			goto valueEntry
		}
		if vc == '\n' {
			if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
				c = b
				pos = np
				goto valueEntry
			} else {
				vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, np, end, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
				c = vc
				goto valueEntry
			}
		}
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
		c = vc
		goto valueEntry
	}
	if delim == ']' {
		pos++
		if pos > end {
			return false
		}
		frameBits >>= 1
		sp--
		goto afterValue
	}
	if delim == '\n' {
		if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, np, end, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
	}
	if delim == ',' {
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, pos, end, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
		c = vc
		goto valueEntry
	}
	if delim == ']' {
		frameBits >>= 1
		sp--
		goto afterValue
	}
	return false
}

func skipSpaceReg(rawPtr unsafe.Pointer, pos, end int, nbkOffPlus64, nbkBits uint64) (byte, int, uint64, uint64, bool) {
	b := *(*byte)(unsafe.Add(rawPtr, pos))
	if b > ' ' || (spaceMask&(uint64(1)<<b)) == 0 {
		return b, pos + 1, nbkOffPlus64, nbkBits, pos < end
	}
	if b, np, hit := skipIndentSWAR(rawPtr, pos+1, end); hit {
		return b, np, nbkOffPlus64, nbkBits, true
	} else {
		pos = np
	}
	b = *(*byte)(unsafe.Add(rawPtr, pos))
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
				return *(*byte)(unsafe.Add(rawPtr, at)), at + 1, nbkOffPlus64, nbkBits, at < end
			}
			pos = blockOff + 64
		}
	}
	for {
		mask := cParityNonspace64(unsafe.Add(rawPtr, pos))
		if mask != 0 {
			nbkBits = mask
			nbkOffPlus64 = uint64(pos + 64)
			at := pos + bits.TrailingZeros64(mask)
			return *(*byte)(unsafe.Add(rawPtr, at)), at + 1, nbkOffPlus64, nbkBits, at < end
		}
		pos += 64
	}
}

func skipStringBodyAt(rawPtr unsafe.Pointer, pos, end int, opts uint64) (int, bool) {
	for {
		bs, quote, control := cParityStringMasks32(unsafe.Add(rawPtr, pos), opts)
		if cParityAtFirst(quote, bs|control) {
			pos += bits.TrailingZeros32(quote) + 1
			return pos, pos <= end
		}
		if cParityAtFirst(bs, quote) {
			pos += bits.TrailingZeros32(bs)
			break
		}
		if opts&(1<<5) != 0 && cParityAtFirst(control, quote) {
			return pos, false
		}
		pos += 32
	}

	for {
		if pos+2 > end {
			return pos, false
		}
		esc := *(*byte)(unsafe.Add(rawPtr, pos+1))
		if esc != 'u' {
			if cParityEscaped[esc] == 0 {
				return pos, false
			}
			pos += 2
		} else {
			for {
				if pos+6 > end {
					return pos, false
				}
				if _, ok := cParityReadHex16(unsafe.Add(rawPtr, pos+2)); !ok {
					return pos, false
				}
				pos += 6
				if *(*byte)(unsafe.Add(rawPtr, pos)) != '\\' || *(*byte)(unsafe.Add(rawPtr, pos+1)) != 'u' {
					break
				}
			}
		}
		if *(*byte)(unsafe.Add(rawPtr, pos)) == '\\' {
			continue
		}
		for {
			bs, quote, control := cParityStringMasks32(unsafe.Add(rawPtr, pos), opts)
			if cParityAtFirst(quote, bs|control) {
				pos += bits.TrailingZeros32(quote) + 1
				return pos, pos <= end
			}
			if opts&(1<<5) != 0 && cParityAtFirst(control, quote) {
				return pos, false
			}
			if cParityAtFirst(bs, quote) {
				pos += bits.TrailingZeros32(bs)
				break
			}
			pos += 32
		}
	}
}

func skipNumberAt(rawPtr unsafe.Pointer, first byte, pos, end int, checkFloatInf bool) (int, bool) {
	if first == '-' {
		first = *(*byte)(unsafe.Add(rawPtr, pos))
		pos++
		if pos > end {
			return pos, false
		}
	}

	intDigits := 0
	if first == '0' {
		intDigits = 1
	} else if first >= '1' && first <= '9' {
		intDigits = 1
		for {
			d := *(*byte)(unsafe.Add(rawPtr, pos))
			if d < '0' || d > '9' {
				break
			}
			pos++
			intDigits++
		}
	} else {
		return pos, false
	}

	d := *(*byte)(unsafe.Add(rawPtr, pos))
	if d == '.' {
		pos++
		f0 := *(*byte)(unsafe.Add(rawPtr, pos))
		if f0 < '0' || f0 > '9' {
			return pos, false
		}
		pos++
		for {
			fd := *(*byte)(unsafe.Add(rawPtr, pos))
			if fd < '0' || fd > '9' {
				d = fd
				break
			}
			pos++
		}
	}

	expDigits := 0
	if d == 'e' || d == 'E' {
		pos++
		ed := *(*byte)(unsafe.Add(rawPtr, pos))
		if ed == '+' || ed == '-' {
			pos++
			ed = *(*byte)(unsafe.Add(rawPtr, pos))
		}
		if ed < '0' || ed > '9' {
			return pos, false
		}
		pos++
		expDigits = 1
		for {
			ed = *(*byte)(unsafe.Add(rawPtr, pos))
			if ed < '0' || ed > '9' {
				break
			}
			pos++
			expDigits++
		}
	}

	if pos > end {
		return pos, false
	}
	if checkFloatInf && (intDigits > 200 || expDigits >= 3) {
		return pos, false
	}
	return pos, true
}
