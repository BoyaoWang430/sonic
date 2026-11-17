//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/cpu"
)

// Assembly owns a flat run of keys/scalars and returns at hierarchy yields
// or cold scalar paths. Go completes cold numbers/literals and Unicode escapes;
// only strings resume an active scalar across an assembly yield.
// Token validity is described exclusively by the returned status code — errors
// never inherit prior escape/number state or publish half-formed scalar nodes.
type cParityRunsState struct {
	phase     uint64
	context   uint64 // String-resume context: root, object value, array value, or key.
	pos       int    // Active key/value/open token start offset; unused for close events.
	stringErr int    // Unicode continuation error only.
	stringDst int    // Offset into JsonBytes() for next escaped string output byte.
}

// The continuation context is shared with the assembly through go_asm.h.
const (
	cParityRunsContextRoot uint64 = iota
	cParityRunsContextObject
	cParityRunsContextArray
	cParityRunsContextKey
)

// String helpers return this negative sentinel to request Unicode handling.
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
	cParityRunsNodeCapFull
)

func cParityParseWithPadding(p *Parser) ErrorCode {
	var state cParityRunsState
	p.scanFlags = p.options & (scanKernelFlagRawNumber | scanKernelFlagRejectControl)
	buf := &p.nbuf
	if len(p.nodes) == 0 {
		n := cap(p.nodes)
		if n == 0 {
			n = defaultNodesCount
			p.nodes = make([]node, n, n)
		} else {
			p.nodes = p.nodes[:n]
		}
		buf.ncap = len(p.nodes)
	} else if buf.ncap <= 0 || buf.ncap > len(p.nodes) {
		buf.ncap = len(p.nodes)
	}
	if buf.parent != -1 {
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
		var event int
		if cpu.UseScalar {
			event = cParityParseRunsScalar(&p.ScanKernelState, &state, raw, p.nodes)
		} else {
			event = cParityParseRunsAsm(&p.ScanKernelState, &state, rawBase, nodesBase)
		}
		if event <= SONIC_STACK_OVERFLOW {
			code = ErrorCode(event)
			break
		}
		switch event {
		case cParityRunsCloseContainer:
			state.phase = cParityRunsParentContinue
		case cParityRunsNodeCapFull:
			code = errCodeNodeCapExhausted
			goto done
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

// cParityRunsFinishNumber decodes a cold or raw-mode numeric token and emits
// its node only when the token is valid. On error it advances p.pos to the
// exact error cursor without publishing a partial or stale node.
func cParityRunsFinishNumber(p *Parser, state *cParityRunsState) ErrorCode {
	raw := p.JsonBytes()
	negative := raw[state.pos] == '-'
	base := unsafe.Pointer(unsafe.SliceData(raw))
	startCur := unsafe.Add(base, p.pos)
	cur := startCur
	var num cParityNum
	var code ErrorCode
	if p.options&(1<<_F_use_number) != 0 {
		remain := p.end - p.pos
		if !negative {
			remain++
		}
		length := cParitySkipPositive(&cur, remain)
		if length < 0 {
			code = ErrorCode(-length)
		} else if negative && length == 0 {
			code = SONIC_INVALID_NUM
		} else {
			value := uint64(length)
			if negative {
				value++
			}
			num = cParityNum{typ: KRawNumber, val: value}
		}
	} else {
		cur, num, code = cParityParseNumberValue(cur, negative, p.dbuf)
		if code == SONIC_FLOAT_INF && p.deferFloatOverflow {
			cur = startCur
			remain := p.end - p.pos
			if !negative {
				remain++
			}
			length := cParitySkipPositive(&cur, remain)
			if length < 0 {
				code = ErrorCode(-length)
			} else if negative && length == 0 {
				code = SONIC_INVALID_NUM
			} else {
				value := uint64(length)
				if negative {
					value++
				}
				num = cParityNum{typ: KRawNumber, val: value}
				code = SONIC_OK
			}
		}
	}
	p.pos += int(uintptr(cur) - uintptr(startCur))
	if code != SONIC_OK {
		return code
	}

	buf := &p.nbuf
	nIdx := buf.nlen
	p.nodes[nIdx] = node{typ: num.typ | uint64(state.pos)<<PosBits, val: num.val}
	buf.nlen = nIdx + 1
	buf.stat.number++
	if buf.parent != -1 && nIdx+2 > buf.ncap {
		return errCodeNodeCapExhausted
	}
	return SONIC_OK
}

// cParityRunsFailLiteral advances p.pos past the first mismatching byte of a
// rejected literal and returns SONIC_INVALID_LITERAL without writing to nodes.
func cParityRunsFailLiteral(p *Parser, pos int) ErrorCode {
	raw := p.JsonBytes()
	suffix := "ull"
	switch raw[pos] {
	case 't':
		suffix = "rue"
	case 'f':
		suffix = "alse"
	}
	for i := 0; i < len(suffix); i++ {
		c := raw[p.pos]
		p.pos++
		if c != suffix[i] {
			break
		}
	}
	return SONIC_INVALID_LITERAL
}
