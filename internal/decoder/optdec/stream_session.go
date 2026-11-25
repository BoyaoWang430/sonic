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
	"encoding/json"
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/errors"
	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/kernels"
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

// StreamSession drives the shared internal/jsonscan.Machine across both
// incremental io.Reader chunks (StreamDecoder) and complete in-memory
// read-only scans (eof=true). Already-scanned prefixes of a multi-chunk value
// are never rescanned on refill, and raw input coordinates remain decoupled
// from unescaped string scratch (s.unesc).
type StreamSession struct {
	p            *Parser
	ctx          Context
	m            jsonscan.Machine
	unesc        []byte
	lastPtrType  reflect.Type
	lastElemType reflect.Type
	lastDec      decFunc
	cur          int
	errPos       int
	nodeLen      int
	active       bool
	utf8Invalid  bool
	lastIsNamed  bool
	lastIsEface  bool
}

func (s *StreamSession) ensureParser() *Parser {
	if s.p == nil {
		s.p = &Parser{
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
	s.m.ReleasePoolRefs()
	p.restoreBoundedPooledNodes(initialStreamNodesCount)
	if cap(s.unesc) > maxRetainedStreamUnescBytes {
		s.unesc = make([]byte, 0, 256)
	} else {
		s.unesc = s.unesc[:0]
	}
	p.nbuf = nodeBuf{parent: -1, ncap: physicalNodesCap(p.nodes)}
	p.Utf8Inv = false
	p.isEface = isEface
	s.cur = 0
	s.errPos = -1
	s.nodeLen = 0
	s.active = true
	s.utf8Invalid = false
	s.m.Unesc = s.unesc
	s.m.Reset(strictProfileFromOpts(opts, false), jsonscan.ModeEmit)
	s.m.Nodes = nodesAsJSONScanWritable(p.nodes)
}

func (s *StreamSession) beginComplete(opts uint64, isEface bool, src string, singleDoc bool) {
	s.Begin(opts, isEface)
	p := s.p
	if singleDoc {
		pos := kernels.SkipSpace(src, 0)
		if pos < len(src) && (src[pos] == '{' || src[pos] == '[') {
			estNodes := cappedInitialNodesCap(src, pos, 12) + nodeGuardSlots
			if estNodes > physicalNodesCap(p.nodes) {
				p.replaceNodesBacking(estNodes, 0)
			}
		}
	} else if physicalNodesCap(p.nodes) < defaultNodesCount {
		p.replaceNodesBacking(defaultNodesCount, 0)
	}
	s.m.Nodes = nodesAsJSONScanWritable(p.nodes)
}

func (s *StreamSession) growNodes(remBytes int, eof bool) {
	p := s.p
	curLen := physicalNodesCap(p.nodes)
	nextCap := curLen * 4
	if eof && curLen >= defaultNodesCount {
		nextCap = curLen * 2
	}
	if nextCap < 64 {
		nextCap = 64
	}
	if eof && remBytes > 0 {
		remMax := s.nodeLen + calMaxNodeCap(remBytes) + nodeGuardSlots
		if nextCap > remMax && remMax >= s.nodeLen+64 {
			nextCap = remMax
		}
	}
	if nextCap <= s.nodeLen+nodeGuardSlots {
		nextCap = s.nodeLen + 64
	}
	p.replaceNodesBacking(nextCap, s.nodeLen)
	s.m.Nodes = nodesAsJSONScanWritable(p.nodes)
}

func (s *StreamSession) syntaxError(src string, code ErrorCode) error {
	if s.errPos >= 0 {
		return buildSyntaxErrorAt(src, s.errPos, code)
	}
	return syntaxErrorFromAfterCursor(src, s.cur, code)
}

// Step advances the shared internal/jsonscan.Machine over src (complete inputs
// pass eof=true; chunked stream inputs pass eof=false until final reader chunk).
func (s *StreamSession) Step(src string, eof bool) (scanStopReason, ErrorCode) {
	p := s.p
	for {
		s.m.Nodes = nodesAsJSONScanWritable(p.nodes)
		res := s.m.Step(src, eof)
		s.unesc = s.m.Unesc
		s.cur = res.Consumed
		s.errPos = res.ErrorOffset
		s.nodeLen = res.Produced
		s.utf8Invalid = s.m.Lex.UTF8Invalid
		p.pos = res.Consumed
		p.nbuf.nlen = res.Produced
		p.nbuf.depth = uint64(s.m.Grammar.Depth)
		p.nbuf.stat = statFromJSONScan(s.m.Stat)
		switch res.Reason {
		case jsonscan.StopNeedOutput:
			s.growNodes(len(src)-res.Consumed, eof)
			continue
		case jsonscan.StopDone:
			return scanStopDone, SONIC_OK
		case jsonscan.StopNeedInput:
			return scanStopNeedMore, SONIC_OK
		default:
			return scanStopSyntaxError, mapJSONScanError(res.Code)
		}
	}
}

// parkCleanup releases scan and materialization borrows and enforces bounded
// scratch retention immediately upon returning from DecodeStream while the
// StreamSession stays parked with buffered next values.
// It preserves caller cursor (*scanp), buffered reader data, and root type cache.
func (s *StreamSession) parkCleanup() {
	s.ctx.resetMaterialization()
	s.m.ReleasePoolRefs()
	s.active = false
	if s.p != nil {
		s.p.restoreBoundedPooledNodes(initialStreamNodesCount)
	}
	if cap(s.unesc) > maxRetainedStreamUnescBytes {
		s.unesc = nil
	} else if s.unesc != nil {
		s.unesc = s.unesc[:0]
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
	s.m = jsonscan.Machine{}
	if s.p != nil {
		s.p.restoreBoundedPooledNodes(initialStreamNodesCount)
		s.p.Json = ""
		s.p.unesc = nil
		s.p.stat = jsonStat{}
		s.p.Utf8Inv = false
		s.p.nbuf = nodeBuf{parent: -1, ncap: physicalNodesCap(s.p.nodes)}
	}
	if cap(s.unesc) > maxRetainedStreamUnescBytes {
		s.unesc = nil
	} else if s.unesc != nil {
		s.unesc = s.unesc[:0]
	}
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
				origValStr := string(rawVal)
				owned := origValStr
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
			sess.parkCleanup()
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
	origS := s
	sess := getStreamSession()
	defer FreeStreamSession(sess)

	isEface := etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	sess.beginComplete(f, isEface, origS, true)
	_, code := sess.Step(origS, true)
	if code != SONIC_OK {
		endPos := sess.cur
		if endPos > len(origS) {
			endPos = len(origS)
		}
		return origS, endPos, fix_error(origS, 0, sess.syntaxError(origS, code))
	}
	origEndPos := sess.cur
	if origEndPos > len(origS) {
		origEndPos = len(origS)
	}
	utf8Inv := sess.utf8Invalid
	if utf8Inv {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(origS[:origEndPos]), "\ufffd")
		s = rt.Mem2Str(dbuf)
		sess.beginComplete(f, isEface, s, true)
		sess.m.Lex.UTF8Checked = len(s)
		_, code = sess.Step(s, true)
		if code != SONIC_OK {
			return origS, origEndPos, fix_error_orig(origS, 0, true, sess.syntaxError(s, code))
		}
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
	return origS, origEndPos, fix_error_orig(origS, 0, utf8Inv, merr)
}
