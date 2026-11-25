//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package optdec

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/jsonscan"
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
	ncap   int // physical backing capacity cap(nodes), including nodeGuardSlots
	stat   jsonStat
}

func (self *nodeBuf) init(nodes []node) {
	self.nlen = 0
	self.ncap = physicalNodesCap(nodes)
	self.parent = -1
}

// ScanKernelState records scan cursors and final tape summary published by
// parseSharedJSONScan (G4.4c: legacy Runs assembly kernel state retired).
type ScanKernelState struct {
	pos  int
	end  int
	nbuf nodeBuf
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
	nodeByteSize           = int(unsafe.Sizeof(node{}))
	nodeGuardSlots         = 1                         // trailing guard slot reserved inside physical backing
	presizeSingleDocBytes  = 128 << 10                 // 128 KiB threshold for single-doc node presize
	largeReadOnlyScanBytes = 4 << 20                   // 4 MiB threshold for read-only no-copy scan
	defaultNodesCount      = (64 << 10) / nodeByteSize // 4096 physical nodes (64 KiB; 4095 writable + 1 guard)
	maxPooledNodesCount    = (1 << 20) / nodeByteSize  // 65536 physical nodes (1 MiB hard pool ceiling, guard included)
	maxInitialPresizeNodes = 1 << 18                   // 262144 physical nodes (4 MiB) max initial presize
)

// physicalNodesCap returns the total element capacity of a node backing slice,
// including the trailing guard slot. Pool bounds apply to physicalNodesCap.
func physicalNodesCap(nodes []node) int {
	return cap(nodes)
}

// writableNodesCap returns the maximum number of nodes a scanner may emit into
// nodes while keeping at least nodeGuardSlots trailing guard element in bounds.
func writableNodesCap(nodes []node) int {
	c := cap(nodes)
	if c <= nodeGuardSlots {
		return 0
	}
	return c - nodeGuardSlots
}

// replaceNodesBacking allocates a new physical node slice of capacity
// newPhysicalCap (which includes nodeGuardSlots), copies p.nodes[:copyPrefix],
// saves any bounded pooled slice in p.backup before installing an oversized
// (> maxPooledNodesCount) slice, and syncs p.nbuf.ncap.
func (p *Parser) replaceNodesBacking(newPhysicalCap, copyPrefix int) {
	if copyPrefix < 0 {
		copyPrefix = 0
	}
	if newPhysicalCap < copyPrefix+nodeGuardSlots {
		newPhysicalCap = copyPrefix + nodeGuardSlots
	}
	grown := make([]node, newPhysicalCap)
	if copyPrefix > 0 {
		copy(grown[:copyPrefix], p.nodes[:copyPrefix])
	}
	if newPhysicalCap > maxPooledNodesCount && p.backup == nil &&
		cap(p.nodes) > nodeGuardSlots && cap(p.nodes) <= maxPooledNodesCount {
		p.backup = p.nodes[:cap(p.nodes)]
	}
	p.nodes = grown
	p.nbuf.ncap = cap(grown)
}

// restoreBoundedPooledNodes restores p.backup when valid and independently
// enforces physicalNodesCap(p.nodes) <= maxPooledNodesCount so no oversized
// backing can ever escape into parsePool or streamSessionPool even if p.backup
// was nil or invalid.
func (p *Parser) restoreBoundedPooledNodes(fallbackPhysicalCap int) {
	nodes := p.nodes
	if p.backup != nil {
		if cap(p.backup) > nodeGuardSlots && cap(p.backup) <= maxPooledNodesCount {
			nodes = p.backup
		}
		p.backup = nil
	}
	if cap(nodes) > maxPooledNodesCount || cap(nodes) <= nodeGuardSlots {
		if fallbackPhysicalCap <= nodeGuardSlots || fallbackPhysicalCap > maxPooledNodesCount {
			fallbackPhysicalCap = defaultNodesCount
		}
		nodes = make([]node, fallbackPhysicalCap, fallbackPhysicalCap)
	} else {
		nodes = nodes[:cap(nodes)]
	}
	p.nodes = nodes
	p.nbuf.ncap = cap(nodes)
}

var parsePool sync.Pool = sync.Pool{
	New: func() interface{} {
		return &Parser{
			Document: Document{
				nodes: make([]node, defaultNodesCount, defaultNodesCount),
			},
		}
	},
}

func newParser(data string, pos int, opt uint64) *Parser {
	return newParserImpl(data, pos, opt)
}

func (p *Parser) Pos() int {
	return p.pos
}

func (p *Parser) JsonBytes() []byte {
	return parserJSONBytes(p)
}

func (p *Parser) finalizeDocumentInPlace() {
	nlen := p.nbuf.nlen
	if nlen > 0 && nlen <= cap(p.nodes) {
		if nlen+nodeGuardSlots > cap(p.nodes) {
			p.replaceNodesBacking(nlen+nodeGuardSlots, nlen)
		}
		p.nodes = p.nodes[:nlen]
	} else if nlen == 0 && p.nodes != nil {
		p.nodes = p.nodes[:0]
	}
	p.stat = p.nbuf.stat
}

func (p *Parser) snapshotDocument() Document {
	p.finalizeDocumentInPlace()
	return p.Document
}

// scanDocumentInPlace executes the scanning stage and finalizes p.Document in
// place without copying the 80-byte Document struct on the single-doc hot path.
func (p *Parser) scanDocumentInPlace(opts uint64, root reflect.Type, proj *projPlan) ErrorCode {
	return p.scanDocumentInPlaceMode(opts, root, proj, false)
}

func (p *Parser) scanDocumentInPlaceMode(opts uint64, root reflect.Type, proj *projPlan, forceScalar bool) ErrorCode {
	return p.scanDocumentInPlaceWithRole(opts, root, proj, forceScalar, jsonscan.DiagRoleMain)
}

func (p *Parser) scanDocumentInPlaceWithRole(opts uint64, root reflect.Type, proj *projPlan, forceScalar bool, diagRole uint8) ErrorCode {
	p.isEface = root == anyType || root == mapEfaceType || root == sliceEfaceType
	p.strictURC = (opts & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (opts & (1 << _F_use_number)) != 0
	p.deferFloatOverflow = !p.keepUseNumber

	var ecode ErrorCode
	if proj != nil {
		ecode = parseWithSchemaOrFullMode(p, proj, forceScalar)
	} else {
		ecode = parseRunsGrowingModeRole(p, forceScalar, diagRole)
	}
	if ecode != SONIC_OK {
		return ecode
	}
	p.finalizeDocumentInPlace()
	return SONIC_OK
}

// scanDocument executes the scanning stage and returns the resulting Document
// and scan status without initializing materialization arenas.
func (p *Parser) scanDocument(opts uint64, root reflect.Type, proj *projPlan) (Document, ErrorCode) {
	ecode := p.scanDocumentInPlace(opts, root, proj)
	if ecode != SONIC_OK {
		return Document{Json: p.Json, Utf8Inv: p.Utf8Inv}, ecode
	}
	return p.Document, SONIC_OK
}

// prepareDecodeContext prepares the pooled DecodeContext directly from p.Document
// without intermediate value copies of the 80-byte Document struct.
func (p *Parser) prepareDecodeContext(opts uint64, root reflect.Type) *Context {
	p.ctx.initFromParserDoc(&p.Document, opts, root, p.isEface)
	return &p.ctx
}

// initDecodeContext prepares the pooled DecodeContext over an already-scanned
// Document with zero scanning or route-switching side effects.
func (p *Parser) initDecodeContext(doc Document, opts uint64, root reflect.Type) *Context {
	p.ctx.initForDocument(doc, opts, root)
	return &p.ctx
}

const (
	minStrSlabCap  = 64
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
		p.nbuf.init(p.nodes)
		return
	}
	estNodes := cappedInitialNodesCap(p.Json, start, 11)
	if estNodes <= cap(p.nodes) {
		p.nodes = p.nodes[:cap(p.nodes)]
		p.nbuf.init(p.nodes)
		return
	}
	p.replaceNodesBacking(estNodes, 0)
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

	// 2. Clear ScanContext/Machine views before restoring bounded pooled node tape
	// so no side reference can keep an oversized backing alive.
	p.options = 0
	p.ScanKernelState = ScanKernelState{}
	p.errOffsetPlus1 = 0
	p.jsMachine.ReleasePoolRefs()
	p.isEface = false
	p.strictURC = false
	p.keepUseNumber = false
	p.singleDoc = false
	p.deferFloatOverflow = false

	// 3. Clear Document views and enforce physicalNodesCap <= maxPooledNodesCount.
	p.restoreBoundedPooledNodes(defaultNodesCount)
	p.Json = ""
	p.unesc = nil
	p.stat = jsonStat{}
	p.Utf8Inv = false

	// 4. Recycle remaining scratch buffers within hard pool bounds.
	if cap(p.unescBuf) > maxPooledUnescCap {
		p.unescBuf = nil
	} else if p.unescBuf != nil {
		p.unescBuf = p.unescBuf[:0]
	}
	idx := p.idxEntries
	if p.idxBackup != nil {
		if cap(p.idxBackup) > 0 && cap(p.idxBackup) <= maxPooledIdxEntriesCount {
			idx = p.idxBackup
		}
		p.idxBackup = nil
	}
	if cap(idx) > maxPooledIdxEntriesCount {
		p.idxEntries = nil
	} else if idx != nil {
		p.idxEntries = idx[:cap(idx)]
	} else {
		p.idxEntries = nil
	}
	if cap(p.idxSpans) > maxPooledIdxEntriesCount {
		p.idxSpans = nil
	} else if p.idxSpans != nil {
		p.idxSpans = p.idxSpans[:0]
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
	if code == SONIC_EOF {
		return buildSyntaxErrorAt(src, len(src), SONIC_EOF)
	}
	return buildSyntaxErrorAt(src, afterPos-1, code)
}

//go:noinline
func (p *Parser) fixError(code ErrorCode) error {
	if code == SONIC_OK {
		return nil
	}
	if p.errOffsetPlus1 > 0 {
		return buildSyntaxErrorAt(p.Json, p.errOffsetPlus1-1, code)
	}
	return syntaxErrorFromAfterCursor(p.Json, p.Pos(), code)
}

func Parse(data string, opt uint64) error {
	p := newParser(data, 0, opt)
	err := p.parse()
	p.free()
	return err
}
