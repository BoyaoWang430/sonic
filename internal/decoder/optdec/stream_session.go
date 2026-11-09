//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"encoding/json"
	"math/bits"
	"reflect"
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
	streamSessionPool.Put(s)
}

// StreamSession maintains a resumable, bounded JSON scan & Tape state across
// incremental io.Reader chunks for StreamDecoder. Already-scanned prefixes of a
// multi-chunk value are never rescanned on refill, and raw input coordinates
// remain decoupled from unescaped string scratch (p.unesc).
type StreamSession struct {
	p           *Parser
	ctx         Context
	unesc       []byte
	cur         int
	nodeLen     int
	tokStart    int
	strScanOff  int
	strFirstEsc int
	utf8Checked int
	phase       uint8
	active      bool
	utf8Invalid bool
	useNumber   bool
	validateStr bool
	trav        streamTraversalBlock
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
	s.useNumber = isEface && (opts&(1<<_F_use_number)) != 0
	s.validateStr = (opts & (1 << _F_validate_string)) != 0
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
	offset := uintptr(s.nodeLen) * unsafe.Sizeof(node{})
	slice := rt.GoSlice{
		Ptr: rt.Mallocgc(uintptr(nextCap)*nodeType.Size, nodeType, false),
		Len: nextCap,
		Cap: nextCap,
	}
	if offset > 0 {
		rt.Memmove(unsafe.Pointer(slice.Ptr), unsafe.Pointer(&p.nodes[0]), offset)
	}
	if !retainInPool && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
		p.backup = p.nodes
	}
	p.nodes = *(*[]node)(unsafe.Pointer(&slice))
}

func (s *StreamSession) emitNode(typ, val uint64) int {
	if s.nodeLen >= len(s.p.nodes) {
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
	case types.ERR_INVALID_UNICODE, types.ERR_INVALID_CHAR:
		return SONIC_INVALID_ESCAPED_UTF
	default:
		return SONIC_INVALID_ESCAPED
	}
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
			s.cur = len(src)
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
		written, errOff, uerr := kernels.Unquote(escTail, dst, types.F_UNICODE_REPLACE)
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
				return scanStopSyntaxError, SONIC_EOF
			}
			ch := src[pos]
			switch ch {
			case '{':
				idx := s.emitNode(KObject|uint64(pos)<<PosBits, uint64(p.nbuf.parent))
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
				idx := s.emitNode(KArray|uint64(pos)<<PosBits, uint64(p.nbuf.parent))
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
					s.cur = len(src)
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
		parent.typ += 1 << 8
		if uint8(parent.typ) == KObject {
			s.phase = streamPhaseObjectAfterVal
		} else {
			s.phase = streamPhaseArrayAfterVal
		}
		continue

	closeContainer:
		pIdx := int(p.nbuf.parent)
		cnode := &p.nodes[pIdx]
		count := uint32((cnode.typ >> 8) & 0xffffff)
		kind := uint8(cnode.typ)
		if kind == KObject {
			p.nbuf.stat.object++
			p.nbuf.stat.object_keys += count
		} else {
			p.nbuf.stat.array++
			p.nbuf.stat.array_elems += count
		}
		prevParent := int64(cnode.val)
		p.nbuf.parent = prevParent
		span := uint64(s.nodeLen - pIdx)
		cnode.typ &= 0xffffffff000000ff
		cnode.val = (span << 32) | uint64(count)
		if uint32(p.nbuf.depth) > p.nbuf.stat.max_depth {
			p.nbuf.stat.max_depth = uint32(p.nbuf.depth)
		}
		p.nbuf.depth--
		if prevParent == -1 {
			return scanStopDone, SONIC_OK
		}
		outer := &p.nodes[prevParent]
		outer.typ += 1 << 8
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
	vv := rt.UnpackEface(val)
	vp := vv.Value
	if vv.Type == nil {
		return &json.InvalidUnmarshalError{}
	}
	if vp == nil || vv.Type.Kind() != reflect.Ptr {
		return &json.InvalidUnmarshalError{Type: vv.Type.Pack()}
	}
	etp := rt.PtrElem(vv.Type)
	if vv.Type.IsNamed() {
		newp := vp
		etp = vv.Type
		vp = unsafe.Pointer(&newp)
	}
	dec, err := findOrCompile(etp)
	if err != nil {
		return err
	}

	sess := *sessPtr
	if sess == nil {
		sess = getStreamSession()
		*sessPtr = sess
	}
	isEface := etp == rt.AnyType || etp == rt.MapEfaceType || etp == rt.SliceEfaceType
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
				sess.ctx.efacePool = newEfacePool(&p.nbuf.stat, (matOpts&(1<<_F_use_number)) != 0)
				if len(sess.ctx.Stack.stack) <= int(p.nbuf.stat.max_depth)+1 {
					sess.ctx.Stack = newStack(int(p.nbuf.stat.max_depth))
				} else {
					sess.ctx.Stack.index = 0
				}
			} else {
				sess.ctx.efacePool = nil
			}
			merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
			sess.ctx.Parser = nil
			sess.ctx.efacePool = nil
			p.Json = ""
			p.unesc = nil
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

func detachStreamError(err error, rawVal []byte) error {
	switch e := err.(type) {
	case SyntaxError:
		return SyntaxError{
			Pos: e.Pos,
			Src: string(rawVal),
			Msg: e.Msg,
		}
	case MismatchTypeError:
		return &MismatchTypeError{
			Pos:  e.Pos,
			Src:  string(rawVal),
			Type: e.Type,
		}
	case *MismatchTypeError:
		cp := *e
		cp.Src = string(rawVal)
		return &cp
	default:
		return err
	}
}
