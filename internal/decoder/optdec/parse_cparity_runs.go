//go:build arm64 && go1.20 && !go1.28

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
	pos       uintptr    // Active key/value/open token start; unused for close events.
	hasEsc    uint64     // Persistent across tokens: C errors can retain the prior flag.
	num       cParityNum // Persistent across tokens: C errors can retain the prior number.
	stringErr int        // Unicode continuation error only
	stringDst uintptr
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
// It publishes input/tape cursors before every return. Private scanner calls
// pass registers without Go argument frames; Go never participates per block.
// Open/close events have consumed their delimiter. Close has no state payload:
// the parent's tape node includes its kind and completed child count.
// Number restores cur to the body after an optional minus;
// Literal leaves cur after t/f/n. Neither has emitted the pending scalar.
// Only Unicode returns an incomplete string: cur addresses its backslash and
// stringDst the next output byte; StringResume alone reads saved context.
//
//go:noescape
func cParityParseRunsAsm(p *Parser, state *cParityRunsState) (event int)

func cParityParseWithPadding(p *Parser) ErrorCode {
	var state cParityRunsState
	buf := &p.nbuf
	if buf.parent != -1 {
		// C's grow/restart contract records the continuation in the last node
		// plus the current parent's type and the key-capacity-failure flag.
		switch (*node)(unsafe.Pointer(buf.ncur-unsafe.Sizeof(node{}))).typ & TypeMask {
		case KObject:
			state.phase = cParityRunsObjectStart
		case KArray:
			state.phase = cParityRunsArrayStart
		default:
			parent := (*node)(unsafe.Pointer(buf.nstart + uintptr(buf.parent)*unsafe.Sizeof(node{})))
			state.phase = cParityRunsParentContinue
			if parent.typ&TypeMask == KObject && buf.iskey {
				state.phase = cParityRunsObjectValue
			}
		}
	}
	var code ErrorCode
	for {
		event := cParityParseRunsAsm(p, &state)
		if event <= SONIC_STACK_OVERFLOW {
			code = ErrorCode(event)
			break
		}
		switch event {
		case cParityRunsOpenObject, cParityRunsOpenArray:
			kind := uint64(KObject)
			state.phase = cParityRunsObjectStart
			if event == cParityRunsOpenArray {
				kind, state.phase = KArray, cParityRunsArrayStart
			}
			if !cParityRunsOpen(buf, state.pos, kind) {
				code = SONIC_VISIT_FAILED
				goto done
			}
		case cParityRunsCloseContainer:
			if cParityRunsClose(p) {
				goto done
			}
			state.phase = cParityRunsParentContinue
		case cParityRunsNumber:
			if numberCode := cParityRunsFinishNumber(p, &state); numberCode != SONIC_OK || buf.parent == -1 {
				code = numberCode
				goto done
			}
			state.phase = cParityRunsParentContinue
		case cParityRunsUnicode:
			src, dst, stringCode := cParityDecodeUnicode(unsafe.Pointer(p.cur), unsafe.Pointer(state.stringDst), true)
			p.cur, state.stringDst = uintptr(src), uintptr(dst)
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

func cParityRunsOpen(buf *nodeBuf, pos uintptr, kind uint64) bool {
	n := (*node)(unsafe.Pointer(buf.ncur))
	n.typ, n.val = kind|uint64(pos)<<PosBits, uint64(buf.parent)
	buf.parent = int64((buf.ncur - buf.nstart) / unsafe.Sizeof(node{}))
	buf.ncur += unsafe.Sizeof(node{})
	buf.depth++
	// The last available node has already been published on failure.
	return buf.ncur+unsafe.Sizeof(node{}) <= buf.nend
}

// Close reads kind and count from the unfinished parent node, their sole owner.
// It returns true when the root is complete or the close-time depth check
// ends parsing. The caller applies C's max-depth error precedence afterward.
// Adjacent matching parent closes stay in this Go operation. Any whitespace,
// separator or mismatched byte resumes assembly without consuming it, keeping
// its whitespace cache and error cursor/count ordering unchanged.
func cParityRunsClose(p *Parser) bool {
	buf := &p.nbuf
	for {
		parent := (*node)(unsafe.Pointer(buf.nstart + uintptr(buf.parent)*unsafe.Sizeof(node{})))
		length := uint64(uint32(parent.typ)) >> TypeBits
		if parent.typ&TypeMask == KObject {
			buf.stat.object++
			buf.stat.object_keys += uint32(length)
		} else {
			buf.stat.array++
			buf.stat.array_elems += uint32(length)
		}
		buf.parent = int64(parent.val)
		span := uint32((buf.ncur - uintptr(unsafe.Pointer(parent))) / unsafe.Sizeof(node{}))
		parent.val = uint64(span)<<ConLenBits | uint64(uint32(length))
		parent.typ &= 0xffffffff000000ff
		if buf.depth > uint64(buf.stat.max_depth) {
			buf.stat.max_depth = uint32(buf.depth)
			if buf.depth > 4096 {
				return true
			}
		}
		buf.depth--
		if buf.parent == -1 {
			return true
		}

		c := *(*byte)(unsafe.Pointer(p.cur))
		if c != '}' && c != ']' {
			return false
		}
		parent = (*node)(unsafe.Pointer(buf.nstart + uintptr(buf.parent)*unsafe.Sizeof(node{})))
		switch parent.typ & TypeMask {
		case KObject:
			if c != '}' {
				return false
			}
		case KArray:
			if c != ']' {
				return false
			}
		default:
			return false
		}
		// Account for the completed child before closing its parent, exactly
		// as the assembly continuation does after consuming a nonspace byte.
		p.cur++
		parent.typ += 1 << TypeBits
	}
}

// FinishNumber owns the complete cold numeric operation: decode, publish its
// partial node/statistics, and apply error/capacity ordering. Successful nested
// values resume ordinary parent grammar; no numeric result returns to assembly.
func cParityRunsFinishNumber(p *Parser, state *cParityRunsState) ErrorCode {
	// Assembly leaves cur at the number body, which may be invalid input.
	// The token's first byte retains its sign, so no separate cross-language
	// negative flag is needed.
	negative := *(*byte)(unsafe.Pointer(p.start + state.pos)) == '-'
	cur := unsafe.Pointer(p.cur)
	var code ErrorCode
	if p.options&(1<<_F_use_number) != 0 {
		remain := int(p.end - p.cur)
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
				p.cur = uintptr(cur)
				return code
			}
		}
	}
	p.cur = uintptr(cur)

	num := state.num
	buf := &p.nbuf
	ncur := buf.ncur
	n := (*node)(unsafe.Pointer(ncur))
	n.typ, n.val = num.typ|uint64(state.pos)<<PosBits, num.val
	buf.ncur = ncur + unsafe.Sizeof(node{})
	buf.stat.number++
	// C publishes values before returning decode errors. Only success checks
	// the reserved spare node, so a decode error takes precedence over growth.
	if code != SONIC_OK {
		return code
	}
	if ncur+2*unsafe.Sizeof(node{}) > buf.nend {
		return SONIC_VISIT_FAILED
	}
	return SONIC_OK
}

// The fixed-width assembly probe has already rejected this literal. Go locates
// the first mismatch, consumes that byte, publishes C's partial literal node,
// and terminates parsing without an assembly resume or a capacity check.
func cParityRunsFailLiteral(p *Parser, pos uintptr) ErrorCode {
	suffix, kind := "ull", uint64(KNull)
	switch *(*byte)(unsafe.Pointer(p.start + pos)) {
	case 't':
		suffix, kind = "rue", KTrue
	case 'f':
		suffix, kind = "alse", KFalse
	}
	for i := 0; i < len(suffix); i++ {
		c := *(*byte)(unsafe.Pointer(p.cur))
		p.cur++
		if c != suffix[i] {
			break
		}
	}
	ncur := p.nbuf.ncur
	n := (*node)(unsafe.Pointer(ncur))
	n.typ = kind | uint64(pos)<<PosBits // The null/bool payload stays unspecified.
	p.nbuf.ncur = ncur + unsafe.Sizeof(node{})
	return SONIC_INVALID_LITERAL
}
