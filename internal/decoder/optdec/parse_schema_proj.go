//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"math/bits"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
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
	strPtr         unsafe.Pointer
	tailPtr        unsafe.Pointer
	pos            int
	end            int
	safe64         int
	tailStart      int
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
	denseBailout   bool
	skippedKeys    int
	skippedBytes   int
	matchedKeys    int
	num            cParityNum
}

func schemaPtrAt64(strPtr, tailPtr unsafe.Pointer, pos, safe64, tailStart int) unsafe.Pointer {
	if pos <= safe64 {
		return unsafe.Add(strPtr, pos)
	}
	return unsafe.Add(tailPtr, pos-tailStart)
}

func schemaByteAt(strPtr unsafe.Pointer, pos, end int) byte {
	if pos < end {
		return *(*byte)(unsafe.Add(strPtr, pos))
	}
	return 'x'
}

func (sc *schemaScanner) ptrAt64(pos int) unsafe.Pointer {
	if pos <= sc.safe64 {
		return unsafe.Add(sc.strPtr, pos)
	}
	return unsafe.Add(sc.tailPtr, pos-sc.tailStart)
}

func (sc *schemaScanner) byteAt(pos int) byte {
	if pos < sc.end {
		return *(*byte)(unsafe.Add(sc.strPtr, pos))
	}
	return 'x'
}

const errCodeGiantStreamFallback ErrorCode = -1

func parseWithSchemaOrFull(p *Parser, plan *projPlan) ErrorCode {
	if plan == nil || plan.kind == projFull || plan.kind == projScalar || plan.kind == projBytes {
		if uintptr(p.end+len(padding)) > giantReadOnlyScanThreshold {
			return errCodeGiantStreamFallback
		}
		return parseImpl(p)
	}
	ok, mutated := parseSchemaRoot(p, plan)
	if ok {
		runtime.KeepAlive(p)
		return SONIC_OK
	}
	if uintptr(p.end+len(padding)) > giantReadOnlyScanThreshold {
		return errCodeGiantStreamFallback
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
	p._nbk = _nospaceBlock{}
	p.nbuf = nodeBuf{}
	if wasLazy && len(p.Json) > 128<<10 && p.sessionPadded == nil && !looksLikeJSONLPrefix(p.Json) {
		p.presizeNodesOne()
	} else {
		p.nbuf.init(p.nodes)
	}
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
	var strPtr, tailPtr unsafe.Pointer
	var safe64, tailStart int
	if p.lazyPadded && p.sessionPadded == nil {
		if p.end <= schemaTailWindowLen {
			copy(p.tailBuf[:p.end], p.Json)
			copy(p.tailBuf[p.end:p.end+len(padding)], padding)
			strPtr = unsafe.Pointer(&p.tailBuf[0])
			tailPtr = strPtr
			safe64 = p.end
		} else {
			tailStart = p.end - schemaTailWindowLen
			copy(p.tailBuf[:schemaTailWindowLen], p.Json[tailStart:p.end])
			copy(p.tailBuf[schemaTailWindowLen:schemaTailWindowLen+len(padding)], padding)
			strPtr = unsafe.Pointer(unsafe.StringData(p.Json))
			tailPtr = unsafe.Pointer(&p.tailBuf[0])
			safe64 = p.end - 64
		}
	} else {
		raw := p.JsonBytes()
		if len(raw) < p.end+len(padding) {
			return false, false
		}
		strPtr = unsafe.Pointer(unsafe.SliceData(raw))
		tailPtr = strPtr
		safe64 = p.end
	}
	oldOpts := p.options
	if !p.isEface && !p.keepUseNumber {
		p.options &^= 1 << _F_use_number
	}
	sc := schemaScanner{
		p:              p,
		jsonStr:        p.Json,
		strPtr:         strPtr,
		tailPtr:        tailPtr,
		pos:            0,
		end:            p.end,
		safe64:         safe64,
		tailStart:      tailStart,
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
		if sc.denseBailout && !plan.hasRawSubtree {
			plan.recordDense()
		}
		return false, sc.mutatedPadded
	}
	if !plan.hasRawSubtree {
		if sc.skippedKeys*4 < sc.matchedKeys && sc.skippedBytes*3 < sc.end {
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
func skipIndentSWAR(strPtr, tailPtr unsafe.Pointer, pos, end, safe64, tailStart int) (byte, int, bool) {
	if pos > end {
		return 0, pos, false
	}
	base := schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart)
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
				b = schemaByteAt(strPtr, at, end)
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

func (sc *schemaScanner) skipSpace() (byte, bool) {
	pos := sc.pos
	c := sc.byteAt(pos)
	if c > ' ' {
		sc.pos = pos + 1
		return c, pos < sc.end
	}
	if (spaceMask & (uint64(1) << c)) == 0 {
		sc.pos = pos + 1
		return c, pos < sc.end
	}
	c2 := sc.byteAt(pos + 1)
	if c2 > ' ' {
		sc.pos = pos + 2
		return c2, pos+1 < sc.end
	}
	return sc.skipSpaceSlow(pos + 1)
}

func (sc *schemaScanner) skipSpaceSlow(pos int) (byte, bool) {
	strPtr := sc.strPtr
	tailPtr := sc.tailPtr
	end := sc.end
	safe64 := sc.safe64
	tailStart := sc.tailStart
	if pos > end {
		return 'x', false
	}
	if b, nextPos, hit := skipIndentSWAR(strPtr, tailPtr, pos, end, safe64, tailStart); hit {
		sc.pos = nextPos
		return b, true
	} else {
		pos = nextPos
	}
	c := schemaByteAt(strPtr, pos, end)
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
				return schemaByteAt(strPtr, at, end), at < end
			}
			pos = blockOff + 64
		}
	}
	for {
		if pos > end {
			return 'x', false
		}
		mask := cParityNonspace64(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart))
		if mask != 0 {
			sc.nbkBits = mask
			sc.nbkOffPlus64 = uint64(pos + 64)
			at := pos + bits.TrailingZeros64(mask)
			sc.pos = at + 1
			return schemaByteAt(strPtr, at, end), at < end
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
	case projBytes:
		if c == '[' {
			return sc.scanSlice(plan, c, valStart)
		}
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
	strPtr := sc.strPtr
	tailPtr := sc.tailPtr
	safe64 := sc.safe64
	tailStart := sc.tailStart

	for {
		if nc != '"' {
			return false
		}
		keyStart := sc.pos
		var klen int
		var keyTyp uint64
		var keyVal uint64
		hasEsc := false
		isASCIIKey := false

		// 64-byte register SWAR fast path for keys (handles >99.9% of JSON keys with zero ABI calls).
		kp := schemaPtrAt64(strPtr, tailPtr, keyStart, safe64, tailStart)
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
			isASCIIKey = (w0 & (qm0 - 1) & swarHigh) == 0
		} else if qm0 == 0 && em0 == 0 {
			highAcc := w0
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
			keyTyp, keyVal, hasEsc, ok = sc.scanStringSlow(keyStart)
			if !ok {
				return false
			}
			if hasEsc {
				klen = int(uint32(keyVal))
			} else {
				klen = int(keyVal)
			}
		}

	afterKeyScan:
		if schemaByteAt(strPtr, sc.pos, sc.end) == ':' {
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
		cVal := schemaByteAt(strPtr, pVal, sc.end)
		if cVal > ' ' {
			vc = cVal
			sc.pos = pVal + 1
			if sc.pos > sc.end {
				return false
			}
		} else if cVal == ' ' {
			cVal2 := schemaByteAt(strPtr, pVal+1, sc.end)
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
				sc.skippedBytes += sc.pos - vStart
			} else {
				if !sc.skipValidateValue(vc) {
					return false
				}
				sc.ensureNodes(3)
				sc.p.nodes[sc.nIdx] = node{typ: keyTyp, val: keyVal}
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
			sc.p.nodes[sc.nIdx] = node{typ: keyTyp | hint, val: keyVal}
			sc.nIdx++
			if !sc.scanByPlan(fplans[idx], vc, vStart) {
				return false
			}
			count++
		}

		delim := schemaByteAt(strPtr, sc.pos, sc.end)
		if delim == ',' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			cNext := schemaByteAt(strPtr, sc.pos, sc.end)
			if cNext == '"' {
				nc = '"'
				sc.pos++
				if sc.pos > sc.end {
					return false
				}
				continue
			}
			if cNext == '\n' {
				if b, nextPos, hit := skipIndentSWAR(strPtr, tailPtr, sc.pos+1, sc.end, safe64, tailStart); hit {
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
			if b, nextPos, hit := skipIndentSWAR(strPtr, tailPtr, sc.pos+1, sc.end, safe64, tailStart); hit {
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
	strPtr := sc.strPtr
	tailPtr := sc.tailPtr
	safe64 := sc.safe64
	tailStart := sc.tailStart

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

		delim := schemaByteAt(strPtr, sc.pos, sc.end)
		if delim == ',' {
			sc.pos++
			if sc.pos > sc.end {
				return false
			}
			cNext := schemaByteAt(strPtr, sc.pos, sc.end)
			if cNext > ' ' {
				ec = cNext
				sc.pos++
				if sc.pos > sc.end {
					return false
				}
				continue
			}
			if cNext == '\n' {
				if b, nextPos, hit := skipIndentSWAR(strPtr, tailPtr, sc.pos+1, sc.end, safe64, tailStart); hit {
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
	strPtr := sc.strPtr

	for {
		if nc != '"' {
			return false
		}
		if !sc.scanStringScalar(sc.pos - 1) {
			return false
		}
		if schemaByteAt(strPtr, sc.pos, sc.end) == ':' {
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
	// Fast zero-copy path for empty '{}' / '[]' subtrees (e.g. empty []any / map[string]any fields).
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
	// cParityParseWithPadding decodes any escaped strings in place inside p.padded
	// (or p.sessionPadded); mark mutatedPadded before invoking it so any fallback
	// or dense-slice bailout restores pristine source bytes first.
	sc.mutatedPadded = true

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

func swarSkipString64(strPtr, tailPtr unsafe.Pointer, pos, end, safe64, tailStart int, validateStr bool) (int, bool, bool) {
	if pos > end {
		return pos, true, false
	}
	base := schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart)
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

// scanStringSlow scans a string starting right after the opening quote (contentStart)
// without mutating input bytes, writing any escaped string into sc.p.unescBuf.
func (sc *schemaScanner) scanStringSlow(contentStart int) (uint64, uint64, bool, bool) {
	pos := contentStart
	end := sc.end
	opts := sc.opts
	strPtr := sc.strPtr
	tailPtr := sc.tailPtr
	safe64 := sc.safe64
	tailStart := sc.tailStart
	for {
		if pos > end {
			return 0, 0, false, false
		}
		bs, quote, control := cParityStringMasks32(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart), opts)
		if cParityAtFirst(quote, bs|control) {
			afterQuote := pos + bits.TrailingZeros32(quote) + 1
			if afterQuote > end {
				return 0, 0, false, false
			}
			sc.pos = afterQuote
			return KStringCommon | uint64(contentStart)<<PosBits, uint64(afterQuote - contentStart - 1), false, true
		}
		if opts&(1<<_F_validate_string) != 0 && cParityAtFirst(control, bs|quote) {
			return 0, 0, false, false
		}
		if cParityAtFirst(bs, quote) {
			pos += bits.TrailingZeros32(bs)
			break
		}
		pos += 32
	}

	firstEsc := pos
	afterQuote, ok := skipStringBodyAt(sc.jsonStr, strPtr, tailPtr, firstEsc, end, safe64, tailStart, opts&^(1<<_F_no_validate_json))
	if !ok {
		return 0, 0, false, false
	}
	quotePos := afterQuote - 1
	p := sc.p
	uoff := len(p.unescBuf)
	prefixLen := firstEsc - contentStart
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
	p.unescBuf = append(p.unescBuf, sc.jsonStr[contentStart:firstEsc]...)
	escTail := sc.jsonStr[firstEsc:quotePos]
	dst := p.unescBuf[uoff+prefixLen : uoff+maxOut]
	var unquoteFlags uint64 = types.F_UNICODE_REPLACE
	if opts&(1<<_F_disable_urc) != 0 {
		unquoteFlags = 0
	}
	written, _, uerr := kernels.Unquote(escTail, dst, unquoteFlags)
	if uerr != 0 {
		p.unescBuf = p.unescBuf[:uoff]
		return 0, 0, false, false
	}
	ulen := prefixLen + written
	p.unescBuf = p.unescBuf[:uoff+ulen]
	p.unesc = p.unescBuf
	sc.pos = afterQuote
	packedVal := escapedInUnescFlag | (uint64(uoff) << 32) | uint64(uint32(ulen))
	return KStringEscaped | uint64(contentStart)<<PosBits, packedVal, true, true
}

func (sc *schemaScanner) scanStringScalar(valStart int) bool {
	contentStart := valStart + 1
	if afterQuote, done, valid := swarSkipString64(sc.strPtr, sc.tailPtr, contentStart, sc.end, sc.safe64, sc.tailStart, sc.validateStr); done {
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

	typ, val, _, ok := sc.scanStringSlow(contentStart)
	if !ok {
		return false
	}
	sc.ensureNodes(2)
	sc.p.nodes[sc.nIdx] = node{
		typ: typ,
		val: val,
	}
	sc.nIdx++
	return true
}

func (sc *schemaScanner) scanNumberScalar(first byte, valStart int) bool {
	strPtr := sc.strPtr
	pos := sc.pos
	end := sc.end
	if sc.opts&(1<<_F_use_number) != 0 {
		numEnd, ok := skipNumberAt(strPtr, first, pos, end, false)
		if !ok {
			return false
		}
		sc.pos = numEnd
		sc.ensureNodes(2)
		sc.p.nodes[sc.nIdx] = node{typ: KRawNumber | uint64(valStart)<<PosBits, val: uint64(numEnd - valStart)}
		sc.nIdx++
		return true
	}

	if first >= '1' && first <= '9' {
		uval := uint64(first - '0')
		digits := 1
		for digits < 19 {
			d := schemaByteAt(strPtr, pos, end)
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
		d := schemaByteAt(strPtr, pos, end)
		if d >= '0' && d <= '9' {
			return false
		}
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
		d0 := schemaByteAt(strPtr, pos, end)
		if d0 >= '1' && d0 <= '9' {
			uval := uint64(d0 - '0')
			p2 := pos + 1
			digits := 1
			for digits < 19 {
				d := schemaByteAt(strPtr, p2, end)
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
			pos = p2
		}
	}

	neg := first == '-'
	numBody := valStart
	if neg {
		numBody++
	}
	var numVal cParityNum
	var numEnd int
	if sc.strPtr == sc.tailPtr {
		startPtr := unsafe.Add(strPtr, numBody)
		nextCur, nv, code := cParityParseNumberValue(startPtr, sc.num, neg, sc.p.dbuf)
		if code != SONIC_OK {
			return false
		}
		numEnd = numBody + int(uintptr(nextCur)-uintptr(startPtr))
		if numEnd > end || isNumberContByte(schemaByteAt(strPtr, numEnd, end)) {
			return false
		}
		numVal = nv
	} else if numBody >= sc.tailStart {
		startPtr := unsafe.Add(sc.tailPtr, numBody-sc.tailStart)
		nextCur, nv, code := cParityParseNumberValue(startPtr, sc.num, neg, sc.p.dbuf)
		if code != SONIC_OK {
			return false
		}
		numEnd = numBody + int(uintptr(nextCur)-uintptr(startPtr))
		if numEnd > end || isNumberContByte(schemaByteAt(strPtr, numEnd, end)) {
			return false
		}
		numVal = nv
	} else {
		endCur := pos
		for endCur <= sc.safe64 && isNumberContByte(*(*byte)(unsafe.Add(strPtr, endCur))) {
			endCur++
		}
		if endCur <= sc.safe64 {
			startPtr := unsafe.Add(strPtr, numBody)
			nextCur, nv, code := cParityParseNumberValue(startPtr, sc.num, neg, sc.p.dbuf)
			if code != SONIC_OK || int(uintptr(nextCur)-uintptr(startPtr)) != endCur-numBody {
				return false
			}
			numEnd = endCur
			numVal = nv
		} else {
			var ok bool
			numEnd, ok = skipNumberAt(strPtr, first, sc.pos, end, false)
			if !ok {
				return false
			}
			p := sc.p
			savedLen := len(p.unescBuf)
			p.unescBuf = append(p.unescBuf, sc.jsonStr[numBody:numEnd]...)
			p.unescBuf = append(p.unescBuf, 'x', 0, 0, 0, 0, 0, 0, 0)
			startPtr := unsafe.Pointer(&p.unescBuf[savedLen])
			nextCur, nv, code := cParityParseNumberValue(startPtr, sc.num, neg, p.dbuf)
			p.unescBuf = p.unescBuf[:savedLen]
			if p.unesc != nil {
				p.unesc = p.unescBuf
			}
			if code != SONIC_OK || int(uintptr(nextCur)-uintptr(startPtr)) != numEnd-numBody {
				return false
			}
			numVal = nv
		}
	}
	sc.num = numVal
	sc.pos = numEnd
	sc.ensureNodes(2)
	sc.p.nodes[sc.nIdx] = node{typ: numVal.typ | uint64(valStart)<<PosBits, val: numVal.val}
	sc.nIdx++
	return true
}

func (sc *schemaScanner) matchTrueTail() bool {
	if sc.pos+3 > sc.end {
		return false
	}
	p := sc.ptrAt64(sc.pos)
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
	p := sc.ptrAt64(sc.pos)
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
	p := sc.ptrAt64(sc.pos)
	if (*(*uint32)(p) & 0x00ffffff) != 0x006c6c75 {
		return false
	}
	sc.pos += 3
	return true
}

// skipValidateValue handles scalar values immediately without touching a container stack,
// and dispatches objects/arrays to skipValidateContainer (64-level register bit-stack).
func (sc *schemaScanner) skipValidateValue(c byte) bool {
	strPtr := sc.strPtr
	tailPtr := sc.tailPtr
	pos := sc.pos
	end := sc.end
	safe64 := sc.safe64
	tailStart := sc.tailStart
	switch c {
	case '"':
		if nextPos, done, valid := swarSkipString64(strPtr, tailPtr, pos, end, safe64, tailStart, sc.validateStr && !sc.noValidateJSON); done {
			if !valid {
				return false
			}
			sc.pos = nextPos
			return true
		} else {
			var ok bool
			pos, ok = skipStringBodyAt(sc.jsonStr, strPtr, tailPtr, nextPos, end, safe64, tailStart, sc.opts)
			if !ok {
				return false
			}
			sc.pos = pos
			return true
		}
	case 't':
		if pos+3 > end || (*(*uint32)(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart))&0x00ffffff) != 0x00657572 {
			return false
		}
		sc.pos = pos + 3
		return true
	case 'f':
		if pos+4 > end || *(*uint32)(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart)) != 0x65736c61 {
			return false
		}
		sc.pos = pos + 4
		return true
	case 'n':
		if pos+3 > end || (*(*uint32)(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart))&0x00ffffff) != 0x006c6c75 {
			return false
		}
		sc.pos = pos + 3
		return true
	case '{', '[':
		if sc.noValidateJSON {
			endPos, err := kernels.SkipContainer(sc.jsonStr[:end], pos-1)
			if err != 0 {
				return false
			}
			sc.pos = endPos
			return true
		}
		return sc.skipValidateContainer(c)
	default:
		if c >= '1' && c <= '9' {
			p2 := pos
			for {
				d := schemaByteAt(strPtr, p2, end)
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
			d := schemaByteAt(strPtr, pos, end)
			if d != '.' && (d|0x20) != 'e' {
				if pos > end {
					return false
				}
				return true
			}
		} else if c != '-' {
			return false
		}
		nextPos, ok := skipNumberAt(strPtr, c, pos, end, sc.opts&(1<<_F_use_number) == 0)
		if !ok {
			return false
		}
		sc.pos = nextPos
		return true
	}
}

func swarSkipString64Raw(rawPtr unsafe.Pointer, pos int, validateStr bool) (int, bool, bool) {
	base := unsafe.Add(rawPtr, pos)
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
			return pos + off + (bits.TrailingZeros64(qm) >> 3) + 1, true, true
		}
		if em != 0 {
			return pos + off, false, false
		}
	}
	return pos + 64, false, false
}

func skipIndentSWARRaw(rawPtr unsafe.Pointer, pos int) (byte, int, bool) {
	base := unsafe.Add(rawPtr, pos)
	w0 := *(*uint64)(base) ^ swarSpaces
	if w0 != 0 {
		rel := bits.TrailingZeros64(w0) >> 3
		b := *(*byte)(unsafe.Add(base, rel))
		if b > ' ' {
			return b, pos + rel + 1, true
		}
		return 0, pos + rel, false
	}
	w1 := *(*uint64)(unsafe.Add(base, 8)) ^ swarSpaces
	if w1 != 0 {
		rel := 8 + (bits.TrailingZeros64(w1) >> 3)
		b := *(*byte)(unsafe.Add(base, rel))
		if b > ' ' {
			return b, pos + rel + 1, true
		}
		return 0, pos + rel, false
	}
	w2 := *(*uint64)(unsafe.Add(base, 16)) ^ swarSpaces
	if w2 != 0 {
		rel := 16 + (bits.TrailingZeros64(w2) >> 3)
		b := *(*byte)(unsafe.Add(base, rel))
		if b > ' ' {
			return b, pos + rel + 1, true
		}
		return 0, pos + rel, false
	}
	return 0, pos + 24, false
}

// skipValidateContainer validates and skips a '{' or '[' subtree with a 64-level
// bit-stack in a single uint64 register (0 = object, 1 = array), zero closures,
// zero stack-array zeroing, inline SWAR indentation & string scanning.
func (sc *schemaScanner) skipValidateContainer(c byte) bool {
	end := sc.end
	fastLimit := end - 128
	if sc.pos > fastLimit {
		return sc.skipValidateContainerTail(c, 0, 0, sc.pos, sc.nbkOffPlus64, sc.nbkBits, 0)
	}

	var frameBits uint64
	sp := 0
	maxSp := int(consts.MaxStack - sc.depth)
	if maxSp > 64 {
		maxSp = 64
	}

	rawPtr := sc.strPtr
	tailPtr := sc.tailPtr
	pos := sc.pos
	safe64 := sc.safe64
	tailStart := sc.tailStart
	opts := sc.opts
	validateStr := sc.validateStr
	checkFloatInf := opts&(1<<_F_use_number) == 0
	nbkOffPlus64 := sc.nbkOffPlus64
	nbkBits := sc.nbkBits

	var nc, vc, delim, colon byte
	var nextPos int
	var done, valid, ok bool

valueEntry:
	if pos > fastLimit {
		return sc.skipValidateContainerTail(c, frameBits, sp, pos, nbkOffPlus64, nbkBits, 0)
	}
	switch c {
	case '"':
		if nextPos, done, valid = swarSkipString64Raw(rawPtr, pos, validateStr); done {
			if !valid {
				return false
			}
			pos = nextPos
			goto afterValue
		}
		pos, ok = skipStringBodyAt(sc.jsonStr, rawPtr, tailPtr, nextPos, end, safe64, tailStart, opts)
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
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWARRaw(rawPtr, pos+1); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWARRaw(rawPtr, pos+1); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
		if (*(*uint32)(unsafe.Add(rawPtr, pos)) & 0x00ffffff) != 0x00657572 {
			return false
		}
		pos += 3
	case 'f':
		if *(*uint32)(unsafe.Add(rawPtr, pos)) != 0x65736c61 {
			return false
		}
		pos += 4
	case 'n':
		if (*(*uint32)(unsafe.Add(rawPtr, pos)) & 0x00ffffff) != 0x006c6c75 {
			return false
		}
		pos += 3
	default:
		if c >= '1' && c <= '9' {
			p2 := pos
			for p2-pos < 20 {
				d := *(*byte)(unsafe.Add(rawPtr, p2))
				if d < '0' || d > '9' {
					if d != '.' && (d|0x20) != 'e' {
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
	if pos > fastLimit {
		return sc.skipValidateContainerTail(0, frameBits, sp, pos, nbkOffPlus64, nbkBits, 1)
	}
	if (frameBits & 1) != 0 {
		goto arrAfterValue
	}
	delim = *(*byte)(unsafe.Add(rawPtr, pos))
	if delim == ',' {
		pos++
		cNext := *(*byte)(unsafe.Add(rawPtr, pos))
		if cNext == '"' {
			pos++
			goto objKeyBody
		}
		if cNext == '\n' {
			if b, np, hit := skipIndentSWARRaw(rawPtr, pos+1); hit {
				nc = b
				pos = np
				goto objKey
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
				goto objKey
			}
		}
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
		goto objKey
	}
	if delim == '}' {
		pos++
		frameBits >>= 1
		sp--
		goto afterValue
	}
	if delim == '\n' {
		if b, np, hit := skipIndentSWARRaw(rawPtr, pos+1); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
	}
	if delim == ',' {
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
	if pos > fastLimit {
		return sc.skipValidateContainerTail(0, frameBits, sp, pos, nbkOffPlus64, nbkBits, 2)
	}
	if nextPos, done, valid = swarSkipString64Raw(rawPtr, pos, validateStr); done {
		if !valid {
			return false
		}
		pos = nextPos
		goto objAfterKey
	}
	pos, ok = skipStringBodyAt(sc.jsonStr, rawPtr, tailPtr, nextPos, end, safe64, tailStart, opts)
	if !ok {
		return false
	}
	if pos > fastLimit {
		return sc.skipValidateContainerTail(0, frameBits, sp, pos, nbkOffPlus64, nbkBits, 3)
	}
objAfterKey:
	if *(*byte)(unsafe.Add(rawPtr, pos)) == ':' {
		pos++
	} else {
		colon, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok || colon != ':' {
			return false
		}
		if pos > fastLimit {
			return sc.skipValidateContainerTail(0, frameBits, sp, pos, nbkOffPlus64, nbkBits, 4)
		}
	}
	vc = *(*byte)(unsafe.Add(rawPtr, pos))
	if vc > ' ' {
		pos++
		c = vc
		goto valueEntry
	}
	if vc == ' ' {
		vc2 := *(*byte)(unsafe.Add(rawPtr, pos+1))
		if vc2 > ' ' {
			pos += 2
			c = vc2
			goto valueEntry
		}
	}
	vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
	if !ok {
		return false
	}
	c = vc
	goto valueEntry

arrAfterValue:
	delim = *(*byte)(unsafe.Add(rawPtr, pos))
	if delim == ',' {
		pos++
		vc = *(*byte)(unsafe.Add(rawPtr, pos))
		if vc > ' ' {
			pos++
			c = vc
			goto valueEntry
		}
		if vc == '\n' {
			if b, np, hit := skipIndentSWARRaw(rawPtr, pos+1); hit {
				c = b
				pos = np
				goto valueEntry
			} else {
				vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
				c = vc
				goto valueEntry
			}
		}
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
		c = vc
		goto valueEntry
	}
	if delim == ']' {
		pos++
		frameBits >>= 1
		sp--
		goto afterValue
	}
	if delim == '\n' {
		if b, np, hit := skipIndentSWARRaw(rawPtr, pos+1); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
	}
	if delim == ',' {
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(rawPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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

// skipValidateContainerTail finishes container skipping in the final <= 128 bytes near EOF
// using the bounds-safe tail window (tailPtr / schemaByteAt).
// entryMode: 0 = valueEntry(c), 1 = afterValue, 2 = objKeyBody, 3 = objAfterKey, 4 = objAfterColon.
func (sc *schemaScanner) skipValidateContainerTail(c byte, frameBits uint64, sp int, pos int, nbkOffPlus64, nbkBits uint64, entryMode uint8) bool {
	maxSp := int(consts.MaxStack - sc.depth)
	if maxSp > 64 {
		maxSp = 64
	}

	strPtr := sc.strPtr
	tailPtr := sc.tailPtr
	end := sc.end
	safe64 := sc.safe64
	tailStart := sc.tailStart
	opts := sc.opts
	validateStr := sc.validateStr
	checkFloatInf := opts&(1<<_F_use_number) == 0

	var nc, vc, delim, colon byte
	var nextPos int
	var done, valid, ok bool

	switch entryMode {
	case 1:
		goto afterValue
	case 2:
		goto objKeyBody
	case 3:
		goto objAfterKey
	case 4:
		goto objAfterColon
	}

valueEntry:
	switch c {
	case '"':
		if nextPos, done, valid = swarSkipString64(strPtr, tailPtr, pos, end, safe64, tailStart, validateStr); done {
			if !valid {
				return false
			}
			pos = nextPos
			goto afterValue
		}
		pos, ok = skipStringBodyAt(sc.jsonStr, strPtr, tailPtr, nextPos, end, safe64, tailStart, opts)
		if !ok {
			return false
		}
	case '{':
		if sp >= maxSp {
			return false
		}
		frameBits <<= 1
		sp++
		nc = schemaByteAt(strPtr, pos, end)
		if nc > ' ' {
			pos++
			if pos > end {
				return false
			}
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
		nc = schemaByteAt(strPtr, pos, end)
		if nc > ' ' {
			pos++
			if pos > end {
				return false
			}
		} else if nc == '\n' {
			if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
				nc = b
				pos = np
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
			}
		} else {
			nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
		if pos+3 > end || (*(*uint32)(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart))&0x00ffffff) != 0x00657572 {
			return false
		}
		pos += 3
	case 'f':
		if pos+4 > end || *(*uint32)(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart)) != 0x65736c61 {
			return false
		}
		pos += 4
	case 'n':
		if pos+3 > end || (*(*uint32)(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart))&0x00ffffff) != 0x006c6c75 {
			return false
		}
		pos += 3
	default:
		if c >= '1' && c <= '9' {
			p2 := pos
			for {
				d := schemaByteAt(strPtr, p2, end)
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
			d := schemaByteAt(strPtr, pos, end)
			if d != '.' && (d|0x20) != 'e' {
				if pos > end {
					return false
				}
				goto afterValue
			}
		} else if c != '-' {
			return false
		}
		pos, ok = skipNumberAt(strPtr, c, pos, end, checkFloatInf)
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
	delim = schemaByteAt(strPtr, pos, end)
	if delim == ',' {
		pos++
		if pos > end {
			return false
		}
		cNext := schemaByteAt(strPtr, pos, end)
		if cNext == '"' {
			pos++
			if pos > end {
				return false
			}
			goto objKeyBody
		}
		if cNext == '\n' {
			if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
				nc = b
				pos = np
				goto objKey
			} else {
				nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
				goto objKey
			}
		}
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
		if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
	}
	if delim == ',' {
		nc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
	if nextPos, done, valid = swarSkipString64(strPtr, tailPtr, pos, end, safe64, tailStart, validateStr); done {
		if !valid {
			return false
		}
		pos = nextPos
		goto objAfterKey
	}
	pos, ok = skipStringBodyAt(sc.jsonStr, strPtr, tailPtr, nextPos, end, safe64, tailStart, opts)
	if !ok {
		return false
	}
objAfterKey:
	if schemaByteAt(strPtr, pos, end) == ':' {
		pos++
		if pos > end {
			return false
		}
	} else {
		colon, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok || colon != ':' {
			return false
		}
	}
objAfterColon:
	vc = schemaByteAt(strPtr, pos, end)
	if vc > ' ' {
		pos++
		if pos > end {
			return false
		}
		c = vc
		goto valueEntry
	}
	if vc == ' ' {
		vc2 := schemaByteAt(strPtr, pos+1, end)
		if vc2 > ' ' {
			pos += 2
			if pos > end {
				return false
			}
			c = vc2
			goto valueEntry
		}
	}
	vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
	if !ok {
		return false
	}
	c = vc
	goto valueEntry

arrAfterValue:
	delim = schemaByteAt(strPtr, pos, end)
	if delim == ',' {
		pos++
		if pos > end {
			return false
		}
		vc = schemaByteAt(strPtr, pos, end)
		if vc > ' ' {
			pos++
			if pos > end {
				return false
			}
			c = vc
			goto valueEntry
		}
		if vc == '\n' {
			if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
				c = b
				pos = np
				goto valueEntry
			} else {
				vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
				if !ok {
					return false
				}
				c = vc
				goto valueEntry
			}
		}
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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
		if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
			delim = b
			pos = np
		} else {
			delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, np, end, safe64, tailStart, nbkOffPlus64, nbkBits)
			if !ok {
				return false
			}
		}
	} else {
		delim, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
		if !ok {
			return false
		}
	}
	if delim == ',' {
		vc, pos, nbkOffPlus64, nbkBits, ok = skipSpaceReg(strPtr, tailPtr, pos, end, safe64, tailStart, nbkOffPlus64, nbkBits)
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

func skipSpaceReg(strPtr, tailPtr unsafe.Pointer, pos, end, safe64, tailStart int, nbkOffPlus64, nbkBits uint64) (byte, int, uint64, uint64, bool) {
	b := schemaByteAt(strPtr, pos, end)
	if b > ' ' || (spaceMask&(uint64(1)<<b)) == 0 {
		return b, pos + 1, nbkOffPlus64, nbkBits, pos < end
	}
	if b, np, hit := skipIndentSWAR(strPtr, tailPtr, pos+1, end, safe64, tailStart); hit {
		return b, np, nbkOffPlus64, nbkBits, true
	} else {
		pos = np
	}
	b = schemaByteAt(strPtr, pos, end)
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
				return schemaByteAt(strPtr, at, end), at + 1, nbkOffPlus64, nbkBits, at < end
			}
			pos = blockOff + 64
		}
	}
	for {
		if pos >= end {
			return 0, pos, nbkOffPlus64, nbkBits, false
		}
		mask := cParityNonspace64(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart))
		if mask != 0 {
			nbkBits = mask
			nbkOffPlus64 = uint64(pos + 64)
			at := pos + bits.TrailingZeros64(mask)
			return schemaByteAt(strPtr, at, end), at + 1, nbkOffPlus64, nbkBits, at < end
		}
		pos += 64
	}
}

func skipStringBodyAt(jsonStr string, strPtr, tailPtr unsafe.Pointer, pos, end, safe64, tailStart int, opts uint64) (int, bool) {
	if pos >= end {
		return pos, false
	}
	if opts&(1<<_F_no_validate_json) != 0 {
		relEnd, err := kernels.SkipStringRelaxed(jsonStr[pos:end])
		if err != 0 {
			return pos, false
		}
		return pos + relEnd, true
	}
	steps := 0
	for {
		if pos >= end {
			return pos, false
		}
		// Avoid ABI0 slice/tail dispatch overhead on short/medium strings (<40B after opening quote);
		// switch to SkipStringStrict once the first 32B chunk finishes without quote/escape/control.
		if steps > 0 && end-pos >= 32 && opts&(1<<_F_validate_string) != 0 && opts&(1<<_F_disable_urc) == 0 {
			relEnd, err := kernels.SkipStringStrict(jsonStr[pos:end])
			if err != 0 {
				return pos, false
			}
			return pos + relEnd, true
		}
		steps++
		bs, quote, control := cParityStringMasks32(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart), opts)
		if cParityAtFirst(quote, bs|control) {
			pos += bits.TrailingZeros32(quote) + 1
			return pos, pos <= end
		}
		if opts&(1<<_F_validate_string) != 0 && cParityAtFirst(control, bs|quote) {
			return pos, false
		}
		if cParityAtFirst(bs, quote) {
			pos += bits.TrailingZeros32(bs)
			break
		}
		pos += 32
	}

	for {
		if pos+2 > end {
			return pos, false
		}
		esc := *(*byte)(unsafe.Add(strPtr, pos+1))
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
				u, ok := cParityReadHex16(unsafe.Add(strPtr, pos+2))
				if !ok {
					return pos, false
				}
				pos += 6
				if opts&(1<<_F_disable_urc) != 0 && u >= 0xd800 && u <= 0xdfff {
					if u > 0xdbff || pos+6 > end || *(*byte)(unsafe.Add(strPtr, pos)) != '\\' || *(*byte)(unsafe.Add(strPtr, pos+1)) != 'u' {
						return pos, false
					}
					u2, ok2 := cParityReadHex16(unsafe.Add(strPtr, pos+2))
					if !ok2 || u2 < 0xdc00 || u2 > 0xdfff {
						return pos, false
					}
					pos += 6
				}
				if pos+2 > end || *(*byte)(unsafe.Add(strPtr, pos)) != '\\' || *(*byte)(unsafe.Add(strPtr, pos+1)) != 'u' {
					break
				}
			}
		}
		if schemaByteAt(strPtr, pos, end) == '\\' {
			continue
		}
		for {
			if pos > end {
				return pos, false
			}
			bs, quote, control := cParityStringMasks32(schemaPtrAt64(strPtr, tailPtr, pos, safe64, tailStart), opts)
			if cParityAtFirst(quote, bs|control) {
				pos += bits.TrailingZeros32(quote) + 1
				return pos, pos <= end
			}
			if opts&(1<<_F_validate_string) != 0 && cParityAtFirst(control, bs|quote) {
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

func skipNumberAt(strPtr unsafe.Pointer, first byte, pos, end int, checkFloatInf bool) (int, bool) {
	if first == '-' {
		first = schemaByteAt(strPtr, pos, end)
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
			d := schemaByteAt(strPtr, pos, end)
			if d < '0' || d > '9' {
				break
			}
			pos++
			intDigits++
		}
	} else {
		return pos, false
	}

	d := schemaByteAt(strPtr, pos, end)
	if d == '.' {
		pos++
		f0 := schemaByteAt(strPtr, pos, end)
		if f0 < '0' || f0 > '9' {
			return pos, false
		}
		pos++
		for {
			fd := schemaByteAt(strPtr, pos, end)
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
		ed := schemaByteAt(strPtr, pos, end)
		if ed == '+' || ed == '-' {
			pos++
			ed = schemaByteAt(strPtr, pos, end)
		}
		if ed < '0' || ed > '9' {
			return pos, false
		}
		pos++
		expDigits = 1
		for {
			ed = schemaByteAt(strPtr, pos, end)
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
