//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
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

// Parser is the pooled execution shell combining ScanContext, Document, and
// DecodeContext without extra heap allocations. Materialization reads Document
// and DecodeContext directly and never reaches back into ScanContext/Parser.
type Parser struct {
	ScanContext
	Document
	ctx Context
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

const (
	nodeByteSize            = int(unsafe.Sizeof(node{}))
	defaultPaddedBufBytes   = 64 << 10                               // 64 KiB initial pooled padded buffer
	maxPooledPaddedBufBytes = 1 << 20                                // 1 MiB max pooled padded buffer
	presizeSingleDocBytes   = 128 << 10                              // 128 KiB threshold for single-doc node presize
	largeReadOnlyScanBytes  = 4 << 20                                // 4 MiB threshold for read-only no-copy scan
	defaultNodesCount       = defaultPaddedBufBytes / nodeByteSize   // 4096 nodes (64 KiB) initial pooled tape
	maxPooledNodesCount     = maxPooledPaddedBufBytes / nodeByteSize // 65536 nodes (1 MiB) max pooled tape
	maxInitialPresizeNodes  = 1 << 18                                // 262144 nodes (4 MiB) max initial presize
)

var parsePool sync.Pool = sync.Pool{
	New: func() interface{} {
		return &Parser{
			ScanContext: ScanContext{
				padded: make([]byte, 0, defaultPaddedBufBytes),
				dbuf:   make([]byte, types.MaxDigitNums, types.MaxDigitNums),
			},
			Document: Document{
				nodes: make([]node, defaultNodesCount, defaultNodesCount),
			},
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

func (p *Parser) snapshotDocument() Document {
	doc := p.Document
	doc.inPlaceBytes = p.JsonBytes()
	doc.stat = p.nbuf.stat
	return doc
}

// scanDocument executes the scanning stage and returns the resulting Document
// and scan status without initializing materialization arenas.
func (p *Parser) scanDocument(opts uint64, root reflect.Type, proj *projPlan) (Document, ErrorCode) {
	p.isEface = root == anyType || root == mapEfaceType || root == sliceEfaceType
	p.strictURC = (opts & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (opts & (1 << _F_use_number)) != 0
	p.strictRawNum = true
	p.deferFloatOverflow = !p.keepUseNumber

	var ecode ErrorCode
	if proj != nil {
		ecode = parseWithSchemaOrFull(p, proj)
	} else {
		ecode = p.parse()
	}
	if ecode != SONIC_OK {
		return Document{Json: p.Json, Utf8Inv: p.Utf8Inv}, ecode
	}
	return p.snapshotDocument(), SONIC_OK
}

// initDecodeContext prepares the pooled DecodeContext over an already-scanned
// Document with zero scanning or route-switching side effects.
func (p *Parser) initDecodeContext(doc Document, opts uint64, root reflect.Type) *Context {
	p.ctx.initForDocument(doc, opts, root)
	return &p.ctx
}

const (
	minStrSlabCap  = 64
	initStrSlabCap = 512
	maxStrSlabCap  = 16384
	maxArenaStrLen = 1024
	maxPtrSlabCap  = 64
)

const (
	escapedInUnescFlag  uint64 = 1 << 63
	maxPooledUnescCap   int    = 64 << 10
	schemaTailWindowLen int    = 256
)

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

func cappedInitialNodesCap(src string, start, divisor int) int {
	if start < 0 || start >= len(src) {
		return 1024
	}
	if start+1 < len(src) && ((src[start] == '{' && src[start+1] == '}') || (src[start] == '[' && src[start+1] == ']')) {
		return 16
	}
	estNodes := (len(src)-start)/divisor + 1024
	if estNodes > maxInitialPresizeNodes {
		return maxInitialPresizeNodes
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
	if estNodes > maxPooledNodesCount && p.backup == nil && cap(p.nodes) <= maxPooledNodesCount {
		p.backup = p.nodes
	}
	p.nodes = make([]node, estNodes)
	p.nbuf.init(p.nodes)
}

func (p *Parser) parse() ErrorCode {
	return parseImpl(p)
}

func (p *Parser) reset() {
	// 1. First drop all references to user-returned allocations in DecodeContext;
	// user-owned storage is never returned to any pool.
	p.ctx.ownedParser = nil
	p.ctx.resetMaterialization()

	// 2. Clear Document views and restore bounded pooled node tape.
	nodes := p.nodes
	if p.backup != nil {
		nodes = p.backup
		p.backup = nil
	}
	p.Document = Document{nodes: nodes}

	// 3. Recycle ScanContext scratch buffers within pool bounds.
	p.options = 0
	if p.paddedBackup != nil {
		p.padded = p.paddedBackup[:0]
		p.paddedBackup = nil
	} else if cap(p.padded) > maxPooledPaddedBufBytes {
		p.padded = nil
	} else {
		p.padded = p.padded[:0]
	}
	p.ScanKernelState = ScanKernelState{}
	p.isEface = false
	p.strictURC = false
	p.keepUseNumber = false
	p.strictRawNum = false
	p.lazyPadded = false
	p.singleDoc = false
	p.deferFloatOverflow = false
	p.sessionPadded = nil
	if cap(p.unescBuf) > maxPooledUnescCap {
		p.unescBuf = nil
	} else if p.unescBuf != nil {
		p.unescBuf = p.unescBuf[:0]
	}
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
