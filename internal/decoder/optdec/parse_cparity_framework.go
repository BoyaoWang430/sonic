//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"fmt"
	"runtime"
	"unsafe"

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
	p := parsePool.Get().(*Parser)
	p.Json = data[pos:]
	if opt&(1<<_F_validate_string) != 0 && !utf8.ValidateString(p.Json) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(p.Json), "\ufffd")
		p.Json = rt.Mem2Str(dbuf)
		p.Utf8Inv = true
	}
	// Escaped strings are decoded in place. Keep that storage separate from
	// p.Json, which still supplies raw strings, numbers, and corrected input.
	needed := len(p.Json) + len(padding)
	if needed > cap(p.padded) {
		if uintptr(needed) > maxPooledJsonPaddedCap {
			if cap(p.padded) > 0 && uintptr(cap(p.padded)) <= maxPooledJsonPaddedCap {
				p.paddedBackup = p.padded[:0]
			}
			p.padded = make([]byte, 0, needed)
		} else {
			allocCap := int(maxPooledJsonPaddedCap)
			if needed <= 256<<10 {
				allocCap = 256 << 10
			}
			p.padded = make([]byte, 0, allocCap)
		}
	}
	p.padded = append(p.padded, p.Json...)
	p.padded = append(p.padded, padding...)
	p.start = uintptr(unsafe.Pointer(rt.SliceData(p.padded)))
	p.cur = p.start
	p.end = p.start + uintptr(len(p.Json))
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
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
	p.start = uintptr(unsafe.Pointer(rt.SliceData(sub)))
	p.cur = p.start
	p.end = p.start + uintptr(len(p.Json))
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
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

func (p *Parser) growNodeTape() {
	// The scanner has already written the last available node. Restart
	// from its packed state after copying exactly the initialized prefix.
	offset := p.nbuf.ncur - p.nbuf.nstart
	curLen := int(offset / unsafe.Sizeof(node{}))
	if curLen != len(p.nodes) {
		panic(fmt.Sprintf("current len: %d, real len: %d cap: %d", curLen, len(p.nodes), cap(p.nodes)))
	}
	remCap := curLen + calMaxNodeCap(len(p.Json)-int(p.cur-p.start))
	nextCap := curLen * 4
	if nextCap < 64 {
		nextCap = 64
	}
	if nextCap > remCap {
		nextCap = remCap
	}
	retainInPool := uintptr(nextCap) <= maxPooledNodesCap
	grown := make([]node, nextCap)
	copy(grown[:curLen], p.nodes[:curLen])
	if !retainInPool && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
		p.backup = p.nodes
	}
	p.nodes = grown
	p.nbuf.nstart = uintptr(unsafe.Pointer(rt.SliceData(p.nodes)))
	p.nbuf.nend = p.nbuf.nstart + uintptr(cap(p.nodes))*unsafe.Sizeof(node{})
	p.nbuf.ncur = p.nbuf.nstart + offset
}

func parseImpl(p *Parser) ErrorCode {
	old := p.options
	if !p.isEface {
		p.options &^= 1 << _F_use_number
	}
	err := cParityParseWithPadding(p)
	for classifyScanStop(err) == scanStopNodeCapExhausted {
		p.growNodeTape()
		err = cParityParseWithPadding(p)
	}
	p.options = old
	runtime.KeepAlive(p)
	return err
}
