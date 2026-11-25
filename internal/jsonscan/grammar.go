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

// GrammarPhase identifies the structural JSON grammar state.
type GrammarPhase uint8

const (
	PhaseValue GrammarPhase = iota
	PhaseObjectOpen
	PhaseObjectKey
	PhaseObjectColon
	PhaseObjectAfterVal
	PhaseArrayOpen
	PhaseArrayAfterVal
	PhaseValueString
	PhaseKeyString
	PhaseValueNumber
	PhaseValueLiteral
	PhaseFlushPending
	PhaseTrailingSpace
	PhaseDone
)

// structAction encodes the canonical structural transition action for a
// (GrammarPhase, TokenKind) pair. Both the full-buffer plain ModeEmit consumer
// and the general resumable Machine.Step driver dispatch exclusively from
// structTransitionTable and structPhaseErrorTable so container syntax rules
// exist in a single Go source of truth.
type structAction uint8

const (
	actError structAction = iota
	actValObjOpen
	actValArrOpen
	actValString
	actValNumber
	actValTrue
	actValFalse
	actValNull
	actObjKey
	actObjColon
	actObjComma
	actArrComma
	actCloseObj
	actCloseArr
)

// structTransitionTable maps [GrammarPhase & 15][TokenKind & 15] -> structAction.
// Fits in 256 bytes (4 cache lines) with zero heap allocations and bounds-check-free indexing.
var structTransitionTable = [16][16]structAction{
	PhaseValue: {
		TokLBrace: actValObjOpen,
		TokLBrack: actValArrOpen,
		TokString: actValString,
		TokNumber: actValNumber,
		TokTrue:   actValTrue,
		TokFalse:  actValFalse,
		TokNull:   actValNull,
	},
	PhaseObjectOpen: {
		TokRBrace: actCloseObj,
		TokString: actObjKey,
	},
	PhaseObjectKey: {
		TokString: actObjKey,
	},
	PhaseObjectColon: {
		TokColon: actObjColon,
	},
	PhaseObjectAfterVal: {
		TokComma:  actObjComma,
		TokRBrace: actCloseObj,
	},
	PhaseArrayOpen: {
		TokRBrack: actCloseArr,
		TokLBrace: actValObjOpen,
		TokLBrack: actValArrOpen,
		TokString: actValString,
		TokNumber: actValNumber,
		TokTrue:   actValTrue,
		TokFalse:  actValFalse,
		TokNull:   actValNull,
	},
	PhaseArrayAfterVal: {
		TokComma:  actArrComma,
		TokRBrack: actCloseArr,
	},
	// PhaseTrailingSpace (and active-token / terminal phases) map all tokens to actError.
}

// structPhaseErrorTable maps an unexpected token in [GrammarPhase & 15] to the
// canonical structural ScanError before any token-internal validation runs.
var structPhaseErrorTable = [16]ScanError{
	PhaseValue:          ErrInvalidChar,
	PhaseObjectOpen:     ErrExpectKey,
	PhaseObjectKey:      ErrExpectKey,
	PhaseObjectColon:    ErrExpectColon,
	PhaseObjectAfterVal: ErrExpectObjCommaOrEnd,
	PhaseArrayOpen:      ErrInvalidChar,
	PhaseArrayAfterVal:  ErrExpectArrCommaOrEnd,
	PhaseTrailingSpace:  ErrInvalidChar,
}

var byteStartTokenKind = [256]TokenKind{
	'{': TokLBrace,
	'}': TokRBrace,
	'[': TokLBrack,
	']': TokRBrack,
	':': TokColon,
	',': TokComma,
	'"': TokString,
	'-': TokNumber,
	'0': TokNumber,
	'1': TokNumber,
	'2': TokNumber,
	'3': TokNumber,
	'4': TokNumber,
	'5': TokNumber,
	'6': TokNumber,
	'7': TokNumber,
	'8': TokNumber,
	'9': TokNumber,
	't': TokTrue,
	'f': TokFalse,
	'n': TokNull,
}

// structByteTransitionTable is mechanically derived at init time from the single
// canonical structTransitionTable and byteStartTokenKind so callers can resolve
// (phase, firstByte) -> structAction in a single bounds-check-free load.
var structByteTransitionTable = func() [16][256]structAction {
	var t [16][256]structAction
	for ph := 0; ph < 16; ph++ {
		for ch := 0; ch < 256; ch++ {
			t[ph][ch] = structTransitionTable[ph][byteStartTokenKind[ch]&15]
		}
	}
	return t
}()

func lookupStructTransition(phase GrammarPhase, kind TokenKind) structAction {
	return structTransitionTable[phase&15][kind&15]
}

func lookupStructByteTransition(phase GrammarPhase, ch byte) structAction {
	return structByteTransitionTable[phase&15][ch]
}

// uniqueTransitionByte mechanically inspects structByteTransitionTable[phase]
// and returns the unique byte (0..255) that triggers want, or -1 if zero or
// multiple bytes map to want. Fast consumers use these derived witnesses to
// test single-byte structural delimiters without hardcoding grammar bytes;
// if structTransitionTable ever changes, a -1 witness automatically disables
// the single-byte shortcut and falls back to lookupStructByteTransition.
func uniqueTransitionByte(phase GrammarPhase, want structAction) int {
	found := -1
	row := &structByteTransitionTable[phase&15]
	for ch := 0; ch < 256; ch++ {
		if row[ch] == want {
			if found >= 0 {
				return -1
			}
			found = ch
		}
	}
	return found
}

var (
	uniqueObjKeyOpenByte = uniqueTransitionByte(PhaseObjectOpen, actObjKey)
	uniqueObjKeyNextByte = uniqueTransitionByte(PhaseObjectKey, actObjKey)
	uniqueObjColonByte   = uniqueTransitionByte(PhaseObjectColon, actObjColon)
	uniqueObjCommaByte   = uniqueTransitionByte(PhaseObjectAfterVal, actObjComma)
	uniqueObjCloseByte   = uniqueTransitionByte(PhaseObjectAfterVal, actCloseObj)
	uniqueArrCommaByte   = uniqueTransitionByte(PhaseArrayAfterVal, actArrComma)
	uniqueArrCloseByte   = uniqueTransitionByte(PhaseArrayAfterVal, actCloseArr)
)

func structPhaseError(phase GrammarPhase) ScanError {
	return structPhaseErrorTable[phase&15]
}

// afterValuePhase returns the next structural GrammarPhase after a value completes
// at container nesting depth `depth`.
func afterValuePhase(depth int, topIsArray bool, fullDoc bool) GrammarPhase {
	if depth == 0 {
		if fullDoc {
			return PhaseTrailingSpace
		}
		return PhaseDone
	}
	if topIsArray {
		return PhaseArrayAfterVal
	}
	return PhaseObjectAfterVal
}

// GrammarState owns container frames, structural phase, depth policy, and
// subtree output-mode transitions (decoupled from Schema plan frames and
// Tape pending-node mechanics).
type GrammarState struct {
	Profile       Profile
	RootMode      OutputMode
	Phase         GrammarPhase
	Cur           int
	ValueStart    int
	Depth         int
	ParentNodeIdx int
	ErrOffset     int

	// BitStack stores 1 bit per open container (0 = object '{', 1 = array '['),
	// supporting both 4096 and 10000 profiles with zero heap allocations.
	BitStack    [maxBitStackWords]uint64
	WasFirstKey bool

	// Non-emitting subtree overlay (ValidateOnly, RawSpan, RelaxedSkip).
	SubModeActive bool
	SubMode       OutputMode
	SubFloorDepth int
	SubStart      int
	SubDisUnknown bool
	SubKeyNode    Node
}

func (g *GrammarState) reset(profile Profile, mode OutputMode) {
	g.Profile = profile
	g.RootMode = mode
	g.Phase = PhaseValue
	g.Cur = 0
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
}

func (g *GrammarState) pushBit(isArray bool) {
	idx := uint(g.Depth)
	word := idx >> 6
	bit := uint64(1) << (idx & 63)
	if isArray {
		g.BitStack[word] |= bit
	} else {
		g.BitStack[word] &^= bit
	}
	g.Depth++
}

func (g *GrammarState) topIsArray() bool {
	idx := uint(g.Depth - 1)
	return (g.BitStack[idx>>6] & (uint64(1) << (idx & 63))) != 0
}

func (g *GrammarState) checkNextDepth() (int, bool) {
	nextDepth := g.Profile.BaseDepth + g.Depth + 1
	return nextDepth, nextDepth <= g.Profile.MaxContainerDepth
}
