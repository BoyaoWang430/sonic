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
	"errors"
	"math"
)

var (
	errInvalidValidatedDoc      = errors.New("sonic/jsonscan: ValidatedDoc not initialized via barrier")
	errInvalidValidatedSpanArgs = errors.New("sonic/jsonscan: invalid ValidatedDoc source bounds for span sidecar")
	errValidatedSpanInvariant   = errors.New("sonic/jsonscan: ValidatedSourceSpan count or cursor invariant mismatch")
)

// ValidatedSourceSpan records the exact raw [RawStart, RawEnd) byte offsets in
// ValidatedDoc.Src() for one logical entry in ValidatedDoc.Entries().
// For JSON strings, RawStart points to the opening '"' and RawEnd points one byte
// past the closing '"'; EntryPosition returns RawStart+1 for strings (matching
// Node.Position() content-start semantics) and RawStart for all other kinds.
type ValidatedSourceSpan struct {
	RawStart uint32
	RawEnd   uint32
}

// EntryPosition returns the byte offset matching legacy Node.Position() for an
// entry with low-4-bit kind entryKind.
func (s ValidatedSourceSpan) EntryPosition(entryKind uint64) int {
	if entryKind == IdxStringPlain || entryKind == IdxStringEsc {
		return int(s.RawStart) + 1
	}
	return int(s.RawStart)
}

func (m *Machine) isPositionSpan() bool {
	return m.Grammar.RootMode == ModePositionSpan && !m.Grammar.SubModeActive
}

func (m *Machine) emitPosSpan(rawStart, rawEnd int) bool {
	if rawStart < 0 || rawEnd < rawStart || uint64(rawEnd) > math.MaxUint32 || m.posLen >= len(m.posSpans) {
		return false
	}
	m.posSpans[m.posLen] = ValidatedSourceSpan{
		RawStart: uint32(rawStart),
		RawEnd:   uint32(rawEnd),
	}
	m.posLen++
	return true
}

func (m *Machine) openPosContainer(tokStart int) bool {
	if tokStart < 0 || uint64(tokStart) > math.MaxUint32 || m.posLen >= len(m.posSpans) {
		return false
	}
	idx := m.posLen
	parentPlus1 := uint32(m.Grammar.ParentNodeIdx + 1)
	m.posSpans[idx] = ValidatedSourceSpan{
		RawStart: uint32(tokStart),
		RawEnd:   parentPlus1,
	}
	m.Grammar.ParentNodeIdx = idx
	m.posLen = idx + 1
	return true
}

func (m *Machine) closePosContainer(closeEnd int) bool {
	pIdx := m.Grammar.ParentNodeIdx
	if pIdx < 0 || pIdx >= m.posLen || closeEnd < 0 || uint64(closeEnd) > math.MaxUint32 {
		return false
	}
	parentPlus1 := m.posSpans[pIdx].RawEnd
	if int(closeEnd) <= int(m.posSpans[pIdx].RawStart) {
		return false
	}
	m.posSpans[pIdx].RawEnd = uint32(closeEnd)
	m.Grammar.ParentNodeIdx = int(parentPlus1) - 1
	return true
}

// BuildValidatedSourceSpans constructs the request-scoped 8B/entry raw-span
// sidecar for a verified ValidatedDoc using the single shared jsonscan grammar
// and lexical rules with zero Unesc output and zero premature float conversions
// .
// Normal hot-path decodes never invoke this function; when invoked on dynamic
// fallback or cold error paths, callers build it at most once per top-level value.
func BuildValidatedSourceSpans(
	d ValidatedDoc,
	startOffset int,
	rejectControl bool,
	rejectLoneSurrogate bool,
	forceScalar bool,
	reuse []ValidatedSourceSpan,
) ([]ValidatedSourceSpan, error) {
	spans, _, err := BuildValidatedSourceSpansObserved(d, startOffset, rejectControl, rejectLoneSurrogate, forceScalar, reuse)
	return spans, err
}

// BuildValidatedSourceSpansObserved is like BuildValidatedSourceSpans and also
// returns the actual LexProducerMask observed during sidecar construction.
func BuildValidatedSourceSpansObserved(
	d ValidatedDoc,
	startOffset int,
	rejectControl bool,
	rejectLoneSurrogate bool,
	forceScalar bool,
	reuse []ValidatedSourceSpan,
) ([]ValidatedSourceSpan, LexProducerMask, error) {
	if !d.validated {
		return nil, 0, errInvalidValidatedDoc
	}
	entries := d.entries
	want := len(entries)
	if want == 0 {
		return nil, 0, errInvalidValidatedDoc
	}
	if startOffset < 0 || d.consumed < startOffset || d.consumed > len(d.src) || d.consumed > MaxValidatedDocBytes {
		return nil, 0, errInvalidValidatedSpanArgs
	}

	var spans []ValidatedSourceSpan
	if cap(reuse) >= want {
		spans = reuse[:want]
	} else {
		spans = make([]ValidatedSourceSpan, want)
	}

	var m Machine
	prof := Profile{
		MaxContainerDepth:   StrictMaxContainerDepth,
		BaseDepth:           0,
		RejectControl:       rejectControl,
		ValidateEscapes:     true,
		ValidateUTF8:        false,
		RejectLoneSurrogate: rejectLoneSurrogate,
		UseNumber:           false,
		DeferFloatOverflow:  true,
		FullDocument:        false,
	}
	m.Reset(prof, ModePositionSpan)
	m.Grammar.Cur = startOffset
	m.SetDiagStartPos(startOffset)
	m.SetDiagRole(DiagRolePosition)
	m.SetForceScalar(forceScalar)
	m.posSpans = spans
	m.posLen = 0

	boundedSrc := d.src[:d.consumed]
	res := m.Step(boundedSrc, true)
	producers := m.ObservedLexProducers()
	m.posSpans = nil
	if res.Reason != StopDone || m.posLen != want || m.Grammar.Cur != d.consumed || m.Grammar.ParentNodeIdx != -1 {
		return nil, producers, errValidatedSpanInvariant
	}
	diagRecordPositionBuild(want)
	return spans, producers, nil
}
