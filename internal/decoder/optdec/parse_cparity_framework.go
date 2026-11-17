//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"fmt"
	"runtime"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

// InputSession caches prepared UTF-8-validated source and padded buffer across
// consecutive multi-value Decode calls on the same string Decoder, eliminating
// O(N^2) suffix copies and repeated UTF-8 full-tail scans.
type InputSession struct {
	src         string
	base        int
	padded      []byte
	utf8Checked bool
}

func hasMoreJSONTokens(s string, pos int) bool {
	for pos < len(s) {
		c := s[pos]
		if c > ' ' || (types.SPACE_MASK&(1<<c)) == 0 {
			return true
		}
		pos++
	}
	return false
}

// The immutable source and mutable padded buffer share the token-offset origin.
func newParserImpl(data string, pos int, opt uint64) *Parser {
	p := newParserLazy(data, pos, opt)
	p.ensurePadded()
	return p
}

// newParserLazy initializes a Parser over data[pos:] without copying the input
// into p.padded, allowing zero-copy schema projection scans to avoid the full-input memcpy.
func newParserLazy(data string, pos int, opt uint64) *Parser {
	if pos < 0 {
		pos = 0
	} else if pos > len(data) {
		pos = len(data)
	}
	p := parsePool.Get().(*Parser)
	p.Json = data[pos:]
	if opt&(1<<_F_validate_string) != 0 && !utf8.ValidateString(p.Json) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(p.Json), "\ufffd")
		p.Json = rt.Mem2Str(dbuf)
		p.Utf8Inv = true
	}
	p.lazyPadded = true
	p.pos = 0
	p.end = len(p.Json)
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

func (p *Parser) ensurePadded() {
	if !p.lazyPadded {
		return
	}
	p.lazyPadded = false
	if p.sessionPadded != nil {
		return
	}
	// Escaped strings are decoded in place by cParityParseWithPadding. Keep that
	// storage separate from p.Json, which still supplies raw strings, numbers, and corrected input.
	needed := len(p.Json) + len(padding)
	if needed > cap(p.padded) {
		if needed > maxPooledPaddedBufBytes {
			if cap(p.padded) > 0 && cap(p.padded) <= maxPooledPaddedBufBytes {
				p.paddedBackup = p.padded[:0]
			}
			p.padded = make([]byte, 0, needed)
		} else {
			allocCap := maxPooledPaddedBufBytes
			if needed <= 4<<10 {
				allocCap = 4 << 10
			} else if needed <= 32<<10 {
				allocCap = 32 << 10
			} else if needed <= 256<<10 {
				allocCap = 256 << 10
			}
			p.padded = make([]byte, 0, allocCap)
		}
	}
	p.padded = append(p.padded[:0], p.Json...)
	p.padded = append(p.padded, padding...)
}

func newParserFromSession(data string, pos int, opt uint64, sess *InputSession) *Parser {
	if sess == nil || sess.padded == nil || sess.src != data || pos < sess.base || pos > len(data) ||
		((opt&(1<<_F_validate_string)) != 0 && !sess.utf8Checked) {
		return newParserImpl(data, pos, opt)
	}
	p := parsePool.Get().(*Parser)
	p.Json = data[pos:]
	sub := sess.padded[pos-sess.base:]
	p.sessionPadded = sub
	p.pos = 0
	p.end = len(p.Json)
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

func newParserLazyFromSession(data string, pos int, opt uint64, sess *InputSession) *Parser {
	if sess != nil && sess.src == data && pos >= sess.base && pos <= len(data) &&
		((opt&(1<<_F_validate_string)) == 0 || sess.utf8Checked) {
		p := parsePool.Get().(*Parser)
		p.Json = data[pos:]
		if sess.padded != nil {
			p.sessionPadded = sess.padded[pos-sess.base:]
		} else {
			p.lazyPadded = true
		}
		p.pos = 0
		p.end = len(p.Json)
		p.options = opt
		p.nbuf.init(p.nodes)
		return p
	}
	return newParserLazy(data, pos, opt)
}

func parserJSONBytes(p *Parser) []byte {
	if p.sessionPadded != nil {
		return p.sessionPadded
	}
	return p.padded
}

func parserCorrectedSource(p *Parser, original string, pos int) string {
	return original[:pos] + p.Json
}

// nodeTapeGrowth tracks how far growNodeTape has escalated for one scan.
type nodeTapeGrowth uint8

const (
	// growGeometric grows by 4x, clamped to the valid-JSON bound.
	growGeometric nodeTapeGrowth = iota
	// growToHardBound is entered after a clamped growth still ran out.
	growToHardBound
	// growExhausted means the absolute bound was already reached.
	growExhausted
)

// growNodeTape grows the node tape after the scanner filled it and reports
// whether the scan may be resumed.
//
// Valid JSON produces at most calMaxNodeCap(remaining) further nodes, so one
// growth clamped to that bound always suffices for well-formed input. Running
// out again after such a clamp means the input is denser than any valid
// document (for example a long unclosed run of '['). Re-clamping to the
// valid-JSON bound would then only add half of the remaining bytes per round,
// copying the whole tape about log2(len) times; instead grow once to the
// absolute bound of one node per remaining byte, which keeps the total work
// linear in the input size.
func (p *Parser) growNodeTape(stage *nodeTapeGrowth) bool {
	// The scanner has already written the last available node. Restart
	// from its packed state after copying exactly the initialized prefix.
	curLen := p.nbuf.nlen
	if curLen != len(p.nodes) {
		panic(fmt.Sprintf("current len: %d, real len: %d cap: %d", curLen, len(p.nodes), cap(p.nodes)))
	}
	rem := len(p.Json) - p.pos
	if rem < 0 {
		rem = 0
	}
	var nextCap int
	switch *stage {
	case growGeometric:
		validCap := curLen + calMaxNodeCap(rem)
		nextCap = curLen * 4
		if nextCap < 64 {
			nextCap = 64
		}
		if nextCap > validCap {
			nextCap = validCap
			*stage = growToHardBound
		}
	case growToHardBound:
		// Every node consumes at least one input byte.
		nextCap = curLen + rem + 2
		*stage = growExhausted
	default:
		return false
	}
	retainInPool := nextCap <= maxPooledNodesCount
	grown := make([]node, nextCap)
	copy(grown[:curLen], p.nodes[:curLen])
	if !retainInPool && p.backup == nil && cap(p.nodes) <= maxPooledNodesCount {
		p.backup = p.nodes
	}
	p.nodes = grown
	p.nbuf.ncap = cap(p.nodes)
	return true
}

// parseRunsGrowing runs the Runs scanner, growing the node tape whenever it
// fills up. The growth is bounded (see growNodeTape), so malformed dense input
// cannot make it loop or copy the tape more than a constant number of times
// beyond the geometric phase.
func parseRunsGrowing(p *Parser) ErrorCode {
	err := cParityParseWithPadding(p)
	stage := growGeometric
	for classifyScanStop(err) == scanStopNodeCapExhausted {
		if p.nbuf.depth > types.MAX_RECURSE && len(p.nodes) > types.MAX_RECURSE+2 {
			if uint32(p.nbuf.depth) > p.nbuf.stat.max_depth {
				p.nbuf.stat.max_depth = uint32(p.nbuf.depth)
			}
			return SONIC_STACK_OVERFLOW
		}
		if !p.growNodeTape(&stage) {
			break
		}
		err = cParityParseWithPadding(p)
	}
	return err
}

func parseImpl(p *Parser) ErrorCode {
	p.ensurePadded()
	old := p.options
	if !p.isEface && !p.keepUseNumber {
		p.options &^= 1 << _F_use_number
	}
	err := parseRunsGrowing(p)
	p.options = old
	runtime.KeepAlive(p)
	return err
}
