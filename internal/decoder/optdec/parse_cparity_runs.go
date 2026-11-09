//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

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
//go:nocheckptr
func cParityParseRunsAsm(p *Parser, state *cParityRunsState) (event int)

//go:nocheckptr
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

// FinishNumber owns the complete cold numeric operation: decode, publish its
// partial node/statistics, and apply error/capacity ordering. Successful nested
// values resume ordinary parent grammar; no numeric result returns to assembly.
//go:nocheckptr
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
//go:nocheckptr
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
