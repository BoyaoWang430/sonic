//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

// Go owns allocation and lifetime management for the ARM64 runs parser.
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
	p.padded = append(p.padded, p.Json...)
	p.padded = append(p.padded, padding...)
	p.start = uintptr(unsafe.Pointer(rt.SliceData(p.padded)))
	p.cur = p.start
	p.end = p.start + uintptr(len(p.Json))
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

func parserJSONBytes(p *Parser) []byte {
	return p.padded
}

func parserCorrectedSource(p *Parser, original string, pos int) string {
	return original[:pos] + p.Json
}

func parseImpl(p *Parser) ErrorCode {
	old := p.options
	if !p.isEface {
		p.options &^= 1 << _F_use_number
	}
	err := cParityParseWithPadding(p)
	if err != SONIC_VISIT_FAILED {
		p.options = old
		runtime.KeepAlive(p)
		return err
	}

	// The C visitor has already written the last available node. Restart
	// from its packed state after copying exactly the initialized prefix.
	offset := p.nbuf.ncur - p.nbuf.nstart
	curLen := int(offset / unsafe.Sizeof(node{}))
	if curLen != len(p.nodes) {
		panic(fmt.Sprintf("current len: %d, real len: %d cap: %d", curLen, len(p.nodes), cap(p.nodes)))
	}
	maxCap := curLen + calMaxNodeCap(len(p.Json)-int(p.cur-p.start))
	slice := rt.GoSlice{
		Ptr: rt.Mallocgc(uintptr(maxCap)*nodeType.Size, nodeType, false),
		Len: maxCap,
		Cap: maxCap,
	}
	rt.Memmove(unsafe.Pointer(slice.Ptr), unsafe.Pointer(&p.nodes[0]), offset)
	p.backup = p.nodes
	p.nodes = *(*[]node)(unsafe.Pointer(&slice))
	p.nbuf.nstart = uintptr(unsafe.Pointer(rt.SliceData(p.nodes)))
	p.nbuf.nend = p.nbuf.nstart + uintptr(cap(p.nodes))*unsafe.Sizeof(node{})
	p.nbuf.ncur = p.nbuf.nstart + offset
	err = cParityParseWithPadding(p)
	p.options = old
	runtime.KeepAlive(p)
	return err
}
