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
	"strings"
)

// NumberForm describes the syntactic shape of a lexed JSON number token.
type NumberForm uint8

const (
	NumberFormInt NumberForm = iota
	NumberFormFrac
	NumberFormExp
	NumberFormFracExp
)

// NumberHintKind identifies an optional, already-verified numeric conversion
// computed during the single-pass fast scan. Hints never alter lexical
// boundaries or error offsets.
type NumberHintKind uint8

const (
	NumberHintNone NumberHintKind = iota
	NumberHintUint
	NumberHintSint
	NumberHintFloat
)

// NumberFaultFact captures neutral lexical error coordinates without consulting
// UseNumber, Schema, or target Go types. Profile-specific public offset
// adjustments live exclusively in error_adapter.go.
type NumberFaultFact struct {
	TokenStart int
	DotPos     int // -1 if no '.' seen in this token
	ExpPos     int // -1 if no 'e'/'E' seen in this token
	SignPos    int // -1 if no exponent '+'/'-' seen in this token
	FaultPos   int // byte offset where lexical validation failed (len(src) on EOF)
	AtEOF      bool
}

// LexNumberResult is the output of the single authoritative JSON number lexer.
type LexNumberResult struct {
	Start           int
	End             int
	Negative        bool
	NegZero         bool
	Form            NumberForm
	Complete        bool
	NeedMore        bool
	ContMarkerAtEnd bool // non-bare-zero token immediately followed by '.', 'e', 'E', '+', or '-'
	HintKind        NumberHintKind
	HintBits        uint64
	Fault           NumberFaultFact
}

// Linear number lexing phases for chunk/window resumption without prefix rescanning.
const (
	numPhaseNone uint8 = iota
	numPhaseAfterMinus
	numPhaseZero
	numPhaseInt
	numPhaseAfterDot
	numPhaseFrac
	numPhaseAfterExp
	numPhaseAfterExpSign
	numPhaseExpDigits
)

var exactPow10 = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7,
	1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15,
	1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

var isNumContByteTable = [256]bool{
	'.': true,
	'e': true,
	'E': true,
	'+': true,
	'-': true,
}

func isNumberContByte(c byte) bool {
	return isNumContByteTable[c]
}

func isBareZeroToken(src string, start, end int) bool {
	return end > start && src[end-1] == '0' && (end == start+1 || (end == start+2 && src[start] == '-'))
}

// scanNumberSpanSuffix validates fractional, exponent, and trailing
// continuation-byte suffixes starting at src[q] after integer digits have been
// consumed. Shared by scanValidNumberSpan so plain_emit, schema_emit, and
// validate_fast enforce a single number-boundary grammar.
func scanNumberSpanSuffix(src string, q int, bareZero, rejectCont bool) (int, bool) {
	srcLen := len(src)
	c := src[q]
	hasFracOrExp := !bareZero
	if c == '.' {
		hasFracOrExp = true
		q++
		if q >= srcLen || src[q]-'0' > 9 {
			return q, false
		}
		q++
		for q < srcLen && src[q]-'0' <= 9 {
			q++
		}
		if q >= srcLen {
			return q, true
		}
		c = src[q]
	}
	if c == 'e' || c == 'E' {
		hasFracOrExp = true
		q++
		if q < srcLen && (src[q] == '+' || src[q] == '-') {
			q++
		}
		if q >= srcLen || src[q]-'0' > 9 {
			return q, false
		}
		q++
		for q < srcLen && src[q]-'0' <= 9 {
			q++
		}
		if q >= srcLen {
			return q, true
		}
		c = src[q]
	}
	if rejectCont && hasFracOrExp && isNumberContByte(c) {
		return q, false
	}
	return q, true
}

// scanValidNumberSpanSlow handles signed numbers, leading-zero tokens,
// fractional/exponent suffixes, and invalid boundaries for scanValidNumberSpan.
func scanValidNumberSpanSlow(src string, pos int, rejectCont bool) (int, bool) {
	srcLen := len(src)
	q := pos
	c0 := src[q]
	if c0 == '-' {
		q++
		if q >= srcLen {
			return 0, false
		}
		c0 = src[q]
	}
	if c0 >= '1' && c0 <= '9' {
		q++
		for q < srcLen && src[q]-'0' <= 9 {
			q++
		}
		if q < srcLen && isNumContByteTable[src[q]] {
			return scanNumberSpanSuffix(src, q, false, rejectCont)
		}
		return q, true
	}
	if c0 == '0' {
		q++
		if q < srcLen {
			c := src[q]
			if c == '.' || c == 'e' || c == 'E' {
				return scanNumberSpanSuffix(src, q, true, rejectCont)
			}
		}
		return q, true
	}
	return 0, false
}

// scanValidNumberFastInt scans an unsigned non-negative integer token ("0" or
// [1-9][0-9]*) starting at src[pos] when followed by EOF or a non-continuation
// delimiter. Kept under Go's inline budget so validate_fast, plain_emit, and
// schema_emit share one number-boundary authority without call overhead on
// common integer tokens; all other forms delegate to scanValidNumberSpanSlow.
func scanValidNumberFastInt(src string, pos int) (int, bool) {
	c0 := src[pos]
	q := pos + 1
	if c0-'1' < 9 {
		for q < len(src) && src[q]-'0' <= 9 {
			q++
		}
	} else if c0 != '0' {
		return 0, false
	}
	if q >= len(src) || !isNumContByteTable[src[q]] {
		return q, true
	}
	return 0, false
}

func classifyNumberForm(span string) NumberForm {
	hasDot := strings.IndexByte(span, '.') >= 0
	hasExp := strings.IndexByte(span, 'e') >= 0 || strings.IndexByte(span, 'E') >= 0
	switch {
	case hasDot && hasExp:
		return NumberFormFracExp
	case hasDot:
		return NumberFormFrac
	case hasExp:
		return NumberFormExp
	default:
		return NumberFormInt
	}
}

func isNegativeZeroSpan(span string) bool {
	if len(span) < 2 || span[0] != '-' || span[1] != '0' {
		return false
	}
	for i := 2; i < len(span); i++ {
		c := span[i]
		if c == 'e' || c == 'E' {
			return true
		}
		if c != '.' && c != '0' {
			return false
		}
	}
	return true
}

// LexNumberAt runs the authoritative JSON number lexer from a fresh token start
// without requiring a full Machine instance.
func LexNumberAt(src string, start int, final bool) LexNumberResult {
	var l LexState
	l.beginNumber(start)
	return lexNumber(&l, src, final)
}

// lexFastNumberSpan performs the single-authority fast scan for <=19-digit
// integers and <=15-significant-digit exact IEEE-754 decimals (including 0.xxx
// and -0.xxx) without allocating or copying a full LexNumberResult struct.
func lexFastNumberSpan(src string, pos int, final bool) (end int, neg bool, negZero bool, form NumberForm, hint NumberHintKind, bits uint64, contAdj bool, ok bool) {
	if uint(pos) >= uint(len(src)) {
		return 0, false, false, 0, 0, 0, false, false
	}
	ch := src[pos]
	neg = ch == '-'
	q := pos
	if neg {
		q++
	}
	if q >= len(src) {
		return 0, false, false, 0, 0, 0, false, false
	}
	c0 := src[q]
	if c0 >= '1' && c0 <= '9' {
		v := uint64(c0 - '0')
		q++
		intDigits := 1
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
			intDigits++
		}
		if q < len(src) {
			term := src[q]
			if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
				cont := isNumberContByte(term)
				if !neg {
					return q, false, false, NumberFormInt, NumberHintUint, v, cont, true
				}
				if v <= 1<<63 {
					return q, true, false, NumberFormInt, NumberHintSint, -v, cont, true
				}
				return q, true, false, NumberFormInt, NumberHintNone, 0, cont, true
			}
			if term == '.' && intDigits <= 15 && q+1 < len(src) && src[q+1]-'0' <= 9 {
				q++
				fracDigits := 0
				maxFrac := 15 - intDigits
				for fracDigits < maxFrac && q < len(src) {
					d := src[q] - '0'
					if d > 9 {
						break
					}
					v = v*10 + uint64(d)
					q++
					fracDigits++
				}
				if fracDigits >= 1 && v>>52 == 0 {
					if q < len(src) {
						term2 := src[q]
						if term2 != '.' && term2 != 'e' && term2 != 'E' && (term2-'0' > 9) {
							fv := float64(v)
							if neg {
								fv = -fv
							}
							fv /= exactPow10[fracDigits]
							return q, neg, false, NumberFormFrac, NumberHintFloat, math.Float64bits(fv), isNumberContByte(term2), true
						}
					} else if final {
						fv := float64(v)
						if neg {
							fv = -fv
						}
						fv /= exactPow10[fracDigits]
						return q, neg, false, NumberFormFrac, NumberHintFloat, math.Float64bits(fv), false, true
					}
				}
			}
		} else if final && intDigits <= 19 {
			if !neg {
				return q, false, false, NumberFormInt, NumberHintUint, v, false, true
			}
			if v <= 1<<63 {
				return q, true, false, NumberFormInt, NumberHintSint, -v, false, true
			}
			return q, true, false, NumberFormInt, NumberHintNone, 0, false, true
		}
	} else if c0 == '0' {
		if q+1 < len(src) {
			term := src[q+1]
			if term != '.' && term != 'e' && term != 'E' {
				hk := NumberHintUint
				if neg {
					hk = NumberHintSint
				}
				return q + 1, neg, neg, NumberFormInt, hk, 0, false, true
			}
			if term == '.' && q+2 < len(src) && src[q+2]-'0' <= 9 {
				q += 2
				var v uint64
				fracDigits := 0
				for fracDigits < 15 && q < len(src) {
					d := src[q] - '0'
					if d > 9 {
						break
					}
					v = v*10 + uint64(d)
					q++
					fracDigits++
				}
				if fracDigits >= 1 && v>>52 == 0 {
					if q < len(src) {
						term2 := src[q]
						if term2 != '.' && term2 != 'e' && term2 != 'E' && (term2-'0' > 9) {
							var fbits uint64
							if v == 0 {
								if neg {
									fbits = 1 << 63
								}
							} else {
								fv := float64(v)
								if neg {
									fv = -fv
								}
								fv /= exactPow10[fracDigits]
								fbits = math.Float64bits(fv)
							}
							return q, neg, neg && v == 0, NumberFormFrac, NumberHintFloat, fbits, isNumberContByte(term2), true
						}
					} else if final {
						var fbits uint64
						if v == 0 {
							if neg {
								fbits = 1 << 63
							}
						} else {
							fv := float64(v)
							if neg {
								fv = -fv
							}
							fv /= exactPow10[fracDigits]
							fbits = math.Float64bits(fv)
						}
						return q, neg, neg && v == 0, NumberFormFrac, NumberHintFloat, fbits, false, true
					}
				}
			}
		} else if final {
			hk := NumberHintUint
			if neg {
				hk = NumberHintSint
			}
			return q + 1, neg, neg, NumberFormInt, hk, 0, false, true
		}
	}
	return 0, false, false, 0, 0, 0, false, false
}

// lexNumber scans one JSON number token from l.TokStart / l.NumScanOff using
// only LexState (never consulting Profile.UseNumber, Schema, or Node layout).
func lexNumber(l *LexState, src string, final bool) LexNumberResult {
	pos := l.TokStart
	if l.NumPhase == numPhaseNone && uint(pos) < uint(len(src)) {
		if end, neg, negZero, form, hint, bits, cont, ok := lexFastNumberSpan(src, pos, final); ok {
			return LexNumberResult{
				Start:           pos,
				End:             end,
				Negative:        neg,
				NegZero:         negZero,
				Form:            form,
				Complete:        true,
				ContMarkerAtEnd: cont,
				HintKind:        hint,
				HintBits:        bits,
			}
		}

		neg := src[pos] == '-'
		end, perr := kernScanNumberPrefix(src, pos)
		if perr == 0 && (final || end < len(src)) {
			span := src[pos:end]
			cont := !isBareZeroToken(src, pos, end) && end < len(src) && isNumberContByte(src[end])
			return LexNumberResult{
				Start:           pos,
				End:             end,
				Negative:        neg,
				NegZero:         neg && isNegativeZeroSpan(span),
				Form:            classifyNumberForm(span),
				Complete:        true,
				ContMarkerAtEnd: cont,
			}
		}
	}

	return advanceNumberLex(l, src, final)
}

// advanceNumberLex advances LexState linearly over src[l.NumScanOff:] for an
// active JSON number token without rescanning earlier chunks or consulting
// UseNumber.
func advanceNumberLex(l *LexState, src string, final bool) LexNumberResult {
	n := len(src)
	pos := l.TokStart
	i := l.NumScanOff
	ph := l.NumPhase

	makeFault := func(faultOff int) LexNumberResult {
		l.NumPhase = numPhaseNone
		return LexNumberResult{
			Start: pos,
			End:   faultOff,
			Fault: NumberFaultFact{
				TokenStart: pos,
				DotPos:     l.NumDotPos,
				ExpPos:     l.NumExpPos,
				SignPos:    l.NumSignPos,
				FaultPos:   faultOff,
				AtEOF:      faultOff >= n,
			},
		}
	}
	makeNeedMore := func(nextPh uint8) LexNumberResult {
		l.NumPhase = nextPh
		l.NumScanOff = n
		return LexNumberResult{
			Start:    pos,
			End:      n,
			NeedMore: true,
		}
	}
	makeComplete := func(end int) LexNumberResult {
		l.NumPhase = numPhaseNone
		neg := pos < end && src[pos] == '-'
		span := src[pos:end]
		cont := !isBareZeroToken(src, pos, end) && end < n && isNumberContByte(src[end])
		return LexNumberResult{
			Start:           pos,
			End:             end,
			Negative:        neg,
			NegZero:         neg && isNegativeZeroSpan(span),
			Form:            l.NumForm,
			Complete:        true,
			ContMarkerAtEnd: cont,
		}
	}

	if ph == numPhaseNone {
		i = pos
		l.NumDotPos = -1
		l.NumExpPos = -1
		l.NumSignPos = -1
		l.NumForm = NumberFormInt
		if i < 0 || i > n {
			return makeFault(pos)
		}
		if i == n {
			if !final {
				return makeNeedMore(numPhaseNone)
			}
			return makeFault(n)
		}
		if src[i] == '-' {
			i++
			ph = numPhaseAfterMinus
		} else if src[i] == '0' {
			i++
			ph = numPhaseZero
		} else if src[i] >= '1' && src[i] <= '9' {
			i++
			ph = numPhaseInt
		} else {
			return makeFault(i)
		}
	}

	for {
		switch ph {
		case numPhaseAfterMinus:
			if i >= n {
				if !final {
					return makeNeedMore(ph)
				}
				return makeFault(n)
			}
			if src[i] == '0' {
				i++
				ph = numPhaseZero
			} else if src[i] >= '1' && src[i] <= '9' {
				i++
				ph = numPhaseInt
			} else {
				return makeFault(i)
			}

		case numPhaseInt:
			for i < n && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			if i >= n {
				if !final {
					return makeNeedMore(numPhaseInt)
				}
				return makeComplete(i)
			}
			if src[i] == '.' {
				l.NumDotPos = i
				l.NumForm = NumberFormFrac
				i++
				ph = numPhaseAfterDot
				continue
			}
			if src[i] == 'e' || src[i] == 'E' {
				l.NumExpPos = i
				l.NumForm = NumberFormExp
				i++
				ph = numPhaseAfterExp
				continue
			}
			return makeComplete(i)

		case numPhaseZero:
			if i >= n {
				if !final {
					return makeNeedMore(numPhaseZero)
				}
				return makeComplete(i)
			}
			if src[i] == '.' {
				l.NumDotPos = i
				l.NumForm = NumberFormFrac
				i++
				ph = numPhaseAfterDot
				continue
			}
			if src[i] == 'e' || src[i] == 'E' {
				l.NumExpPos = i
				l.NumForm = NumberFormExp
				i++
				ph = numPhaseAfterExp
				continue
			}
			return makeComplete(i)

		case numPhaseAfterDot:
			if i >= n {
				if !final {
					return makeNeedMore(ph)
				}
				return makeFault(n)
			}
			if src[i] < '0' || src[i] > '9' {
				return makeFault(i)
			}
			i++
			ph = numPhaseFrac

		case numPhaseFrac:
			for i < n && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			if i >= n {
				if !final {
					return makeNeedMore(ph)
				}
				return makeComplete(i)
			}
			if src[i] == 'e' || src[i] == 'E' {
				l.NumExpPos = i
				l.NumForm = NumberFormFracExp
				i++
				ph = numPhaseAfterExp
				continue
			}
			return makeComplete(i)

		case numPhaseAfterExp:
			if i >= n {
				if !final {
					return makeNeedMore(ph)
				}
				return makeFault(n)
			}
			if src[i] == '+' || src[i] == '-' {
				l.NumSignPos = i
				i++
				ph = numPhaseAfterExpSign
				continue
			}
			if src[i] < '0' || src[i] > '9' {
				return makeFault(i)
			}
			i++
			ph = numPhaseExpDigits

		case numPhaseAfterExpSign:
			if i >= n {
				if !final {
					return makeNeedMore(ph)
				}
				return makeFault(n)
			}
			if src[i] < '0' || src[i] > '9' {
				return makeFault(i)
			}
			i++
			ph = numPhaseExpDigits

		case numPhaseExpDigits:
			for i < n && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			if i >= n {
				if !final {
					return makeNeedMore(ph)
				}
			}
			return makeComplete(i)
		}
	}
}
