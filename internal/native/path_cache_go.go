//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
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
	b.events, b.nonspace, b.boundaries, b.end = kernels.PathMasks(src, pos)
	b.base = pos
	b.end += pos
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

func (b *pathBlock) nextSlow(src string, pos int) (byte, int) {
	if uint(pos) >= uint(len(src)) {
		return 0, len(src)
	}
	for {
		if b.nonspace|b.boundaries == 0 || pos < b.base || pos >= b.end {
			// Most formatting gaps are shorter than a classification block.
			if src[pos] == '\n' {
				pos++
				base := unsafe.Pointer(rt.StringData(src))
				for pos+8 <= len(src) && *(*uint64)(unsafe.Add(base, pos)) == 0x2020202020202020 {
					pos += 8
				}
				if pos+4 <= len(src) && *(*uint32)(unsafe.Add(base, pos)) == 0x20202020 {
					pos += 4
				}
				if uint(pos) < uint(len(src)) && src[pos] > ' ' {
					return src[pos], pos + 1
				}
			}
			limit := minNative(len(src), pos+24)
			for pos < limit && utils.IsSpace(src[pos]) {
				pos++
			}
			if pos == len(src) {
				return 0, pos
			}
			if pos < limit {
				return src[pos], pos + 1
			}
			b.load(src, pos)
		}
		mask := b.nonspace >> uint(pos-b.base)
		if mask != 0 {
			pos += bits.TrailingZeros64(mask)
			return src[pos], pos + 1
		}
		pos = b.end
		if pos == len(src) {
			return 0, pos
		}
	}
}

// string reuses cached classifications when available, or probes a short
// uncached prefix. A first cached quote completes a plain string; a first
// backslash hands the remaining string to the complete scanner.
func (b *pathBlock) string(src string, begin int) (end, escape int, err types.ParsingError) {
	if begin < b.base || begin >= b.end {
		return b.stringTail(src, begin)
	}
	pos := begin
	mask := b.events >> uint(pos-b.base)
	if mask == 0 {
		pos = b.end
	} else {
		pos += bits.TrailingZeros64(mask)
		if src[pos] == '"' {
			return pos + 1, -1, 0
		}
	}
	end, escape, err, events, width := kernels.ScanPathStringBlock(src[pos:])
	end += pos
	if escape >= 0 {
		escape += pos
	}
	b.saveStringTail(end, events, width)
	return end, escape, err
}

// Preserve validation's error cursor and active grammar by replaying only
// failures. Successful strings can consume the path's existing classifications.
func (b *pathBlock) validateTarget(src string, origPos int, m *types.StateMachine) (int, int) {
	return b.validateTargetAtDepth(src, origPos, m, 0)
}

func (b *pathBlock) validateTargetAtDepth(src string, origPos int, m *types.StateMachine, baseDepth int) (int, int) {
	pos := origPos
	var ch byte
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		ch = src[pos]
		pos++
	} else {
		ch, pos = b.nextSlow(src, pos)
	}
	if ch == '"' {
		if end, _, err := b.string(src, pos); err == 0 {
			if m != nil {
				m.Sp = 0
			}
			return pos - 1, end
		}
	}
	p := origPos
	ret := ValidateOneAtDepth(&src, &p, m, 0, baseDepth)
	return ret, p
}

func (b *pathBlock) matchKey(src string, begin int, key string) (int, bool, types.ParsingError) {
	// Establish a short plain key's boundary and equality in one pass,
	// including mismatches. Quotes and backslashes still need the scanner.
	if len(key) <= 16 && len(src)-begin > len(key) && src[begin+len(key)] == '"' {
		var diff byte
		for i := 0; i < len(key); i++ {
			c := src[begin+i]
			if c == '"' || c == '\\' {
				goto scanKey
			}
			diff |= c ^ key[i]
		}
		return begin + len(key) + 1, diff == 0, 0
	}
scanKey:
	end, escape, err := b.string(src, begin)
	if err != 0 {
		return end, false, err
	}
	if escape < 0 {
		return end, src[begin:end-1] == key, 0
	}
	return matchEscapedPathKey(src, key, begin, end)
}

// skipValue returns the final cursor and the token start or error. Traversal
// deliberately ignores errors for unselected values and checks the following
// delimiter; a selected target still propagates the returned error.
func (b *pathBlock) skipValue(src string, pos int) (end, ret int) {
	var ch byte
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		ch = src[pos]
		pos++
	} else if uint(pos+1) < uint(len(src)) && src[pos] == ' ' && src[pos+1] > ' ' {
		ch = src[pos+1]
		pos += 2
	} else {
		ch, pos = b.nextSlow(src, pos)
	}
	start := pos - 1
	switch ch {
	case 0:
		return pos, -int(types.ERR_EOF)
	case '[', '{':
		if uint(pos) < uint(len(src)) && src[pos] == ch+2 {
			return pos + 1, start
		}
		end, err := kernels.SkipContainer(src, start)
		if err != 0 {
			return end, -int(err)
		}
		return end, start
	case '"':
		if pos < b.base || pos >= b.end {
			end, _, err := b.stringTail(src, pos)
			if err != 0 {
				return end, -int(err)
			}
			return end, start
		}
		mask := b.events >> uint(pos-b.base)
		if mask == 0 {
			pos = b.end
		} else {
			pos += bits.TrailingZeros64(mask)
			if src[pos] == '"' {
				return pos + 1, start
			}
		}
		end, _, err, events, width := kernels.ScanPathStringBlock(src[pos:])
		end += pos
		b.saveStringTail(end, events, width)
		if err != 0 {
			return end, -int(err)
		}
		return end, start
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if b.nonspace|b.boundaries != 0 && pos >= b.base && pos < b.end {
			mask := b.boundaries >> uint(pos-b.base)
			if mask != 0 {
				return pos + bits.TrailingZeros64(mask), start
			}
			pos = b.end
		}
		return pathNumberTail(src, pos), start
	case 'n', 't', 'f':
		width := 4
		if ch == 'f' {
			width = 5
		}
		if len(src)-start < width {
			return len(src), -int(types.ERR_EOF)
		}
		return start + width, start
	default:
		return start, -int(types.ERR_INVALID_CHAR)
	}
}

// Path search deliberately skips malformed unselected numeric candidates too.
// Only these seven boundaries terminate a candidate; grammar markers do not.
func pathNumberTail(src string, pos int) int {
	limit := minNative(len(src), pos+8)
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
	return kernels.SkipNumber(src, pos)
}

// A string scanner returns only classifications it already computed. Other
// consumers must not interpret the stale whitespace/number masks as this span.
func (b *pathBlock) saveStringTail(end int, events uint64, width int) {
	if width != 0 {
		b.base, b.end, b.events = end, end+width, events
		b.nonspace, b.boundaries = 0, 0
	}
}
