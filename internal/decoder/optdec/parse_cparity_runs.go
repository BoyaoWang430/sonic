//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import "unsafe"

// Assembly owns a flat run of keys/scalars and returns at hierarchy changes
// or complex decoding. Go completes cold numbers/literals through node emission
// and errors; only strings resume a partially decoded scalar in assembly.
// Every address here refers to input/tape storage owned by Parser.
// State survives these internal yields; actual tape growth starts a new
// invocation with fresh numeric/escape state, matching the C parser.
type cParityRunsState struct {
	phase     uint64
	context   uint64     // String-resume context: root, object value, array value, or key.
	pos       int        // Active key/value/open token start offset; unused for close events.
	hasEsc    uint64     // Persistent across tokens: C errors can retain the prior flag.
	num       cParityNum // Persistent across tokens: C errors can retain the prior number.
	stringErr int        // Unicode continuation error only
	stringDst int        // Offset into JsonBytes() for next escaped string output byte
}

// The continuation context is shared with the assembly through go_asm.h.
const (
	cParityRunsContextRoot uint64 = iota
	cParityRunsContextObject
	cParityRunsContextArray
	cParityRunsContextKey
)

// String helpers return this negative sentinel to request Unicode handling.
// It lies outside the C error range and is never exposed as a parse error.
const cParityRunsStringYield = 16

const (
	cParityRunsRoot uint64 = iota
	cParityRunsObjectStart
	cParityRunsArrayStart
	cParityRunsObjectValue
	cParityRunsParentContinue
	cParityRunsStringResume
)

const (
	cParityRunsNumber = 16 + iota
	cParityRunsOpenObject
	cParityRunsOpenArray
	cParityRunsCloseContainer
	cParityRunsUnicode
	cParityRunsLiteral
)

// cParityParseRunsAsm returns a C error/done code or a hierarchy/cold event.
// It publishes input/tape cursor offsets before every return. Private scanner calls
// pass registers without Go argument frames; Go never participates per block.
// Open/close events have consumed their delimiter. Close has no state payload:
// the parent's tape node includes its kind and completed child count.
// Number restores pos to the body after an optional minus;
// Literal leaves pos after t/f/n. Neither has emitted the pending scalar.
// Only Unicode returns an incomplete string: pos addresses its backslash and
// stringDst the next output byte offset; StringResume alone reads saved context.
//
//go:noescape
//go:nocheckptr
func cParityParseRunsAsm(p *Parser, state *cParityRunsState, rawBase *byte, nodesBase *node) (event int)

func cParityParseWithPadding(p *Parser) ErrorCode {
	var state cParityRunsState
	buf := &p.nbuf
	if len(p.nodes) == 0 {
		n := cap(p.nodes)
		if n == 0 {
			n = int(defaultNodesCap)
			p.nodes = make([]node, n, n)
		} else {
			p.nodes = p.nodes[:n]
		}
		buf.ncap = len(p.nodes)
	} else if buf.ncap <= 0 || buf.ncap > len(p.nodes) {
		buf.ncap = len(p.nodes)
	}
	if buf.parent != -1 {
		// C's grow/restart contract records the continuation in the last node
		// plus the current parent's type and the key-capacity-failure flag.
		iskey := buf.iskey
		buf.iskey = false
		used := buf.nlen
		switch p.nodes[used-1].typ & TypeMask {
		case KObject:
			state.phase = cParityRunsObjectStart
		case KArray:
			state.phase = cParityRunsArrayStart
		default:
			parent := &p.nodes[buf.parent]
			state.phase = cParityRunsParentContinue
			if parent.typ&TypeMask == KObject && iskey {
				state.phase = cParityRunsObjectValue
			}
		}
	}
	raw := p.JsonBytes()
	rawBase := unsafe.SliceData(raw)
	nodesBase := unsafe.SliceData(p.nodes)
	var code ErrorCode
	for {
		event := cParityParseRunsAsm(p, &state, rawBase, nodesBase)
		if event <= SONIC_STACK_OVERFLOW {
			code = ErrorCode(event)
			break
		}
		switch event {
		case cParityRunsCloseContainer:
			// Bounded preemption yield after every 4096 closed containers.
			state.phase = cParityRunsParentContinue
		case cParityRunsNumber:
			if numberCode := cParityRunsFinishNumber(p, &state); numberCode != SONIC_OK || buf.parent == -1 {
				code = numberCode
				goto done
			}
			state.phase = cParityRunsParentContinue
		case cParityRunsUnicode:
			base := unsafe.Pointer(rawBase)
			srcPtr := unsafe.Add(base, p.pos)
			dstPtr := unsafe.Add(base, state.stringDst)
			nextSrc, nextDst, stringCode := cParityDecodeUnicodeOpt(srcPtr, dstPtr, true, p.strictURC)
			p.pos += int(uintptr(nextSrc) - uintptr(srcPtr))
			state.stringDst += int(uintptr(nextDst) - uintptr(dstPtr))
			state.stringErr = stringCode
			state.phase = cParityRunsStringResume
		case cParityRunsLiteral:
			code = cParityRunsFailLiteral(p, state.pos)
			goto done
		default:
			panic("invalid scalar-runs parser event")
		}
	}

done:
	if buf.stat.max_depth > 4096 {
		return SONIC_STACK_OVERFLOW
	}
	return code
}

// FinishNumber owns the complete cold numeric operation: decode, publish its
// partial node/statistics, and apply error/capacity ordering. Successful nested
// values resume ordinary parent grammar; no numeric result returns to assembly.
func cParityRunsFinishNumber(p *Parser, state *cParityRunsState) ErrorCode {
	// Assembly leaves pos at the number body, which may be invalid input.
	// The token's first byte retains its sign, so no separate cross-language
	// negative flag is needed.
	raw := p.JsonBytes()
	negative := raw[state.pos] == '-'
	base := unsafe.Pointer(unsafe.SliceData(raw))
	startCur := unsafe.Add(base, p.pos)
	cur := startCur
	var code ErrorCode
	if p.options&(1<<_F_use_number) != 0 {
		remain := p.end - p.pos
		if !negative {
			remain++
		}
		length := cParitySkipPositive(&cur, remain)
		if length < 0 {
			code = ErrorCode(-length)
		}
		value := uint64(length)
		if negative {
			value++
		}
		state.num = cParityNum{typ: KRawNumber, val: value}
	} else {
		// On failure the preceding successful number deliberately survives.
		cur, state.num, code = cParityParseNumberValue(cur, state.num, negative, p.dbuf)
		// Every successful parse sets KUint, KSint or KReal. Only an error
		// can leave an invalid prior type, which C skips instead of emitting.
		if code != SONIC_OK {
			kind := state.num.typ
			if kind != KUint && kind != KSint && kind != KReal {
				p.pos += int(uintptr(cur) - uintptr(startCur))
				return code
			}
		}
	}
	p.pos += int(uintptr(cur) - uintptr(startCur))

	num := state.num
	buf := &p.nbuf
	nIdx := buf.nlen
	p.nodes[nIdx] = node{typ: num.typ | uint64(state.pos)<<PosBits, val: num.val}
	buf.nlen = nIdx + 1
	buf.stat.number++
	// C publishes values before returning decode errors. Only success checks
	// the reserved spare node, so a decode error takes precedence over growth.
	if code != SONIC_OK {
		return code
	}
	if nIdx+2 > buf.ncap {
		return SONIC_VISIT_FAILED
	}
	return SONIC_OK
}

// The fixed-width assembly probe has already rejected this literal. Go locates
// the first mismatch, consumes that byte, publishes C's partial literal node,
// and terminates parsing without an assembly resume or a capacity check.
func cParityRunsFailLiteral(p *Parser, pos int) ErrorCode {
	raw := p.JsonBytes()
	suffix, kind := "ull", uint64(KNull)
	switch raw[pos] {
	case 't':
		suffix, kind = "rue", KTrue
	case 'f':
		suffix, kind = "alse", KFalse
	}
	for i := 0; i < len(suffix); i++ {
		c := raw[p.pos]
		p.pos++
		if c != suffix[i] {
			break
		}
	}
	nIdx := p.nbuf.nlen
	p.nodes[nIdx].typ = kind | uint64(pos)<<PosBits // The null/bool payload stays unspecified.
	p.nbuf.nlen = nIdx + 1
	return SONIC_INVALID_LITERAL
}
