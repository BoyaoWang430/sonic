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

import "math"

const (
	// StrictMaxContainerDepth is the container nesting ceiling for strict Sonic
	// Decode, Validate, and strict AST validation profiles.
	StrictMaxContainerDepth = 4096

	// CompactMaxContainerDepth is the container nesting ceiling preserved for
	// Compact / AppendCompactedJSON profiles.
	CompactMaxContainerDepth = 10000

	// maxBitStackWords holds 160 * 64 = 10240 container bits (0 = object, 1 = array),
	// covering both 4096 and 10000 profiles with zero heap allocations.
	maxBitStackWords = 160
)

// Tape node kind and bit-packing constants (exact bit-for-bit match with optdec tape nodes).
const (
	TypeBits   = 8
	TypeMask   = 0xFF
	PosBits    = 32
	ConLenBits = 32

	// BasicType: low 3 bits
	KNull   uint64 = 0 // xxxxx000
	KBool   uint64 = 2 // xxxxx010
	KNumber uint64 = 3 // xxxxx011
	KString uint64 = 4 // xxxxx100
	KRaw    uint64 = 5 // xxxxx101
	KObject uint64 = 6 // xxxxx110
	KArray  uint64 = 7 // xxxxx111

	// SubType: bits 3..4
	KFalse         uint64 = (0 << 3) | KBool   // 2
	KTrue          uint64 = (1 << 3) | KBool   // 10
	KUint          uint64 = (0 << 3) | KNumber // 3
	KSint          uint64 = (1 << 3) | KNumber // 11
	KReal          uint64 = (2 << 3) | KNumber // 19
	KRawNumber     uint64 = (3 << 3) | KNumber // 27
	KStringCommon  uint64 = KString            // 4
	KStringEscaped uint64 = (1 << 3) | KString // 12

	EscapedInUnescFlag uint64 = 1 << 63
	MaxUnescOffset     int    = (1 << 31) - 1
	MaxUnescLen        int    = math.MaxInt32
	MaxSourceOffset    int    = math.MaxInt32
)

// PackEscapedSpan encodes an unescaped string slice [uoff : uoff+ulen] stored in
// the side unesc arena into a 64-bit node payload, checking bounds explicitly so
// offsets never collide with EscapedInUnescFlag or silently truncate.
func PackEscapedSpan(uoff, ulen int) (uint64, bool) {
	if uoff < 0 || uoff > MaxUnescOffset || ulen < 0 || ulen > MaxUnescLen {
		return 0, false
	}
	return EscapedInUnescFlag | (uint64(uint32(uoff)) << 32) | uint64(uint32(ulen)), true
}

// UnpackEscapedSpan decodes a packed escaped string payload produced by PackEscapedSpan.
func UnpackEscapedSpan(packed uint64) (uoff, ulen int, inUnesc bool) {
	if (packed & EscapedInUnescFlag) == 0 {
		return 0, int(packed), false
	}
	v := packed &^ EscapedInUnescFlag
	return int(uint32(v >> 32)), int(uint32(v)), true
}

// CheckSourceSpan verifies that [pos, pos+length] fits within non-overflowing
// compact 32-bit node position coordinates and len(src) bounds.
func CheckSourceSpan(pos, length, srcLen int) bool {
	if pos < 0 || length < 0 || pos > MaxSourceOffset || length > srcLen || pos > srcLen-length {
		return false
	}
	return true
}

// OutputMode selects how the shared grammar driver publishes validated JSON tokens
// without duplicating container or token syntax rules.
type OutputMode uint8

const (
	ModeEmit OutputMode = iota
	ModeRawSpan
	ModeValidateOnly
	ModeCompactObserve
	ModeRelaxedSkip
	ModeEventSink
	ModePositionSpan
)

// StopReason classifies why a bounded scan step returned to the caller.
type StopReason uint8

const (
	StopDone StopReason = iota
	StopNeedInput
	StopNeedOutput
	StopLexicalEvent
	StopUserAbort
	StopError

	// Internal Step-loop dispatch sentinels (never returned outside Machine.Step).
	stopHandoffGeneral StopReason = 254
	stopYieldFast      StopReason = 255
)

// ScanError is a neutral lexical/structural error code independent of public
// decoder, encoder, or AST error types.
type ScanError uint8

const (
	ErrNone ScanError = iota
	ErrControlChar
	ErrInvalidEscape
	ErrInvalidNumber
	ErrFloatInf
	ErrEOF
	ErrInvalidChar
	ErrExpectKey
	ErrExpectColon
	ErrExpectObjCommaOrEnd
	ErrExpectArrCommaOrEnd
	ErrInvalidEscapeUTF
	ErrInvalidLiteral
	ErrDepthOverflow
	ErrSpanOverflow
)

// Profile defines an immutable grammar & resource policy over the shared JSON
// state machine, keeping control-char, escape-syntax, UTF-8, lone-surrogate,
// number, and document-boundary dimensions orthogonal.
type Profile struct {
	MaxContainerDepth     int
	BaseDepth             int
	RejectControl         bool
	ValidateEscapes       bool
	ValidateUTF8          bool
	RejectLoneSurrogate   bool
	UseNumber             bool
	DeferFloatOverflow    bool
	FullDocument          bool
	EscapeHTMLObserve     bool
	CompactFirstDirtyOnly bool
	ASTCompatLex          bool
}

// StrictProfile returns the canonical 4096-depth profile for Sonic Decode
// and strict Schema/RawSpan/ValidateOnly subtrees.
func StrictProfile(rejectControl, validateUTF8, rejectLoneSurrogate, useNumber, deferFloatOverflow, fullDoc bool) Profile {
	return Profile{
		MaxContainerDepth:   StrictMaxContainerDepth,
		RejectControl:       rejectControl,
		ValidateEscapes:     true,
		ValidateUTF8:        validateUTF8,
		RejectLoneSurrogate: rejectLoneSurrogate,
		UseNumber:           useNumber,
		DeferFloatOverflow:  deferFloatOverflow,
		FullDocument:        fullDoc,
	}
}

// ASTValidateProfile returns the canonical 4096-depth single-value prefix
// profile for ValidateOne / ValidateOneAtDepth and strict AST validation.
func ASTValidateProfile(validateStrings bool, baseDepth int) Profile {
	return Profile{
		MaxContainerDepth: StrictMaxContainerDepth,
		BaseDepth:         baseDepth,
		RejectControl:     validateStrings,
		ValidateEscapes:   validateStrings,
		ASTCompatLex:      true,
	}
}

// ASTEventProfile returns the canonical 4096-depth single-value prefix profile
// for eager AST event consumers (Loads, LoadsUseNumber, Node.rawInterface,
// Preorder) without duplicating container syntax or allocating an OptDec tape.
func ASTEventProfile(useNumber bool) Profile {
	return Profile{
		MaxContainerDepth: StrictMaxContainerDepth,
		RejectControl:     false,
		ValidateEscapes:   true,
		UseNumber:         useNumber,
		ASTCompatLex:      true,
	}
}

// CompactProfile returns the canonical 10000-depth full-document profile for
// full CompactObservation reporting (HasSpace, NeedsRewrite, and FirstDirtyPos
// are all observed independently across the whole document).
func CompactProfile(escapeHTML, validateUTF8 bool) Profile {
	return Profile{
		MaxContainerDepth: CompactMaxContainerDepth,
		RejectControl:     true,
		ValidateEscapes:   true,
		ValidateUTF8:      validateUTF8,
		FullDocument:      true,
		EscapeHTMLObserve: escapeHTML,
	}
}

// CompactFirstDirtyProfile returns the 10000-depth full-document profile used by
// AppendCompactedJSON, which always applies full sanitize flags from FirstDirtyPos
// onward. Full JSON grammar, escape, and control-character validation still runs
// to EOF, while subsequent HTML/UTF-8 rewrite-need observation is skipped once
// FirstDirtyPos >= 0. Callers must test FirstDirtyPos < 0 for cleanliness rather
// than assuming !NeedsRewrite implies no HTML/UTF-8 bytes after FirstDirtyPos.
func CompactFirstDirtyProfile(escapeHTML, validateUTF8 bool) Profile {
	p := CompactProfile(escapeHTML, validateUTF8)
	p.CompactFirstDirtyOnly = true
	return p
}

// Node is a compact 16-byte tape entry emitted in ModeEmit / ModeRawSpan.
type Node struct {
	Typ uint64
	Val uint64
}

// Stat records structural counts accumulated during ModeEmit.
type Stat struct {
	Object     uint32
	Array      uint32
	Str        uint32
	Number     uint32
	ArrayElems uint32
	ObjectKeys uint32
	MaxDepth   uint32
}

// CompactObservation reports single-pass structural + lexical observations for Compact.
type CompactObservation struct {
	HasSpace      bool
	NeedsRewrite  bool
	FirstDirtyPos int
}

// PlanKind classifies a static consumption plan node for Schema projection.
type PlanKind uint8

const (
	PlanFull PlanKind = iota
	PlanScalar
	PlanBytes
	PlanRaw
	PlanStruct
	PlanSlice
	PlanMap
)

// FieldMatcher resolves JSON object keys against a compiled struct schema without
// coupling internal/jsonscan to decoder types or reflection.
type FieldMatcher interface {
	MatchField(key string, caseSensitive, keyASCII bool) int
	FieldName(idx int) string
	FieldCount() int
}

// Plan describes what subtrees a Schema consumer wants materialized vs validated
// in place; container grammar transitions remain 100% owned by Machine.
type Plan struct {
	Kind        PlanKind
	TypedNumber bool // PlanScalar leaf targets a concrete typed Go primitive (not json.Number/any), so ModeEmit may parse KUint/KSint/KReal even under UseNumber.
	Struct      FieldMatcher
	FieldNames  []string
	Elem        *Plan
	Fields      []*Plan
}

// ScanResult summarizes a completed or yielded Machine.Step invocation.
type ScanResult struct {
	Consumed    int
	Produced    int
	ValueStart  int
	Reason      StopReason
	ErrorOffset int
	Code        ScanError
}
