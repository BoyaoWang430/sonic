package optdec

import (
	"reflect"
	"unsafe"

	"sync"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

type ErrorCode int

const (
	SONIC_OK                      = 0
	SONIC_CONTROL_CHAR            = 1
	SONIC_INVALID_ESCAPED         = 2
	SONIC_INVALID_NUM             = 3
	SONIC_FLOAT_INF               = 4
	SONIC_EOF                     = 5
	SONIC_INVALID_CHAR            = 6
	SONIC_EXPECT_KEY              = 7
	SONIC_EXPECT_COLON            = 8
	SONIC_EXPECT_OBJ_COMMA_OR_END = 9
	SONIC_EXPECT_ARR_COMMA_OR_END = 10
	SONIC_VISIT_FAILED            = 11
	SONIC_INVALID_ESCAPED_UTF     = 12
	SONIC_INVALID_LITERAL         = 13
	SONIC_STACK_OVERFLOW          = 14
)

var ParsingErrors = []string{
	SONIC_OK:                      "ok",
	SONIC_CONTROL_CHAR:            "control chars in string",
	SONIC_INVALID_ESCAPED:         "invalid escaped chars in string",
	SONIC_INVALID_NUM:             "invalid number",
	SONIC_FLOAT_INF:               "float infinity",
	SONIC_EOF:                     "eof",
	SONIC_INVALID_CHAR:            "invalid chars",
	SONIC_EXPECT_KEY:              "expect a json key",
	SONIC_EXPECT_COLON:            "expect a `:`",
	SONIC_EXPECT_OBJ_COMMA_OR_END: "expect a `,` or `}`",
	SONIC_EXPECT_ARR_COMMA_OR_END: "expect a `,` or `]`",
	SONIC_VISIT_FAILED:            "failed in json visitor",
	SONIC_INVALID_ESCAPED_UTF:     "invalid escaped unicodes",
	SONIC_INVALID_LITERAL:         "invalid literal(true/false/null)",
	SONIC_STACK_OVERFLOW:          "json is exceeded max depth 4096, cause stack overflow",
}

func (code ErrorCode) Error() string {
	return ParsingErrors[code]
}

type node struct {
	typ uint64
	val uint64
}

// rvFrame is scratch state for the Go parser. Indices remain valid when the
// node buffer grows, and frames never retain pointers into an input document.
type rvFrame struct {
	node  int
	count uint32
	state uint8
}

// should consistent with native/parser.c
type _nospaceBlock struct {
	_ [8]byte
	_ [8]byte
}

// should consistent with native/parser.c
type nodeBuf struct {
	ncur   uintptr
	parent int64
	depth  uint64
	nstart uintptr
	nend   uintptr
	iskey  bool
	stat   jsonStat
}

func (self *nodeBuf) init(nodes []node) {
	self.ncur = uintptr(unsafe.Pointer(&nodes[0]))
	self.nstart = self.ncur
	self.nend = self.ncur + uintptr(cap(nodes))*unsafe.Sizeof(node{})
	self.parent = -1
}

// should consistent with native/parser.c
type Parser struct {
	Json   string
	padded []byte
	nodes  []node
	dbuf   []byte
	backup []node

	options uint64
	// JSON cursor
	start uintptr
	cur   uintptr
	end   uintptr
	_nbk  _nospaceBlock

	// node buffer cursor
	nbuf    nodeBuf
	Utf8Inv bool
	isEface bool

	// Go-only scratch follows the native ABI prefix. Only the RISC-V parser
	// uses it; reuse it with the Parser instead of allocating a stack per call.
	rvFrames []rvFrame
}

// only when parse non-empty object/array are needed.
type jsonStat struct {
	object      uint32
	array       uint32
	str         uint32
	number      uint32
	array_elems uint32
	object_keys uint32
	max_depth   uint32
}

var (
	defaultJsonPaddedCap uintptr = 1 << 20                           // 1 Mb
	defaultNodesCap      uintptr = (1 << 20) / unsafe.Sizeof(node{}) // 1 Mb
)

var parsePool sync.Pool = sync.Pool{
	New: func() interface{} {
		return &Parser{
			options: 0,
			padded:  make([]byte, 0, defaultJsonPaddedCap),
			nodes:   make([]node, defaultNodesCap, defaultNodesCap),
			dbuf:    make([]byte, types.MaxDigitNums, types.MaxDigitNums),
		}
	},
}

var padding string = "x\"x\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"

func newParser(data string, pos int, opt uint64) *Parser {
	return newParserImpl(data, pos, opt)
}

func (p *Parser) Pos() int {
	return int(p.cur - p.start)
}

func (p *Parser) JsonBytes() []byte {
	return parserJSONBytes(p)
}

var nodeType = rt.UnpackType(reflect.TypeOf(node{}))

//go:inline
func calMaxNodeCap(jsonSize int) int {
	return jsonSize/2 + 2
}

func (p *Parser) parse() ErrorCode {
	return parseImpl(p)
}

func (p *Parser) reset() {
	p.options = 0
	p.padded = p.padded[:0]
	// nodes is too large here, we will not reset it and use small backup nodes buffer
	if p.backup != nil {
		p.nodes = p.backup
		p.backup = nil
	}
	p.start = 0
	p.cur = 0
	p.end = 0
	p.Json = ""
	p.nbuf = nodeBuf{}
	p._nbk = _nospaceBlock{}
	p.Utf8Inv = false
	p.isEface = false
}

func (p *Parser) free() {
	p.reset()
	parsePool.Put(p)
}

//go:noinline
func (p *Parser) fixError(code ErrorCode) error {
	if code == SONIC_OK {
		return nil
	}

	if p.Pos() == 0 {
		code = SONIC_EOF
	}

	pos := p.Pos() - 1
	return error_syntax(pos, p.Json, ParsingErrors[code])
}

func Parse(data string, opt uint64) error {
	p := newParser(data, 0, opt)
	err := p.parse()
	p.free()
	return err
}
