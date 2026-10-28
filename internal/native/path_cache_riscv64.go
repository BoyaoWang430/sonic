//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"math/bits"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/utils"
)

// pathBlock keeps classifications after a token boundary for the following
// key, colon, value and separator. It never changes relaxed search semantics.
// A nonempty full block has nonspace|boundaries != 0: every byte is either
// non-space or a numeric boundary. Both zero therefore mark an events-only tail.
type pathBlock struct {
	base, end                    int
	events, nonspace, boundaries uint64
}

func (b *pathBlock) load(src string, pos int) {
	var width int
	b.events, b.nonspace, b.boundaries, width = riscv.PathMasks(src, pos)
	b.base, b.end = pos, pos+width
}

// pathOrdinaryByte consumes an ordinary non-space byte. Returning zero leaves
// the cursor unchanged so the caller can handle whitespace, controls and EOF.
// Keeping the slow call in the grammar phase allows this operation to inline.
func pathOrdinaryByte(src string, p *int) byte {
	pos := *p
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		*p = pos + 1
		return src[pos]
	}
	return 0
}

func (b *pathBlock) nextSlow(src string, p *int) byte {
	pos := *p
	if uint(pos) >= uint(len(src)) {
		*p = len(src)
		return 0
	}
	for {
		if b.nonspace|b.boundaries == 0 || pos < b.base || pos >= b.end {
			// Most formatting gaps are shorter than a classification block.
			limit := min(len(src), pos+8)
			for pos < limit && utils.IsSpace(src[pos]) {
				pos++
			}
			if pos == len(src) {
				*p = pos
				return 0
			}
			if pos < limit {
				*p = pos + 1
				return src[pos]
			}
			b.load(src, pos)
		}
		mask := b.nonspace >> uint(pos-b.base)
		if mask != 0 {
			pos += bits.TrailingZeros64(mask)
			*p = pos + 1
			return src[pos]
		}
		pos = b.end
		if pos == len(src) {
			*p = pos
			return 0
		}
	}
}

// stringCached consumes all quote/escape events already classified for this
// path, including the selected value. It leaves any later events cached.
func (b *pathBlock) stringCached(src string, begin int) (end, escape int, err types.ParsingError) {
	pos, escape := begin, -1
	for pos >= b.base && pos < b.end {
		mask := b.events >> uint(pos-b.base)
		if mask == 0 {
			pos = b.end
			break
		}
		pos += bits.TrailingZeros64(mask)
		if src[pos] == '"' {
			return pos + 1, escape, 0
		}
		if escape < 0 {
			escape = pos
		}
		if len(src)-pos < 2 {
			return len(src), escape, types.ERR_EOF
		}
		pos += 2
	}
	end, nextEscape, err := b.stringTail(src, pos)
	if escape < 0 {
		escape = nextEscape
	}
	return end, escape, err
}

// Preserve validation's error cursor and active grammar by replaying only
// failures. Successful strings can consume the path's existing classifications.
func (b *pathBlock) validateTarget(s *string, p *int, m *types.StateMachine) int {
	pos := *p
	ch := pathOrdinaryByte(*s, &pos)
	if ch == 0 {
		ch = b.nextSlow(*s, &pos)
	}
	if ch == '"' {
		if end, _, err := b.string(*s, pos); err == 0 {
			*p, m.Sp = end, 0
			return pos - 1
		}
	}
	return SkipOne(s, p, m, 0)
}

func (b *pathBlock) matchKey(src string, p *int, key string) (bool, types.ParsingError) {
	begin := *p
	// Establish a short plain key's boundary and equality in one pass,
	// including mismatches. Quotes and backslashes still need the scanner.
	if len(key) <= 8 && len(src)-begin > len(key) && src[begin+len(key)] == '"' {
		var diff byte
		for i := 0; i < len(key); i++ {
			c := src[begin+i]
			if c == '"' || c == '\\' {
				goto scanKey
			}
			diff |= c ^ key[i]
		}
		*p = begin + len(key) + 1
		return diff == 0, 0
	}
scanKey:
	end, escape, err := b.string(src, begin)
	*p = end
	if err != 0 {
		return false, err
	}
	if escape < 0 {
		return src[begin:end-1] == key, 0
	}
	return matchEscapedPathKey(src, p, key, begin, end)
}

func (b *pathBlock) skipValue(src string, p *int) int {
	ch := pathOrdinaryByte(src, p)
	if ch == 0 {
		ch = b.nextSlow(src, p)
	}
	start := *p - 1
	switch ch {
	case 0:
		return -int(types.ERR_EOF)
	case '[', '{':
		return skipContainerFast(src, p)
	case '"':
		end, _, err := b.string(src, *p)
		*p = end
		if err != 0 {
			return -int(err)
		}
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		pos := *p
		if b.nonspace|b.boundaries != 0 && pos >= b.base && pos < b.end {
			mask := b.boundaries >> uint(pos-b.base)
			if mask != 0 {
				*p = pos + bits.TrailingZeros64(mask)
				return start
			}
			pos = b.end
		}
		*p = pathNumberTail(src, pos)
	case 'n', 't', 'f':
		width := 4
		if ch == 'f' {
			width = 5
		}
		if len(src)-start < width {
			*p = len(src)
			return -int(types.ERR_EOF)
		}
		*p = start + width
	default:
		*p = start
		return -int(types.ERR_INVALID_CHAR)
	}
	return start
}

// Path search deliberately skips malformed unselected numeric candidates too.
// Only these seven boundaries terminate a candidate; grammar markers do not.
func pathNumberTail(src string, pos int) int {
	limit := min(len(src), pos+8)
	for pos < limit {
		c := src[pos]
		if c == ',' || c == ']' || c == '}' || utils.IsSpace(c) {
			return pos
		}
		pos++
	}
	if pos == len(src) {
		return pos
	}
	return riscv.SkipNumber(src, pos)
}

// A string scanner returns only classifications it already computed. Other
// consumers must not interpret the stale whitespace/number masks as this span.
func (b *pathBlock) saveStringTail(end int, events uint64, width int) {
	if width != 0 {
		b.base, b.end, b.events = end, end+width, events
		b.nonspace, b.boundaries = 0, 0
	}
}
