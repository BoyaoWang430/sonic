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

import "github.com/bytedance/sonic/internal/native/types"

// error_adapter.go centralizes public ScanResult construction and profile-specific
// error/cursor coordinate adjustments on the cold error/return boundary.

func (m *Machine) SetDiagRole(role uint8) {
	m.batch.diagRole = role
}

func (m *Machine) SetDiagStartPos(startPos int) {
	m.diag.setStartPos(startPos)
}

func (m *Machine) finishResult(src string, final bool, reason StopReason, code ScanError) ScanResult {
	if reason == StopDone || reason == StopError {
		m.diag.recordTapeComplete(m.Grammar.Cur, m.NodeLen, len(m.Unesc), m.batch.diagRole)
	}
	switch reason {
	case StopNeedInput:
		limit := m.Grammar.Cur
		if m.Lex.StrScanOff > limit {
			limit = m.Lex.StrScanOff
		}
		if m.Lex.NumScanOff > limit {
			limit = m.Lex.NumScanOff
		}
		m.checkConsumedUTF8(src, limit, false)
	default:
		m.checkConsumedUTF8(src, m.Grammar.Cur, final)
	}
	errOff := m.Grammar.ErrOffset
	if reason == StopError && errOff < 0 {
		if code == ErrEOF {
			errOff = len(src)
		} else if m.Grammar.Cur > 0 {
			errOff = m.Grammar.Cur - 1
		} else {
			errOff = 0
		}
	}
	return ScanResult{
		Consumed:    m.Grammar.Cur,
		Produced:    m.NodeLen,
		ValueStart:  m.Grammar.ValueStart,
		Reason:      reason,
		ErrorOffset: errOff,
		Code:        code,
	}
}

func (m *Machine) failEOF(src string, final bool) ScanResult {
	m.Grammar.Cur = len(src)
	m.Grammar.ErrOffset = len(src)
	return m.finishResult(src, final, StopError, ErrEOF)
}

func (m *Machine) failAt(src string, final bool, errOff, nextCur int, code ScanError) ScanResult {
	if nextCur > len(src) {
		nextCur = len(src)
	}
	m.Grammar.Cur = nextCur
	m.Grammar.ErrOffset = errOff
	return m.finishResult(src, final, StopError, code)
}

func errorConsumedAfterOff(errOff int, astCompatLex bool) int {
	if astCompatLex {
		return errOff
	}
	return errOff + 1
}

// adaptNumberFaultOffset maps neutral NumberFaultFact coordinates to public
// error offsets without rescanning the input.
func adaptNumberFaultOffset(src string, fault NumberFaultFact, legacyUseNumberPos bool) int {
	if !legacyUseNumberPos {
		return fault.FaultPos
	}
	if !fault.AtEOF && uint(fault.FaultPos) < uint(len(src)) && isNumberContByte(src[fault.FaultPos]) {
		return fault.FaultPos
	}
	switch {
	case fault.SignPos >= 0 && fault.FaultPos == fault.SignPos+1:
		return fault.SignPos
	case fault.ExpPos >= 0 && fault.FaultPos == fault.ExpPos+1:
		return fault.ExpPos
	case fault.DotPos >= 0 && fault.FaultPos == fault.DotPos+1:
		return fault.DotPos
	default:
		return fault.FaultPos
	}
}

func (m *Machine) failNumberLex(src string, final bool, fault NumberFaultFact) ScanResult {
	prof := m.Grammar.Profile
	if prof.ASTCompatLex && final {
		end, _ := kernScanNumberStrict(src, fault.TokenStart)
		return m.failAt(src, final, end, end, ErrInvalidNumber)
	}
	errOff := adaptNumberFaultOffset(src, fault, prof.UseNumber)
	return m.failAt(src, final, errOff, errorConsumedAfterOff(errOff, prof.ASTCompatLex), ErrInvalidNumber)
}

func (m *Machine) checkNumberContBoundary(src string, final bool, lexRes LexNumberResult) (bool, ScanResult) {
	if !lexRes.ContMarkerAtEnd {
		return true, ScanResult{}
	}
	prof := m.Grammar.Profile
	if prof.ASTCompatLex {
		end, _ := kernScanNumberStrict(src, lexRes.Start)
		return false, m.failAt(src, final, end, end, ErrInvalidNumber)
	}
	if prof.UseNumber {
		return false, m.failAt(src, final, lexRes.End, lexRes.End+1, ErrInvalidNumber)
	}
	return true, ScanResult{}
}

func (m *Machine) failFloatOverflow(src string, final bool, start, end int) ScanResult {
	errAt := end
	if errAt > start {
		errAt--
	}
	return m.failAt(src, final, errAt, end, ErrFloatInf)
}

// NormalizeUnquoteError maps kernUnquoteRaw's neutral (rawErrPos, perr) relative
// to escTail into Sonic's public backslash-relative offset and ScanError:
//   - ERR_INVALID_ESCAPE ('\g'): rawErrPos == backslash+1 across scalar and all ISAs -> O(1) rawErrPos-1.
//   - ERR_INVALID_CHAR ('\u12x4'): rawErrPos == backslash+2+badHexIdx across scalar and all ISAs -> O(1) 4-byte window.
//   - ERR_INVALID_UNICODE / ERR_EOF (lone surrogate back-pointing or truncated '\u' tail):
//     normalized through the single authoritative StringValidateState up to rawErrPos+4.
func NormalizeUnquoteError(escTail string, rawErrPos int, perr types.ParsingError, rejectLoneSurrogate bool) (backslashOff int, code ScanError) {
	switch perr {
	case types.ERR_INVALID_ESCAPE:
		if rawErrPos > 0 && rawErrPos <= len(escTail) && escTail[rawErrPos-1] == '\\' {
			return rawErrPos - 1, ErrInvalidEscape
		}
	case types.ERR_INVALID_CHAR:
		for k := 2; k <= 5; k++ {
			b := rawErrPos - k
			if b >= 0 && b+1 < len(escTail) && escTail[b] == '\\' && escTail[b+1] == 'u' {
				return b, ErrInvalidEscapeUTF
			}
		}
	}

	// Include [rawErrPos : rawErrPos+10] so an unpaired high surrogate whose
	// rawErrPos == r-4 still exposes a following truncated or malformed \uYYYY
	// candidate [r : r+6] without touching any trailing multi-MiB suffix.
	limit := len(escTail)
	if rawErrPos >= 0 && rawErrPos+10 < limit {
		limit = rawErrPos + 10
	}
	var st StringValidateState
	st.reset()
	off, sc := st.advanceWindow(escTail, 0, limit, true, StringPolicy{
		ValidateEscapes:     true,
		RejectLoneSurrogate: rejectLoneSurrogate,
	}, false)
	if sc != ErrNone && off >= 0 {
		if perr == types.ERR_EOF {
			return off, ErrInvalidEscapeUTF
		}
		return off, sc
	}
	if rawErrPos > 0 {
		return rawErrPos - 1, ErrInvalidEscapeUTF
	}
	return 0, ErrInvalidEscapeUTF
}

// UnquoteInto decodes escTail into dst (len(dst) >= len(escTail)), preserving
// and normalizing kernel error offsets via NormalizeUnquoteError on failure.
func UnquoteInto(escTail string, dst []byte, rejectLoneSurrogate bool) (written int, errRelOff int, code ScanError) {
	w, rawErrPos, perr := kernUnquoteRaw(escTail, dst, !rejectLoneSurrogate)
	if perr == 0 {
		return w, -1, ErrNone
	}
	off, sc := NormalizeUnquoteError(escTail, rawErrPos, perr, rejectLoneSurrogate)
	return w, off, sc
}
