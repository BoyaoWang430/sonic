//go:build !riscv64 || !riscv64.rva22u64 || !go1.26 || go1.28

package optdec

import (
	"fmt"
	"unsafe"

	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

func newParserImpl(data string, pos int, opt uint64) *Parser {
	p := parsePool.Get().(*Parser)

	/* validate json if needed */
	if (opt&(1<<_F_validate_string)) != 0 && !utf8.ValidateString(data) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(data[pos:]), "\ufffd")
		dbuf = append(dbuf, padding...)
		p.Json = rt.Mem2Str(dbuf[:len(dbuf)-len(padding)])
		p.Utf8Inv = true
		p.start = uintptr((*rt.GoString)(unsafe.Pointer(&p.Json)).Ptr)
	} else {
		p.Json = data
		// TODO: prevent too large JSON
		p.padded = append(p.padded, data[pos:]...)
		p.padded = append(p.padded, padding...)
		p.start = uintptr((*rt.GoSlice)(unsafe.Pointer(&p.padded)).Ptr)
	}

	p.cur = p.start
	p.end = p.cur + uintptr(len(p.Json))
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

func parserJSONBytes(p *Parser) []byte {
	if p.Utf8Inv {
		return (rt.Str2Mem(p.Json))
	} else {
		return p.padded
	}
}

func parseImpl(p *Parser) ErrorCode {
	// when decode into struct, we should decode number as possible
	old := p.options
	if !p.isEface {
		p.options &^= 1 << _F_use_number
	}

	// fast path with limited node buffer
	err := ErrorCode(native.ParseWithPadding(unsafe.Pointer(p)))
	if err != SONIC_VISIT_FAILED {
		p.options = old
		return err
	}

	// check OoB here
	offset := p.nbuf.ncur - p.nbuf.nstart
	curLen := int(offset / unsafe.Sizeof(node{}))
	if curLen != len(p.nodes) {
		panic(fmt.Sprintf("current len: %d, real len: %d cap: %d", curLen, len(p.nodes), cap(p.nodes)))
	}

	// node buf is not enough, continue parse
	// the maxCap is always meet all valid JSON
	maxCap := curLen + calMaxNodeCap(len(p.Json)-int(p.cur-p.start))
	slice := rt.GoSlice{
		Ptr: rt.Mallocgc(uintptr(maxCap)*nodeType.Size, nodeType, false),
		Len: maxCap,
		Cap: maxCap,
	}
	rt.Memmove(unsafe.Pointer(slice.Ptr), unsafe.Pointer(&p.nodes[0]), offset)
	p.backup = p.nodes
	p.nodes = *(*[]node)(unsafe.Pointer(&slice))

	// update node cursor
	p.nbuf.nstart = uintptr(unsafe.Pointer(&p.nodes[0]))
	p.nbuf.nend = p.nbuf.nstart + uintptr(cap(p.nodes))*unsafe.Sizeof(node{})
	p.nbuf.ncur = p.nbuf.nstart + offset

	// continue parse json
	err = ErrorCode(native.ParseWithPadding(unsafe.Pointer(p)))
	p.options = old
	return err
}

func parserCorrectedSource(p *Parser, original string, pos int) string {
	return p.Json
}
