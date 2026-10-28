//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package optdec

import (
	"math"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

func newParserImpl(data string, pos int, opt uint64) *Parser {
	p := parsePool.Get().(*Parser)
	// Source and decoded buffer share the node offsets' origin.
	p.Json = data[pos:]
	if opt&(1<<_F_validate_string) != 0 && !utf8.ValidateString(p.Json) {
		p.Json = string(utf8.CorrectWith(nil, rt.Str2Mem(p.Json), "\ufffd"))
		p.Utf8Inv = true
	}
	p.padded = append(p.padded, p.Json...)
	p.start = uintptr(unsafe.Pointer(unsafe.SliceData(p.padded)))
	p.cur = p.start
	p.end = p.start + uintptr(len(p.Json))
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

func parserJSONBytes(p *Parser) []byte { return p.padded }

func parserCorrectedSource(p *Parser, original string, pos int) string {
	return original[:pos] + p.Json
}

const (
	rvArrayFirst uint8 = iota
	rvArrayValue
	rvArrayAfter
	rvObjectFirst
	rvObjectKey
	rvObjectValue
	rvObjectAfter
)

type rvParser struct {
	p         *Parser
	data      []byte
	src       string
	pos       int
	used      int
	frames    []rvFrame
	spaceBase int
	spaceEnd  int
	spaceMask uint64
}

func parseImpl(p *Parser) ErrorCode {
	old := p.options
	if !p.isEface {
		p.options &^= 1 << _F_use_number
	}
	if p.rvFrames == nil {
		p.rvFrames = make([]rvFrame, 0, 64)
	}
	r := rvParser{
		p: p, data: p.padded[:len(p.Json)],
		frames: p.rvFrames[:0],
	}
	// Scan immutable input while unescaping the separate byte buffer.
	r.src = p.Json
	code := r.value()
	for code == SONIC_OK && len(r.frames) != 0 {
		code = r.step()
	}
	// Reuse frames after errors too; each push overwrites its frame.
	p.rvFrames = r.frames[:0]
	p.cur = p.start + uintptr(r.pos)
	base := uintptr(unsafe.Pointer(unsafe.SliceData(p.nodes)))
	p.nbuf.nstart = base
	p.nbuf.nend = base + uintptr(len(p.nodes))*unsafe.Sizeof(node{})
	p.nbuf.ncur = base + uintptr(r.used)*unsafe.Sizeof(node{})
	p.nbuf.parent = -1
	p.nbuf.depth = uint64(len(r.frames))
	if len(r.frames) != 0 {
		p.nbuf.parent = int64(r.frames[len(r.frames)-1].node)
	}
	p.options = old
	return code
}

func (r *rvParser) fail(code ErrorCode, at int) ErrorCode {
	// Parser.fixError translates this consumed position back to the offending
	// byte. EOF is represented by an error offset equal to the logical length.
	r.pos = at + 1
	return code
}

func (r *rvParser) space() {
	// Keep the no-whitespace case small enough for Go to inline into the FSM.
	if r.pos == len(r.src) || r.src[r.pos] > ' ' {
		return
	}
	r.spaceSlow()
}

func (r *rvParser) spaceSlow() {
	// Probe short gaps only outside the cached block.
	if r.pos < r.spaceBase || r.pos >= r.spaceEnd {
		for n := 0; n < 2; n++ {
			if r.pos == len(r.src) {
				return
			}
			switch r.src[r.pos] {
			case ' ', '\t', '\r', '\n':
				r.pos++
			default:
				return
			}
		}
	}
	for r.pos < len(r.src) {
		if r.pos < r.spaceBase || r.pos >= r.spaceEnd {
			var width int
			// Classify immutable input so unescaping cannot invalidate the mask.
			r.spaceBase = r.pos
			r.spaceMask, width = riscv.NonSpaceMask(r.src, r.pos)
			r.spaceEnd = r.pos + width
		}
		mask := r.spaceMask >> uint(r.pos-r.spaceBase)
		if mask != 0 {
			r.pos += bits.TrailingZeros64(mask)
			return
		}
		r.pos = r.spaceEnd
	}
}

func (r *rvParser) add(kind uint64, at int, value uint64) int {
	if r.used == len(r.p.nodes) {
		r.growNodes()
	}
	i := r.used
	r.p.nodes[i] = node{typ: kind | uint64(at)<<PosBits, val: value}
	r.used++
	return i
}

func (r *rvParser) growNodes() {
	n := len(r.p.nodes) * 2
	if limit := calMaxNodeCap(len(r.src)); n > limit {
		n = limit
	}
	if n <= r.used {
		n = r.used + 1
	}
	// Nodes have no pointers. Copy initialized nodes; add initializes each new slot.
	nodes := unsafe.Slice((*node)(rt.Mallocgc(uintptr(n)*unsafe.Sizeof(node{}), nodeType, false)), n)
	copy(nodes, r.p.nodes[:r.used])
	if r.p.backup == nil {
		r.p.backup = r.p.nodes
	}
	r.p.nodes = nodes
}

func (r *rvParser) literal(word string, kind uint64) ErrorCode {
	start := r.pos
	for i := 0; i < len(word); i++ {
		at := start + i
		if at >= len(r.data) {
			return r.fail(SONIC_EOF, len(r.data))
		}
		if r.data[at] != word[i] {
			return r.fail(SONIC_INVALID_LITERAL, at)
		}
	}
	r.pos += len(word)
	r.add(kind, start, 0)
	return SONIC_OK
}

func (r *rvParser) text(key bool) ErrorCode {
	start := r.pos
	end, size, escaped, at, code := riscv.ParseString(r.data, start,
		r.p.options&(1<<_F_validate_string) != 0,
		r.p.options&(1<<_F_disable_urc) != 0)
	if code != 0 {
		return r.fail(ErrorCode(code), at)
	}
	kind := uint64(KStringCommon)
	if escaped {
		kind = KStringEscaped
	}
	r.add(kind, start+1, uint64(size))
	r.pos = end
	if !key {
		r.p.nbuf.stat.str++
	}
	return SONIC_OK
}

func (r *rvParser) value() ErrorCode {
	r.space()
	if r.pos == len(r.data) {
		return r.fail(SONIC_EOF, r.pos)
	}
	return r.valueReady()
}

// valueReady consumes a value after its caller has skipped whitespace and
// checked EOF. Container transitions have already done both in step.
func (r *rvParser) valueReady() ErrorCode {
	start := r.pos
	switch c := r.data[start]; c {
	case 'n':
		return r.literal("null", KNull)
	case 't':
		return r.literal("true", KTrue)
	case 'f':
		return r.literal("false", KFalse)
	case '"':
		return r.text(false)
	case '[', '{':
		if len(r.frames) == types.MAX_RECURSE {
			return r.fail(SONIC_STACK_OVERFLOW, start)
		}
		kind, state := uint64(KArray), uint8(rvArrayFirst)
		if c == '{' {
			kind, state = KObject, rvObjectFirst
		}
		i := r.add(kind, start, 0)
		r.frames = append(r.frames, rvFrame{node: i, state: state})
		if depth := uint32(len(r.frames)); depth > r.p.nbuf.stat.max_depth {
			r.p.nbuf.stat.max_depth = depth
		}
		r.pos++
		return SONIC_OK
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var end int
		if r.p.options&(1<<_F_use_number) != 0 {
			var err types.ParsingError
			end, err = number.Scan(r.src, start)
			if err != 0 {
				return r.fail(SONIC_INVALID_NUM, end)
			}
			r.add(KRawNumber, start, uint64(end-start))
		} else {
			n, next, err := number.ParsePrefix(r.src, start)
			end = next
			if err == types.ERR_FLOAT_INFINITY {
				return r.fail(SONIC_FLOAT_INF, end-1)
			}
			if err != 0 {
				return r.fail(SONIC_INVALID_NUM, end)
			}
			switch n.Kind {
			case number.Signed:
				r.add(KSint, start, uint64(n.Int64))
			case number.Unsigned:
				r.add(KUint, start, n.Uint64)
			case number.Float:
				r.add(KReal, start, math.Float64bits(n.Float64))
			}
		}
		r.p.nbuf.stat.number++
		r.pos = end
		return SONIC_OK
	default:
		return r.fail(SONIC_INVALID_CHAR, start)
	}
}

func (r *rvParser) close(object bool) ErrorCode {
	f := r.frames[len(r.frames)-1]
	r.p.nodes[f.node].val = uint64(f.count) | uint64(r.used-f.node)<<ConLenBits
	if object {
		r.p.nbuf.stat.object++
		r.p.nbuf.stat.object_keys += f.count
	} else {
		r.p.nbuf.stat.array++
		r.p.nbuf.stat.array_elems += f.count
	}
	r.frames = r.frames[:len(r.frames)-1]
	r.pos++
	return SONIC_OK
}

func (r *rvParser) step() ErrorCode {
	r.space()
	if r.pos == len(r.data) {
		return r.fail(SONIC_EOF, r.pos)
	}
	i := len(r.frames) - 1
	switch r.frames[i].state {
	case rvArrayFirst, rvArrayValue:
		if r.frames[i].state == rvArrayFirst && r.data[r.pos] == ']' {
			return r.close(false)
		}
		r.frames[i].state = rvArrayAfter
		r.frames[i].count++
		return r.valueReady()
	case rvArrayAfter:
		switch r.data[r.pos] {
		case ']':
			return r.close(false)
		case ',':
			r.pos++
			r.frames[i].state = rvArrayValue
			return SONIC_OK
		default:
			return r.fail(SONIC_EXPECT_ARR_COMMA_OR_END, r.pos)
		}
	case rvObjectFirst, rvObjectKey:
		if r.frames[i].state == rvObjectFirst && r.data[r.pos] == '}' {
			return r.close(true)
		}
		if r.data[r.pos] != '"' {
			return r.fail(SONIC_EXPECT_KEY, r.pos)
		}
		if code := r.text(true); code != 0 {
			return code
		}
		r.space()
		if r.pos == len(r.data) || r.data[r.pos] != ':' {
			return r.fail(SONIC_EXPECT_COLON, r.pos)
		}
		r.pos++
		r.frames[i].state = rvObjectValue
		return SONIC_OK
	case rvObjectValue:
		r.frames[i].state = rvObjectAfter
		r.frames[i].count++
		return r.valueReady()
	case rvObjectAfter:
		switch r.data[r.pos] {
		case '}':
			return r.close(true)
		case ',':
			r.pos++
			r.frames[i].state = rvObjectKey
			return SONIC_OK
		default:
			return r.fail(SONIC_EXPECT_OBJ_COMMA_OR_END, r.pos)
		}
	default:
		panic("invalid Go JSON parser state")
	}
}
