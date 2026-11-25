/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package jsonscan

import (
	"math"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

// tapePendingQueue buffers at most 2 validated tape nodes across StopNeedOutput
// yields so even cap(Nodes)==0/1/2 progresses deterministically without
// re-lexing or mutating LexState cursors.
type tapePendingQueue struct {
	count          uint8
	idx            uint8
	nodes          [2]Node
	isOpen         bool
	openIsArray    bool
	openPlan       *Plan
	nextPhase      GrammarPhase
	resumeAfterVal bool
}

func (q *tapePendingQueue) reset() {
	q.count = 0
	q.idx = 0
	q.isOpen = false
	q.openPlan = nil
	q.resumeAfterVal = false
}

func packOpenContainerVal(parentIdx int) uint64 {
	return uint64(uint32(parentIdx)) << 32
}

func unpackOpenParentIdx(openVal uint64) int {
	return int(int32(uint32(openVal >> 32)))
}

func (m *Machine) rollbackUnemittedEscapedKey(keyNode Node) {
	if (keyNode.Typ & TypeMask) == KStringEscaped {
		if uoff, _, inU := UnpackEscapedSpan(keyNode.Val); inU && uoff <= len(m.Unesc) {
			m.Unesc = m.Unesc[:uoff]
		}
	}
}

func (m *Machine) tryEmitSingle(nd Node, nextPhase GrammarPhase, resumeAfterVal bool) bool {
	if m.NodeLen < len(m.Nodes) {
		m.Nodes[m.NodeLen] = nd
		m.NodeLen++
		return true
	}
	m.pending.nodes[0] = nd
	m.pending.count = 1
	m.pending.idx = 0
	m.pending.isOpen = false
	m.pending.nextPhase = nextPhase
	m.pending.resumeAfterVal = resumeAfterVal
	m.Grammar.Phase = PhaseFlushPending
	return false
}

func (m *Machine) tryEmitPair(n0, n1 Node, resumeAfterVal bool) bool {
	if m.NodeLen+2 <= len(m.Nodes) {
		m.Nodes[m.NodeLen] = n0
		m.Nodes[m.NodeLen+1] = n1
		m.NodeLen += 2
		return true
	}
	m.pending.nodes[0] = n0
	m.pending.nodes[1] = n1
	m.pending.count = 2
	m.pending.idx = 0
	m.pending.isOpen = false
	m.pending.resumeAfterVal = resumeAfterVal
	m.Grammar.Phase = PhaseFlushPending
	return false
}

func (m *Machine) tryEmitOpenContainer(nd Node, isArray bool, containerPlan *Plan, nextPhase GrammarPhase) bool {
	if m.NodeLen < len(m.Nodes) {
		idx := m.NodeLen
		m.Nodes[idx] = nd
		m.NodeLen = idx + 1
		m.Grammar.ParentNodeIdx = idx
		m.Grammar.pushBit(isArray)
		if uint32(m.Grammar.Depth) > m.Stat.MaxDepth {
			m.Stat.MaxDepth = uint32(m.Grammar.Depth)
		}
		if m.Schema.HasPlan {
			m.Schema.pushPlanFrame(containerPlan)
		}
		m.Grammar.Phase = nextPhase
		return true
	}
	m.pending.nodes[0] = nd
	m.pending.count = 1
	m.pending.idx = 0
	m.pending.isOpen = true
	m.pending.openIsArray = isArray
	m.pending.openPlan = containerPlan
	m.pending.nextPhase = nextPhase
	m.pending.resumeAfterVal = false
	m.Grammar.Phase = PhaseFlushPending
	return false
}

// flushPending drains queued nodes into m.Nodes. Returns (flushedAll, resumeAfterVal).
func (m *Machine) flushPending() (bool, bool) {
	g := &m.Grammar
	for m.pending.idx < m.pending.count {
		if m.NodeLen >= len(m.Nodes) {
			return false, false
		}
		idx := m.NodeLen
		m.Nodes[idx] = m.pending.nodes[m.pending.idx]
		m.NodeLen = idx + 1
		m.pending.idx++
		if m.pending.isOpen {
			m.pending.isOpen = false
			g.ParentNodeIdx = idx
			g.pushBit(m.pending.openIsArray)
			if uint32(g.Depth) > m.Stat.MaxDepth {
				m.Stat.MaxDepth = uint32(g.Depth)
			}
			if m.Schema.HasPlan {
				m.Schema.pushPlanFrame(m.pending.openPlan)
				m.pending.openPlan = nil
			}
		}
	}
	resume := m.pending.resumeAfterVal
	nextPh := m.pending.nextPhase
	m.pending.reset()
	if !resume {
		g.Phase = nextPh
	}
	return true, resume
}

func (m *Machine) closeEmittedContainer(isArr bool) {
	if m.Schema.HasPlan && m.Schema.PlanDepth > 0 {
		m.Schema.popPlanFrame()
	}
	pIdx := m.Grammar.ParentNodeIdx
	cnode := &m.Nodes[pIdx]
	openVal := cnode.Val
	count := uint32(openVal)
	if isArr {
		m.Stat.Array++
		m.Stat.ArrayElems += count
	} else {
		m.Stat.Object++
		m.Stat.ObjectKeys += count
	}
	m.Grammar.ParentNodeIdx = unpackOpenParentIdx(openVal)
	span := uint64(m.NodeLen - pIdx)
	cnode.Val = (span << ConLenBits) | uint64(count)
}

func (m *Machine) decodeEscapedStringToUnesc(
	src string,
	rawStart, firstEsc, rawContentEnd int,
	rejectLoneSurrogate bool,
	isKey bool,
) (nd Node, keyStr string, errRelOff int, errCode ScanError) {
	uoff := len(m.Unesc)
	prefixLen := firstEsc - rawStart
	maxOut := rawContentEnd - rawStart
	if cap(m.Unesc)-uoff < maxOut {
		newCap := cap(m.Unesc) * 2
		if newCap < 256 {
			newCap = 256
		}
		if newCap < uoff+maxOut {
			newCap = uoff + maxOut
		}
		grown := make([]byte, uoff, newCap)
		copy(grown, m.Unesc)
		m.Unesc = grown
	}
	m.Unesc = append(m.Unesc, src[rawStart:firstEsc]...)
	escTail := src[firstEsc:rawContentEnd]
	dst := m.Unesc[uoff+prefixLen : uoff+maxOut]
	written, errRel, uerr := UnquoteInto(escTail, dst, rejectLoneSurrogate)
	if uerr != ErrNone {
		m.Unesc = m.Unesc[:uoff]
		return Node{}, "", errRel, uerr
	}
	ulen := prefixLen + written
	m.Unesc = m.Unesc[:uoff+ulen]
	packedVal, ok := PackEscapedSpan(uoff, ulen)
	if !ok {
		m.Unesc = m.Unesc[:uoff]
		return Node{}, "", 0, ErrSpanOverflow
	}
	nd = Node{Typ: KStringEscaped | uint64(rawStart)<<PosBits, Val: packedVal}
	if isKey {
		b := m.Unesc[uoff : uoff+ulen]
		keyStr = unsafe.String(unsafe.SliceData(b), len(b))
	}
	return nd, keyStr, 0, ErrNone
}

// parseValidatedNumberSpanNode converts an already-lexed valid number span
// src[start:end] into a Tape Node using the single number conversion authority.
// Returns (nd, ErrNone) on success, or (Node{}, ErrFloatInf / ErrInvalidNumber).
func parseValidatedNumberSpanNode(src string, start, end int, deferFloatOverflow bool) (Node, ScanError) {
	resNum, perr := number.ParseValidatedNumber(src[start:end])
	if perr == types.ERR_FLOAT_INFINITY {
		if deferFloatOverflow {
			return Node{Typ: KRawNumber | uint64(start)<<PosBits, Val: uint64(end - start)}, ErrNone
		}
		return Node{}, ErrFloatInf
	}
	if perr != 0 {
		return Node{}, ErrInvalidNumber
	}
	switch resNum.Kind {
	case number.Unsigned:
		return Node{Typ: KUint | uint64(start)<<PosBits, Val: resNum.Uint64}, ErrNone
	case number.Signed:
		return Node{Typ: KSint | uint64(start)<<PosBits, Val: uint64(resNum.Int64)}, ErrNone
	default:
		return Node{Typ: KReal | uint64(start)<<PosBits, Val: math.Float64bits(resNum.Float64)}, ErrNone
	}
}

// scanPlainColdNumberNode scans and converts a full-buffer (final==true) cold
// number starting at pos directly into a Tape Node without constructing a 64B
// LexNumberResult on the hot path. rejectCont enforces the profile's trailing
// continuation-byte policy (Profile.UseNumber) independently of emitRaw, so
// schema typed-number emission never alters number boundaries or error codes.
// Returns ok=false on any lexical, boundary, or overflow error so the caller
// delegates error formatting to the canonical stepNumber / failNumberLex /
// checkNumberContBoundary / failFloatOverflow path.
func scanPlainColdNumberNode(src string, pos int, emitRaw, rejectCont, deferFloatOverflow bool) (nd Node, endPos int, ok bool) {
	if end, _, _, _, hint, bits, cont, fastOK := lexFastNumberSpan(src, pos, true); fastOK {
		if rejectCont && cont {
			return Node{}, 0, false
		}
		if emitRaw {
			return Node{Typ: KRawNumber | uint64(pos)<<PosBits, Val: uint64(end - pos)}, end, true
		}
		switch hint {
		case NumberHintUint:
			return Node{Typ: KUint | uint64(pos)<<PosBits, Val: bits}, end, true
		case NumberHintSint:
			return Node{Typ: KSint | uint64(pos)<<PosBits, Val: bits}, end, true
		case NumberHintFloat:
			return Node{Typ: KReal | uint64(pos)<<PosBits, Val: bits}, end, true
		}
		convNd, ecode := parseValidatedNumberSpanNode(src, pos, end, deferFloatOverflow)
		if ecode != ErrNone {
			return Node{}, 0, false
		}
		return convNd, end, true
	}

	end, perr := kernScanNumberPrefix(src, pos)
	if perr != 0 {
		return Node{}, 0, false
	}
	if rejectCont && !isBareZeroToken(src, pos, end) && end < len(src) && isNumberContByte(src[end]) {
		return Node{}, 0, false
	}
	if emitRaw {
		return Node{Typ: KRawNumber | uint64(pos)<<PosBits, Val: uint64(end - pos)}, end, true
	}
	convNd, ecode := parseValidatedNumberSpanNode(src, pos, end, deferFloatOverflow)
	if ecode != ErrNone {
		return Node{}, 0, false
	}
	return convNd, end, true
}

// consumeNumberToken converts or records an already-lexed valid JSON number span
// without re-deciding the token boundary.
func (m *Machine) consumeNumberToken(src string, final bool, lexRes LexNumberResult) (ok bool, failRes ScanResult, emitNode bool, nd Node) {
	if okCont, contErr := m.checkNumberContBoundary(src, final, lexRes); !okCont {
		return false, contErr, false, Node{}
	}
	g := &m.Grammar
	start := lexRes.Start
	end := lexRes.End
	if m.isEventSink() {
		g.Cur = end
		if uerr := m.sink.OnNumber(src[start:end], lexRes); uerr != nil {
			return false, m.abortSink(src, final, uerr), false, Node{}
		}
		return true, ScanResult{}, false, Node{}
	}
	if !m.isEmitting() {
		g.Cur = end
		return true, ScanResult{}, false, Node{}
	}
	if !CheckSourceSpan(start, end-start, len(src)) {
		return false, m.failAt(src, final, start, end, ErrSpanOverflow), false, Node{}
	}
	if g.Profile.UseNumber {
		g.Cur = end
		m.Stat.Number++
		return true, ScanResult{}, true, Node{Typ: KRawNumber | uint64(start)<<PosBits, Val: uint64(end - start)}
	}
	switch lexRes.HintKind {
	case NumberHintUint:
		g.Cur = end
		m.Stat.Number++
		return true, ScanResult{}, true, Node{Typ: KUint | uint64(start)<<PosBits, Val: lexRes.HintBits}
	case NumberHintSint:
		g.Cur = end
		m.Stat.Number++
		return true, ScanResult{}, true, Node{Typ: KSint | uint64(start)<<PosBits, Val: lexRes.HintBits}
	case NumberHintFloat:
		g.Cur = end
		m.Stat.Number++
		return true, ScanResult{}, true, Node{Typ: KReal | uint64(start)<<PosBits, Val: lexRes.HintBits}
	}

	nd, ecode := parseValidatedNumberSpanNode(src, start, end, g.Profile.DeferFloatOverflow)
	if ecode == ErrFloatInf {
		return false, m.failFloatOverflow(src, final, start, end), false, Node{}
	}
	if ecode != ErrNone {
		return false, m.failAt(src, final, end, end+1, ErrInvalidNumber), false, Node{}
	}
	g.Cur = end
	m.Stat.Number++
	return true, ScanResult{}, true, nd
}
