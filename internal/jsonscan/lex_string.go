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
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// StringOutputNeed specifies what output (if any) a consumer requires from a
// JSON string token, orthogonal to validation strictness.
type StringOutputNeed uint8

const (
	StringOutputNone           StringOutputNeed = iota // ValidateOnly / RawSpan: zero decoded bytes
	StringOutputObserveCompact                         // CompactObserve: dirty/HTML/UTF-8 check, zero decoded bytes
	StringOutputDecode                                 // Emit: materialize unescaped bytes into Unesc when escaped
)

// StringPolicy captures the orthogonal validation dimensions for JSON string
// lexing across Strict, Relaxed, ASTValidate, and Compact profiles.
type StringPolicy struct {
	ValidateEscapes     bool
	RejectControl       bool
	RejectLoneSurrogate bool
	ValidateUTF8        bool
	EscapeHTMLObserve   bool
	ASTCompatLex        bool
	Output              StringOutputNeed
}

func stringPolicyFor(prof Profile, output StringOutputNeed) StringPolicy {
	return StringPolicy{
		ValidateEscapes:     prof.ValidateEscapes,
		RejectControl:       prof.RejectControl,
		RejectLoneSurrogate: prof.RejectLoneSurrogate,
		ValidateUTF8:        prof.ValidateUTF8,
		EscapeHTMLObserve:   prof.EscapeHTMLObserve,
		ASTCompatLex:        prof.ASTCompatLex,
		Output:              output,
	}
}

// Authoritative JSON escape & hex lookup tables shared by all string lexing,
// validation, and error-normalization paths in jsonscan.
var simpleEscapeTable = [256]byte{
	'"': '"', '/': '/', '\\': '\\', 'b': '\b',
	'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

var hexDigitTable = [256]uint8{
	'0': 0x10, '1': 0x11, '2': 0x12, '3': 0x13, '4': 0x14,
	'5': 0x15, '6': 0x16, '7': 0x17, '8': 0x18, '9': 0x19,
	'a': 0x1a, 'b': 0x1b, 'c': 0x1c, 'd': 0x1d, 'e': 0x1e, 'f': 0x1f,
	'A': 0x1a, 'B': 0x1b, 'C': 0x1c, 'D': 0x1d, 'E': 0x1e, 'F': 0x1f,
}

func parseHex16(s string) (uint32, bool) {
	a := uint32(hexDigitTable[s[0]])
	b := uint32(hexDigitTable[s[1]])
	c := uint32(hexDigitTable[s[2]])
	d := uint32(hexDigitTable[s[3]])
	return (a << 12) + (b << 8) + (c << 4) + d - 0x11110, a&b&c&d&0x10 != 0
}

// Bounded O(1) cross-chunk escape & surrogate validation stages.
const (
	strValPlain uint8 = iota
	strValAfterSlash
	strValHex
	strValAfterHighSurr
	strValHighSurrSlash
	strValLowHex
)

// StringValidateState holds bounded O(1) state for validating JSON string bodies
// (including escapes, raw controls, and UTF-16 surrogate pairs) across arbitrary
// chunk boundaries without allocating or writing any decoded output bytes.
type StringValidateState struct {
	Active          bool
	Stage           uint8
	HexCount        uint8
	HexAcc          uint16
	Pos             int // next byte offset in src to inspect
	EscOff          int // offset of active '\\'
	HighOff         int // offset of pending high-surrogate '\\'
	DeferredSurrOff int // earliest lone-surrogate offset deferred until raw-control scan completes (-1 if none)
}

func (st *StringValidateState) reset() {
	*st = StringValidateState{DeferredSurrOff: -1}
}

func (st *StringValidateState) recordSurrogateErr(off int, deferForControlCheck bool) (stopNow bool) {
	if !deferForControlCheck {
		return true
	}
	if st.DeferredSurrOff < 0 {
		st.DeferredSurrOff = off
	}
	return false
}

// advanceWindow validates src[start:limit] (where limit <= closingQuote or safeResumeOffset)
// in O(limit - st.Pos) time with zero output allocations.
// When deferSurrogateForCtrl is true (RejectControl=true), lone-surrogate errors are latched
// in st.DeferredSurrOff so any raw control char (< 0x20) later in the string retains higher
// priority, matching ModeEmit's ScanString-then-Unquote contract at every chunk split.
func (st *StringValidateState) advanceWindow(
	src string,
	start int,
	limit int,
	atStringEnd bool,
	pol StringPolicy,
	deferSurrogateForCtrl bool,
) (errOff int, code ScanError) {
	if !st.Active {
		st.Active = true
		st.Stage = strValPlain
		st.Pos = start
		st.DeferredSurrOff = -1
	}
	i := st.Pos
	if i < start {
		i = start
	}
	b := unsafe.Slice(unsafe.StringData(src), len(src))
	valEsc := pol.ValidateEscapes
	rejCtrl := pol.RejectControl
	rejSurr := pol.RejectLoneSurrogate

	for {
		switch st.Stage {
		case strValPlain:
			for i+8 <= limit {
				w := binary.LittleEndian.Uint64(b[i : i+8 : i+8])
				xb := w ^ 0x5c5c5c5c5c5c5c5c
				mask := (xb - 0x0101010101010101) & ^xb & 0x8080808080808080
				if rejCtrl {
					ctrl := (w - 0x2020202020202020) & ^w & 0x8080808080808080
					mask |= ctrl
				}
				if mask != 0 {
					i += bits.TrailingZeros64(mask) >> 3
					break
				}
				i += 8
			}
			if i >= limit {
				st.Pos = limit
				if atStringEnd && st.DeferredSurrOff >= 0 {
					return st.DeferredSurrOff, ErrInvalidEscapeUTF
				}
				return -1, ErrNone
			}
			c := b[i]
			if c == '\\' {
				st.EscOff = i
				i++
				st.Stage = strValAfterSlash
				continue
			}
			if rejCtrl && c < 0x20 {
				st.Pos = i
				return i, ErrControlChar
			}
			i++

		case strValAfterSlash:
			if i >= limit {
				st.Pos = limit
				if atStringEnd {
					return st.EscOff, ErrInvalidEscape
				}
				return -1, ErrNone
			}
			c := b[i]
			if !valEsc {
				i++
				st.Stage = strValPlain
				continue
			}
			if c == 'u' {
				i++
				st.Stage = strValHex
				st.HexCount = 0
				st.HexAcc = 0
				continue
			}
			if simpleEscapeTable[c] == 0 {
				st.Pos = i
				return st.EscOff, ErrInvalidEscape
			}
			i++
			st.Stage = strValPlain

		case strValHex:
			for st.HexCount < 4 {
				if i >= limit {
					st.Pos = limit
					if atStringEnd {
						return st.EscOff, ErrInvalidEscapeUTF
					}
					return -1, ErrNone
				}
				hv := hexDigitTable[b[i]]
				if hv == 0 {
					st.Pos = i
					return st.EscOff, ErrInvalidEscapeUTF
				}
				st.HexAcc = (st.HexAcc << 4) | uint16(hv&0x0f)
				st.HexCount++
				i++
			}
			r0 := st.HexAcc
			if r0 >= 0xd800 && r0 <= 0xdfff {
				if r0 > 0xdbff {
					if rejSurr && st.recordSurrogateErr(st.EscOff, deferSurrogateForCtrl) {
						return st.EscOff, ErrInvalidEscapeUTF
					}
					st.Stage = strValPlain
					continue
				}
				if rejSurr {
					st.HighOff = st.EscOff
					st.Stage = strValAfterHighSurr
					continue
				}
			}
			st.Stage = strValPlain

		case strValAfterHighSurr:
			if i >= limit {
				st.Pos = limit
				if atStringEnd {
					if st.DeferredSurrOff >= 0 && st.DeferredSurrOff < st.HighOff {
						return st.DeferredSurrOff, ErrInvalidEscapeUTF
					}
					return st.HighOff, ErrInvalidEscapeUTF
				}
				return -1, ErrNone
			}
			c := b[i]
			if c != '\\' {
				if st.recordSurrogateErr(st.HighOff, deferSurrogateForCtrl) {
					return st.HighOff, ErrInvalidEscapeUTF
				}
				st.Stage = strValPlain
				continue // re-inspect `c` in strValPlain (e.g. if `c` is raw < 0x20!)
			}
			st.EscOff = i
			i++
			st.Stage = strValHighSurrSlash

		case strValHighSurrSlash:
			if i >= limit {
				st.Pos = limit
				if atStringEnd {
					if st.DeferredSurrOff >= 0 && st.DeferredSurrOff < st.HighOff {
						return st.DeferredSurrOff, ErrInvalidEscapeUTF
					}
					return st.HighOff, ErrInvalidEscapeUTF
				}
				return -1, ErrNone
			}
			c := b[i]
			if c != 'u' {
				if simpleEscapeTable[c] == 0 {
					// If deferSurrogateForCtrl is false, HighOff was already invalid at HighOff!
					// If deferSurrogateForCtrl is true, basic escape error `\q` is caught immediately
					// just like ScanStringStrict would catch `\q`!
					if st.recordSurrogateErr(st.HighOff, deferSurrogateForCtrl) {
						return st.HighOff, ErrInvalidEscapeUTF
					}
					return st.EscOff, ErrInvalidEscape
				}
				if st.recordSurrogateErr(st.HighOff, deferSurrogateForCtrl) {
					return st.HighOff, ErrInvalidEscapeUTF
				}
				i++
				st.Stage = strValPlain
				continue
			}
			i++
			st.Stage = strValLowHex
			st.HexCount = 0
			st.HexAcc = 0

		case strValLowHex:
			for st.HexCount < 4 {
				if i >= limit {
					st.Pos = limit
					if atStringEnd {
						return st.EscOff, ErrInvalidEscapeUTF
					}
					return -1, ErrNone
				}
				hv := hexDigitTable[b[i]]
				if hv == 0 {
					st.Pos = i
					return st.EscOff, ErrInvalidEscapeUTF
				}
				st.HexAcc = (st.HexAcc << 4) | uint16(hv&0x0f)
				st.HexCount++
				i++
			}
			r1 := st.HexAcc
			if r1 >= 0xdc00 && r1 <= 0xdfff {
				st.Stage = strValPlain
				continue
			}
			if r1 >= 0xd800 && r1 <= 0xdbff {
				// Chained high surrogate `\uD800\uD801...`: record first lone high surrogate,
				// and keep `\uD801` as the active high surrogate!
				if st.recordSurrogateErr(st.HighOff, deferSurrogateForCtrl) {
					return st.HighOff, ErrInvalidEscapeUTF
				}
				st.HighOff = st.EscOff
				st.Stage = strValAfterHighSurr
				continue
			}
			if st.recordSurrogateErr(st.HighOff, deferSurrogateForCtrl) {
				return st.HighOff, ErrInvalidEscapeUTF
			}
			st.Stage = strValPlain
		}
	}
}

func utf8SafePrefixEnd(src string, from, limit int, final bool) int {
	if limit > len(src) {
		limit = len(src)
	}
	if final || limit <= from {
		return limit
	}
	floor := limit - 3
	if floor < from {
		floor = from
	}
	for i := limit - 1; i >= floor; i-- {
		b := src[i]
		if b < 0x80 {
			return limit
		}
		if (b & 0xc0) == 0x80 {
			continue
		}
		var need int
		switch {
		case b >= 0xc2 && b <= 0xdf:
			need = 2
		case b >= 0xe0 && b <= 0xef:
			need = 3
		case b >= 0xf0 && b <= 0xf4:
			need = 4
		default:
			return limit
		}
		if limit-i < need {
			return i
		}
		return limit
	}
	return limit
}

func (m *Machine) checkConsumedUTF8(src string, limit int, final bool) {
	if !m.Grammar.Profile.ValidateUTF8 || m.Grammar.RootMode == ModeCompactObserve || m.Lex.UTF8Invalid {
		return
	}
	end := utf8SafePrefixEnd(src, m.Lex.UTF8Checked, limit, final)
	if end <= m.Lex.UTF8Checked {
		return
	}
	if !kernValidateUTF8(src[m.Lex.UTF8Checked:end]) {
		m.Lex.UTF8Invalid = true
		return
	}
	m.Lex.UTF8Checked = end
}

// safeStringResumeOffset returns the furthest offset <= len(src) from which
// string scanning can resume on the next chunk without splitting a trailing '\',
// '\uXXXX', or '\uD800\uDC00' surrogate pair (<= 11 bytes held back).
func safeStringResumeOffset(src string, floor int) int {
	n := len(src)
	p := n - 12
	if p < floor {
		p = floor
	}
	for p > floor && src[p-1] == '\\' {
		p--
	}
	q := p
	for q < n {
		if src[q] != '\\' {
			q++
			continue
		}
		if q+2 > n {
			return q
		}
		if src[q+1] != 'u' {
			q += 2
			continue
		}
		if q+6 > n {
			return q
		}
		r0, ok := parseHex16(src[q+2 : q+6])
		if ok && r0 >= 0xd800 && r0 <= 0xdbff && q+12 > n {
			return q
		}
		q += 6
	}
	return n
}

// CheckEarlierEscapeBeforeEOF checks whether an unclosed string tail (missing its
// closing quote at EOF) contains an already-complete invalid escape or unpaired
// surrogate before the final truncated sequence, delegating entirely to the single
// authoritative StringValidateState.
func CheckEarlierEscapeBeforeEOF(escTail string, rejectLoneSurrogate bool) (int, ScanError, bool) {
	return checkEarlierEscapeOrCtrlBeforeEOF(escTail, false, rejectLoneSurrogate)
}

func checkEarlierEscapeOrCtrlBeforeEOF(escTail string, rejectControl, rejectLoneSurrogate bool) (int, ScanError, bool) {
	var st StringValidateState
	st.reset()
	pol := StringPolicy{
		ValidateEscapes:     true,
		RejectControl:       rejectControl,
		RejectLoneSurrogate: rejectLoneSurrogate,
	}
	errOff, code := st.advanceWindow(escTail, 0, len(escTail), false, pol, rejectControl)
	if code != ErrNone {
		return errOff, code, true
	}
	if st.DeferredSurrOff >= 0 {
		return st.DeferredSurrOff, ErrInvalidEscapeUTF, true
	}
	if rejectLoneSurrogate && (st.Stage == strValAfterHighSurr || st.Stage == strValHighSurrSlash) {
		return st.HighOff, ErrInvalidEscapeUTF, true
	}
	return 0, ErrNone, false
}

func swarPlainQuoteLen64(src string, rawStart int, rejectCtrl bool) (int, bool) {
	rem := len(src) - rawStart
	if rem < 8 {
		return -1, false
	}
	b := unsafe.Slice(unsafe.StringData(src), len(src))
	maxWords := rem >> 3
	if maxWords > 8 {
		maxWords = 8
	}
	off := rawStart
	var highBits uint64
	for wIdx := 0; wIdx < maxWords; wIdx++ {
		w := binary.LittleEndian.Uint64(b[off : off+8 : off+8])
		xq := w ^ 0x2222222222222222
		q := (xq - 0x0101010101010101) & ^xq & 0x8080808080808080
		xb := w ^ 0x5c5c5c5c5c5c5c5c
		bs := (xb - 0x0101010101010101) & ^xb & 0x8080808080808080
		var ctrl uint64
		if rejectCtrl {
			ctrl = (w - 0x2020202020202020) & ^w & 0x8080808080808080
		}
		if q != 0 {
			lsb := q & -q
			beforeMask := lsb - 1
			if (bs|ctrl)&beforeMask == 0 {
				highBits |= (w & 0x8080808080808080) & beforeMask
				return (wIdx << 3) + (bits.TrailingZeros64(lsb) >> 3), highBits == 0
			}
			return -1, false
		}
		if (bs | ctrl) != 0 {
			return -1, false
		}
		highBits |= w & 0x8080808080808080
		off += 8
	}
	return -1, false
}

// scanStringStep scans or resumes a JSON string starting at m.Lex.TokStart ('"'),
// keeping the unescaped SWAR fast path compact and delegating escaped/chunked
// strings to scanStringStepSlow without duplicating any grammar logic.
func (m *Machine) scanStringStep(src string, final bool, isKey bool) (done bool, stop StopReason, errCode ScanError, emittedOrKey Node, keyView string, keyASCII bool) {
	g := &m.Grammar
	l := &m.Lex
	pos := l.StrScanOff
	observeCompact := g.RootMode == ModeCompactObserve
	emitting := m.isEmitting()
	eventSink := m.isEventSink()

	if (!observeCompact || (!g.Profile.EscapeHTMLObserve && !g.Profile.ValidateUTF8)) && l.StrFirstEsc < 0 && pos == l.TokStart+1 {
		if slen, ascii := swarPlainQuoteLen64(src, pos, g.Profile.RejectControl); slen >= 0 {
			if emitting && !CheckSourceSpan(pos, slen, len(src)) {
				return false, StopError, ErrSpanOverflow, Node{}, "", false
			}
			var nd Node
			if emitting {
				nd = Node{Typ: KStringCommon | uint64(pos)<<PosBits, Val: uint64(slen)}
				if !isKey {
					m.Stat.Str++
				}
			}
			l.StrScanOff = 0
			g.Cur = pos + slen + 1
			if (isKey && !observeCompact) || eventSink {
				return true, StopDone, ErrNone, nd, src[pos : pos+slen], ascii
			}
			return true, StopDone, ErrNone, nd, "", false
		}
	}
	return m.scanStringStepSlow(src, final, isKey)
}

//go:noinline
func (m *Machine) scanStringStepSlow(src string, final bool, isKey bool) (done bool, stop StopReason, errCode ScanError, emittedOrKey Node, keyView string, keyASCII bool) {
	g := &m.Grammar
	l := &m.Lex
	pos := l.StrScanOff
	emitting := m.isEmitting()
	eventSink := m.isEventSink()
	decoding := emitting || eventSink
	observeCompact := g.RootMode == ModeCompactObserve

	// Non-decoding AST non-strict-string mode (ValidateEscapes=false && RejectControl=false):
	// validate closing quote boundary while skipping '\\' pairs without escape/control checks.
	if !decoding && !observeCompact && !g.Profile.ValidateEscapes && !g.Profile.RejectControl {
		if pos >= len(src) {
			if !final {
				l.StrScanOff = pos
				return false, StopNeedInput, ErrNone, Node{}, "", false
			}
			g.ErrOffset = len(src)
			g.Cur = len(src)
			return false, StopError, ErrEOF, Node{}, "", false
		}
		relEnd, perr := kernSkipStringRelaxed(src[pos:])
		if perr != 0 {
			if !final {
				l.StrScanOff = safeStringResumeOffset(src, pos)
				return false, StopNeedInput, ErrNone, Node{}, "", false
			}
			g.ErrOffset = len(src)
			g.Cur = len(src)
			return false, StopError, ErrEOF, Node{}, "", false
		}
		l.StrScanOff = 0
		g.Cur = pos + relEnd
		return true, StopDone, ErrNone, Node{}, "", false
	}

	nonDecoding := !decoding || observeCompact
	outNeed := StringOutputDecode
	if observeCompact {
		outNeed = StringOutputObserveCompact
	} else if !decoding {
		outNeed = StringOutputNone
	}
	pol := stringPolicyFor(g.Profile, outNeed)

	var endPos, escPos, errPos int = 0, -1, -1
	var perr uint64
	strictScan := pol.RejectControl || observeCompact || (nonDecoding && pol.ValidateEscapes && !pol.RejectLoneSurrogate && !l.StrRawCtrlSeen)
	if pos >= len(src) {
		perr = errEOFSentinel
	} else {
		e, esc, ep, pe := kernScanString(src, pos-1, strictScan)
		endPos, escPos, errPos = e, esc, ep
		if pe != 0 {
			if isKernelEOF(pe) {
				perr = errEOFSentinel
			} else {
				perr = uint64(pe)
			}
		}
	}

	if perr != 0 {
		if perr == errEOFSentinel && !final {
			if l.StrFirstEsc < 0 && escPos >= 0 {
				l.StrFirstEsc = escPos
			}
			resumeOff := safeStringResumeOffset(src, pos)
			if nonDecoding && l.StrFirstEsc >= 0 && ((!strictScan && pol.ValidateEscapes) || pol.RejectLoneSurrogate) {
				valStart := l.StrFirstEsc
				if !pol.RejectLoneSurrogate && pos > valStart {
					valStart = pos
				}
				if vErrOff, vCode := l.StrVal.advanceWindow(src, valStart, resumeOff, false, pol, pol.RejectControl); vCode != ErrNone {
					g.ErrOffset = vErrOff
					g.Cur = errorConsumedAfterOff(vErrOff, pol.ASTCompatLex)
					return false, StopError, vCode, Node{}, "", false
				}
			}
			l.StrScanOff = resumeOff
			return false, StopNeedInput, ErrNone, Node{}, "", false
		}
		if perr == errEOFSentinel {
			firstEsc := l.StrFirstEsc
			if firstEsc < 0 && escPos >= 0 {
				firstEsc = escPos
			}
			if firstEsc >= 0 && pol.ValidateEscapes && !pol.ASTCompatLex {
				if l.StrVal.Active {
					vErrOff, vCode := l.StrVal.advanceWindow(src, firstEsc, len(src), false, pol, pol.RejectControl)
					if vCode != ErrNone {
						g.ErrOffset = vErrOff
						g.Cur = vErrOff + 1
						return false, StopError, vCode, Node{}, "", false
					}
					if l.StrVal.DeferredSurrOff >= 0 {
						g.ErrOffset = l.StrVal.DeferredSurrOff
						g.Cur = g.ErrOffset + 1
						return false, StopError, ErrInvalidEscapeUTF, Node{}, "", false
					}
					if pol.RejectLoneSurrogate && (l.StrVal.Stage == strValAfterHighSurr || l.StrVal.Stage == strValHighSurrSlash) {
						g.ErrOffset = l.StrVal.HighOff
						g.Cur = g.ErrOffset + 1
						return false, StopError, ErrInvalidEscapeUTF, Node{}, "", false
					}
				} else {
					checkFrom := firstEsc
					if strictScan && !pol.RejectLoneSurrogate && pos > checkFrom && !l.StrRawCtrlSeen {
						checkFrom = pos
					}
					if relOff, ecode, found := checkEarlierEscapeOrCtrlBeforeEOF(src[checkFrom:], pol.RejectControl, pol.RejectLoneSurrogate); found {
						g.ErrOffset = checkFrom + relOff
						g.Cur = errorConsumedAfterOff(g.ErrOffset, pol.ASTCompatLex)
						return false, StopError, ecode, Node{}, "", false
					}
				}
			} else if pol.RejectControl {
				for k := pos; k < len(src); k++ {
					if src[k] < 0x20 {
						g.ErrOffset = k
						g.Cur = errorConsumedAfterOff(k, pol.ASTCompatLex)
						return false, StopError, ErrControlChar, Node{}, "", false
					}
				}
			}
			g.ErrOffset = len(src)
			g.Cur = len(src)
			return false, StopError, ErrEOF, Node{}, "", false
		}
		if errPos < len(src) && src[errPos] < 0x20 {
			if !pol.RejectControl {
				l.StrRawCtrlSeen = true
				goto manualValidateUnclosedOrEscaped
			}
			g.ErrOffset = errPos
			g.Cur = errorConsumedAfterOff(errPos, pol.ASTCompatLex)
			return false, StopError, ErrControlChar, Node{}, "", false
		}
		g.ErrOffset = errPos
		g.Cur = errorConsumedAfterOff(errPos, pol.ASTCompatLex)
		if errPos+1 < len(src) && src[errPos] == '\\' && src[errPos+1] == 'u' {
			return false, StopError, ErrInvalidEscapeUTF, Node{}, "", false
		}
		return false, StopError, ErrInvalidEscape, Node{}, "", false
	}
	goto scannedQuote

manualValidateUnclosedOrEscaped:
	{
		e, esc, ep, pe := kernScanString(src, pos-1, false)
		endPos, escPos, errPos = e, esc, ep
		if pe != 0 {
			if isKernelEOF(pe) && !final {
				if l.StrFirstEsc < 0 && escPos >= 0 {
					l.StrFirstEsc = escPos
				}
				resumeOff := safeStringResumeOffset(src, pos)
				if nonDecoding && l.StrFirstEsc >= 0 && pol.ValidateEscapes {
					valStart := l.StrFirstEsc
					if !pol.RejectLoneSurrogate && pos > valStart {
						valStart = pos
					}
					if vErrOff, vCode := l.StrVal.advanceWindow(src, valStart, resumeOff, false, pol, false); vCode != ErrNone {
						g.ErrOffset = vErrOff
						g.Cur = vErrOff + 1
						return false, StopError, vCode, Node{}, "", false
					}
				}
				l.StrScanOff = resumeOff
				return false, StopNeedInput, ErrNone, Node{}, "", false
			}
			firstEsc := l.StrFirstEsc
			if firstEsc < 0 && escPos >= 0 {
				firstEsc = escPos
			}
			if firstEsc >= 0 && pol.ValidateEscapes {
				if l.StrVal.Active {
					vErrOff, vCode := l.StrVal.advanceWindow(src, firstEsc, len(src), false, pol, false)
					if vCode != ErrNone {
						g.ErrOffset = vErrOff
						g.Cur = vErrOff + 1
						return false, StopError, vCode, Node{}, "", false
					}
					if pol.RejectLoneSurrogate && (l.StrVal.Stage == strValAfterHighSurr || l.StrVal.Stage == strValHighSurrSlash) {
						g.ErrOffset = l.StrVal.HighOff
						g.Cur = g.ErrOffset + 1
						return false, StopError, ErrInvalidEscapeUTF, Node{}, "", false
					}
				} else {
					checkFrom := firstEsc
					if relOff, ecode, found := CheckEarlierEscapeBeforeEOF(src[checkFrom:], pol.RejectLoneSurrogate); found {
						g.ErrOffset = checkFrom + relOff
						g.Cur = g.ErrOffset + 1
						return false, StopError, ecode, Node{}, "", false
					}
				}
			}
			g.ErrOffset = len(src)
			g.Cur = len(src)
			return false, StopError, ErrEOF, Node{}, "", false
		}
	}

scannedQuote:
	firstEsc := l.StrFirstEsc
	if firstEsc < 0 && escPos >= 0 {
		firstEsc = escPos
	}
	rawStart := l.TokStart + 1
	rawContentEnd := endPos - 1
	rawLen := rawContentEnd - rawStart

	if observeCompact {
		if firstEsc >= 0 && pol.RejectLoneSurrogate {
			errAt, uerr := l.StrVal.advanceWindow(src, firstEsc, rawContentEnd, true, pol, true)
			if uerr != ErrNone {
				g.ErrOffset = errAt
				g.Cur = errAt + 1
				return false, StopError, uerr, Node{}, "", false
			}
		}
		m.observeCompactString(src, l.TokStart, rawStart, rawContentEnd, pol.EscapeHTMLObserve, pol.ValidateUTF8)
		l.StrScanOff = 0
		l.StrVal.reset()
		g.Cur = endPos
		return true, StopDone, ErrNone, Node{}, "", false
	}

	if !decoding {
		if firstEsc >= 0 && ((l.StrRawCtrlSeen && pol.ValidateEscapes) || pol.RejectLoneSurrogate) {
			valStart := firstEsc
			if !pol.RejectLoneSurrogate && pos > valStart && !l.StrVal.Active {
				valStart = pos
			}
			errAt, uerr := l.StrVal.advanceWindow(src, valStart, rawContentEnd, true, pol, pol.RejectControl)
			if uerr != ErrNone {
				g.ErrOffset = errAt
				g.Cur = errAt + 1
				return false, StopError, uerr, Node{}, "", false
			}
		}
		l.StrScanOff = 0
		l.StrVal.reset()
		g.Cur = endPos
		return true, StopDone, ErrNone, Node{}, "", false
	}

	if eventSink {
		var strVal string
		if firstEsc < 0 {
			strVal = src[rawStart:rawContentEnd]
		} else {
			var uerr ScanError
			var errRel int
			strVal, errRel, uerr = m.decodeEscapedStringCopy(src, rawStart, firstEsc, rawContentEnd, pol.RejectLoneSurrogate, pol.ASTCompatLex)
			if uerr != ErrNone {
				if uerr == ErrEOF && pol.ASTCompatLex {
					g.ErrOffset = len(src)
					g.Cur = len(src)
					return false, StopError, ErrEOF, Node{}, "", false
				}
				errAt := firstEsc + errRel
				g.ErrOffset = errAt
				g.Cur = errorConsumedAfterOff(errAt, pol.ASTCompatLex)
				return false, StopError, uerr, Node{}, "", false
			}
		}
		l.StrScanOff = 0
		g.Cur = endPos
		return true, StopDone, ErrNone, Node{}, strVal, false
	}

	if !CheckSourceSpan(rawStart, rawLen, len(src)) {
		g.ErrOffset = l.TokStart
		return false, StopError, ErrSpanOverflow, Node{}, "", false
	}

	var nd Node
	var kstr string
	if firstEsc < 0 {
		nd = Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
		if isKey {
			kstr = src[rawStart:rawContentEnd]
		}
	} else {
		var uerr ScanError
		var errRel int
		nd, kstr, errRel, uerr = m.decodeEscapedStringToUnesc(src, rawStart, firstEsc, rawContentEnd, pol.RejectLoneSurrogate, isKey)
		if uerr != ErrNone {
			if uerr == ErrSpanOverflow {
				g.ErrOffset = l.TokStart
				return false, StopError, ErrSpanOverflow, Node{}, "", false
			}
			errAt := firstEsc + errRel
			g.ErrOffset = errAt
			g.Cur = errAt + 1
			return false, StopError, uerr, Node{}, "", false
		}
	}

	if !isKey {
		m.Stat.Str++
	}
	l.StrScanOff = 0
	g.Cur = endPos
	return true, StopDone, ErrNone, nd, kstr, false
}

// scanPlainColdStringNode scans and unquotes a full-buffer (final==true) cold
// string in plain ModeEmit (where len(src) <= MaxSourceOffset and no Schema key
// string view is needed), resuming from scanFrom without prefix rescanning.
// Returns ok=false on any kernel/unquote error or raw control byte so the caller
// delegates 100% of error priority handling to scanStringStepSlow.
func (m *Machine) scanPlainColdStringNode(src string, quotePos, scanFrom int, rejectControl, rejectLoneSurrogate bool) (nd Node, endPos int, ok bool) {
	rawStart := quotePos + 1
	pos := scanFrom
	if pos < rawStart {
		pos = rawStart
	}
	if pos >= len(src) {
		return Node{}, 0, false
	}
	e, esc, _, pe := kernScanString(src, pos-1, rejectControl)
	if pe != 0 {
		return Node{}, 0, false
	}
	firstEsc := -1
	if pos > rawStart && pos < len(src) && src[pos] == '\\' {
		firstEsc = pos
	} else if esc >= 0 {
		firstEsc = esc
	}
	rawContentEnd := e - 1
	rawLen := rawContentEnd - rawStart
	if firstEsc < 0 {
		return Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}, e, true
	}
	uoff := len(m.Unesc)
	nd, _, _, uerr := m.decodeEscapedStringToUnesc(src, rawStart, firstEsc, rawContentEnd, rejectLoneSurrogate, false)
	if uerr != ErrNone {
		m.Unesc = m.Unesc[:uoff]
		return Node{}, 0, false
	}
	return nd, e, true
}
