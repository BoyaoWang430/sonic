package optdec

import (
	"strings"
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
	strSlab       []byte
	strSlabOff    int
	strSlabNext   int
	ctx           Context

	// Per-decode typed pointer slabs for optional *Scalar / *string struct fields.
	// Placed after ctx so assembly-referenced Parser offsets remain unchanged.
	hasProjHints   bool
	strictURC      bool
	keepUseNumber  bool
	scalarSlab     []uint64
	scalarSlabOff  int
	scalarSlabNext int
	strPtrSlab     []string
	strPtrSlabOff  int
	strPtrSlabNext int
	ptrStructSlabs [4]ptrStructSlabSlot
}

type ptrStructSlabSlot struct {
	sd   *structDecoder
	base unsafe.Pointer
	rem  int
	next int
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
	maxPooledJsonPaddedCap uintptr = 1 << 20                            // 1 MB max pooled padded buffer
	maxPooledNodesCap      uintptr = (1 << 20) / unsafe.Sizeof(node{})  // 65536 nodes (1 MB) max pooled node buffer
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

const (
	minStrSlabCap  = 64
	initStrSlabCap = 512
	maxStrSlabCap  = 16384
	maxArenaStrLen = 1024
	maxPtrSlabCap  = 64
)

func (p *Parser) resetSlabs() {
	p.strSlab = nil
	p.strSlabOff = 0
	p.strSlabNext = 0
	p.scalarSlab = nil
	p.scalarSlabOff = 0
	p.scalarSlabNext = 0
	p.strPtrSlab = nil
	p.strPtrSlabOff = 0
	p.strPtrSlabNext = 0
	p.ptrStructSlabs = [4]ptrStructSlabSlot{}
}

func (p *Parser) allocScalarPtr(hint int) unsafe.Pointer {
	off := p.scalarSlabOff
	if off < len(p.scalarSlab) {
		p.scalarSlabOff = off + 1
		return unsafe.Pointer(&p.scalarSlab[off])
	}
	return p.allocScalarPtrSlow(hint)
}

//go:noinline
func (p *Parser) allocScalarPtrSlow(hint int) unsafe.Pointer {
	n := p.scalarSlabNext
	if n == 0 {
		if hint >= 1 && hint <= 16 {
			n = hint
		} else if hint > 16 {
			n = 16
		} else {
			n = 4
		}
	}
	next := n * 4
	if next < 8 {
		next = 8
	} else if next > maxPtrSlabCap {
		next = maxPtrSlabCap
	}
	p.scalarSlabNext = next
	slab := make([]uint64, n)
	p.scalarSlab = slab
	p.scalarSlabOff = 1
	return unsafe.Pointer(&slab[0])
}

func (p *Parser) allocStrPtr(hint int) *string {
	off := p.strPtrSlabOff
	if off < len(p.strPtrSlab) {
		p.strPtrSlabOff = off + 1
		return &p.strPtrSlab[off]
	}
	return p.allocStrPtrSlow(hint)
}

//go:noinline
func (p *Parser) allocStrPtrSlow(hint int) *string {
	n := p.strPtrSlabNext
	if n == 0 {
		if hint >= 1 && hint <= 16 {
			n = hint
		} else if hint > 16 {
			n = 16
		} else {
			n = 4
		}
	}
	next := n * 4
	if next < 8 {
		next = 8
	} else if next > maxPtrSlabCap {
		next = maxPtrSlabCap
	}
	p.strPtrSlabNext = next
	slab := make([]string, n)
	p.strPtrSlab = slab
	p.strPtrSlabOff = 1
	return &slab[0]
}

// strCommon returns an unescaped JSON string token, copying if copyStr is true.
func (p *Parser) strCommon(off, length int, copyStr bool) string {
	s := p.Json[off : off+length]
	if !copyStr {
		return s
	}
	return p.copyArenaString(s)
}

func (p *Parser) copyArenaHit(s string) (string, bool) {
	n := len(s)
	off := p.strSlabOff
	if len(p.strSlab)-off >= n {
		p.strSlabOff = off + n
		dst := p.strSlab[off : off+n]
		copy(dst, s)
		return unsafe.String(&dst[0], n), true
	}
	return s, false
}

func (p *Parser) copyArenaString(s string) string {
	if len(s) == 0 {
		return ""
	}
	if out, ok := p.copyArenaHit(s); ok {
		return out
	}
	return p.copyArenaStringSlow(s)
}

//go:noinline
func (p *Parser) copyArenaStringSlow(s string) string {
	n := len(s)
	if n > maxArenaStrLen {
		return string(rt.Str2Mem(s))
	}
	slab := p.allocStrSlab(n)
	copy(slab[:n], s)
	return unsafe.String(&slab[0], n)
}

//go:noinline
func (p *Parser) copyArenaBytesSlow(b []byte) string {
	n := len(b)
	if n > maxArenaStrLen {
		return string(b)
	}
	slab := p.allocStrSlab(n)
	copy(slab[:n], b)
	return unsafe.String(&slab[0], n)
}

func (p *Parser) allocStrSlab(n int) []byte {
	nextCap := p.strSlabNext
	if nextCap == 0 {
		jsonLen := len(p.Json)
		if p.options&(1<<_F_copy_string) == 0 {
			nextCap = 128
		} else if p.isEface {
			nextCap = jsonLen / 2
			if nextCap < minStrSlabCap {
				nextCap = minStrSlabCap
			} else if nextCap > maxStrSlabCap {
				nextCap = maxStrSlabCap
			}
		} else if jsonLen <= 128 {
			nextCap = minStrSlabCap
		} else if jsonLen <= 1024 {
			nextCap = 256
		} else {
			nextCap = jsonLen / 3
			if nextCap < initStrSlabCap {
				nextCap = initStrSlabCap
			} else if nextCap > 8192 {
				nextCap = 8192
			}
		}
	}
	if nextCap < n*2 {
		nextCap = n * 2
	}
	growCap := nextCap * 2
	if growCap > maxStrSlabCap {
		growCap = maxStrSlabCap
	}
	p.strSlabNext = growCap
	slab := make([]byte, nextCap)
	p.strSlab = slab
	p.strSlabOff = n
	return slab
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
	b := p.bytesEscaped(off, val)
	n := len(b)
	if n == 0 {
		return ""
	}
	slabOff := p.strSlabOff
	if len(p.strSlab)-slabOff >= n {
		p.strSlabOff = slabOff + n
		dst := p.strSlab[slabOff : slabOff+n]
		copy(dst, b)
		return unsafe.String(&dst[0], n)
	}
	return p.copyArenaBytesSlow(b)
}

//go:inline
func calMaxNodeCap(jsonSize int) int {
	return jsonSize/2 + 2
}

func firstContainerStart(s string) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c > ' ' {
			if c == '{' || c == '[' {
				return i
			}
			return -1
		}
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return -1
		}
	}
	return -1
}

// looksLikeJSONLPrefix checks up to the first 4 KiB for a newline-delimited
// root boundary (`}\n{`, `}\n[`, `]\n[`, `]\n{`), avoiding whole-stream node
// pre-sizing on multi-document JSONL streams passed to NewDecoder.
func looksLikeJSONLPrefix(s string) bool {
	limit := len(s)
	if limit > 4096 {
		limit = 4096
	}
	sub := s[:limit]
	off := 0
	for off < limit {
		rel := strings.IndexByte(sub[off:], '\n')
		if rel < 0 {
			return false
		}
		i := off + rel
		p := i - 1
		for p >= 0 && (sub[p] == ' ' || sub[p] == '\r' || sub[p] == '\t') {
			p--
		}
		if p >= 0 && (sub[p] == '}' || sub[p] == ']') {
			q := i + 1
			for q < limit && (sub[q] == ' ' || sub[q] == '\r' || sub[q] == '\t' || sub[q] == '\n') {
				q++
			}
			if q < limit && (sub[q] == '{' || sub[q] == '[') {
				return true
			}
		}
		off = i + 1
	}
	return false
}

func (p *Parser) presizeNodesOne() {
	start := firstContainerStart(p.Json)
	if start < 0 {
		return
	}
	estNodes := (len(p.Json)-start)/11 + 1024
	if estNodes <= cap(p.nodes) {
		return
	}
	if uintptr(estNodes) > maxPooledNodesCap && p.backup == nil && uintptr(cap(p.nodes)) <= maxPooledNodesCap {
		p.backup = p.nodes
	}
	p.nodes = make([]node, estNodes)
	p.nbuf.init(p.nodes)
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
	p.hasProjHints = false
	p.strictURC = false
	p.keepUseNumber = false
	p.sessionPadded = nil
	p.unesc = nil
	p.resetSlabs()
	p.ctx = Context{}
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
	if pos > len(p.Json) {
		pos = len(p.Json)
	}
	return error_syntax(pos, p.Json, ParsingErrors[code])
}

func Parse(data string, opt uint64) error {
	p := newParser(data, 0, opt)
	err := p.parse()
	p.free()
	return err
}
