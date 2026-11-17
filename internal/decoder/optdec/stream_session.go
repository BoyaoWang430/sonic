//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import (
	"encoding/json"
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/errors"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

const (
	maxRetainedStreamUnescBytes = 64 << 10
	initialStreamNodesCount     = 512
)

var streamSessionPool = sync.Pool{
	New: func() interface{} {
		return &StreamSession{
			p: &Parser{
				ScanContext: ScanContext{
					dbuf: make([]byte, types.MaxDigitNums, types.MaxDigitNums),
				},
				Document: Document{
					nodes: make([]node, initialStreamNodesCount, initialStreamNodesCount),
				},
			},
			unesc: make([]byte, 0, 256),
		}
	},
}

func getStreamSession() *StreamSession {
	return streamSessionPool.Get().(*StreamSession)
}

// FreeStreamSession resets and returns a StreamSession to the pool once a
// StreamDecoder drains its current buffer or terminates.
func FreeStreamSession(s *StreamSession) {
	if s == nil {
		return
	}
	s.Reset()
	s.lastPtrType = nil
	s.lastElemType = nil
	s.lastDec = nil
	s.lastIsNamed = false
	s.lastIsEface = false
	streamSessionPool.Put(s)
}

// StreamSession maintains a resumable, bounded JSON scan & Tape state across
// both incremental io.Reader chunks (StreamDecoder) and complete in-memory
// read-only scans (eof=true). Already-scanned prefixes of a multi-chunk value
// are never rescanned on refill, and raw input coordinates remain decoupled
// from unescaped string scratch (s.unesc).
type StreamSession struct {
	p                  *Parser
	ctx                Context
	unesc              []byte
	lastPtrType        reflect.Type
	lastElemType       reflect.Type
	lastDec            decFunc
	cur                int
	errPos             int
	nodeLen            int
	tokStart           int
	strScanOff         int
	strFirstEsc        int
	utf8Checked        int
	phase              uint8
	active             bool
	utf8Invalid        bool
	useNumber          bool
	deferFloatOverflow bool
	validateStr        bool
	lastIsNamed        bool
	lastIsEface        bool
	unquoteFlags       uint64
	trav               streamTraversalBlock
}

func (s *StreamSession) ensureParser() *Parser {
	if s.p == nil {
		s.p = &Parser{
			ScanContext: ScanContext{
				dbuf: make([]byte, types.MaxDigitNums, types.MaxDigitNums),
			},
			Document: Document{
				nodes: make([]node, defaultNodesCount, defaultNodesCount),
			},
		}
	}
	if s.unesc == nil {
		s.unesc = make([]byte, 0, 256)
	}
	return s.p
}

// Begin prepares or resets the session for scanning a new root JSON value.
func (s *StreamSession) Begin(opts uint64, isEface bool) {
	s.ctx.resetMaterialization()
	p := s.ensureParser()
	if p.backup != nil {
		p.nodes = p.backup
		p.backup = nil
	}
	if cap(s.unesc) > maxRetainedStreamUnescBytes {
		s.unesc = make([]byte, 0, 256)
	} else {
		s.unesc = s.unesc[:0]
	}
	p.nbuf = nodeBuf{parent: -1}
	p.Utf8Inv = false
	p.isEface = isEface
	s.cur = 0
	s.errPos = -1
	s.nodeLen = 0
	s.tokStart = 0
	s.strScanOff = 0
	s.strFirstEsc = -1
	s.utf8Checked = 0
	s.phase = streamPhaseValue
	s.active = true
	s.utf8Invalid = false
	s.useNumber = (opts & (1 << _F_use_number)) != 0
	s.deferFloatOverflow = !s.useNumber
	s.validateStr = (opts & (1 << _F_validate_string)) != 0
	if opts&(1<<_F_disable_urc) != 0 {
		s.unquoteFlags = 0
	} else {
		s.unquoteFlags = types.F_UNICODE_REPLACE
	}
	s.trav.reset()
}

func (s *StreamSession) beginComplete(opts uint64, isEface bool, src string, singleDoc bool) {
	s.Begin(opts, isEface)
	p := s.p
	if singleDoc {
		pos := s.trav.skipSpace(src, 0)
		s.cur = pos
		if pos < len(src) && (src[pos] == '{' || src[pos] == '[') {
			estNodes := cappedInitialNodesCap(src, pos, 12)
			if estNodes > len(p.nodes) {
				if estNodes > maxPooledNodesCount && p.backup == nil && cap(p.nodes) <= maxPooledNodesCount {
					p.backup = p.nodes
				}
				p.nodes = make([]node, estNodes)
			}
		}
	} else if len(p.nodes) < defaultNodesCount {
		p.nodes = make([]node, defaultNodesCount, defaultNodesCount)
	}
}

// Reset aborts any in-progress scan and releases oversized scratch buffers.
func (s *StreamSession) Reset() {
	if s == nil {
		return
	}
	s.active = false
	// 1. First drop user-returned allocation references in DecodeContext.
	s.ctx.resetMaterialization()
	// 2. Clear Document & ScanContext scratch.
	if s.p != nil {
		nodes := s.p.nodes
		if s.p.backup != nil {
			nodes = s.p.backup
			s.p.backup = nil
		}
		s.p.Document = Document{nodes: nodes}
		s.p.nbuf = nodeBuf{parent: -1}
	}
	if cap(s.unesc) > maxRetainedStreamUnescBytes {
		s.unesc = nil
	} else if s.unesc != nil {
		s.unesc = s.unesc[:0]
	}
	s.trav.reset()
}

// DecodeStream drives incremental scan + single-pass materialization from the
// caller's stream buffer (starting at *scanp), calling refill when more chunk
// bytes are required from io.Reader.
func DecodeStream(buf *[]byte, scanp *int, f uint64, val interface{}, sessPtr **StreamSession, refillArg unsafe.Pointer, refill func(arg unsafe.Pointer, compactTo int) (eof bool, readErr error)) error {
	if val == nil {
		return &json.InvalidUnmarshalError{}
	}
	sess := *sessPtr
	if sess == nil {
		sess = getStreamSession()
		*sessPtr = sess
	}

	var etp reflect.Type
	var vp unsafe.Pointer
	var dec decFunc
	var isEface bool

	rv := reflect.ValueOf(val)
	vt := rv.Type()
	if vt == sess.lastPtrType && !sess.lastIsNamed {
		if rv.IsNil() {
			return &json.InvalidUnmarshalError{Type: vt}
		}
		etp = sess.lastElemType
		vp = rv.UnsafePointer()
		dec = sess.lastDec
		isEface = sess.lastIsEface
	} else {
		var err error
		etp, vp, err = unpackRootValue(val)
		if err != nil {
			return err
		}
		dec, err = findOrCompile(etp)
		if err != nil {
			return err
		}
		isEface = etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
		sess.lastPtrType = vt
		sess.lastElemType = etp
		sess.lastDec = dec
		sess.lastIsNamed = vt.Name() != ""
		sess.lastIsEface = isEface
	}

	// Force OptionCopyString because StreamDecoder recycles/slides *buf across calls.
	matOpts := f | (1 << _F_copy_string)
	sess.Begin(matOpts, isEface)

	rootStart := *scanp
	eof := false
	for {
		raw := (*buf)[rootStart:]
		reason, code := sess.Step(rt.Mem2Str(raw), eof)
		switch reason {
		case scanStopDone:
			valEnd := rootStart + sess.cur
			rawVal := (*buf)[rootStart:valEnd]
			if sess.utf8Invalid {
				owned := string(rawVal)
				pos := 0
				sess.Reset()
				derr := Decode(&owned, &pos, matOpts, val)
				*scanp = valEnd
				return derr
			}

			p := sess.p
			doc := Document{
				Json:  rt.Mem2Str(rawVal),
				unesc: sess.unesc,
				nodes: p.nodes[:sess.nodeLen],
				stat:  p.nbuf.stat,
			}
			sess.ctx.initForDocument(doc, matOpts, etp)
			merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
			sess.ctx.resetMaterialization()
			sess.active = false
			if p.backup != nil {
				p.nodes = p.backup
				p.backup = nil
			}
			if merr != nil {
				return errors.DetachSource(merr, rawVal)
			}
			*scanp = valEnd
			return nil

		case scanStopNeedMore:
			var rerr error
			eof, rerr = refill(refillArg, rootStart)
			rootStart = *scanp
			if rerr != nil {
				sess.Reset()
				return rerr
			}

		default:
			raw := (*buf)[rootStart:]
			ownedSrc := string(raw)
			err := sess.syntaxError(ownedSrc, code)
			sess.Reset()
			return err
		}
	}
}

func decodeReadOnlyOneCompiled(s string, f uint64, etp reflect.Type, vp unsafe.Pointer, dec decFunc) (string, int, error) {
	sess := getStreamSession()
	defer FreeStreamSession(sess)

	isEface := etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	sess.beginComplete(f, isEface, s, true)
	_, code := sess.Step(s, true)
	if sess.utf8Invalid || (code != SONIC_OK && sess.validateStr && sess.cur < len(s) && !utf8.ValidateString(s[sess.cur:])) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(s), "\ufffd")
		s = rt.Mem2Str(dbuf)
		sess.beginComplete(f&^(1<<_F_validate_string), isEface, s, true)
		_, code = sess.Step(s, true)
	}
	if code != SONIC_OK {
		endPos := sess.cur
		if endPos > len(s) {
			endPos = len(s)
		}
		return s, endPos, fix_error(s, 0, sess.syntaxError(s, code))
	}

	p := sess.p
	doc := Document{
		Json:  s,
		unesc: sess.unesc,
		nodes: p.nodes[:sess.nodeLen],
		stat:  p.nbuf.stat,
	}
	sess.ctx.initForDocument(doc, f, etp)
	merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
	sess.ctx.resetMaterialization()
	endPos := sess.cur
	if endPos > len(s) {
		endPos = len(s)
	}
	if (f&(1<<_F_validate_string)) != 0 && endPos < len(s) && !utf8.ValidateString(s[endPos:]) {
		tail := utf8.CorrectWith(nil, rt.Str2Mem(s[endPos:]), "\ufffd")
		s = s[:endPos] + rt.Mem2Str(tail)
	}
	return s, endPos, fix_error(s, 0, merr)
}

func decodeReadOnlyWithSessionCompiled(s *string, i *int, f uint64, etp reflect.Type, vp unsafe.Pointer, dec decFunc, sessPtr **InputSession) error {
	origPos := *i
	sub := (*s)[origPos:]

	sess := getStreamSession()
	defer FreeStreamSession(sess)

	isEface := etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	sess.beginComplete(f, isEface, sub, false)
	_, code := sess.Step(sub, true)

	if sess.utf8Invalid {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(sub), "\ufffd")
		sub = rt.Mem2Str(dbuf)
		*s = (*s)[:origPos] + sub
		sess.beginComplete(f&^(1<<_F_validate_string), isEface, sub, false)
		_, code = sess.Step(sub, true)
	}

	if code != SONIC_OK {
		if sessPtr != nil {
			*sessPtr = nil
		}
		newPos := origPos + sess.cur
		if newPos > len(*s) {
			newPos = len(*s)
		}
		*i = newPos
		return fix_error(*s, origPos, sess.syntaxError(sub, code))
	}

	p := sess.p
	doc := Document{
		Json:  sub,
		unesc: sess.unesc,
		nodes: p.nodes[:sess.nodeLen],
		stat:  p.nbuf.stat,
	}
	sess.ctx.initForDocument(doc, f, etp)
	merr := dec.FromDom(vp, sess.ctx.Root(), &sess.ctx)
	sess.ctx.resetMaterialization()
	err := fix_error(*s, origPos, merr)

	newPos := origPos + sess.cur
	if newPos > len(*s) {
		newPos = len(*s)
	}
	if sessPtr != nil && err != nil {
		*sessPtr = nil
	}
	*i = newPos
	return err
}
