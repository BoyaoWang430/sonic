package optdec

import (
	"sync"
	"unsafe"

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

// scanStopReason classifies why a bounded scan pass returned to Go.
type scanStopReason uint8

const (
	scanStopDone scanStopReason = iota
	scanStopNodeCapExhausted
	scanStopTextCapExhausted
	scanStopBudgetExhausted
	scanStopNeedMore
	scanStopSyntaxError
)

func classifyScanStop(code ErrorCode) scanStopReason {
	switch code {
	case SONIC_OK:
		return scanStopDone
	case SONIC_VISIT_FAILED:
		return scanStopNodeCapExhausted
	default:
		return scanStopSyntaxError
	}
}

type node struct {
	typ uint64
	val uint64
}

// Layout must match the assembly offsets in parse_cparity_runs_*.s.
type _nospaceBlock struct {
	_ [8]byte
	_ [8]byte
}

// Layout must match the assembly offsets in parse_cparity_runs_*.s.
type nodeBuf struct {
	nlen   int
	parent int64
	depth  uint64
	ncap   int
	iskey  bool
	stat   jsonStat
}

func (self *nodeBuf) init(nodes []node) {
	self.nlen = 0
	self.ncap = cap(nodes)
	self.parent = -1
}

// Layout must match the assembly offsets in parse_cparity_runs_*.s.
type Parser struct {
	Json   string
	padded []byte
	nodes  []node
	dbuf   []byte
	backup []node

	options uint64
	// JSON cursor offsets (relative to JsonBytes()[0])
	pos  int
	end  int
	_nbk _nospaceBlock

	// node buffer cursor
	nbuf          nodeBuf
	Utf8Inv       bool
	isEface       bool
	paddedBackup  []byte
	sessionPadded []byte
	unesc         []byte
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
	defaultJsonPaddedCap   uintptr = 64 << 10                          // 64 KB initial pool buffer
	defaultNodesCap        uintptr = (64 << 10) / unsafe.Sizeof(node{}) // 4096 nodes (64 KB) initial pool buffer
	maxPooledJsonPaddedCap uintptr = 1 << 20                           // 1 MB max pooled padded buffer
	maxPooledNodesCap      uintptr = (64 << 10) / unsafe.Sizeof(node{}) // 4096 nodes (64 KB) max pooled node buffer
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
	return p.pos
}

func (p *Parser) JsonBytes() []byte {
	return parserJSONBytes(p)
}

// rawSlice returns a zero-copy substring of the source JSON document [start:end].
func (p *Parser) rawSlice(start, end int) string {
	return p.Json[start:end]
}

// strRefCommon returns a zero-copy string view of an unescaped JSON string token.
func (p *Parser) strRefCommon(off, length int) string {
	return p.Json[off : off+length]
}

// strCommon returns an unescaped JSON string token, copying if copyStr is true.
func (p *Parser) strCommon(off, length int, copyStr bool) string {
	s := p.Json[off : off+length]
	if !copyStr {
		return s
	}
	return string(rt.Str2Mem(s))
}

// bytesEscaped returns the decoded byte slice for an escaped JSON string token.
func (p *Parser) bytesEscaped(off int, val uint64) []byte {
	if p.unesc != nil {
		uoff := int(val >> 32)
		ulen := int(uint32(val))
		return p.unesc[uoff : uoff+ulen]
	}
	return p.JsonBytes()[off : off+int(val)]
}

// strRefEscaped returns a transient string view of a decoded escaped JSON string token.
func (p *Parser) strRefEscaped(off int, val uint64) string {
	return rt.Mem2Str(p.bytesEscaped(off, val))
}

// strEscaped returns a heap-copied string of a decoded escaped JSON string token.
func (p *Parser) strEscaped(off int, val uint64) string {
	return string(p.bytesEscaped(off, val))
}

//go:inline
func calMaxNodeCap(jsonSize int) int {
	return jsonSize/2 + 2
}

func (p *Parser) parse() ErrorCode {
	return parseImpl(p)
}

func (p *Parser) reset() {
	p.options = 0
	if p.paddedBackup != nil {
		p.padded = p.paddedBackup[:0]
		p.paddedBackup = nil
	} else if uintptr(cap(p.padded)) > maxPooledJsonPaddedCap {
		p.padded = nil
	} else {
		p.padded = p.padded[:0]
	}
	// nodes is too large here, we will not reset it and use small backup nodes buffer
	if p.backup != nil {
		p.nodes = p.backup
		p.backup = nil
	}
	p.pos = 0
	p.end = 0
	p.Json = ""
	p.nbuf = nodeBuf{}
	p._nbk = _nospaceBlock{}
	p.Utf8Inv = false
	p.isEface = false
	p.sessionPadded = nil
	p.unesc = nil
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
