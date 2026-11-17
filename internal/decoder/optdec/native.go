//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"math/bits"
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
	if code == errCodeNodeCapExhausted {
		return "node capacity exhausted"
	}
	if code >= 0 && int(code) < len(ParsingErrors) {
		return ParsingErrors[code]
	}
	return "unknown scan status"
}

const errCodeNodeCapExhausted ErrorCode = -2

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
	case errCodeNodeCapExhausted:
		return scanStopNodeCapExhausted
	default:
		return scanStopSyntaxError
	}
}

type node struct {
	typ uint64
	val uint64
}

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

const (
	scanKernelFlagRawNumber     uint64 = 1 << 1
	scanKernelFlagRejectControl uint64 = 1 << 5
)

// ScanKernelState is the compact, pointer-free ABI boundary shared between Go
// and the Runs assembly scanners via go_asm.h. Evolving Parser, DecodeContext,
// Schema projection plans, or allocators never changes ScanKernelState layout.
type ScanKernelState struct {
	scanFlags uint64
	pos       int
	end       int
	nbk       cParityNonspaceBlock
	nbuf      nodeBuf
}

type Parser struct {
	Json   string
	padded []byte
	nodes  []node
	dbuf   []byte
	backup []node

	options uint64
	ScanKernelState

	Utf8Inv       bool
	isEface       bool
	paddedBackup  []byte
	sessionPadded []byte
	unesc         []byte
	strSlab       []byte
	strSlabOff    int
	strSlabNext   int
	ctx           Context

	hasProjHints       bool
	strictURC          bool
	keepUseNumber      bool
	strictRawNum       bool
	scalarSlab         []uint64
	scalarSlabOff      int
	scalarSlabNext     int
	strPtrSlab         []string
	strPtrSlabOff      int
	strPtrSlabNext     int
	ptrStructSlabs     [4]ptrStructSlabSlot
	lazyPadded         bool
	deferFloatOverflow bool
	strAnyMask         uint32
	strAnyCache        [32]strAnySlot
	unescBuf           []byte
	tailBuf            [320]byte
}

type strAnySlot struct {
	s string
	v any
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
	defaultJsonPaddedCap   uintptr = 64 << 10                           // 64 KB initial pool buffer
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

func (p *Parser) clearStrAnyCache() {
	for m := p.strAnyMask; m != 0; m &= m - 1 {
		p.strAnyCache[bits.TrailingZeros32(m)] = strAnySlot{}
	}
	p.strAnyMask = 0
}

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
	if p.ptrStructSlabs[0].sd != nil {
		p.ptrStructSlabs = [4]ptrStructSlabSlot{}
	}
	if p.strAnyMask != 0 {
		p.clearStrAnyCache()
	}
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
	jsonLen := len(p.Json)
	slabMax := maxStrSlabCap
	// Cap slabs at 4 KiB for <=32 KiB payloads so ~13 KiB docs don't jump from 6.5 KiB to 13 KiB.
	if jsonLen <= 32768 {
		slabMax = 4096
	}
	nextCap := p.strSlabNext
	if nextCap == 0 {
		if p.options&(1<<_F_copy_string) == 0 {
			if jsonLen > 1024 {
				nextCap = 512
			} else {
				nextCap = 128
			}
		} else if p.isEface {
			nextCap = jsonLen / 2
			if nextCap < minStrSlabCap {
				nextCap = minStrSlabCap
			} else if nextCap > slabMax {
				nextCap = slabMax
			}
		} else if jsonLen <= 128 {
			nextCap = minStrSlabCap
		} else if jsonLen <= 1024 {
			nextCap = 256
		} else {
			nextCap = jsonLen / 4
			if nextCap < initStrSlabCap {
				nextCap = initStrSlabCap
			} else if nextCap > slabMax {
				nextCap = slabMax
			}
		}
	}
	if nextCap < n*2 {
		nextCap = n * 2
	}
	growCap := nextCap * 2
	if growCap > slabMax {
		growCap = slabMax
	}
	p.strSlabNext = growCap
	slab := make([]byte, nextCap)
	p.strSlab = slab
	p.strSlabOff = n
	return slab
}

const (
	escapedInUnescFlag  uint64 = 1 << 63
	maxPooledUnescCap   int    = 64 << 10
	schemaTailWindowLen int    = 256
)

// bytesEscaped returns the decoded byte slice for an escaped JSON string token.
func (p *Parser) bytesEscaped(off int, val uint64) []byte {
	if val&escapedInUnescFlag != 0 {
		u := val &^ escapedInUnescFlag
		uoff := int(u >> 32)
		ulen := int(uint32(u))
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
	return p.strEscapedBytes(p.bytesEscaped(off, val))
}

func (p *Parser) strEscapedBytes(b []byte) string {
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

// maxPresizeNodesOne caps the initial up-front node reservation before any
// scan runs, so pathological inputs (e.g. multi-MiB unclosed '[' or small
// containers followed by huge trailing whitespace) never pre-allocate millions
// of nodes up front before actual scanning/depth checks run.
const maxPresizeNodesOne = 1 << 18

func cappedInitialNodesCap(src string, start, divisor int) int {
	if start < 0 || start >= len(src) {
		return 1024
	}
	if start+1 < len(src) && ((src[start] == '{' && src[start+1] == '}') || (src[start] == '[' && src[start+1] == ']')) {
		return 16
	}
	estNodes := (len(src)-start)/divisor + 1024
	if estNodes > maxPresizeNodesOne {
		return maxPresizeNodesOne
	}
	return estNodes
}

func (p *Parser) presizeNodesOne() {
	start := firstContainerStart(p.Json)
	if start < 0 {
		return
	}
	estNodes := cappedInitialNodesCap(p.Json, start, 11)
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
	p.Json = ""
	p.ScanKernelState = ScanKernelState{}
	p.Utf8Inv = false
	p.isEface = false
	p.hasProjHints = false
	p.strictURC = false
	p.keepUseNumber = false
	p.strictRawNum = false
	p.lazyPadded = false
	p.deferFloatOverflow = false
	if p.strAnyMask != 0 {
		p.clearStrAnyCache()
	}
	p.sessionPadded = nil
	p.unesc = nil
	if cap(p.unescBuf) > maxPooledUnescCap {
		p.unescBuf = nil
	} else if p.unescBuf != nil {
		p.unescBuf = p.unescBuf[:0]
	}
	p.resetSlabs()
	p.ctx = Context{}
}

func (p *Parser) free() {
	p.reset()
	parsePool.Put(p)
}

func buildSyntaxErrorAt(src string, errOffset int, code ErrorCode) error {
	if errOffset < 0 {
		errOffset = 0
	} else if errOffset > len(src) {
		errOffset = len(src)
	}
	return error_syntax(errOffset, src, ParsingErrors[code])
}

func syntaxErrorFromAfterCursor(src string, afterPos int, code ErrorCode) error {
	if afterPos <= 0 {
		return buildSyntaxErrorAt(src, 0, SONIC_EOF)
	}
	return buildSyntaxErrorAt(src, afterPos-1, code)
}

//go:noinline
func (p *Parser) fixError(code ErrorCode) error {
	if code == SONIC_OK {
		return nil
	}
	return syntaxErrorFromAfterCursor(p.Json, p.Pos(), code)
}

func Parse(data string, opt uint64) error {
	p := newParser(data, 0, opt)
	err := p.parse()
	p.free()
	return err
}
