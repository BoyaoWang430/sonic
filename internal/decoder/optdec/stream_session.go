//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"math/bits"
	"reflect"
	"strconv"
	"sync"
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

const maxRetainedStreamUnescCap = 64 << 10
const initialStreamNodesCap = 512

var streamSessionPool = sync.Pool{
	New: func() interface{} {
		return &StreamSession{
			p: &Parser{
				nodes: make([]node, initialStreamNodesCap, initialStreamNodesCap),
				dbuf:  make([]byte, types.MaxDigitNums, types.MaxDigitNums),
			},
			unesc: make([]byte, 0, 256),
		}
	},
}

func getStreamSession() *StreamSession {
	return streamSessionPool.Get().(*StreamSession)
}

// FreeStreamSession resets and returns a StreamSession to the pool once a
// StreamDecoder drains its current buffer or terminates.
func FreeStreamSession(s *StreamSession) {
	if s == nil {
		return
	}
	s.Reset()
	s.lastPtrType = nil
	s.lastElemType = nil
	s.lastDec = nil
	s.lastIsNamed = false
	s.lastIsEface = false
	streamSessionPool.Put(s)
}

// StreamSession maintains a resumable, bounded JSON scan & Tape state across
// incremental io.Reader chunks for StreamDecoder. Already-scanned prefixes of a
// multi-chunk value are never rescanned on refill, and raw input coordinates
// remain decoupled from unescaped string scratch (p.unesc).
type StreamSession struct {
	p            *Parser
	ctx          Context
	unesc        []byte
	lastPtrType  reflect.Type
	lastElemType reflect.Type
	lastDec      decFunc
	cur          int
	nodeLen      int
	tokStart     int
	strScanOff   int
	strFirstEsc  int
	utf8Checked  int
	phase        uint8
	active       bool
	utf8Invalid  bool
	useNumber    bool
	validateStr  bool
	lastIsNamed  bool
	lastIsEface  bool
	unquoteFlags uint64
	trav         streamTraversalBlock
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

func (s *StreamSession) ensureParser() *Parser {
	if s.p == nil {
		s.p = &Parser{
			nodes: make([]node, defaultNodesCap, defaultNodesCap),
			dbuf:  make([]byte, types.MaxDigitNums, types.MaxDigitNums),
		}
	}
	if s.unesc == nil {
		s.unesc = make([]byte, 0, 256)
	}
	return s.p
}

// Begin prepares or resets the session for scanning a new root JSON value.
func (s *StreamSession) Begin(opts uint64, isEface bool) {
	p := s.ensureParser()
	if p.backup != nil {
		p.nodes = p.backup
		p.backup = nil
	}
	if cap(s.unesc) > maxRetainedStreamUnescCap {
		s.unesc = make([]byte, 0, 256)
	} else {
		s.unesc = s.unesc[:0]
	}
	p.nbuf = nodeBuf{parent: -1}
	p.resetSlabs()
	p.Utf8Inv = false
	p.isEface = isEface
	s.cur = 0
	s.nodeLen = 0
	s.tokStart = 0
	s.strScanOff = 0
	s.strFirstEsc = -1
	s.utf8Checked = 0
	s.phase = streamPhaseValue
	s.active = true
	s.utf8Invalid = false
	s.useNumber = (opts & (1 << _F_use_number)) != 0
	s.validateStr = (opts & (1 << _F_validate_string)) != 0
	if opts&(1<<_F_disable_urc) != 0 {
		s.unquoteFlags = 0
	} else {
		s.unquoteFlags = types.F_UNICODE_REPLACE
	}
	s.trav.reset()
}

// Reset aborts any in-progress scan and releases oversized scratch buffers.
func (s *StreamSession) Reset() {
	if s == nil {
		return
	}
	s.active = false
	if s.p != nil {
		if s.p.backup != nil {
			s.p.nodes = s.p.backup
			s.p.backup = nil
		}
		s.p.Json = ""
		s.p.unesc = nil
		s.p.resetSlabs()
		s.p.nbuf = nodeBuf{parent: -1}
	}
	if cap(s.unesc) > maxRetainedStreamUnescCap {
		s.unesc = nil
	} else if s.unesc != nil {
		s.unesc = s.unesc[:0]
	}
	s.trav.reset()
}

func (s *StreamSession) growNodes() {
	p := s.p
	curLen := len(p.nodes)
	nextCap := curLen * 4
	if nextCap < 64 {
		nextCap = 64
	}
	retainInPool := uintptr(nextCap) <= maxPooledNodesCap
	grown := make([]node, nextCap)
	if s.nodeLen > 0 {
		copy(grown[:s.nodeLen], p.nodes[:s.nodeLen])
	}
	if !retainInPool && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
		p.backup = p.nodes
	}
	p.nodes = grown
}

func (s *StreamSession) emitNode(typ, val uint64) int {
	if s.nodeLen+1 >= len(s.p.nodes) {
		s.growNodes()
	}
	idx := s.nodeLen
	s.p.nodes[idx] = node{typ: typ, val: val}
	s.nodeLen = idx + 1
	return idx
}

// utf8SafePrefixEnd returns the end offset up to which src[from:] ends on a
// complete UTF-8 sequence boundary when !eof, holding back 1..3 bytes of a
// potentially valid split multi-byte UTF-8 rune at the chunk boundary.
func utf8SafePrefixEnd(src string, from int, eof bool) int {
	n := len(src)
	if eof || n <= from {
		return n
	}
	limit := n - 3
	if limit < from {
		limit = from
	}
	for i := n - 1; i >= limit; i-- {
		b := src[i]
		if b < 0x80 {
			return n
		}
		if (b & 0xc0) == 0x80 {
			continue
		}
		// Leading byte of a multi-byte sequence at index i.
		var need int
		switch {
		case b >= 0xc2 && b <= 0xdf:
			need = 2
		case b >= 0xe0 && b <= 0xef:
			need = 3
		case b >= 0xf0 && b <= 0xf4:
			need = 4
		default:
			return n // invalid leading byte; let ValidateString catch it now
		}
		if n-i < need {
			return i
		}
		return n
	}
	return n
}

func (s *StreamSession) checkIncrementalUTF8(src string, eof bool) {
	if !s.validateStr || s.utf8Invalid {
		return
	}
	end := utf8SafePrefixEnd(src, s.utf8Checked, eof)
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

func mapUnquoteError(perr types.ParsingError) ErrorCode {
	switch perr {
	case types.ERR_INVALID_UNICODE, types.ERR_INVALID_CHAR, types.ERR_EOF:
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

func eofCursor(n int) int {
	if n > 0 {
		return n + 1
	}
	return 0
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

// scanStringStep scans or resumes a string starting at s.tokStart ('"').
// Returns (done, stopReason, errCode). When done is true, the string node has
// been emitted into s.p.nodes and s.cur sits right after the closing '"'.
func (s *StreamSession) scanStringStep(src string, eof bool, isKey bool) (bool, scanStopReason, ErrorCode) {
	pos := s.strScanOff
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
			s.cur = eofCursor(len(src))
			return false, scanStopSyntaxError, SONIC_EOF
		}
		errPos := pos + errRel
		if errPos < len(src) && src[errPos] < 0x20 {
			s.cur = errPos + 1
			return false, scanStopSyntaxError, SONIC_CONTROL_CHAR
		}
		// Distinguish invalid \uXXXX hex vs invalid \x escape for exact C parity.
		if errPos+1 < len(src) && src[errPos] == '\\' && src[errPos+1] == 'u' {
			s.cur = errPos + 1
			return false, scanStopSyntaxError, SONIC_INVALID_ESCAPED_UTF
		}
		s.cur = errPos + 1
		return false, scanStopSyntaxError, SONIC_INVALID_ESCAPED
	}

	end := pos + endRel
	firstEsc := s.strFirstEsc
	if firstEsc < 0 && escRel >= 0 {
		firstEsc = pos + escRel
	}
	rawStart := s.tokStart + 1
	rawContentEnd := end - 1

	if firstEsc < 0 {
		s.emitNode(KStringCommon|uint64(rawStart)<<PosBits, uint64(rawContentEnd-rawStart))
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
		written, errOff, uerr := kernels.Unquote(escTail, dst, s.unquoteFlags)
		if uerr != 0 {
			s.unesc = s.unesc[:uoff]
			s.cur = firstEsc + errOff + 1
			if s.cur > len(src) {
				s.cur = len(src)
			}
			return false, scanStopSyntaxError, mapUnquoteError(uerr)
		}
		ulen := prefixLen + written
		s.unesc = s.unesc[:uoff+ulen]
		packedVal := (uint64(uoff) << 32) | uint64(uint32(ulen))
		s.emitNode(KStringEscaped|uint64(rawStart)<<PosBits, packedVal)
	}

	if !isKey {
		s.p.nbuf.stat.str++
	}
	s.cur = end
	return true, scanStopDone, SONIC_OK
}

// Step advances the resumable scan over src (which always starts at offset 0 of
// the current root JSON value and grows as io.Reader delivers more bytes).
func (s *StreamSession) Step(src string, eof bool) (scanStopReason, ErrorCode) {
	s.checkIncrementalUTF8(src, eof)
	p := s.p
	var parent *node

	for {
		switch s.phase {
		case streamPhaseValue:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
			}
			ch := src[pos]
			switch ch {
			case '{':
				idx := s.emitNode(KObject|uint64(pos)<<PosBits, packOpenContainerVal(p.nbuf.parent))
				p.nbuf.parent = int64(idx)
				p.nbuf.depth++
				if p.nbuf.depth > consts.MaxStack {
					s.cur = pos + 1
					return scanStopSyntaxError, SONIC_STACK_OVERFLOW
				}
				s.cur = pos + 1
				s.phase = streamPhaseObjectOpen
				continue

			case '[':
				idx := s.emitNode(KArray|uint64(pos)<<PosBits, packOpenContainerVal(p.nbuf.parent))
				p.nbuf.parent = int64(idx)
				p.nbuf.depth++
				if p.nbuf.depth > consts.MaxStack {
					s.cur = pos + 1
					return scanStopSyntaxError, SONIC_STACK_OVERFLOW
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
					return reason, code
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
					s.emitNode(kind|uint64(pos)<<PosBits, 0)
					goto afterValue
				}
				limit := len(want)
				if avail < limit {
					limit = avail
				}
				for i := 1; i < limit; i++ {
					if src[pos+i] != want[i] {
						s.cur = pos + i + 1
						return scanStopSyntaxError, SONIC_INVALID_LITERAL
					}
				}
				if avail < len(want) {
					if !eof {
						return scanStopNeedMore, SONIC_OK
					}
					s.cur = eofCursor(len(src))
					return scanStopSyntaxError, SONIC_INVALID_LITERAL
				}
				// Mismatch occurred within full length.
				for i := 1; i < len(want); i++ {
					if src[pos+i] != want[i] {
						s.cur = pos + i + 1
						break
					}
				}
				return scanStopSyntaxError, SONIC_INVALID_LITERAL

			case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				if !s.useNumber {
					if ch >= '1' && ch <= '9' {
						v := uint64(ch - '0')
						q := pos + 1
						lim := pos + 19
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
						if q < len(src) {
							term := src[q]
							if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
								s.cur = q
								s.emitNode(KUint|uint64(pos)<<PosBits, v)
								p.nbuf.stat.number++
								goto afterValue
							}
						}
					} else if ch == '0' && pos+1 < len(src) {
						term := src[pos+1]
						if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
							s.cur = pos + 1
							s.emitNode(KUint|uint64(pos)<<PosBits, 0)
							p.nbuf.stat.number++
							goto afterValue
						}
					}
				}

				end, perr := kernels.ScanNumberStrict(src, pos)
				if !eof && (end == len(src) || isValidNumberPrefixToEnd(src, pos)) {
					return scanStopNeedMore, SONIC_OK
				}
				if perr != 0 {
					s.cur = end + 1
					if s.cur > len(src) {
						s.cur = len(src)
					}
					return scanStopSyntaxError, SONIC_INVALID_NUM
				}
				if s.useNumber {
					s.cur = end
					s.emitNode(KRawNumber|uint64(pos)<<PosBits, uint64(end-pos))
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
				if code != SONIC_OK {
					s.cur = end
					return scanStopSyntaxError, code
				}
				s.cur = end
				s.emitNode(num.typ|uint64(pos)<<PosBits, num.val)
				p.nbuf.stat.number++
				goto afterValue

			default:
				s.cur = pos + 1
				return scanStopSyntaxError, SONIC_INVALID_CHAR
			}

		case streamPhaseValueString:
			ok, reason, code := s.scanStringStep(src, eof, false)
			if !ok {
				return reason, code
			}
			goto afterValue

		case streamPhaseKeyString:
			ok, reason, code := s.scanStringStep(src, eof, true)
			if !ok {
				return reason, code
			}
			s.phase = streamPhaseObjectColon
			fallthrough

		case streamPhaseObjectColon:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
			}
			if src[pos] != ':' {
				s.cur = pos + 1
				return scanStopSyntaxError, SONIC_EXPECT_COLON
			}
			s.cur = pos + 1
			s.phase = streamPhaseValue
			continue

		case streamPhaseObjectOpen:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
			}
			ch := src[pos]
			if ch == '}' {
				s.cur = pos + 1
				goto closeContainer
			}
			if ch != '"' {
				s.cur = pos + 1
				return scanStopSyntaxError, SONIC_EXPECT_KEY
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
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
			}
			if src[pos] != '"' {
				s.cur = pos + 1
				return scanStopSyntaxError, SONIC_EXPECT_KEY
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
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
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
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
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
			return scanStopSyntaxError, SONIC_EXPECT_OBJ_COMMA_OR_END

		case streamPhaseArrayAfterVal:
			pos := s.trav.skipSpace(src, s.cur)
			s.cur = pos
			if pos >= len(src) {
				if !eof {
					return scanStopNeedMore, SONIC_OK
				}
				s.cur = eofCursor(len(src))
				return scanStopSyntaxError, SONIC_EOF
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
			return scanStopSyntaxError, SONIC_EXPECT_ARR_COMMA_OR_END
		}

	afterValue:
		if p.nbuf.parent == -1 {
			return scanStopDone, SONIC_OK
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
			return scanStopDone, SONIC_OK
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

// DecodeStream drives incremental scan + single-pass materialization from the
// caller's stream buffer (starting at *scanp), calling refill when more chunk
// bytes are required from io.Reader.
func DecodeStream(buf *[]byte, scanp *int, f uint64, val interface{}, sessPtr **StreamSession, refillArg unsafe.Pointer, refill func(arg unsafe.Pointer, compactTo int) (eof bool, readErr error)) error {
	if val == nil {
		return &json.InvalidUnmarshalError{}
	}
	sess := *sessPtr
	if sess == nil {
		sess = getStreamSession()
		*sessPtr = sess
	}

	var etp reflect.Type
	var vp unsafe.Pointer
	var dec decFunc
	var isEface bool

	rv := reflect.ValueOf(val)
	vt := rv.Type()
	if vt == sess.lastPtrType && !sess.lastIsNamed {
		if rv.IsNil() {
			return &json.InvalidUnmarshalError{Type: vt}
		}
		etp = sess.lastElemType
		vp = rv.UnsafePointer()
		dec = sess.lastDec
		isEface = sess.lastIsEface
	} else {
		var err error
		etp, vp, err = unpackRootValue(val)
		if err != nil {
			return err
		}
		dec, err = findOrCompile(etp)
		if err != nil {
			return err
		}
		isEface = etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
		sess.lastPtrType = vt
		sess.lastElemType = etp
		sess.lastDec = dec
		sess.lastIsNamed = vt.Name() != ""
		sess.lastIsEface = isEface
	}

	// Force OptionCopyString because StreamDecoder recycles/slides *buf across calls.
	matOpts := f | (1 << _F_copy_string)
	sess.Begin(matOpts, isEface)

	rootStart := *scanp
	eof := false
	for {
		raw := (*buf)[rootStart:]
		reason, code := sess.Step(rt.Mem2Str(raw), eof)
		switch reason {
		case scanStopDone:
			valEnd := rootStart + sess.cur
			rawVal := (*buf)[rootStart:valEnd]
			// Cold fallback when ValidateString detected invalid UTF-8 bytes that
			// must be replaced with \ufffd before decoding.
			if sess.utf8Invalid {
				owned := string(rawVal)
				pos := 0
				sess.Reset()
				derr := Decode(&owned, &pos, matOpts, val)
				*scanp = valEnd
				return derr
			}

			p := sess.p
			p.Json = rt.Mem2Str(rawVal)
			p.unesc = sess.unesc
			p.options = matOpts
			sess.ctx.Parser = p
			if canUseFastMap(matOpts, etp) {
				sess.ctx.initSliceArena()
			} else {
				sess.ctx.sliceArena = nil
			}
			merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
			sess.ctx.Parser = nil
			sess.ctx.sliceArena = nil
			p.Json = ""
			p.unesc = nil
			p.resetSlabs()
			sess.active = false
			if p.backup != nil {
				p.nodes = p.backup
				p.backup = nil
			}
			if merr != nil {
				return detachStreamError(merr, rawVal)
			}
			*scanp = valEnd
			return nil

		case scanStopNeedMore:
			var rerr error
			eof, rerr = refill(refillArg, rootStart)
			rootStart = *scanp
			if rerr != nil {
				sess.Reset()
				return rerr
			}

		default:
			raw := (*buf)[rootStart:]
			pos := sess.cur - 1
			if sess.cur == 0 {
				code = SONIC_EOF
				pos = 0
			} else if pos < 0 {
				pos = 0
			}
			ownedSrc := string(raw)
			sess.Reset()
			return error_syntax(pos, ownedSrc, ParsingErrors[code])
		}
	}
}

func cloneIfAliasesStreamVal(s string, b0, bEnd uintptr) (string, bool) {
	if len(s) == 0 {
		return s, false
	}
	sp := uintptr(rt.IndexChar(s, 0))
	if sp < b0 || sp >= bEnd {
		return s, false
	}
	return string([]byte(s)), true
}

func detachStreamError(err error, rawVal []byte) error {
	switch e := err.(type) {
	case SyntaxError:
		return SyntaxError{
			Pos:  e.Pos,
			Src:  string(rawVal),
			Code: e.Code,
			Msg:  e.Msg,
		}
	case *SyntaxError:
		if e == nil {
			return err
		}
		cp := *e
		cp.Src = string(rawVal)
		return &cp
	case MismatchTypeError:
		return &MismatchTypeError{
			Pos:  e.Pos,
			Src:  string(rawVal),
			Type: e.Type,
		}
	case *MismatchTypeError:
		if e == nil {
			return err
		}
		cp := *e
		cp.Src = string(rawVal)
		return &cp
	case *json.UnmarshalTypeError:
		if e == nil || len(rawVal) == 0 {
			return err
		}
		b0 := uintptr(unsafe.Pointer(&rawVal[0]))
		bEnd := b0 + uintptr(len(rawVal))
		valCloned, okV := cloneIfAliasesStreamVal(e.Value, b0, bEnd)
		fldCloned, okF := cloneIfAliasesStreamVal(e.Field, b0, bEnd)
		if okV || okF {
			cp := *e
			if okV {
				cp.Value = valCloned
			}
			if okF {
				cp.Field = fldCloned
			}
			return &cp
		}
		return err
	case *strconv.NumError:
		if e == nil || len(rawVal) == 0 {
			return err
		}
		b0 := uintptr(unsafe.Pointer(&rawVal[0]))
		bEnd := b0 + uintptr(len(rawVal))
		if cloned, ok := cloneIfAliasesStreamVal(e.Num, b0, bEnd); ok {
			cp := *e
			cp.Num = cloned
			return &cp
		}
		return err
	default:
		return err
	}
}

var giantPow10 = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7,
	1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15,
	1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

func swarPlainQuoteLen64(b []byte, rawStart int) int {
	rem := len(b) - rawStart
	if rem < 8 {
		return -1
	}
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

func (s *StreamSession) growNodesForComplete(remBytes int) {
	p := s.p
	curLen := len(p.nodes)
	nextCap := curLen * 2
	if nextCap < int(defaultNodesCap) {
		nextCap = int(defaultNodesCap)
	}
	if remBytes > 0 {
		remMax := s.nodeLen + calMaxNodeCap(remBytes)
		if nextCap > remMax && remMax >= s.nodeLen+64 {
			nextCap = remMax
		}
	}
	retainInPool := uintptr(nextCap) <= maxPooledNodesCap
	grown := make([]node, nextCap)
	if s.nodeLen > 0 {
		copy(grown[:s.nodeLen], p.nodes[:s.nodeLen])
	}
	if !retainInPool && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
		p.backup = p.nodes
	}
	p.nodes = grown
}

func (s *StreamSession) beginGiant(opts uint64, isEface bool, src string, singleDoc bool) {
	s.Begin(opts, isEface)
	p := s.p
	if singleDoc {
		pos := s.trav.skipSpace(src, 0)
		s.cur = pos
		if pos < len(src) && (src[pos] == '{' || src[pos] == '[') {
			estNodes := (len(src)-pos)/12 + 1024
			if estNodes > len(p.nodes) {
				if uintptr(estNodes) > maxPooledNodesCap && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
					p.backup = p.nodes
				}
				p.nodes = make([]node, estNodes)
			}
		}
	} else if len(p.nodes) < int(defaultNodesCap) {
		p.nodes = make([]node, defaultNodesCap, defaultNodesCap)
	}
}

// scanComplete scans a complete in-memory JSON value starting at s.cur without
// allocating or copying into a full-input padded slice.
func (s *StreamSession) scanComplete(src string) ErrorCode {
	n := len(src)
	if n == 0 {
		s.cur = 0
		return SONIC_EOF
	}
	b := unsafe.Slice(unsafe.StringData(src), n)
	p := s.p
	nodes := p.nodes
	nlen := s.nodeLen
	pos := s.cur
	parentIdx := p.nbuf.parent
	depth := p.nbuf.depth
	useNumber := s.useNumber
	var arrElems uint32

scanValue:
	if pos < n && b[pos] > ' ' {
		// fast zero-space hit
	} else if pos+1 < n && b[pos] == ' ' && b[pos+1] > ' ' {
		pos++
	} else {
		pos = s.trav.skipSpace(src, pos)
		if pos >= n {
			s.cur = n + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_EOF
		}
	}

	switch ch := b[pos]; ch {
	case '{':
		if nlen >= len(nodes) {
			s.nodeLen = nlen
			s.growNodesForComplete(n - pos)
			nodes = p.nodes
		}
		idx := nlen
		nodes[idx] = node{typ: KObject | uint64(pos)<<PosBits, val: packOpenContainerVal(parentIdx)}
		nlen = idx + 1
		parentIdx = int64(idx)
		depth++
		if depth > consts.MaxStack {
			s.cur = pos + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_STACK_OVERFLOW
		}
		pos++
		if pos < n && b[pos] > ' ' {
			if b[pos] == '}' {
				pos++
				goto closeContainer
			}
			if b[pos] == '"' {
				goto objectKeyAtQuote
			}
			s.cur = pos + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_EXPECT_KEY
		}
		pos = s.trav.skipSpace(src, pos)
		if pos >= n {
			s.cur = n + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_EOF
		}
		if b[pos] == '}' {
			pos++
			goto closeContainer
		}
		if b[pos] != '"' {
			s.cur = pos + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_EXPECT_KEY
		}
		goto objectKeyAtQuote

	case '[':
		if nlen >= len(nodes) {
			s.nodeLen = nlen
			s.growNodesForComplete(n - pos)
			nodes = p.nodes
		}
		idx := nlen
		nodes[idx] = node{typ: KArray | uint64(pos)<<PosBits, val: packOpenContainerVal(parentIdx)}
		nlen = idx + 1
		parentIdx = int64(idx)
		depth++
		if depth > consts.MaxStack {
			s.cur = pos + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_STACK_OVERFLOW
		}
		pos++
		if pos < n && b[pos] > ' ' {
			if b[pos] == ']' {
				pos++
				goto closeContainer
			}
			goto scanValue
		}
		pos = s.trav.skipSpace(src, pos)
		if pos >= n {
			s.cur = n + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_EOF
		}
		if b[pos] == ']' {
			pos++
			goto closeContainer
		}
		goto scanValue

	case '"':
		rawStart := pos + 1
		if slen := swarPlainQuoteLen64(b, rawStart); slen >= 0 {
			if nlen >= len(nodes) {
				s.nodeLen = nlen
				s.growNodesForComplete(n - pos)
				nodes = p.nodes
			}
			nodes[nlen] = node{typ: KStringCommon | uint64(rawStart)<<PosBits, val: uint64(slen)}
			nlen++
			pos = rawStart + slen + 1
			goto afterValue
		}
		s.tokStart = pos
		s.cur = rawStart
		s.strScanOff = rawStart
		s.strFirstEsc = -1
		s.nodeLen = nlen
		ok, _, code := s.scanStringStep(src, true, false)
		nodes = p.nodes
		nlen = s.nodeLen
		if !ok {
			if code == SONIC_EOF && n > 0 {
				s.cur = n + 1
			}
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return code
		}
		pos = s.cur
		goto afterValue

	case 't':
		if pos+4 <= n && src[pos:pos+4] == "true" {
			if nlen >= len(nodes) {
				s.nodeLen = nlen
				s.growNodesForComplete(n - pos)
				nodes = p.nodes
			}
			nodes[nlen] = node{typ: KTrue | uint64(pos)<<PosBits, val: 0}
			nlen++
			pos += 4
			goto afterValue
		}
		return s.failLiteralComplete(src, pos, "true", nlen, parentIdx, depth)

	case 'f':
		if pos+5 <= n && src[pos:pos+5] == "false" {
			if nlen >= len(nodes) {
				s.nodeLen = nlen
				s.growNodesForComplete(n - pos)
				nodes = p.nodes
			}
			nodes[nlen] = node{typ: KFalse | uint64(pos)<<PosBits, val: 0}
			nlen++
			pos += 5
			goto afterValue
		}
		return s.failLiteralComplete(src, pos, "false", nlen, parentIdx, depth)

	case 'n':
		if pos+4 <= n && src[pos:pos+4] == "null" {
			if nlen >= len(nodes) {
				s.nodeLen = nlen
				s.growNodesForComplete(n - pos)
				nodes = p.nodes
			}
			nodes[nlen] = node{typ: KNull | uint64(pos)<<PosBits, val: 0}
			nlen++
			pos += 4
			goto afterValue
		}
		return s.failLiteralComplete(src, pos, "null", nlen, parentIdx, depth)

	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if !useNumber {
			neg := ch == '-'
			q := pos
			if neg {
				q++
			}
			if q < n {
				c0 := b[q]
				if c0 >= '1' && c0 <= '9' {
					v := uint64(c0 - '0')
					q++
					intDigits := 1
					lim := q + 18
					if lim > n {
						lim = n
					}
					for q < lim {
						d := b[q] - '0'
						if d > 9 {
							break
						}
						v = v*10 + uint64(d)
						q++
						intDigits++
					}
					if q < n {
						term := b[q]
						if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
							if nlen >= len(nodes) {
								s.nodeLen = nlen
								s.growNodesForComplete(n - pos)
								nodes = p.nodes
							}
							if !neg {
								nodes[nlen] = node{typ: KUint | uint64(pos)<<PosBits, val: v}
								nlen++
								pos = q
								goto afterValue
							} else if v <= 1<<63 {
								nodes[nlen] = node{typ: KSint | uint64(pos)<<PosBits, val: -v}
								nlen++
								pos = q
								goto afterValue
							}
						} else if term == '.' && intDigits <= 15 && q+1 < n && b[q+1]-'0' <= 9 {
							q++
							fracDigits := 0
							maxFrac := 15 - intDigits
							for fracDigits < maxFrac && q < n {
								d := b[q] - '0'
								if d > 9 {
									break
								}
								v = v*10 + uint64(d)
								q++
								fracDigits++
							}
							if q < n && fracDigits >= 1 && v>>52 == 0 {
								term2 := b[q]
								if term2 != '.' && term2 != 'e' && term2 != 'E' && (term2-'0' > 9) {
									fv := float64(v)
									if neg {
										fv = -fv
									}
									fv /= giantPow10[fracDigits]
									if nlen >= len(nodes) {
										s.nodeLen = nlen
										s.growNodesForComplete(n - pos)
										nodes = p.nodes
									}
									nodes[nlen] = node{typ: KReal | uint64(pos)<<PosBits, val: math.Float64bits(fv)}
									nlen++
									pos = q
									goto afterValue
								}
							}
						}
					}
				} else if c0 == '0' && q+1 < n {
					term := b[q+1]
					if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
						if nlen >= len(nodes) {
							s.nodeLen = nlen
							s.growNodesForComplete(n - pos)
							nodes = p.nodes
						}
						typ := uint64(KUint)
						if neg {
							typ = KSint
						}
						nodes[nlen] = node{typ: typ | uint64(pos)<<PosBits, val: 0}
						nlen++
						pos = q + 1
						goto afterValue
					}
				}
			}
		}

		end, perr := kernels.ScanNumberStrict(src, pos)
		if perr != 0 {
			s.cur = end + 1
			if s.cur > n+1 {
				s.cur = n + 1
			}
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_INVALID_NUM
		}
		if useNumber {
			if nlen >= len(nodes) {
				s.nodeLen = nlen
				s.growNodesForComplete(n - pos)
				nodes = p.nodes
			}
			nodes[nlen] = node{typ: KRawNumber | uint64(pos)<<PosBits, val: uint64(end - pos)}
			nlen++
			pos = end
			goto afterValue
		}
		neg := ch == '-'
		bodyOff := pos
		if neg {
			bodyOff++
		}
		var num cParityNum
		var code ErrorCode
		if end+8 <= n {
			term := b[end]
			if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
				_, num, code = cParityParseNumberValue(unsafe.Pointer(rt.IndexChar(src, bodyOff)), num, neg, p.dbuf)
				goto finishGiantNum
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
	finishGiantNum:
		if code != SONIC_OK {
			s.cur = end
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return code
		}
		if nlen >= len(nodes) {
			s.nodeLen = nlen
			s.growNodesForComplete(n - pos)
			nodes = p.nodes
		}
		nodes[nlen] = node{typ: num.typ | uint64(pos)<<PosBits, val: num.val}
		nlen++
		p.nbuf.stat.number++
		pos = end
		goto afterValue

	default:
		s.cur = pos + 1
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_INVALID_CHAR
	}

objectKey:
	if pos < n && b[pos] == '"' {
		goto objectKeyAtQuote
	}
	if pos+1 < n && b[pos] == ' ' && b[pos+1] == '"' {
		pos++
		goto objectKeyAtQuote
	}
	pos = s.trav.skipSpace(src, pos)
	if pos >= n {
		s.cur = n + 1
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EOF
	}
	if b[pos] != '"' {
		s.cur = pos + 1
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EXPECT_KEY
	}

objectKeyAtQuote:
	{
		rawStart := pos + 1
		if slen := swarPlainQuoteLen64(b, rawStart); slen >= 0 {
			if nlen >= len(nodes) {
				s.nodeLen = nlen
				s.growNodesForComplete(n - pos)
				nodes = p.nodes
			}
			nodes[nlen] = node{typ: KStringCommon | uint64(rawStart)<<PosBits, val: uint64(slen)}
			nlen++
			pos = rawStart + slen + 1
		} else {
			s.tokStart = pos
			s.cur = rawStart
			s.strScanOff = rawStart
			s.strFirstEsc = -1
			s.nodeLen = nlen
			ok, _, code := s.scanStringStep(src, true, true)
			nodes = p.nodes
			nlen = s.nodeLen
			if !ok {
				if code == SONIC_EOF && n > 0 {
					s.cur = n + 1
				}
				p.nbuf.parent = parentIdx
				p.nbuf.depth = depth
				return code
			}
			pos = s.cur
		}
	}
	if pos < n && b[pos] == ':' {
		pos++
		goto scanValue
	}
	if pos+1 < n && b[pos] == ' ' && b[pos+1] == ':' {
		pos += 2
		goto scanValue
	}
	pos = s.trav.skipSpace(src, pos)
	if pos >= n {
		s.cur = n + 1
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EOF
	}
	if b[pos] != ':' {
		s.cur = pos + 1
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EXPECT_COLON
	}
	pos++
	goto scanValue

afterValue:
	if parentIdx == -1 {
		s.cur = pos
		s.nodeLen = nlen
		p.nbuf.nlen = nlen
		p.nbuf.parent = -1
		p.nbuf.depth = 0
		p.nbuf.stat.array_elems += arrElems
		p.pos = pos
		return SONIC_OK
	}
	nodes[parentIdx].val++
	if uint8(nodes[parentIdx].typ) == KObject {
		if pos < n {
			ch := b[pos]
			if ch == ',' {
				pos++
				goto objectKey
			}
			if ch == '}' {
				pos++
				goto closeContainer
			}
		}
		pos = s.trav.skipSpace(src, pos)
		if pos >= n {
			s.cur = n + 1
			s.nodeLen = nlen
			p.nbuf.parent = parentIdx
			p.nbuf.depth = depth
			return SONIC_EOF
		}
		ch := b[pos]
		pos++
		if ch == ',' {
			goto objectKey
		}
		if ch == '}' {
			goto closeContainer
		}
		s.cur = pos
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EXPECT_OBJ_COMMA_OR_END
	}
	if pos < n {
		ch := b[pos]
		if ch == ',' {
			pos++
			goto scanValue
		}
		if ch == ']' {
			pos++
			goto closeContainer
		}
	}
	pos = s.trav.skipSpace(src, pos)
	if pos >= n {
		s.cur = n + 1
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EOF
	}
	{
		ch := b[pos]
		pos++
		if ch == ',' {
			goto scanValue
		}
		if ch == ']' {
			goto closeContainer
		}
		s.cur = pos
		s.nodeLen = nlen
		p.nbuf.parent = parentIdx
		p.nbuf.depth = depth
		return SONIC_EXPECT_ARR_COMMA_OR_END
	}

closeContainer:
	{
		pIdx := int(parentIdx)
		cnode := &nodes[pIdx]
		openVal := cnode.val
		count := uint32(openVal)
		if uint8(cnode.typ) != KObject {
			arrElems += count
		}
		prevParent := unpackOpenParentIdx(openVal)
		parentIdx = prevParent
		span := uint64(nlen - pIdx)
		cnode.val = (span << 32) | uint64(count)
		depth--
		goto afterValue
	}
}

func (s *StreamSession) failLiteralComplete(src string, pos int, want string, nlen int, parentIdx int64, depth uint64) ErrorCode {
	avail := len(src) - pos
	limit := len(want)
	if avail < limit {
		limit = avail
	}
	for i := 1; i < limit; i++ {
		if src[pos+i] != want[i] {
			s.cur = pos + i + 1
			s.nodeLen = nlen
			s.p.nbuf.parent = parentIdx
			s.p.nbuf.depth = depth
			return SONIC_INVALID_LITERAL
		}
	}
	if avail < len(want) {
		s.cur = len(src) + 1
	} else {
		s.cur = pos + len(want)
	}
	s.nodeLen = nlen
	s.p.nbuf.parent = parentIdx
	s.p.nbuf.depth = depth
	return SONIC_INVALID_LITERAL
}

func giantSyntaxError(src string, cur int, code ErrorCode) error {
	if cur <= 0 {
		return error_syntax(0, src, ParsingErrors[SONIC_EOF])
	}
	return error_syntax(cur-1, src, ParsingErrors[code])
}

func decodeGiantOneCompiled(s string, f uint64, etp reflect.Type, vp unsafe.Pointer, dec decFunc) (string, int, error) {
	if f&(1<<_F_validate_string) != 0 && !utf8.ValidateString(s) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(s), "\ufffd")
		s = rt.Mem2Str(dbuf)
	}

	sess := getStreamSession()
	defer FreeStreamSession(sess)

	isEface := etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	sess.beginGiant(f, isEface, s, true)
	code := sess.scanComplete(s)
	if code != SONIC_OK {
		endPos := sess.cur
		if endPos > len(s) {
			endPos = len(s)
		}
		return s, endPos, fix_error(s, 0, giantSyntaxError(s, sess.cur, code))
	}

	p := sess.p
	p.Json = s
	p.unesc = sess.unesc
	p.options = f
	sess.ctx.Parser = p
	if canUseFastMap(f, etp) {
		sess.ctx.initSliceArena()
	} else {
		sess.ctx.sliceArena = nil
	}
	merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
	sess.ctx.Parser = nil
	sess.ctx.sliceArena = nil
	err := fix_error(s, 0, merr)
	endPos := sess.cur
	if endPos > len(s) {
		endPos = len(s)
	}
	return s, endPos, err
}

func decodeGiantWithSessionCompiled(s *string, i *int, f uint64, etp reflect.Type, vp unsafe.Pointer, dec decFunc, sessPtr **InputSession) error {
	origPos := *i
	sub := (*s)[origPos:]

	sess := getStreamSession()
	defer FreeStreamSession(sess)

	isEface := etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	sess.beginGiant(f, isEface, sub, false)
	code := sess.scanComplete(sub)

	checkEnd := sess.cur
	if checkEnd > len(sub) {
		checkEnd = len(sub)
	}
	if f&(1<<_F_validate_string) != 0 && checkEnd > 0 && !utf8.ValidateString(sub[:checkEnd]) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(sub), "\ufffd")
		sub = rt.Mem2Str(dbuf)
		*s = (*s)[:origPos] + sub
		sess.beginGiant(f, isEface, sub, false)
		code = sess.scanComplete(sub)
	}

	if code != SONIC_OK {
		if sessPtr != nil {
			*sessPtr = nil
		}
		newPos := origPos + sess.cur
		if newPos > len(*s) {
			newPos = len(*s)
		}
		*i = newPos
		return fix_error(*s, origPos, giantSyntaxError(sub, sess.cur, code))
	}

	p := sess.p
	p.Json = sub
	p.unesc = sess.unesc
	p.options = f
	sess.ctx.Parser = p
	if canUseFastMap(f, etp) {
		sess.ctx.initSliceArena()
	} else {
		sess.ctx.sliceArena = nil
	}
	merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
	sess.ctx.Parser = nil
	sess.ctx.sliceArena = nil
	err := fix_error(*s, origPos, merr)

	newPos := origPos + sess.cur
	if newPos > len(*s) {
		newPos = len(*s)
	}
	if sessPtr != nil && err != nil {
		*sessPtr = nil
	}
	*i = newPos
	return err
}
