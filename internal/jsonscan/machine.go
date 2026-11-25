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
	"math/bits"

	"github.com/bytedance/sonic/internal/native/types"
)

const errEOFSentinel uint64 = 1 << 62

func isKernelEOF(pe types.ParsingError) bool {
	return pe == types.ERR_EOF
}

// LexState owns resumable active-token lexical progress across chunk/window
// boundaries without rescanning already-verified prefixes.
type LexState struct {
	TokStart       int
	StrScanOff     int
	StrFirstEsc    int
	StrRawCtrlSeen bool
	StrVal         StringValidateState

	NumScanOff int
	NumDotPos  int
	NumExpPos  int
	NumSignPos int
	NumPhase   uint8
	NumForm    NumberForm

	LitChar    byte
	LitMatched uint8

	UTF8Checked int
	UTF8Invalid bool

	TravBase     int
	TravEnd      int
	TravEvents   uint64
	TravNonspace uint64
}

// reset clears only fields that may be inspected before beginString/beginNumber
// initializes cold token state, avoiding a 168-byte duffcopy on every Reset.
func (l *LexState) reset() {
	l.StrScanOff = 0
	l.NumScanOff = 0
	l.NumPhase = numPhaseNone
	l.UTF8Checked = 0
	l.UTF8Invalid = false
	l.TravBase = 0
	l.TravEnd = 0
}

func (l *LexState) beginString(quotePos int) {
	l.TokStart = quotePos
	l.StrScanOff = quotePos + 1
	l.StrFirstEsc = -1
	l.StrRawCtrlSeen = false
	l.StrVal.reset()
}

func (l *LexState) beginNumber(pos int) {
	l.TokStart = pos
	l.NumScanOff = pos
	l.NumDotPos = -1
	l.NumExpPos = -1
	l.NumSignPos = -1
	l.NumPhase = numPhaseNone
	l.NumForm = NumberFormInt
}

// blockBatchState buffers up to DefaultLexBatchCap pointer-free 64-byte block
// bitmask records across StopNeedOutput yields without re-invoking LexBatch on
// already-classified blocks.
type blockBatchState struct {
	blocks      [DefaultLexBatchCap]LexBlockMask
	carry       LexBlockCarry
	batchStart  int
	batchEnd    int
	expectedCur int
	ev          uint64
	nBlocks     uint8
	blkIdx      uint8
	active      bool
	forceScalar bool
	capOverride uint8           // 0 = DefaultLexBatchCap (16); 1..16 = custom cap; 255 = 1-block cold mode
	diagRole    uint8           // DiagRoleMain (0), DiagRolePosition (1), DiagRoleSubtree (2)
	witness     lexWitnessState // non-trailing: 0B in!sonic_witness &&!sonic_diag; 2B mask in witness/diag builds
	calls       uint64
}

func (b *blockBatchState) reset() {
	b.carry = LexBlockCarry{}
	b.batchStart = 0
	b.batchEnd = 0
	b.expectedCur = 0
	b.ev = 0
	b.nBlocks = 0
	b.blkIdx = 0
	b.active = false
	b.forceScalar = false
	b.capOverride = 0
	b.diagRole = DiagRoleMain
	b.witness.reset()
	b.calls = 0
}

func (b *blockBatchState) clearActive() {
	b.carry = LexBlockCarry{}
	b.ev = 0
	b.nBlocks = 0
	b.blkIdx = 0
	b.active = false
}

// hasReusableSchemaWindow reports whether b holds an active classified window
// whose cursor, bounds, and block index are consistent with cur so ownership can
// be transferred between stepSchemaFullEmit and stepGeneralMode without re-lexing.
func (b *blockBatchState) hasReusableSchemaWindow(cur int) bool {
	return b.active && b.expectedCur == cur && cur >= b.batchStart && cur < b.batchEnd && int(b.blkIdx) < int(b.nBlocks)
}

// takeReusableSchemaWindow transfers an active verified LexBatch window from b
// into stepSchemaFullEmit registers, clearing b.active on transfer or clearing
// the entire batch state if the cursor/window invariants are not satisfied.
//
//go:noinline
func (b *blockBatchState) takeReusableSchemaWindow(cur int) (carry types.LexBlockCarry, batchStart, batchEnd, nBlocks, blkIdx int, ev uint64, ok bool) {
	if !b.hasReusableSchemaWindow(cur) {
		b.clearActive()
		return types.LexBlockCarry{}, cur, cur, 0, 0, 0, false
	}
	carry = b.carry
	batchStart = b.batchStart
	batchEnd = b.batchEnd
	nBlocks = int(b.nBlocks)
	blkIdx = int(b.blkIdx)
	ev = b.ev
	b.active = false
	return carry, batchStart, batchEnd, nBlocks, blkIdx, ev, true
}

// publishSchemaWindow hands a verified classified window from stepSchemaFullEmit
// to stepGeneralMode at cur (an opening '"' structural token outside any string).
// If cur no longer lies inside [batchStart, batchEnd) (for example after a key
// crossed a 1 KiB batch boundary and advanced batchStart), it clears b safely.
//
//go:noinline
func (b *blockBatchState) publishSchemaWindow(carry types.LexBlockCarry, batchStart, batchEnd, nBlocks, cur int) {
	if nBlocks > 0 && cur >= batchStart && cur < batchEnd {
		nb := (cur - batchStart) >> 6
		rel := uint(cur - (batchStart + nb*64))
		b.carry = carry
		b.batchStart = batchStart
		b.batchEnd = batchEnd
		b.nBlocks = uint8(nBlocks)
		b.blkIdx = uint8(nb)
		b.ev = b.blocks[nb].EventMask &^ ((uint64(1) << rel) - 1)
		b.expectedCur = cur
		b.active = true
		return
	}
	b.clearActive()
}

func (b *blockBatchState) effectiveCap() int {
	if b.capOverride == 255 {
		return 0
	}
	if b.capOverride > 0 && int(b.capOverride) <= DefaultLexBatchCap {
		return int(b.capOverride)
	}
	return DefaultLexBatchCap
}

// seekAfterCompletedToken advances b to newCur after a multi-byte scalar, string,
// or skipped subtree finishes outside any string. If newCur lies inside the
// already-classified [batchStart, batchEnd) range, b reuses the buffered block
// bitmasks directly without invoking LexBatch again.
func (b *blockBatchState) seekAfterCompletedToken(src string, newCur int) {
	if !b.active || newCur < b.batchStart || newCur >= b.batchEnd {
		b.clearActive()
		return
	}
	nb := (newCur - b.batchStart) >> 6
	b.blkIdx = uint8(nb)
	blockBase := b.batchStart + nb*64
	rel := uint(newCur - blockBase)
	ev := b.blocks[nb].EventMask &^ ((uint64(1) << rel) - 1)
	if newCur < len(src) && isScalarContByte[src[newCur]] {
		ev |= uint64(1) << rel
	}
	b.ev = ev
	b.expectedCur = newCur
}

// Machine is the thin orchestrator coordinating GrammarState, SchemaState,
// LexState, LexBatch block-mask buffering, and Tape/Compact/EventSink consumers
// without duplicating JSON structural syntax.
type Machine struct {
	// diag is placed before non-empty fields so its zero-sized !sonic_diag
	// representation never triggers Go's trailing zero-sized field padding.
	diag       diagMachineState
	Grammar    GrammarState
	Schema     SchemaState
	Lex        LexState
	Nodes      []Node
	NodeLen    int
	Unesc      []byte
	Stat       Stat
	CompactObs CompactObservation

	sink     EventSink
	sinkErr  error
	posSpans []ValidatedSourceSpan
	posLen   int
	pending  tapePendingQueue
	batch    blockBatchState
}

// Reset initializes m for a new scan under profile and mode without zeroing
// the BitStack / InlinePlans / block scratch arrays (slots are written before read).
func (m *Machine) Reset(profile Profile, mode OutputMode) {
	m.Schema.reset()
	m.Grammar.reset(profile, mode)
	m.Lex.reset()
	m.NodeLen = 0
	if m.Unesc != nil {
		m.Unesc = m.Unesc[:0]
	}
	m.Stat = Stat{}
	if mode == ModeCompactObserve {
		m.CompactObs = CompactObservation{FirstDirtyPos: -1}
	}
	m.sink = nil
	m.sinkErr = nil
	m.posSpans = nil
	m.posLen = 0
	m.pending.reset()
	m.batch.reset()
	m.diag.reset(0)
}

// ResetFastEmit initializes m in-place for standard Sonic ModeEmit decoding
// without constructing or copying a 32-byte Profile struct by value.
func (m *Machine) ResetFastEmit(useNumber, rejectControl, rejectLoneSurrogate, deferFloatOverflow, forceScalar bool, startPos int) {
	m.Schema.reset()
	g := &m.Grammar
	g.Profile.MaxContainerDepth = StrictMaxContainerDepth
	g.Profile.BaseDepth = 0
	g.Profile.RejectControl = rejectControl
	g.Profile.ValidateEscapes = true
	g.Profile.ValidateUTF8 = false
	g.Profile.RejectLoneSurrogate = rejectLoneSurrogate
	g.Profile.UseNumber = useNumber
	g.Profile.DeferFloatOverflow = deferFloatOverflow
	g.Profile.FullDocument = false
	g.Profile.EscapeHTMLObserve = false
	g.Profile.CompactFirstDirtyOnly = false
	g.Profile.ASTCompatLex = false
	g.RootMode = ModeEmit
	g.Phase = PhaseValue
	g.Cur = startPos
	g.ValueStart = -1
	g.Depth = 0
	g.ParentNodeIdx = -1
	g.ErrOffset = -1
	g.WasFirstKey = false
	if g.SubModeActive || g.SubDisUnknown {
		g.SubModeActive = false
		g.SubMode = ModeEmit
		g.SubFloorDepth = 0
		g.SubStart = 0
		g.SubDisUnknown = false
		g.SubKeyNode = Node{}
	}
	m.Lex.reset()
	m.NodeLen = 0
	if m.Unesc != nil {
		m.Unesc = m.Unesc[:0]
	}
	m.Stat = Stat{}
	m.sink = nil
	m.sinkErr = nil
	m.pending.reset()
	m.batch.reset()
	m.batch.forceScalar = forceScalar
	m.diag.reset(startPos)
}

// ResetValidatedDoc performs the minimal GrammarState reset required before
// ScanValidatedDocInto, avoiding redundant clears of Lex/Stat/pending/batch
// fields that ScanValidatedDocInto never reads on the hot path.
func (m *Machine) ResetValidatedDoc(rejectControl, rejectLoneSurrogate bool, startPos int) {
	m.Schema.reset()
	g := &m.Grammar
	g.Profile.MaxContainerDepth = StrictMaxContainerDepth
	g.Profile.BaseDepth = 0
	g.Profile.RejectControl = rejectControl
	g.Profile.ValidateEscapes = true
	g.Profile.ValidateUTF8 = false
	g.Profile.RejectLoneSurrogate = rejectLoneSurrogate
	g.Profile.UseNumber = false
	g.Profile.DeferFloatOverflow = true
	g.Profile.FullDocument = false
	g.Profile.EscapeHTMLObserve = false
	g.Profile.CompactFirstDirtyOnly = false
	g.Profile.ASTCompatLex = false
	g.RootMode = ModeEmit
	g.Phase = PhaseValue
	g.Cur = startPos
	g.ValueStart = -1
	g.Depth = 0
	g.ParentNodeIdx = -1
	g.ErrOffset = -1
	g.WasFirstKey = false
	if g.SubModeActive || g.SubDisUnknown {
		g.SubModeActive = false
		g.SubMode = ModeEmit
		g.SubFloorDepth = 0
		g.SubStart = 0
		g.SubDisUnknown = false
		g.SubKeyNode = Node{}
	}
	m.batch.forceScalar = false
	m.batch.capOverride = 0
	m.batch.diagRole = DiagRoleMain
	m.batch.witness.reset()
	m.batch.calls = 0
	m.diag.reset(startPos)
}

// ResetValidateOne initializes m in-place for ValidateOne / ValidateOneAtDepth
// (ASTValidateProfile, ModeValidateOnly) without constructing a Profile value
// or clearing emit/compact/position fields unused by validation.
func (m *Machine) ResetValidateOne(validateStrings bool, baseDepth, maxDepth, startPos int) {
	m.Schema.reset()
	g := &m.Grammar
	g.Profile.MaxContainerDepth = maxDepth
	g.Profile.BaseDepth = baseDepth
	g.Profile.RejectControl = validateStrings
	g.Profile.ValidateEscapes = validateStrings
	g.Profile.ValidateUTF8 = false
	g.Profile.RejectLoneSurrogate = false
	g.Profile.UseNumber = false
	g.Profile.DeferFloatOverflow = false
	g.Profile.FullDocument = false
	g.Profile.EscapeHTMLObserve = false
	g.Profile.CompactFirstDirtyOnly = false
	g.Profile.ASTCompatLex = true
	g.RootMode = ModeValidateOnly
	g.Phase = PhaseValue
	g.Cur = startPos
	g.ValueStart = -1
	g.Depth = 0
	g.ParentNodeIdx = -1
	g.ErrOffset = -1
	g.WasFirstKey = false
	if g.SubModeActive || g.SubDisUnknown {
		g.SubModeActive = false
		g.SubMode = ModeEmit
		g.SubFloorDepth = 0
		g.SubStart = 0
		g.SubDisUnknown = false
		g.SubKeyNode = Node{}
	}
	m.Lex.reset()
	m.NodeLen = 0
	m.Stat.MaxDepth = 0
	m.sink = nil
	m.batch.reset()
	m.diag.reset(startPos)
}

// SetBatchCapForTest overrides the LexBatch block buffer capacity for contract
// testing (0 selects 1-block cold stepping mode; 1..DefaultLexBatchCap bounds dst).
func (m *Machine) SetBatchCapForTest(capBlocks int) {
	if capBlocks <= 0 {
		m.batch.capOverride = 255
		return
	}
	if capBlocks > DefaultLexBatchCap {
		capBlocks = DefaultLexBatchCap
	}
	m.batch.capOverride = uint8(capBlocks)
}

// SetForceScalar selects the pure-Go scalar LexBatch reference on m without
// mutating global cpu.UseScalar state across concurrent goroutines.
func (m *Machine) SetForceScalar(force bool) {
	m.batch.forceScalar = force
}

// ForceScalar reports the current request-scoped forceScalar policy on m.
func (m *Machine) ForceScalar() bool {
	return m.batch.forceScalar
}

// ObservedLexProducers returns the bitset of outer LexProducer families invoked
// since the last Reset/ResetFastEmit/ResetValidatedDoc on m when WitnessEnabled
// is true, or 0 (DISABLED/NOT_COLLECTED) in default release builds.
func (m *Machine) ObservedLexProducers() LexProducerMask {
	return m.batch.witness.observedMask()
}

// LexBatchCalls returns the number of LexBatch kernel invocations since Reset.
func (m *Machine) LexBatchCalls() uint64 {
	return m.batch.calls
}

// ConfigureSchema attaches a static Schema projection plan to a ModeEmit Machine.
func (m *Machine) ConfigureSchema(plan *Plan, caseSensitive, disallowUnknown, relaxedUnknownSkip bool) {
	m.Schema.configure(plan, caseSensitive, disallowUnknown, relaxedUnknownSkip)
}

// ReleasePoolRefs detaches borrowed output buffers, schema plan references, and
// request-scoped policy state before returning an embedding parser to a pool.
func (m *Machine) ReleasePoolRefs() {
	m.Nodes = nil
	m.Unesc = nil
	m.sink = nil
	m.sinkErr = nil
	m.posSpans = nil
	m.posLen = 0
	m.Schema.reset()
	m.batch.clearActive()
	m.batch.forceScalar = false
	m.batch.capOverride = 0
	m.batch.diagRole = DiagRoleMain
	m.batch.witness.reset()
	m.diag.reset(0)
}

func (m *Machine) isEmitting() bool {
	return m.Grammar.RootMode == ModeEmit && !m.Grammar.SubModeActive
}

func (m *Machine) skipSpace(src string, pos int) (int, bool) {
	observe := m.Grammar.RootMode == ModeCompactObserve
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		return pos, true
	}
	if !observe && uint(pos+1) < uint(len(src)) && src[pos] == ' ' && src[pos+1] > ' ' {
		return pos + 1, true
	}
	return m.skipSpaceSlow(src, pos, observe)
}

func (m *Machine) skipSpaceSlow(src string, pos int, observe bool) (int, bool) {
	l := &m.Lex
	for pos < len(src) {
		c := src[pos]
		if c > ' ' {
			return pos, true
		}
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return pos, true
		}
		if observe {
			m.recordCompactSpace(pos)
		}
		if pos+1 < len(src) && src[pos+1] > ' ' {
			return pos + 1, true
		}
		if pos < l.TravBase || pos >= l.TravEnd {
			ev, ns, width := kernTraversalMasks(src, pos, m.Grammar.Profile.RejectControl)
			l.TravBase = pos
			l.TravEnd = pos + width
			l.TravEvents = ev
			l.TravNonspace = ns
		}
		mask := l.TravNonspace >> uint(pos-l.TravBase)
		if mask != 0 {
			return pos + bits.TrailingZeros64(mask), true
		}
		pos = l.TravEnd
	}
	return len(src), false
}

func (m *Machine) stepRelaxedSkip(src string, final bool, pos int, ch byte) (ok bool, failRes ScanResult) {
	g := &m.Grammar
	switch ch {
	case '{', '[':
		endPos, err := kernSkipContainerRelaxed(src, pos)
		if err != 0 {
			return false, m.failEOF(src, final)
		}
		g.Cur = endPos
		return true, ScanResult{}
	case '"':
		relEnd, err := kernSkipStringRelaxed(src[pos+1:])
		if err != 0 {
			return false, m.failEOF(src, final)
		}
		g.Cur = pos + 1 + relEnd
		return true, ScanResult{}
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		g.Cur = kernSkipNumberRelaxed(src, pos+1)
		return true, ScanResult{}
	case 't', 'n':
		if len(src)-pos < 4 {
			return false, m.failEOF(src, final)
		}
		g.Cur = pos + 4
		return true, ScanResult{}
	case 'f':
		if len(src)-pos < 5 {
			return false, m.failEOF(src, final)
		}
		g.Cur = pos + 5
		return true, ScanResult{}
	default:
		return false, m.failAt(src, final, pos, pos+1, ErrInvalidChar)
	}
}

func (m *Machine) activateValueSubMode(pos int, ch byte) {
	g := &m.Grammar
	s := &m.Schema
	if g.ValueStart < 0 {
		g.ValueStart = pos
	}
	if g.SubModeActive && g.SubStart == -2 {
		g.SubStart = pos
	}
	if !g.SubModeActive {
		if g.RootMode == ModeRawSpan && g.Depth == 0 {
			g.SubModeActive = true
			g.SubMode = ModeRawSpan
			g.SubFloorDepth = 0
			g.SubStart = pos
		} else if g.RootMode == ModeEmit && s.HasPlan && s.ActivePlan != nil && s.ActivePlan.Kind == PlanRaw {
			if ch == 'n' {
				s.ActivePlan = nil
			} else {
				g.SubModeActive = true
				g.SubMode = ModeRawSpan
				g.SubFloorDepth = g.Depth
				g.SubStart = pos
				s.ActivePlan = nil
			}
		}
	}
}

func (m *Machine) isRelaxedSkipValue(final bool) bool {
	g := &m.Grammar
	return final && (g.RootMode == ModeRelaxedSkip || (g.SubModeActive && g.SubMode == ModeRelaxedSkip && g.Depth == g.SubFloorDepth))
}

func isShortASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Step advances the shared JSON state machine over src. Pass final=true when src
// contains the end of input, or final=false when additional bytes may arrive.
func (m *Machine) Step(src string, final bool) ScanResult {
	g := &m.Grammar
	b := &m.batch

	if g.Profile.BaseDepth < 0 || g.Profile.BaseDepth > g.Profile.MaxContainerDepth {
		g.ErrOffset = g.Cur
		return m.finishResult(src, final, StopError, ErrDepthOverflow)
	}

	for {
		if b.active && g.Cur != b.expectedCur {
			b.clearActive()
		}
		if b.effectiveCap() == 0 {
			return m.stepUnbatched(src, final)
		}

		// Drain queued pending output on grown plain ModeEmit buffers so large
		// full-buffer inputs immediately re-enter stepPlainFullEmit.
		if g.Phase == PhaseFlushPending && g.RootMode == ModeEmit && !m.Schema.HasPlan && !g.SubModeActive {
			flushed, resumeAfterVal := m.flushPending()
			if !flushed {
				b.expectedCur = g.Cur
				return m.finishResult(src, final, StopNeedOutput, ErrNone)
			}
			if resumeAfterVal {
				if g.Depth == 0 {
					if g.Profile.FullDocument {
						g.Phase = PhaseTrailingSpace
					} else {
						b.clearActive()
						g.Phase = PhaseDone
						return m.finishResult(src, final, StopDone, ErrNone)
					}
				} else {
					m.Nodes[g.ParentNodeIdx].Val++
					g.Phase = afterValuePhase(g.Depth, g.topIsArray(), g.Profile.FullDocument)
				}
			}
		}

		var res ScanResult
		switch {
		case m.canEnterPlainFullEmit(src, final):
			res = m.stepPlainFullEmit(src)
		case m.canEnterSchemaFullEmit(src, final):
			res = m.stepSchemaFullEmit(src)
		case m.canEnterDefaultValidateOnly(src, final):
			res = m.stepDefaultValidateOnly(src)
		case m.canEnterCompactObserve(src, final):
			res = m.stepCompactObserve(src)
		default:
			res = m.stepGeneralMode(src, final, false)
		}
		if res.Reason == stopHandoffGeneral {
			res = m.stepGeneralMode(src, final, true)
		}
		if res.Reason == stopYieldFast {
			diagRecordFastResume()
			continue
		}
		return res
	}
}

// shouldYieldToFastEmit reports whether stepGeneral has completed a cold detour
// and the current deterministic grammar state qualifies to resume stepSchemaFullEmit
// or stepPlainFullEmit in Machine.Step's outer loop (zero cross-request state).
func (m *Machine) shouldYieldToFastEmit(src string, final bool) bool {
	if !final {
		return false
	}
	g := &m.Grammar
	b := &m.batch
	if g.Cur >= len(src) || g.RootMode != ModeEmit || g.SubModeActive || m.sink != nil {
		return false
	}
	if g.Profile.ASTCompatLex || g.Profile.BaseDepth != 0 || len(src) > LexMaxWindowBytes || b.capOverride != 0 {
		return false
	}
	if !(g.Phase <= PhaseArrayAfterVal || g.Phase == PhaseTrailingSpace) {
		return false
	}
	// If the remaining document tail (<=64B) is already lexed in b.blocks, finish
	// in stepGeneral instead of discarding the final block only to re-lex it.
	if b.active && b.batchEnd >= len(src) && len(src)-g.Cur <= 64 {
		return false
	}
	if len(m.Nodes)-m.NodeLen < plainEmitBlockMaxNodes {
		return false
	}
	s := &m.Schema
	if s.HasPlan {
		return s.PlanDepth == g.Depth && g.Depth < len(s.InlinePlans)
	}
	return true
}

func (m *Machine) stepGeneral(src string, final bool) ScanResult {
	return m.stepGeneralMode(src, final, false)
}

func (m *Machine) stepGeneralMode(src string, final bool, fromFastHandoff bool) ScanResult {
	g := &m.Grammar
	s := &m.Schema
	l := &m.Lex
	b := &m.batch
	handoffBatchBlocks := 1
	// Local fact for this stepGeneralMode call: true only when stepSchemaFullEmit
	// handed over an active reusable window in strict ModeEmit schema mode, so
	// subsequent refills in this call can pass any unused tail back on resume
	// instead of discarding it. Cold handoffs without a preserved window (Raw,
	// escaped keys, deep nesting, capacity growth, cross-batch keys) keep the
	// baseline 1->2->4->8->16 ramp.
	preservedHandoffWindow := fromFastHandoff && final && g.RootMode == ModeEmit && s.HasPlan && !s.RelaxedUnknownSkip && b.hasReusableSchemaWindow(g.Cur)
	// Local per-call counter: after completing a strict ModeValidateOnly unknown
	// field value, defer yielding back to stepSchemaFullEmit until 2 subsequent
	// normal (!SubModeActive) value completions occur in this call (which may be
	// sibling known fields or elements/subfields inside a known nested container).
	postUnknownValueCooldown := 0
	if DiagEnabled {
		genStartCur := g.Cur
		defer func() {
			diagRecordGeneralBytes(g.Cur - genStartCur)
		}()
	}

	var lexFlags LexFlags
	if g.Profile.RejectControl || g.RootMode == ModeCompactObserve {
		lexFlags = LexFlagRejectControl
	}
	observeSpace := g.RootMode == ModeCompactObserve
	posMode := g.RootMode == ModePositionSpan

	for {
		switch g.Phase {
		case PhaseDone:
			b.clearActive()
			return m.finishResult(src, final, StopDone, ErrNone)

		case PhaseFlushPending:
			flushed, resumeAfterVal := m.flushPending()
			if !flushed {
				b.expectedCur = g.Cur
				return m.finishResult(src, final, StopNeedOutput, ErrNone)
			}
			if resumeAfterVal {
				goto afterValue
			}
			continue

		case PhaseValueString:
			emitting := m.isEmitting()
			strStart := l.TokStart
			done, stop, ecode, nd, strVal, _ := m.scanStringStep(src, final, false)
			if !done {
				return m.finishResult(src, final, stop, ecode)
			}
			if m.isEventSink() {
				if uerr := m.sink.OnString(strVal); uerr != nil {
					return m.abortSink(src, final, uerr)
				}
				goto afterValue
			}
			if posMode && !g.SubModeActive {
				if !m.emitPosSpan(strStart, g.Cur) {
					return m.failAt(src, final, strStart, g.Cur, ErrSpanOverflow)
				}
				goto afterValue
			}
			if emitting && !m.tryEmitSingle(nd, PhaseValue, true) {
				continue
			}
			goto afterValue

		case PhaseKeyString:
			emitting := m.isEmitting()
			keyStart := l.TokStart
			done, stop, ecode, keyNode, keyStr, keyASCII := m.scanStringStep(src, final, true)
			if !done {
				return m.finishResult(src, final, stop, ecode)
			}
			if m.isEventSink() {
				g.Phase = PhaseObjectColon
				if uerr := m.sink.OnObjectKey(keyStr); uerr != nil {
					return m.abortSink(src, final, uerr)
				}
				continue
			}
			if posMode && !g.SubModeActive {
				if !m.emitPosSpan(keyStart, g.Cur) {
					return m.failAt(src, final, keyStart, g.Cur, ErrSpanOverflow)
				}
				g.Phase = PhaseObjectColon
				continue
			}
			if emitting {
				emitKey, updatedKey := m.applySchemaKeySelection(keyNode, keyStr, keyASCII)
				if !emitKey {
					g.Phase = PhaseObjectColon
					continue
				}
				if !m.tryEmitSingle(updatedKey, PhaseObjectColon, false) {
					continue
				}
			}
			g.Phase = PhaseObjectColon
			continue

		case PhaseValueLiteral:
			litCh := l.LitChar
			litStart := l.TokStart
			done, res, emitNd, nd := m.stepLiteral(src, final)
			if !done {
				return res
			}
			if m.isEventSink() {
				if uerr := m.emitLiteralToSink(litCh); uerr != nil {
					return m.abortSink(src, final, uerr)
				}
				goto afterValue
			}
			if posMode && !g.SubModeActive {
				if !m.emitPosSpan(litStart, g.Cur) {
					return m.failAt(src, final, litStart, g.Cur, ErrSpanOverflow)
				}
				goto afterValue
			}
			if emitNd && !m.tryEmitSingle(nd, PhaseValue, true) {
				continue
			}
			goto afterValue

		case PhaseValueNumber:
			numStart := l.TokStart
			done, res, emitNd, nd := m.stepNumber(src, final)
			if !done {
				return res
			}
			if posMode && !g.SubModeActive {
				if !m.emitPosSpan(numStart, g.Cur) {
					return m.failAt(src, final, numStart, g.Cur, ErrSpanOverflow)
				}
				goto afterValue
			}
			if emitNd && !m.tryEmitSingle(nd, PhaseValue, true) {
				continue
			}
			goto afterValue
		}

		// Relaxed skip short-circuit before running strict LexBatch over an ignored subtree.
		if g.Phase == PhaseValue && m.isRelaxedSkipValue(final) {
			var pos int
			foundInBatch := false
			if b.active {
				for {
					if b.ev != 0 {
						pos = b.batchStart + int(b.blkIdx)*64 + bits.TrailingZeros64(b.ev)
						foundInBatch = true
						break
					}
					b.blkIdx++
					if b.blkIdx >= b.nBlocks {
						g.Cur = b.batchEnd
						b.clearActive()
						break
					}
					b.ev = b.blocks[b.blkIdx].EventMask
				}
			}
			if !foundInBatch {
				var found bool
				pos, found = m.skipSpace(src, g.Cur)
				g.Cur = pos
				if !found {
					return m.failEOF(src, final)
				}
			}
			if g.ValueStart < 0 {
				g.ValueStart = pos
			}
			if g.Profile.ASTCompatLex && src[pos] == 0 {
				b.clearActive()
				return m.failAt(src, final, pos, pos+1, ErrEOF)
			}
			okSkip, failRes := m.stepRelaxedSkip(src, final, pos, src[pos])
			if !okSkip {
				b.clearActive()
				return failRes
			}
			b.seekAfterCompletedToken(src, g.Cur)
			goto afterValue
		}

		// Advance to the next lexical event bit in the current or next 64-byte block batch.
		for b.ev == 0 {
			if b.active {
				b.blkIdx++
				for b.blkIdx < b.nBlocks {
					ev := b.blocks[b.blkIdx].EventMask
					if ev != 0 {
						b.ev = ev
						break
					}
					b.blkIdx++
				}
				if b.ev != 0 {
					break
				}
				// Entire [g.Cur, b.batchEnd) range contained only verified JSON whitespace.
				if observeSpace && b.batchEnd > g.Cur {
					m.recordCompactSpace(g.Cur)
				}
				g.Cur = b.batchEnd
				b.clearActive()
			}
			if g.Cur >= len(src) {
				if g.Phase == PhaseTrailingSpace {
					if !final {
						return m.finishResult(src, final, StopNeedInput, ErrNone)
					}
					g.Phase = PhaseDone
					return m.finishResult(src, final, StopDone, ErrNone)
				}
				if !final {
					return m.finishResult(src, final, StopNeedInput, ErrNone)
				}
				return m.failEOF(src, final)
			}
			capBlk := b.effectiveCap()
			if s.HasPlan && s.RelaxedUnknownSkip && b.capOverride == 0 && capBlk > 1 {
				capBlk = 1
			} else if fromFastHandoff && !preservedHandoffWindow && b.capOverride == 0 && capBlk > 1 {
				if capBlk > handoffBatchBlocks {
					capBlk = handoffBatchBlocks
				}
				if handoffBatchBlocks < DefaultLexBatchCap {
					handoffBatchBlocks <<= 1
				}
			}
			b.carry = LexBlockCarry{}
			winBase, out := b.fillLexBatchWindowRole(src, g.Cur, b.blocks[:capBlk], lexFlags, &b.carry, b.forceScalar, b.diagRole)
			b.calls++
			b.batchStart = winBase
			b.batchEnd = winBase + int(out.BytesScanned)
			b.nBlocks = uint8(out.BlocksProduced)
			b.blkIdx = 0
			b.active = true
			b.expectedCur = g.Cur
			for b.blkIdx < b.nBlocks {
				ev := b.blocks[b.blkIdx].EventMask
				if ev != 0 {
					b.ev = ev
					break
				}
				b.blkIdx++
			}
		}

		{
			blockBase := b.batchStart + int(b.blkIdx)*64
			bit := uint(bits.TrailingZeros64(b.ev))
			tokStart := blockBase + int(bit)
			if observeSpace && tokStart > g.Cur {
				m.recordCompactSpace(g.Cur)
			}

			ch := src[tokStart]
			if ch == 0 && g.Profile.ASTCompatLex {
				b.clearActive()
				return m.failAt(src, final, tokStart, tokStart+1, ErrEOF)
			}
			act := lookupStructByteTransition(g.Phase, ch)
			if act == actError {
				ecode := structPhaseError(g.Phase)
				if g.Phase == PhaseArrayOpen {
					m.prepareArrayElemPlan()
					g.Phase = PhaseValue
				}
				if g.Phase == PhaseValue {
					m.activateValueSubMode(tokStart, ch)
					if m.isRelaxedSkipValue(final) {
						okSkip, failRes := m.stepRelaxedSkip(src, final, tokStart, ch)
						if !okSkip {
							b.clearActive()
							return failRes
						}
						b.seekAfterCompletedToken(src, g.Cur)
						goto afterValue
					}
				}
				b.clearActive()
				return m.failAt(src, final, tokStart, tokStart+1, ecode)
			}

			if g.Phase == PhaseArrayOpen && act != actCloseArr {
				m.prepareArrayElemPlan()
				g.Phase = PhaseValue
			}

			// Pop current event bit now that grammar accepted the token start.
			b.ev &= b.ev - 1

			switch act {
			case actObjKey:
				g.WasFirstKey = g.Phase == PhaseObjectOpen
				tokEnd, firstSpec, hasSpec, closed := b.resolvePlainStringInBatch(tokStart, blockBase, bit)
				if closed && !hasSpec {
					if okKey, failRes, yielded := m.consumePlainKeySpan(src, final, tokStart, tokEnd, observeSpace); !okKey {
						b.clearActive()
						return failRes
					} else if yielded {
						continue
					}
					continue
				}
				if hasSpec && src[firstSpec] != '\\' {
					b.clearActive()
					g.Phase = PhaseKeyString
					return m.failAt(src, final, firstSpec, errorConsumedAfterOff(firstSpec, g.Profile.ASTCompatLex), ErrControlChar)
				}
				l.beginString(tokStart)
				if hasSpec {
					l.StrScanOff = firstSpec
					l.StrFirstEsc = firstSpec
				} else if b.batchEnd > tokStart+1 {
					l.StrScanOff = b.batchEnd
				}
				g.Cur = tokStart + 1
				g.Phase = PhaseKeyString
				b.clearActive()
				continue

			case actObjColon:
				g.Cur = tokStart + 1
				b.expectedCur = g.Cur
				if g.SubModeActive && g.SubDisUnknown && g.SubStart < 0 {
					g.SubStart = -2
				}
				g.Phase = PhaseValue
				continue

			case actObjComma:
				g.Cur = tokStart + 1
				b.expectedCur = g.Cur
				g.Phase = PhaseObjectKey
				continue

			case actArrComma:
				g.Cur = tokStart + 1
				b.expectedCur = g.Cur
				m.prepareArrayElemPlan()
				g.Phase = PhaseValue
				continue

			case actCloseObj, actCloseArr:
				g.Cur = tokStart + 1
				b.expectedCur = g.Cur
				goto closeContainer
			}

			// Value actions:
			m.activateValueSubMode(tokStart, ch)
			if m.isRelaxedSkipValue(final) {
				okSkip, failRes := m.stepRelaxedSkip(src, final, tokStart, ch)
				if !okSkip {
					b.clearActive()
					return failRes
				}
				b.seekAfterCompletedToken(src, g.Cur)
				goto afterValue
			}
			emitting := m.isEmitting()

			switch act {
			case actValObjOpen:
				nextDepth, okDepth := g.checkNextDepth()
				if !okDepth {
					b.clearActive()
					m.Stat.MaxDepth = uint32(nextDepth)
					return m.failAt(src, final, tokStart, tokStart+1, ErrDepthOverflow)
				}
				if !CheckSourceSpan(tokStart, 1, len(src)) {
					b.clearActive()
					return m.failAt(src, final, tokStart, tokStart+1, ErrSpanOverflow)
				}
				g.Cur = tokStart + 1
				b.expectedCur = g.Cur
				if m.isEventSink() {
					evAct, uerr := m.sink.OnObjectBegin()
					if uerr != nil {
						b.clearActive()
						return m.abortSink(src, final, uerr)
					}
					if evAct == EventContainerSkip {
						if final {
							okSkip, failRes := m.stepRelaxedSkip(src, true, tokStart, '{')
							if !okSkip {
								b.clearActive()
								return failRes
							}
							b.seekAfterCompletedToken(src, g.Cur)
							if uerr := m.sink.OnObjectEnd(); uerr != nil {
								b.clearActive()
								return m.abortSink(src, true, uerr)
							}
							goto afterValue
						}
						g.SubModeActive = true
						g.SubMode = ModeValidateOnly
						g.SubFloorDepth = g.Depth
						g.SubStart = tokStart
					}
					g.pushBit(false)
					if uint32(g.Depth) > m.Stat.MaxDepth {
						m.Stat.MaxDepth = uint32(g.Depth)
					}
					g.Phase = PhaseObjectOpen
					continue
				}
				if emitting {
					var cplan *Plan
					if s.HasPlan && s.ActivePlan != nil && (s.ActivePlan.Kind == PlanStruct || s.ActivePlan.Kind == PlanMap) {
						cplan = s.ActivePlan
					}
					s.ActivePlan = nil
					nd := Node{Typ: KObject | uint64(tokStart)<<PosBits, Val: packOpenContainerVal(g.ParentNodeIdx)}
					if !m.tryEmitOpenContainer(nd, false, cplan, PhaseObjectOpen) {
						continue
					}
					continue
				}
				if posMode && !g.SubModeActive {
					if !m.openPosContainer(tokStart) {
						b.clearActive()
						return m.failAt(src, final, tokStart, tokStart+1, ErrSpanOverflow)
					}
				}
				g.pushBit(false)
				if uint32(g.Depth) > m.Stat.MaxDepth {
					m.Stat.MaxDepth = uint32(g.Depth)
				}
				g.Phase = PhaseObjectOpen
				continue

			case actValArrOpen:
				nextDepth, okDepth := g.checkNextDepth()
				if !okDepth {
					b.clearActive()
					m.Stat.MaxDepth = uint32(nextDepth)
					return m.failAt(src, final, tokStart, tokStart+1, ErrDepthOverflow)
				}
				if !CheckSourceSpan(tokStart, 1, len(src)) {
					b.clearActive()
					return m.failAt(src, final, tokStart, tokStart+1, ErrSpanOverflow)
				}
				g.Cur = tokStart + 1
				b.expectedCur = g.Cur
				if m.isEventSink() {
					evAct, uerr := m.sink.OnArrayBegin()
					if uerr != nil {
						b.clearActive()
						return m.abortSink(src, final, uerr)
					}
					if evAct == EventContainerSkip {
						if final {
							okSkip, failRes := m.stepRelaxedSkip(src, true, tokStart, '[')
							if !okSkip {
								b.clearActive()
								return failRes
							}
							b.seekAfterCompletedToken(src, g.Cur)
							if uerr := m.sink.OnArrayEnd(); uerr != nil {
								b.clearActive()
								return m.abortSink(src, true, uerr)
							}
							goto afterValue
						}
						g.SubModeActive = true
						g.SubMode = ModeValidateOnly
						g.SubFloorDepth = g.Depth
						g.SubStart = tokStart
					}
					g.pushBit(true)
					if uint32(g.Depth) > m.Stat.MaxDepth {
						m.Stat.MaxDepth = uint32(g.Depth)
					}
					g.Phase = PhaseArrayOpen
					continue
				}
				if emitting {
					var cplan *Plan
					if s.HasPlan && s.ActivePlan != nil && (s.ActivePlan.Kind == PlanSlice || s.ActivePlan.Kind == PlanBytes) {
						cplan = s.ActivePlan
					}
					s.ActivePlan = nil
					nd := Node{Typ: KArray | uint64(tokStart)<<PosBits, Val: packOpenContainerVal(g.ParentNodeIdx)}
					if !m.tryEmitOpenContainer(nd, true, cplan, PhaseArrayOpen) {
						continue
					}
					continue
				}
				if posMode && !g.SubModeActive {
					if !m.openPosContainer(tokStart) {
						b.clearActive()
						return m.failAt(src, final, tokStart, tokStart+1, ErrSpanOverflow)
					}
				}
				g.pushBit(true)
				if uint32(g.Depth) > m.Stat.MaxDepth {
					m.Stat.MaxDepth = uint32(g.Depth)
				}
				g.Phase = PhaseArrayOpen
				continue

			case actValString:
				s.ActivePlan = nil
				tokEnd, firstSpec, hasSpec, closed := b.resolvePlainStringInBatch(tokStart, blockBase, bit)
				if closed && !hasSpec {
					rawStart := tokStart + 1
					rawContentEnd := tokEnd - 1
					rawLen := rawContentEnd - rawStart
					if observeSpace {
						m.observeCompactString(src, tokStart, rawStart, rawContentEnd, g.Profile.EscapeHTMLObserve, g.Profile.ValidateUTF8)
					}
					if m.isEventSink() {
						g.Cur = tokEnd
						b.expectedCur = g.Cur
						if uerr := m.sink.OnString(src[rawStart:rawContentEnd]); uerr != nil {
							b.clearActive()
							return m.abortSink(src, final, uerr)
						}
						goto afterValue
					}
					if !emitting {
						if posMode && !g.SubModeActive && !m.emitPosSpan(tokStart, tokEnd) {
							b.clearActive()
							return m.failAt(src, final, tokStart, tokEnd, ErrSpanOverflow)
						}
						g.Cur = tokEnd
						b.expectedCur = g.Cur
						goto afterValue
					}
					if !CheckSourceSpan(rawStart, rawLen, len(src)) {
						b.clearActive()
						return m.failAt(src, final, tokStart, tokEnd, ErrSpanOverflow)
					}
					g.Cur = tokEnd
					b.expectedCur = g.Cur
					m.Stat.Str++
					nd := Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
					if !m.tryEmitSingle(nd, PhaseValue, true) {
						continue
					}
					goto afterValue
				}
				if hasSpec && src[firstSpec] != '\\' {
					b.clearActive()
					g.Phase = PhaseValueString
					return m.failAt(src, final, firstSpec, errorConsumedAfterOff(firstSpec, g.Profile.ASTCompatLex), ErrControlChar)
				}
				l.beginString(tokStart)
				if hasSpec {
					l.StrScanOff = firstSpec
					l.StrFirstEsc = firstSpec
				} else if b.batchEnd > tokStart+1 {
					l.StrScanOff = b.batchEnd
				}
				g.Cur = tokStart + 1
				g.Phase = PhaseValueString
				b.clearActive()
				continue

			case actValTrue, actValFalse, actValNull:
				s.ActivePlan = nil
				var tokEnd int
				matched := false
				switch act {
				case actValTrue:
					if tokStart+4 <= len(src) && src[tokStart+1] == 'r' && src[tokStart+2] == 'u' && src[tokStart+3] == 'e' {
						tokEnd = tokStart + 4
						matched = true
					}
				case actValFalse:
					if tokStart+5 <= len(src) && src[tokStart+1] == 'a' && src[tokStart+2] == 'l' && src[tokStart+3] == 's' && src[tokStart+4] == 'e' {
						tokEnd = tokStart + 5
						matched = true
					}
				default:
					if tokStart+4 <= len(src) && src[tokStart+1] == 'u' && src[tokStart+2] == 'l' && src[tokStart+3] == 'l' {
						tokEnd = tokStart + 4
						matched = true
					}
				}
				if !matched {
					l.TokStart = tokStart
					l.LitChar = ch
					l.LitMatched = 1
					g.Cur = tokStart + 1
					g.Phase = PhaseValueLiteral
					b.clearActive()
					continue
				}
				if emitting && !CheckSourceSpan(tokStart, tokEnd-tokStart, len(src)) {
					b.clearActive()
					return m.failAt(src, final, tokStart, tokEnd, ErrSpanOverflow)
				}
				g.Cur = tokEnd
				b.seekAfterCompletedToken(src, tokEnd)
				if m.isEventSink() {
					var uerr error
					switch act {
					case actValTrue:
						uerr = m.sink.OnBool(true)
					case actValFalse:
						uerr = m.sink.OnBool(false)
					default:
						uerr = m.sink.OnNull()
					}
					if uerr != nil {
						b.clearActive()
						return m.abortSink(src, final, uerr)
					}
					goto afterValue
				}
				if emitting {
					nkind := KNull
					if act == actValTrue {
						nkind = KTrue
					} else if act == actValFalse {
						nkind = KFalse
					}
					nd := Node{Typ: nkind | uint64(tokStart)<<PosBits}
					if !m.tryEmitSingle(nd, PhaseValue, true) {
						continue
					}
				} else if posMode && !g.SubModeActive {
					if !m.emitPosSpan(tokStart, tokEnd) {
						b.clearActive()
						return m.failAt(src, final, tokStart, tokEnd, ErrSpanOverflow)
					}
				}
				goto afterValue

			case actValNumber:
				s.ActivePlan = nil
				if final && emitting && !g.Profile.UseNumber && ch >= '1' && ch <= '9' {
					v := uint64(ch - '0')
					q := tokStart + 1
					lim := q + 18
					if lim > len(src) {
						lim = len(src)
					}
					for q < lim {
						d := src[q] - '0'
						if d > 9 {
							break
						}
						v = v*10 + uint64(d)
						q++
					}
					if (q == len(src) || !isScalarContByte[src[q]]) && CheckSourceSpan(tokStart, q-tokStart, len(src)) {
						g.Cur = q
						m.Stat.Number++
						b.seekAfterCompletedToken(src, q)
						nd := Node{Typ: KUint | uint64(tokStart)<<PosBits, Val: v}
						if !m.tryEmitSingle(nd, PhaseValue, true) {
							continue
						}
						goto afterValue
					}
				}
				if end, neg, negZero, form, hint, nbits, contAdj, fastOK := lexFastNumberSpan(src, tokStart, final); fastOK {
					if !contAdj && !emitting && !m.isEventSink() {
						if posMode && !g.SubModeActive && !m.emitPosSpan(tokStart, end) {
							b.clearActive()
							return m.failAt(src, final, tokStart, end, ErrSpanOverflow)
						}
						g.Cur = end
						b.seekAfterCompletedToken(src, end)
						goto afterValue
					}
					lexRes := LexNumberResult{
						Start:           tokStart,
						End:             end,
						Negative:        neg,
						NegZero:         negZero,
						Form:            form,
						Complete:        true,
						ContMarkerAtEnd: contAdj,
						HintKind:        hint,
						HintBits:        nbits,
					}
					okNum, failRes, emitNd, nd := m.consumeNumberToken(src, final, lexRes)
					if !okNum {
						b.clearActive()
						return failRes
					}
					b.seekAfterCompletedToken(src, g.Cur)
					if posMode && !g.SubModeActive && !m.emitPosSpan(tokStart, g.Cur) {
						b.clearActive()
						return m.failAt(src, final, tokStart, g.Cur, ErrSpanOverflow)
					}
					if emitNd && !m.tryEmitSingle(nd, PhaseValue, true) {
						continue
					}
					goto afterValue
				}
				g.Cur = tokStart
				l.beginNumber(tokStart)
				g.Phase = PhaseValueNumber
				b.clearActive()
				continue
			}
		}

	afterValue:
		if g.SubModeActive && g.Depth == g.SubFloorDepth {
			subMode := g.SubMode
			subStart := g.SubStart
			disUnk := g.SubDisUnknown
			keyNd := g.SubKeyNode
			g.SubModeActive = false
			g.SubDisUnknown = false

			if subMode == ModeRawSpan && g.RootMode == ModeEmit {
				rawLen := g.Cur - subStart
				if !CheckSourceSpan(subStart, rawLen, len(src)) {
					b.clearActive()
					return m.failAt(src, final, subStart, g.Cur, ErrSpanOverflow)
				}
				rawNd := Node{Typ: KRaw | uint64(subStart)<<PosBits, Val: uint64(rawLen)}
				if !m.tryEmitSingle(rawNd, PhaseValue, true) {
					continue
				}
			} else if disUnk && g.RootMode == ModeEmit {
				vStart := subStart
				if vStart < 0 {
					vStart = 0
				}
				nullNd := Node{Typ: KNull | uint64(vStart)<<PosBits}
				if !m.tryEmitPair(keyNd, nullNd, true) {
					continue
				}
			} else if g.RootMode == ModeEmit && g.Depth > 0 {
				g.Phase = PhaseObjectAfterVal
				if subMode == ModeValidateOnly {
					postUnknownValueCooldown = 2
					continue
				}
				if m.shouldYieldToFastEmit(src, final) {
					if !(s.HasPlan && b.hasReusableSchemaWindow(g.Cur)) {
						b.clearActive()
					}
					return ScanResult{Reason: stopYieldFast}
				}
				continue
			}
		}

		if g.Depth == 0 {
			if g.Profile.FullDocument {
				g.Phase = PhaseTrailingSpace
				continue
			}
			b.clearActive()
			g.Phase = PhaseDone
			return m.finishResult(src, final, StopDone, ErrNone)
		}

		if m.isEmitting() {
			m.Nodes[g.ParentNodeIdx].Val++
		}
		g.Phase = afterValuePhase(g.Depth, g.topIsArray(), g.Profile.FullDocument)
		if !g.SubModeActive {
			if postUnknownValueCooldown > 0 {
				postUnknownValueCooldown--
				if postUnknownValueCooldown > 0 {
					continue
				}
			}
			if m.shouldYieldToFastEmit(src, final) {
				if !(s.HasPlan && b.hasReusableSchemaWindow(g.Cur)) {
					b.clearActive()
				}
				return ScanResult{Reason: stopYieldFast}
			}
		}
		continue

	closeContainer:
		isArr := g.topIsArray()
		if uint32(g.Depth) > m.Stat.MaxDepth {
			m.Stat.MaxDepth = uint32(g.Depth)
		}
		g.Depth--
		if m.isEmitting() {
			m.closeEmittedContainer(isArr)
		} else if posMode && !g.SubModeActive {
			if !m.closePosContainer(g.Cur) {
				b.clearActive()
				return m.failAt(src, final, g.Cur-1, g.Cur, ErrSpanOverflow)
			}
		} else if g.RootMode == ModeEventSink && m.sink != nil && (!g.SubModeActive || g.Depth == g.SubFloorDepth) {
			if g.SubModeActive {
				g.SubModeActive = false
				g.SubDisUnknown = false
			}
			var uerr error
			if isArr {
				uerr = m.sink.OnArrayEnd()
			} else {
				uerr = m.sink.OnObjectEnd()
			}
			if uerr != nil {
				b.clearActive()
				return m.abortSink(src, final, uerr)
			}
		}
		goto afterValue
	}
}

// resolvePlainStringInBatch inspects the active block batch starting from the
// opening '"' at (blkIdx, bit) = tokStart. If the string closes within the
// current batch with zero '\\' or raw control bytes, it advances (b.blkIdx, b.ev)
// past the closing '"' and returns (tokEnd, -1, false, true). Otherwise it
// reports the earliest InsideSpecial offset (if any) without mutating b.
func (b *blockBatchState) resolvePlainStringInBatch(tokStart, blockBase int, bit uint) (tokEnd, firstSpec int, hasSpec, closed bool) {
	blk := b.blocks[b.blkIdx]
	cq := blk.CloseQuote >> bit
	if cq != 0 {
		relClose := uint(bits.TrailingZeros64(cq))
		closeBit := bit + relClose
		between := blk.InsideSpecial & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
		if between != 0 {
			return tokStart + int(relClose) + 1, blockBase + bits.TrailingZeros64(between), true, true
		}
		return tokStart + int(relClose) + 1, -1, false, true
	}

	firstSpec = -1
	if bit < 63 {
		tailSpec := blk.InsideSpecial &^ ((uint64(2) << bit) - 1)
		if tailSpec != 0 {
			hasSpec = true
			firstSpec = blockBase + bits.TrailingZeros64(tailSpec)
		}
	}
	for nb := int(b.blkIdx) + 1; nb < int(b.nBlocks); nb++ {
		nbBlk := b.blocks[nb]
		if nbBlk.CloseQuote != 0 {
			closeBit := uint(bits.TrailingZeros64(nbBlk.CloseQuote))
			closeAbs := b.batchStart + nb*64 + int(closeBit)
			if !hasSpec {
				prefSpec := nbBlk.InsideSpecial & ((uint64(1) << closeBit) - 1)
				if prefSpec != 0 {
					hasSpec = true
					firstSpec = b.batchStart + nb*64 + bits.TrailingZeros64(prefSpec)
				}
			}
			if !hasSpec {
				b.blkIdx = uint8(nb)
				if closeBit == 63 {
					b.ev = 0
				} else {
					b.ev = nbBlk.EventMask &^ ((uint64(2) << closeBit) - 1)
				}
			}
			return closeAbs + 1, firstSpec, hasSpec, true
		}
		if !hasSpec && nbBlk.InsideSpecial != 0 {
			hasSpec = true
			firstSpec = b.batchStart + nb*64 + bits.TrailingZeros64(nbBlk.InsideSpecial)
		}
	}
	return 0, firstSpec, hasSpec, false
}

func (m *Machine) consumePlainKeySpan(src string, final bool, tokStart, tokEnd int, observeSpace bool) (ok bool, failRes ScanResult, yielded bool) {
	g := &m.Grammar
	b := &m.batch
	rawStart := tokStart + 1
	rawContentEnd := tokEnd - 1
	rawLen := rawContentEnd - rawStart
	if observeSpace {
		m.observeCompactString(src, tokStart, rawStart, rawContentEnd, g.Profile.EscapeHTMLObserve, g.Profile.ValidateUTF8)
	}
	if m.isEventSink() {
		keyStr := src[rawStart:rawContentEnd]
		g.Cur = tokEnd
		b.expectedCur = g.Cur
		g.Phase = PhaseObjectColon
		if uerr := m.sink.OnObjectKey(keyStr); uerr != nil {
			return false, m.abortSink(src, final, uerr), false
		}
		return true, ScanResult{}, false
	}
	if !m.isEmitting() {
		if m.isPositionSpan() && !m.emitPosSpan(tokStart, tokEnd) {
			return false, m.failAt(src, final, tokStart, tokEnd, ErrSpanOverflow), false
		}
		g.Cur = tokEnd
		b.expectedCur = g.Cur
		g.Phase = PhaseObjectColon
		return true, ScanResult{}, false
	}
	if !CheckSourceSpan(rawStart, rawLen, len(src)) {
		return false, m.failAt(src, final, tokStart, tokEnd, ErrSpanOverflow), false
	}
	keyNode := Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
	emitKey := true
	updatedKey := keyNode
	if m.Schema.HasPlan && m.Schema.PlanDepth > 0 {
		keyStr := src[rawStart:rawContentEnd]
		emitKey, updatedKey = m.applySchemaKeySelection(keyNode, keyStr, false)
	}
	g.Cur = tokEnd
	b.expectedCur = g.Cur
	g.Phase = PhaseObjectColon
	if emitKey && !m.tryEmitSingle(updatedKey, PhaseObjectColon, false) {
		return true, ScanResult{}, true
	}
	if b.ev != 0 && !observeSpace && !(g.SubModeActive && g.SubDisUnknown) {
		cPos := b.batchStart + int(b.blkIdx)*64 + bits.TrailingZeros64(b.ev)
		if uint(cPos) < uint(len(src)) && lookupStructByteTransition(PhaseObjectColon, src[cPos]) == actObjColon {
			b.ev &= b.ev - 1
			g.Cur = cPos + 1
			b.expectedCur = g.Cur
			g.Phase = PhaseValue
		}
	}
	return true, ScanResult{}, false
}

// stepUnbatched exercises the zero-batch-capacity fallback (SetBatchCapForTest(0))
// by stepping through LexBatch with a single 64-byte block slot so zero duplicate
// grammar rules exist anywhere in jsonscan.
func (m *Machine) stepUnbatched(src string, final bool) ScanResult {
	saved := m.batch.capOverride
	m.batch.capOverride = 1
	res := m.Step(src, final)
	m.batch.capOverride = saved
	return res
}

func (m *Machine) stepNumber(src string, final bool) (done bool, res ScanResult, emitNode bool, nd Node) {
	lexRes := lexNumber(&m.Lex, src, final)
	if lexRes.NeedMore {
		return false, m.finishResult(src, final, StopNeedInput, ErrNone), false, Node{}
	}
	if !lexRes.Complete {
		return false, m.failNumberLex(src, final, lexRes.Fault), false, Node{}
	}
	return m.consumeNumberToken(src, final, lexRes)
}
