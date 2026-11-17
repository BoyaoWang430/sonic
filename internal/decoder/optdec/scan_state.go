//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"encoding/binary"
	"math"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

const (
	streamPhaseValue uint8 = iota
	streamPhaseObjectOpen
	streamPhaseObjectKey
	streamPhaseObjectColon
	streamPhaseObjectAfterVal
	streamPhaseArrayOpen
	streamPhaseArrayAfterVal
	streamPhaseValueString
	streamPhaseKeyString
)

var exactPow10 = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7,
	1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15,
	1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

type streamTraversalBlock struct {
	base     int
	end      int
	nonspace uint64
}

func (b *streamTraversalBlock) reset() {
	b.base = 0
	b.end = 0
	b.nonspace = 0
}

func (b *streamTraversalBlock) skipSpace(src string, pos int) int {
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		return pos
	}
	if uint(pos+1) < uint(len(src)) && src[pos] == ' ' && src[pos+1] > ' ' {
		return pos + 1
	}
	return b.skipSpaceSlow(src, pos)
}

func (b *streamTraversalBlock) skipSpaceSlow(src string, pos int) int {
	for pos < len(src) {
		c := src[pos]
		if c > ' ' {
			return pos
		}
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return pos
		}
		if pos+1 < len(src) && src[pos+1] > ' ' {
			return pos + 1
		}
		if pos < b.base || pos >= b.end {
			_, ns, width := kernels.TraversalMasks(src, pos, false)
			b.base = pos
			b.end = pos + width
			b.nonspace = ns
		}
		mask := b.nonspace >> uint(pos-b.base)
		if mask != 0 {
			return pos + bits.TrailingZeros64(mask)
		}
		pos = b.end
	}
	return len(src)
}

func (s *StreamSession) growNodes(remBytes int, eof bool) {
	p := s.p
	curLen := len(p.nodes)
	nextCap := curLen * 4
	if eof && curLen >= defaultNodesCount {
		nextCap = curLen * 2
	}
	if nextCap < 64 {
		nextCap = 64
	}
	if eof && remBytes > 0 {
		remMax := s.nodeLen + calMaxNodeCap(remBytes)
		if nextCap > remMax && remMax >= s.nodeLen+64 {
			nextCap = remMax
		}
	}
	retainInPool := nextCap <= maxPooledNodesCount
	grown := make([]node, nextCap)
	if s.nodeLen > 0 {
		copy(grown[:s.nodeLen], p.nodes[:s.nodeLen])
	}
	if !retainInPool && p.backup == nil && cap(p.nodes) <= maxPooledNodesCount {
		p.backup = p.nodes
	}
	p.nodes = grown
}

func (s *StreamSession) emitNode(typ, val uint64, remBytes int, eof bool) int {
	if s.nodeLen+1 >= len(s.p.nodes) {
		s.growNodes(remBytes, eof)
	}
	idx := s.nodeLen
	s.p.nodes[idx] = node{typ: typ, val: val}
	s.nodeLen = idx + 1
	return idx
}

// utf8SafePrefixEnd returns the end offset up to which src[from:limit] ends on a
// complete UTF-8 sequence boundary when !eof, holding back 1..3 bytes of a
// potentially valid split multi-byte UTF-8 rune at the chunk boundary.
func utf8SafePrefixEnd(src string, from, limit int, eof bool) int {
	if limit > len(src) {
		limit = len(src)
	}
	if eof || limit <= from {
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
			return limit // invalid leading byte; let ValidateString catch it now
		}
		if limit-i < need {
			return i
		}
		return limit
	}
	return limit
}

func (s *StreamSession) checkConsumedUTF8(src string, limit int, eof bool) {
	if !s.validateStr || s.utf8Invalid {
		return
	}
	end := utf8SafePrefixEnd(src, s.utf8Checked, limit, eof)
	if end <= s.utf8Checked {
		return
	}
	if !utf8.ValidateString(src[s.utf8Checked:end]) {
		s.utf8Invalid = true
		return
	}
	s.utf8Checked = end
}

// safeStringResumeOffset returns the furthest offset <= len(src) from which
// string scanning can resume on the next chunk without splitting an incomplete
// trailing '\' or '\uXXXX' escape sequence.
func safeStringResumeOffset(src string, floor int) int {
	n := len(src)
	p := n - 6
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
		if src[q+1] == 'u' {
			if q+6 > n {
				return q
			}
			q += 6
		} else {
			q += 2
		}
	}
	return n
}

func mapUnquoteStatus(st kernels.KernelStatus) ErrorCode {
	switch st {
	case kernels.StatusInvalidUnicode, kernels.StatusEOF:
		return SONIC_INVALID_ESCAPED_UTF
	default:
		return SONIC_INVALID_ESCAPED
	}
}

// Open containers store (uint32(parentIdx) << 32) | uint32(elemCount) in node.val
// until closed, preserving all 32 bits for element/pair counts (instead of 24 bits
// in node.typ) while keeping node.typ untouched as (pos << 32) | kind.
func packOpenContainerVal(parentIdx int64) uint64 {
	return uint64(uint32(parentIdx)) << 32
}

func unpackOpenParentIdx(openVal uint64) int64 {
	return int64(int32(uint32(openVal >> 32)))
}

func (s *StreamSession) failEOF(srcLen int) ErrorCode {
	s.cur = srcLen
	s.errPos = srcLen
	return SONIC_EOF
}

func (s *StreamSession) failAt(errOffset, nextCur, srcLen int, code ErrorCode) ErrorCode {
	if nextCur > srcLen {
		nextCur = srcLen
	}
	s.cur = nextCur
	s.errPos = errOffset
	return code
}

func (s *StreamSession) syntaxError(src string, code ErrorCode) error {
	if s.errPos >= 0 {
		return buildSyntaxErrorAt(src, s.errPos, code)
	}
	return syntaxErrorFromAfterCursor(src, s.cur, code)
}

// isValidNumberPrefixToEnd reports whether src[pos:] is a syntactically valid
// (possibly incomplete) prefix of a JSON number reaching the end of src.
func isValidNumberPrefixToEnd(src string, pos int) bool {
	n := len(src)
	if pos >= n {
		return true
	}
	i := pos
	if src[i] == '-' {
		i++
		if i == n {
			return true
		}
	}
	if src[i] == '0' {
		i++
		if i == n {
			return true
		}
	} else if src[i] >= '1' && src[i] <= '9' {
		i++
		for i < n && src[i] >= '0' && src[i] <= '9' {
			i++
		}
		if i == n {
			return true
		}
	} else {
		return false
	}
	if src[i] == '.' {
		i++
		for i < n && src[i] >= '0' && src[i] <= '9' {
			i++
		}
		if i == n {
			return true
		}
	}
	if src[i] == 'e' || src[i] == 'E' {
		i++
		if i == n {
			return true
		}
		if src[i] == '+' || src[i] == '-' {
			i++
			if i == n {
				return true
			}
		}
		for i < n && src[i] >= '0' && src[i] <= '9' {
			i++
		}
		return i == n
	}
	return false
}

func swarPlainQuoteLen64Str(src string, rawStart int) int {
	rem := len(src) - rawStart
	if rem < 8 {
		return -1
	}
	b := unsafe.Slice(unsafe.StringData(src), len(src))
	maxWords := rem >> 3
	if maxWords > 8 {
		maxWords = 8
	}
	off := rawStart
	for wIdx := 0; wIdx < maxWords; wIdx++ {
		w := binary.LittleEndian.Uint64(b[off : off+8 : off+8])
		xq := w ^ 0x2222222222222222
		q := (xq - 0x0101010101010101) & ^xq & 0x8080808080808080
		xb := w ^ 0x5c5c5c5c5c5c5c5c
		bs := (xb - 0x0101010101010101) & ^xb & 0x8080808080808080
		ctrl := (w - 0x2020202020202020) & ^w & 0x8080808080808080
		if q != 0 {
			lsb := q & -q
			if (bs|ctrl)&(lsb-1) == 0 {
				return (wIdx << 3) + (bits.TrailingZeros64(lsb) >> 3)
			}
			return -1
		}
		if (bs | ctrl) != 0 {
			return -1
		}
		off += 8
	}
	return -1
}

// scanStringStep scans or resumes a string starting at s.tokStart ('"').
// Returns (done, stopReason, errCode). When done is true, the string node has
// been emitted into s.p.nodes and s.cur sits right after the closing '"'.
func (s *StreamSession) scanStringStep(src string, eof bool, isKey bool) (bool, scanStopReason, ErrorCode) {
	pos := s.strScanOff
	if s.strFirstEsc < 0 && pos == s.tokStart+1 {
		if slen := swarPlainQuoteLen64Str(src, pos); slen >= 0 {
			s.emitNode(KStringCommon|uint64(pos)<<PosBits, uint64(slen), len(src)-s.tokStart, eof)
			if !isKey {
				s.p.nbuf.stat.str++
			}
			s.strScanOff = 0
			s.cur = pos + slen + 1
			return true, scanStopDone, SONIC_OK
		}
	}

	tail := src[pos:]
	var endRel, escRel, errRel int
	var perr types.ParsingError
	if len(tail) == 0 {
		perr = types.ERR_EOF
	} else {
		endRel, escRel, errRel, perr = kernels.ScanString(src, pos-1, s.validateStr)
		endRel -= pos
		if escRel >= 0 {
			escRel -= pos
		}
		if errRel >= 0 {
			errRel -= pos
		}
	}

	if perr != 0 {
		if perr == types.ERR_EOF && !eof {
			if s.strFirstEsc < 0 && escRel >= 0 {
				s.strFirstEsc = pos + escRel
			}
			s.strScanOff = safeStringResumeOffset(src, pos)
			return false, scanStopNeedMore, SONIC_OK
		}
		if perr == types.ERR_EOF {
			return false, scanStopSyntaxError, s.failEOF(len(src))
		}
		errPos := pos + errRel
		if errPos < len(src) && src[errPos] < 0x20 {
			return false, scanStopSyntaxError, s.failAt(errPos, errPos+1, len(src), SONIC_CONTROL_CHAR)
		}
		if errPos+1 < len(src) && src[errPos] == '\\' && src[errPos+1] == 'u' {
			return false, scanStopSyntaxError, s.failAt(errPos, errPos+1, len(src), SONIC_INVALID_ESCAPED_UTF)
		}
		return false, scanStopSyntaxError, s.failAt(errPos, errPos+1, len(src), SONIC_INVALID_ESCAPED)
	}

	end := pos + endRel
	firstEsc := s.strFirstEsc
	if firstEsc < 0 && escRel >= 0 {
		firstEsc = pos + escRel
	}
	rawStart := s.tokStart + 1
	rawContentEnd := end - 1

	if firstEsc < 0 {
		s.emitNode(KStringCommon|uint64(rawStart)<<PosBits, uint64(rawContentEnd-rawStart), len(src)-s.tokStart, eof)
	} else {
		uoff := len(s.unesc)
		prefixLen := firstEsc - rawStart
		maxOut := rawContentEnd - rawStart
		if cap(s.unesc)-uoff < maxOut {
			newCap := cap(s.unesc) * 2
			if newCap < uoff+maxOut {
				newCap = uoff + maxOut
			}
			grown := make([]byte, uoff, newCap)
			copy(grown, s.unesc)
			s.unesc = grown
		}
		s.unesc = append(s.unesc, src[rawStart:firstEsc]...)
		escTail := src[firstEsc:rawContentEnd]
		dst := s.unesc[uoff+prefixLen : uoff+maxOut]
		written, errOff, ustatus := kernels.UnquoteStatus(escTail, dst, s.unquoteFlags)
		if ustatus != kernels.StatusOK {
			s.unesc = s.unesc[:uoff]
			errAt := firstEsc + errOff
			return false, scanStopSyntaxError, s.failAt(errAt, errAt+1, len(src), mapUnquoteStatus(ustatus))
		}
		ulen := prefixLen + written
		s.unesc = s.unesc[:uoff+ulen]
		packedVal := escapedInUnescFlag | (uint64(uoff) << 32) | uint64(uint32(ulen))
		s.emitNode(KStringEscaped|uint64(rawStart)<<PosBits, packedVal, len(src)-s.tokStart, eof)
	}

	if !isKey {
		s.p.nbuf.stat.str++
	}
	s.strScanOff = 0
	s.cur = end
	return true, scanStopDone, SONIC_OK
}

func (s *StreamSession) finishStep(src string, reason scanStopReason, code ErrorCode) (scanStopReason, ErrorCode) {
	p := s.p
	p.nbuf.nlen = s.nodeLen
	p.pos = s.cur
	switch reason {
	case scanStopNeedMore:
		limit := s.cur
		if s.strScanOff > limit {
			limit = s.strScanOff
		}
		s.checkConsumedUTF8(src, limit, false)
	default:
		s.checkConsumedUTF8(src, s.cur, true)
	}
	return reason, code
}

// Step advances the unified resumable JSON grammar state machine over src
// (complete inputs pass eof=true; chunked stream inputs pass eof=false until
// the final io.Reader chunk).
func (s *StreamSession) Step(src string, eof bool) (scanStopReason, ErrorCode) {
	p := s.p
	var parent *node

	for {
		switch s.phase {
		case streamPhaseValue:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			ch := src[pos]
			switch ch {
			case '{':
				idx := s.emitNode(KObject|uint64(pos)<<PosBits, packOpenContainerVal(p.nbuf.parent), len(src)-pos, eof)
				p.nbuf.parent = int64(idx)
				p.nbuf.depth++
				if p.nbuf.depth > consts.MaxStack {
					s.cur = pos + 1
					return s.finishStep(src, scanStopSyntaxError, SONIC_STACK_OVERFLOW)
				}
				s.cur = pos + 1
				s.phase = streamPhaseObjectOpen
				continue

			case '[':
				idx := s.emitNode(KArray|uint64(pos)<<PosBits, packOpenContainerVal(p.nbuf.parent), len(src)-pos, eof)
				p.nbuf.parent = int64(idx)
				p.nbuf.depth++
				if p.nbuf.depth > consts.MaxStack {
					s.cur = pos + 1
					return s.finishStep(src, scanStopSyntaxError, SONIC_STACK_OVERFLOW)
				}
				s.cur = pos + 1
				s.phase = streamPhaseArrayOpen
				continue

			case '"':
				s.tokStart = pos
				s.cur = pos + 1
				s.strScanOff = pos + 1
				s.strFirstEsc = -1
				s.phase = streamPhaseValueString
				ok, reason, code := s.scanStringStep(src, eof, false)
				if !ok {
					return s.finishStep(src, reason, code)
				}
				goto afterValue

			case 't', 'f', 'n':
				want := "null"
				kind := uint64(KNull)
				if ch == 't' {
					want, kind = "true", KTrue
				} else if ch == 'f' {
					want, kind = "false", KFalse
				}
				avail := len(src) - pos
				if avail >= len(want) && src[pos:pos+len(want)] == want {
					s.cur = pos + len(want)
					s.emitNode(kind|uint64(pos)<<PosBits, 0, len(src)-pos, eof)
					goto afterValue
				}
				limit := len(want)
				if avail < limit {
					limit = avail
				}
				for i := 1; i < limit; i++ {
					if src[pos+i] != want[i] {
						s.cur = pos + i + 1
						return s.finishStep(src, scanStopSyntaxError, SONIC_INVALID_LITERAL)
					}
				}
				if avail < len(want) {
					if !eof {
						return s.finishStep(src, scanStopNeedMore, SONIC_OK)
					}
					return s.finishStep(src, scanStopSyntaxError, s.failAt(len(src), len(src), len(src), SONIC_INVALID_LITERAL))
				}
				for i := 1; i < len(want); i++ {
					if src[pos+i] != want[i] {
						s.cur = pos + i + 1
						break
					}
				}
				return s.finishStep(src, scanStopSyntaxError, SONIC_INVALID_LITERAL)

			case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				if !s.useNumber {
					neg := ch == '-'
					q := pos
					if neg {
						q++
					}
					if q < len(src) {
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
									if !neg {
										s.cur = q
										s.emitNode(KUint|uint64(pos)<<PosBits, v, len(src)-pos, eof)
										p.nbuf.stat.number++
										goto afterValue
									} else if v <= 1<<63 {
										s.cur = q
										s.emitNode(KSint|uint64(pos)<<PosBits, -v, len(src)-pos, eof)
										p.nbuf.stat.number++
										goto afterValue
									}
								} else if term == '.' && intDigits <= 15 && q+1 < len(src) && src[q+1]-'0' <= 9 {
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
									if q < len(src) && fracDigits >= 1 && v>>52 == 0 {
										term2 := src[q]
										if term2 != '.' && term2 != 'e' && term2 != 'E' && (term2-'0' > 9) {
											fv := float64(v)
											if neg {
												fv = -fv
											}
											fv /= exactPow10[fracDigits]
											s.cur = q
											s.emitNode(KReal|uint64(pos)<<PosBits, math.Float64bits(fv), len(src)-pos, eof)
											p.nbuf.stat.number++
											goto afterValue
										}
									}
								}
							}
						} else if c0 == '0' && q+1 < len(src) {
							term := src[q+1]
							if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
								s.cur = q + 1
								typ := uint64(KUint)
								if neg {
									typ = KSint
								}
								s.emitNode(typ|uint64(pos)<<PosBits, 0, len(src)-pos, eof)
								p.nbuf.stat.number++
								goto afterValue
							}
						}
					}
				}

				end, perr := kernels.ScanNumberStrict(src, pos)
				if !eof && (end == len(src) || isValidNumberPrefixToEnd(src, pos)) {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				if perr != 0 {
					return s.finishStep(src, scanStopSyntaxError, s.failAt(end, end+1, len(src), SONIC_INVALID_NUM))
				}
				if s.useNumber {
					s.cur = end
					s.emitNode(KRawNumber|uint64(pos)<<PosBits, uint64(end-pos), len(src)-pos, eof)
					p.nbuf.stat.number++
					goto afterValue
				}
				neg := ch == '-'
				bodyOff := pos
				if neg {
					bodyOff++
				}
				var num cParityNum
				var code ErrorCode
				if end+8 <= len(src) {
					term := src[end]
					if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
						_, num, code = cParityParseNumberValue(unsafe.Pointer(rt.IndexChar(src, bodyOff)), num, neg, p.dbuf)
						goto finishNum
					}
				}
				{
					bodyLen := end - bodyOff
					var stackBuf [128]byte
					if bodyLen+8 <= len(stackBuf) {
						copy(stackBuf[:bodyLen], src[bodyOff:end])
						stackBuf[bodyLen] = ' '
						_, num, code = cParityParseNumberValue(unsafe.Pointer(&stackBuf[0]), num, neg, p.dbuf)
					} else {
						savedLen := len(s.unesc)
						s.unesc = append(s.unesc, src[bodyOff:end]...)
						s.unesc = append(s.unesc, "        "...)
						_, num, code = cParityParseNumberValue(unsafe.Pointer(&s.unesc[savedLen]), num, neg, p.dbuf)
						s.unesc = s.unesc[:savedLen]
					}
				}
			finishNum:
				if code == SONIC_FLOAT_INF && s.deferFloatOverflow {
					s.cur = end
					s.emitNode(KRawNumber|uint64(pos)<<PosBits, uint64(end-pos), len(src)-pos, eof)
					p.nbuf.stat.number++
					goto afterValue
				}
				if code != SONIC_OK {
					s.cur = end
					return s.finishStep(src, scanStopSyntaxError, code)
				}
				s.cur = end
				s.emitNode(num.typ|uint64(pos)<<PosBits, num.val, len(src)-pos, eof)
				p.nbuf.stat.number++
				goto afterValue

			default:
				s.cur = pos + 1
				return s.finishStep(src, scanStopSyntaxError, SONIC_INVALID_CHAR)
			}

		case streamPhaseValueString:
			ok, reason, code := s.scanStringStep(src, eof, false)
			if !ok {
				return s.finishStep(src, reason, code)
			}
			goto afterValue

		case streamPhaseKeyString:
			ok, reason, code := s.scanStringStep(src, eof, true)
			if !ok {
				return s.finishStep(src, reason, code)
			}
			s.phase = streamPhaseObjectColon
			fallthrough

		case streamPhaseObjectColon:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			if src[pos] != ':' {
				s.cur = pos + 1
				return s.finishStep(src, scanStopSyntaxError, SONIC_EXPECT_COLON)
			}
			s.cur = pos + 1
			s.phase = streamPhaseValue
			continue

		case streamPhaseObjectOpen:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			ch := src[pos]
			if ch == '}' {
				s.cur = pos + 1
				goto closeContainer
			}
			if ch != '"' {
				s.cur = pos + 1
				return s.finishStep(src, scanStopSyntaxError, SONIC_EXPECT_KEY)
			}
			s.tokStart = pos
			s.cur = pos + 1
			s.strScanOff = pos + 1
			s.strFirstEsc = -1
			s.phase = streamPhaseKeyString
			continue

		case streamPhaseObjectKey:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			if src[pos] != '"' {
				s.cur = pos + 1
				return s.finishStep(src, scanStopSyntaxError, SONIC_EXPECT_KEY)
			}
			s.tokStart = pos
			s.cur = pos + 1
			s.strScanOff = pos + 1
			s.strFirstEsc = -1
			s.phase = streamPhaseKeyString
			continue

		case streamPhaseArrayOpen:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			if src[pos] == ']' {
				s.cur = pos + 1
				goto closeContainer
			}
			s.phase = streamPhaseValue
			continue

		case streamPhaseObjectAfterVal:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			ch := src[pos]
			s.cur = pos + 1
			if ch == ',' {
				s.phase = streamPhaseObjectKey
				continue
			}
			if ch == '}' {
				goto closeContainer
			}
			return s.finishStep(src, scanStopSyntaxError, SONIC_EXPECT_OBJ_COMMA_OR_END)

		case streamPhaseArrayAfterVal:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return s.finishStep(src, scanStopNeedMore, SONIC_OK)
				}
				return s.finishStep(src, scanStopSyntaxError, s.failEOF(len(src)))
			}
			ch := src[pos]
			s.cur = pos + 1
			if ch == ',' {
				s.phase = streamPhaseValue
				continue
			}
			if ch == ']' {
				goto closeContainer
			}
			return s.finishStep(src, scanStopSyntaxError, SONIC_EXPECT_ARR_COMMA_OR_END)
		}

	afterValue:
		if p.nbuf.parent == -1 {
			return s.finishStep(src, scanStopDone, SONIC_OK)
		}
		parent = &p.nodes[p.nbuf.parent]
		parent.val++
		if uint8(parent.typ) == KObject {
			s.phase = streamPhaseObjectAfterVal
		} else {
			s.phase = streamPhaseArrayAfterVal
		}
		continue

	closeContainer:
		pIdx := int(p.nbuf.parent)
		cnode := &p.nodes[pIdx]
		openVal := cnode.val
		count := uint32(openVal)
		kind := uint8(cnode.typ)
		if kind == KObject {
			p.nbuf.stat.object++
			p.nbuf.stat.object_keys += count
		} else {
			p.nbuf.stat.array++
			p.nbuf.stat.array_elems += count
		}
		prevParent := unpackOpenParentIdx(openVal)
		p.nbuf.parent = prevParent
		span := uint64(s.nodeLen - pIdx)
		cnode.val = (span << 32) | uint64(count)
		if uint32(p.nbuf.depth) > p.nbuf.stat.max_depth {
			p.nbuf.stat.max_depth = uint32(p.nbuf.depth)
		}
		p.nbuf.depth--
		if prevParent == -1 {
			return s.finishStep(src, scanStopDone, SONIC_OK)
		}
		outer := &p.nodes[prevParent]
		outer.val++
		if uint8(outer.typ) == KObject {
			s.phase = streamPhaseObjectAfterVal
		} else {
			s.phase = streamPhaseArrayAfterVal
		}
	}
}
