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
	"errors"
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/rt"
)

const (
	defaultIdxEntriesCount   = 4096
	maxPooledIdxEntriesCount = 1 << 17 // 131,072 uint64s = 1 MiB max pooled index

	idxExcludeOptsMask = uint64(consts.OptionCaseSensitive) |
		uint64(OptionDisableUnknown) |
		(1 << _F_use_number) |
		(1 << _F_use_int64)
)

type idxRootKind uint8

const (
	idxRootNone idxRootKind = iota
	idxRootStruct
	idxRootPtrStruct
	idxRootStructSlice
	idxRootSliceInt
	idxRootSliceI64
	idxRootSliceI32
	idxRootSliceUint
	idxRootSliceU64
	idxRootSliceU32
	idxRootSliceF64
	idxRootSliceF32
	idxRootSliceBool
	idxRootSliceString
)

// idxRootPlan is an immutable compile-time witness proving that a root Go target
// type belongs to the static ValidatedDoc direct-materialization subset.
// Admission is decided strictly BEFORE scanning begins; it NEVER falls back to
// FromDom after mutating any user target slot.
type idxRootPlan struct {
	kind      idxRootKind
	structDec *structDecoder
	sliceDec  *sliceDecoder
	ptrDec    *ptrDecoder
}

// idxBindState bundles read-only ValidatedDoc views and materialization options
// into a single stack-allocated pointer so recursive struct/slice bind calls pass
// all arguments in CPU registers without copying the 88-byte ValidatedDoc struct.
type idxBindState struct {
	vdoc        jsonscan.ValidatedDoc
	entries     []uint64
	src         string
	unesc       []byte
	ctx         *Context
	parser      *Parser
	witness     *routeWitness
	firstErr    error
	opts        uint64
	startPos    int
	copyStr     bool
	validateStr bool
	strictURC   bool
	forceScalar bool
	spansBuilt  bool
}

// errIdxSuppressed is a non-nil sentinel returned after bs.firstErr has already
// captured the earliest chronological error in a request.
// Returning a non-nil sentinel preserves all downstream failure control-flow
// (e.g. outer nil any not overwriting on error) while skipping redundant error
// allocations and position lookups. bindValidatedRoot always unwraps bs.firstErr.
var errIdxSuppressed = errors.New("sonic/optdec: subsequent error diagnostic suppressed")

var (
	idxPlanSliceInt    = &idxRootPlan{kind: idxRootSliceInt}
	idxPlanSliceI64    = &idxRootPlan{kind: idxRootSliceI64}
	idxPlanSliceI32    = &idxRootPlan{kind: idxRootSliceI32}
	idxPlanSliceUint   = &idxRootPlan{kind: idxRootSliceUint}
	idxPlanSliceU64    = &idxRootPlan{kind: idxRootSliceU64}
	idxPlanSliceU32    = &idxRootPlan{kind: idxRootSliceU32}
	idxPlanSliceF64    = &idxRootPlan{kind: idxRootSliceF64}
	idxPlanSliceF32    = &idxRootPlan{kind: idxRootSliceF32}
	idxPlanSliceBool   = &idxRootPlan{kind: idxRootSliceBool}
	idxPlanSliceString = &idxRootPlan{kind: idxRootSliceString}

	exactPow10F64 = [32]float64{
		1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7,
		1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15,
		1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
		1, 1, 1, 1, 1, 1, 1, 1, 1,
	}
	exactPow10F32 = [16]float32{
		1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
		1, 1, 1, 1, 1,
	}
)

func compileIdxRootPlan(dec decFunc) *idxRootPlan {
	visited := make(map[*structDecoder]uint8, 8) // 0=unseen, 1=visiting(supported cycle), 2=supported, 3=unsupported
	switch d := dec.(type) {
	case *sliceIntDecoder:
		return idxPlanSliceInt
	case *sliceI64Decoder:
		return idxPlanSliceI64
	case *sliceI32Decoder:
		return idxPlanSliceI32
	case *sliceUintDecoder:
		return idxPlanSliceUint
	case *sliceU64Decoder:
		return idxPlanSliceU64
	case *sliceU32Decoder:
		return idxPlanSliceU32
	case *sliceF64Decoder:
		return idxPlanSliceF64
	case *sliceF32Decoder:
		return idxPlanSliceF32
	case *sliceBoolDecoder:
		return idxPlanSliceBool
	case *sliceStringDecoder:
		return idxPlanSliceString
	case *structDecoder:
		if canBindIdxStruct(d, visited) {
			return &idxRootPlan{kind: idxRootStruct, structDec: d}
		}
	case *ptrDecoder:
		if sd, ok := d.deref.(*structDecoder); ok && sd.goType == d.typ && canBindIdxStruct(sd, visited) {
			return &idxRootPlan{kind: idxRootPtrStruct, structDec: sd, ptrDec: d}
		}
	case *sliceDecoder:
		if canBindIdxSliceDecoder(d, visited) {
			return &idxRootPlan{kind: idxRootStructSlice, sliceDec: d}
		}
	}
	return nil
}

func canBindIdxSliceDecoder(sd *sliceDecoder, visited map[*structDecoder]uint8) bool {
	if sd == nil {
		return false
	}
	if sd.structDec != nil {
		return canBindIdxStruct(sd.structDec, visited)
	}
	if sd.ptrStructDec != nil {
		pd, ok := sd.elemDec.(*ptrDecoder)
		return ok && pd.typ == sd.ptrStructDec.goType && canBindIdxStruct(sd.ptrStructDec, visited)
	}
	return false
}

func canBindIdxStruct(sd *structDecoder, visited map[*structDecoder]uint8) bool {
	if sd == nil || !sd.fast {
		return false
	}
	switch visited[sd] {
	case 1, 2:
		return true
	case 3:
		return false
	}
	visited[sd] = 1
	for i := range sd.fields {
		fe := &sd.fields[i]
		switch fe.kind {
		case fieldKindString,
			fieldKindI64, fieldKindI32, fieldKindI16, fieldKindI8,
			fieldKindU64, fieldKindU32, fieldKindU16, fieldKindU8,
			fieldKindBool, fieldKindF64, fieldKindF32,
			fieldKindEface, fieldKindSliceEface,
			fieldKindSliceInt, fieldKindSliceI64, fieldKindSliceI32,
			fieldKindSliceUint, fieldKindSliceU64, fieldKindSliceU32,
			fieldKindSliceF64, fieldKindSliceF32, fieldKindSliceBool, fieldKindSliceString,
			fieldKindPtrString, fieldKindPtrI64, fieldKindPtrInt,
			fieldKindPtrI32, fieldKindPtrI16, fieldKindPtrI8,
			fieldKindPtrU64, fieldKindPtrUint, fieldKindPtrU32,
			fieldKindPtrU16, fieldKindPtrU8, fieldKindPtrBool,
			fieldKindPtrF64, fieldKindPtrF32:
			continue
		case fieldKindStruct, fieldKindPtrStruct:
			subSD := (*structDecoder)(fe.subDec)
			if !canBindIdxStruct(subSD, visited) {
				visited[sd] = 3
				return false
			}
		case fieldKindSlice:
			subSlice := (*sliceDecoder)(fe.subDec)
			if !canBindIdxSliceDecoder(subSlice, visited) {
				visited[sd] = 3
				return false
			}
		default:
			// Excludes fieldKindGeneric (Unmarshaler/RawMessage/map/non-empty interface/custom),
			// fieldKindArray, fieldKindWideOffset.
			visited[sd] = 3
			return false
		}
	}
	visited[sd] = 2
	return true
}

func (p *projPlan) canUseIdxOpts(jsonLen int, f uint64) bool {
	return p != nil && p.idxPlan != nil && (f&idxExcludeOptsMask) == 0 && jsonLen >= 0 && jsonLen <= jsonscan.MaxValidatedDocBytes
}

// canAdmitPreparedIdx performs the authoritative post-repair index admission check
// over the actual prepared source slice length. Both DecodeOne and
// DecodeWithSession call this helper after UTF-8 validation/repair completes.
func (p *projPlan) canAdmitPreparedIdx(preparedLen int, f uint64, ctrl *decodeTestControl) bool {
	if ctrl != nil && ctrl.forceRep == repForceTape {
		return false
	}
	if p == nil || p.idxPlan == nil || (f&idxExcludeOptsMask) != 0 {
		return false
	}
	maxBytes := ctrl.effectiveMaxIdxDocBytes()
	return preparedLen >= 0 && preparedLen <= maxBytes && preparedLen <= jsonscan.MaxValidatedDocBytes
}

func (p *Parser) presizeIdxOne() {
	start := firstContainerStart(p.Json)
	if start < 0 {
		return
	}
	estEntries := cappedInitialNodesCap(p.Json, start, 11)
	if estEntries <= cap(p.idxEntries) {
		p.idxEntries = p.idxEntries[:cap(p.idxEntries)]
		return
	}
	if estEntries > maxPooledIdxEntriesCount && p.idxBackup == nil && cap(p.idxEntries) > 0 && cap(p.idxEntries) <= maxPooledIdxEntriesCount {
		p.idxBackup = p.idxEntries
	}
	p.idxEntries = make([]uint64, estEntries)
}

func idxHardEntryBound(scanBytes int, ctrl *decodeTestControl) int {
	if scanBytes < 0 {
		scanBytes = 0
	}
	if scanBytes > jsonscan.MaxValidatedDocBytes {
		scanBytes = jsonscan.MaxValidatedDocBytes
	}
	// Each JSON byte can emit at most 1 index entry; +72 covers the 64-entry block check headroom.
	bound := scanBytes + 72
	if bound < minIdxHardBound {
		bound = minIdxHardBound
	}
	if ctrl != nil && ctrl.maxIdxEntryBound > 0 && ctrl.maxIdxEntryBound < bound {
		return ctrl.maxIdxEntryBound
	}
	return bound
}

const minIdxHardBound = 128

// growIdxEntries strictly grows p.idxEntries within the provable hard upper bound
// for scanBytes. Returns false if no further valid growth is possible.
func growIdxEntries(p *Parser, curLen, remBytes, scanBytes int, stage *nodeTapeGrowth, ctrl *decodeTestControl) bool {
	if ctrl != nil && ctrl.simulateNoGrow {
		return false
	}
	if *stage == growExhausted {
		return false
	}
	if remBytes < 0 {
		remBytes = 0
	}
	oldCap := len(p.idxEntries)
	hardBound := idxHardEntryBound(scanBytes, ctrl)
	if oldCap >= hardBound {
		*stage = growExhausted
		return false
	}

	var nextCap int
	switch *stage {
	case growGeometric:
		if oldCap > hardBound/4 {
			nextCap = hardBound
			*stage = growExhausted
		} else {
			nextCap = oldCap * 4
		}
		if nextCap < 256 {
			nextCap = 256
		}
		validCap := curLen + calMaxNodeCap(remBytes) + 68
		if nextCap > validCap && validCap > oldCap && validCap >= curLen+128 {
			nextCap = validCap
			*stage = growToHardBound
		}
	default:
		nextCap = curLen + remBytes + 72
		if nextCap < hardBound {
			nextCap = hardBound
		}
		*stage = growExhausted
	}
	if nextCap <= curLen+68 {
		nextCap = curLen + 128
	}
	if nextCap <= oldCap {
		nextCap = oldCap + 128
	}
	if nextCap > hardBound {
		nextCap = hardBound
		*stage = growExhausted
	}
	if nextCap <= oldCap {
		return false
	}
	if nextCap > maxPooledIdxEntriesCount && p.idxBackup == nil && cap(p.idxEntries) > 0 && cap(p.idxEntries) <= maxPooledIdxEntriesCount {
		p.idxBackup = p.idxEntries
	}
	grown := make([]uint64, nextCap)
	if curLen > 0 {
		copy(grown[:curLen], p.idxEntries[:curLen])
	}
	p.idxEntries = grown
	return true
}

// decodeViaValidatedIndex executes the 2-stage P4 pipeline:
//  1. Barrier stage: ScanValidatedDocBounded validates the entire top-level JSON value
//     with zero target mutation. If the input or bounded capacity is not applicable
//     to the index representation, fallbackTape=true is returned with zero user side effects.
//  2. Materialization stage: bindValidatedRoot traverses the verified 8B index
//     entries without re-parsing JSON punctuation.
func (p *Parser) decodeViaValidatedIndex(opts uint64, _ reflect.Type, vp unsafe.Pointer, plan *idxRootPlan, ctrl *decodeTestControl) (fallbackTape bool, err error) {
	if ctrl != nil && ctrl.initialIdxCap > 0 {
		p.idxEntries = make([]uint64, ctrl.initialIdxCap)
	} else if len(p.idxEntries) < 64 {
		n := cap(p.idxEntries)
		if n < 64 {
			n = defaultIdxEntriesCount
			p.idxEntries = make([]uint64, n)
		} else {
			p.idxEntries = p.idxEntries[:n]
		}
	}

	validateStr := (opts & (1 << _F_validate_string)) != 0
	strictURC := (opts & (1 << _F_disable_urc)) != 0
	startPos := 0
	if p.pos > 0 {
		startPos = p.pos
	}
	fullLen := len(p.Json)
	if p.end >= 0 && p.end < fullLen {
		fullLen = p.end
	}
	src := p.Json[:fullLen]
	scanBytes := fullLen - startPos
	maxDocBytes := ctrl.effectiveMaxIdxDocBytes()
	m := &p.jsMachine
	stage := growGeometric

	var vdoc jsonscan.ValidatedDoc
	var ckpt jsonscan.ValidatedScanCheckpoint
	forceScalar := ctrl != nil && ctrl.forceScalar
	var witness *routeWitness
	if ctrl != nil {
		witness = ctrl.witness
	}
	jsonscan.DiagRecordIndexScan()
	if witness != nil {
		witness.IndexScanStarted++
		witness.RequestedForceScalar = forceScalar
	}
	if p.unescBuf != nil {
		m.Unesc = p.unescBuf[:0]
	} else {
		m.Unesc = nil
	}
	m.ResetValidatedDoc(validateStr, strictURC, startPos)
	m.SetForceScalar(forceScalar)

	for {
		out := m.ScanValidatedDocResumable(src, p.idxEntries, maxDocBytes, &ckpt, &vdoc)
		p.unescBuf = m.Unesc
		if witness != nil {
			witness.ObservedProducers |= m.ObservedLexProducers()
		}
		switch out.Status {
		case jsonscan.ValidatedScanNeedsCapacity:
			hadProgress := ckpt.HasProgress()
			if !growIdxEntries(p, out.Result.Produced, fullLen-out.Result.Consumed, scanBytes, &stage, ctrl) {
				if witness != nil {
					witness.IndexFallbackToTape++
				}
				return true, nil
			}
			if hadProgress {
				jsonscan.DiagRecordIndexResume()
			} else {
				m.ResetValidatedDoc(validateStr, strictURC, startPos)
				m.SetForceScalar(forceScalar)
			}
			if witness != nil {
				witness.IndexCapacityGrows++
			}
			continue
		case jsonscan.ValidatedScanNotApplicable:
			if ctrl != nil && ctrl.witness != nil {
				ctrl.witness.IndexFallbackToTape++
			}
			return true, nil
		case jsonscan.ValidatedScanComplete:
			p.errOffsetPlus1 = 0
			p.pos = out.Result.Consumed
			p.end = fullLen
			goto barrierPassed
		default:
			res := out.Result
			errPos := res.Consumed
			switch res.Code {
			case jsonscan.ErrControlChar, jsonscan.ErrInvalidEscape, jsonscan.ErrInvalidEscapeUTF, jsonscan.ErrInvalidNumber:
				errPos = res.ErrorOffset
			}
			p.pos = errPos
			p.end = fullLen
			p.errOffsetPlus1 = res.ErrorOffset + 1
			return false, p.fixError(mapJSONScanError(res.Code))
		}
	}

barrierPassed:
	if !vdoc.IsValid() {
		return false, p.fixError(SONIC_INVALID_CHAR)
	}
	jsonscan.DiagRecordIndexBarrier()
	if ctrl != nil && ctrl.witness != nil {
		ctrl.witness.IndexBarrierPassed++
	}
	ctx := &p.ctx
	if ctx.active {
		ctx.resetMaterialization()
	}
	ctx.active = true
	ctx.options = opts
	ctx.Utf8Inv = p.Utf8Inv
	ctx.Doc.Json = src
	ctx.Doc.unesc = vdoc.Unesc()
	ctx.Doc.Utf8Inv = p.Utf8Inv

	bs := idxBindState{
		vdoc:        vdoc,
		entries:     vdoc.Entries(),
		src:         src,
		unesc:       vdoc.Unesc(),
		ctx:         ctx,
		parser:      p,
		witness:     witness,
		opts:        opts,
		startPos:    startPos,
		copyStr:     (opts & (1 << _F_copy_string)) != 0,
		validateStr: validateStr,
		strictURC:   strictURC,
		forceScalar: forceScalar,
	}
	jsonscan.DiagRecordIndexBind()
	if witness != nil {
		witness.IndexBinderEntered++
	}
	return false, bindValidatedRoot(vp, plan, &bs)
}

func bindValidatedRoot(vp unsafe.Pointer, plan *idxRootPlan, bs *idxBindState) error {
	err := bindValidatedRootInner(vp, plan, bs)
	if err != nil && bs.firstErr != nil {
		return bs.firstErr
	}
	return err
}

func bindValidatedRootInner(vp unsafe.Pointer, plan *idxRootPlan, bs *idxBindState) error {
	entries := bs.entries
	switch plan.kind {
	case idxRootStruct:
		e := entries[0]
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxNull {
			return nil
		}
		if k != jsonscan.IdxObject {
			return idxMismatchErr(bs, 0, plan.structDec.typ)
		}
		cnt := int(uint32(e >> 4))
		if cnt == 0 {
			return nil
		}
		return plan.structDec.bindValidatedObj(vp, 1, cnt, bs)

	case idxRootPtrStruct:
		e := entries[0]
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxNull {
			*(*unsafe.Pointer)(vp) = nil
			return nil
		}
		dst := *(*unsafe.Pointer)(vp)
		if dst == nil {
			dst = reflect.New(plan.structDec.goType).UnsafePointer()
			*(*unsafe.Pointer)(vp) = dst
		}
		if k != jsonscan.IdxObject {
			return idxMismatchErr(bs, 0, plan.structDec.typ)
		}
		cnt := int(uint32(e >> 4))
		if cnt == 0 {
			return nil
		}
		return plan.structDec.bindValidatedObj(dst, 1, cnt, bs)

	case idxRootStructSlice:
		return bindValidatedStructSlice(plan.sliceDec, vp, 0, bs)
	case idxRootSliceInt:
		return bindValidatedSliceInt(vp, 0, bs)
	case idxRootSliceI64:
		return bindValidatedSliceI64(vp, 0, bs)
	case idxRootSliceI32:
		return bindValidatedSliceI32(vp, 0, bs)
	case idxRootSliceUint:
		return bindValidatedSliceUint(vp, 0, bs)
	case idxRootSliceU64:
		return bindValidatedSliceU64(vp, 0, bs)
	case idxRootSliceU32:
		return bindValidatedSliceU32(vp, 0, bs)
	case idxRootSliceF64:
		return bindValidatedSliceF64(vp, 0, bs)
	case idxRootSliceF32:
		return bindValidatedSliceF32(vp, 0, bs)
	case idxRootSliceBool:
		return bindValidatedSliceBool(vp, 0, bs)
	case idxRootSliceString:
		return bindValidatedSliceString(vp, 0, bs)
	default:
		return nil
	}
}

func idxNextSibling(entries []uint64, idx int) int {
	e := entries[idx]
	k := e & jsonscan.IdxKindMask
	if k == jsonscan.IdxObject || k == jsonscan.IdxArray {
		return int(e >> 36)
	}
	return idx + 1
}

// ensureSpans builds the request-scoped raw-span sidecar at most once per
// top-level value on cold error or dynamic fallback paths.
//
//go:noinline
func (bs *idxBindState) ensureSpans() ([]jsonscan.ValidatedSourceSpan, error) {
	if !bs.spansBuilt {
		spans, posProducers, err := jsonscan.BuildValidatedSourceSpansObserved(
			bs.vdoc,
			bs.startPos,
			bs.validateStr,
			bs.strictURC,
			bs.forceScalar,
			bs.parser.idxSpans,
		)
		if bs.witness != nil {
			bs.witness.PositionProducers |= posProducers
		}
		if err != nil {
			return nil, err
		}
		bs.parser.idxSpans = spans
		bs.spansBuilt = true
		if bs.witness != nil {
			bs.witness.PositionBuilds++
		}
	}
	return bs.parser.idxSpans, nil
}

// resolveEntryPos returns the exact Node.Position() offset for logical entry idx
// in O(1) after at most a single O(L+N) sidecar construction across the request.
//
//go:noinline
func (bs *idxBindState) resolveEntryPos(idx int) (int, error) {
	if idx < 0 || idx >= len(bs.entries) {
		return 0, error_syntax(bs.startPos, bs.src, "internal index entry out of bounds")
	}
	e := bs.entries[idx]
	k := e & jsonscan.IdxKindMask
	switch k {
	case jsonscan.IdxNumberSpan, jsonscan.IdxStringPlain:
		return int(uint32(e >> 4)), nil
	}
	if idx == 0 && bs.vdoc.ValueStart() >= 0 {
		if k == jsonscan.IdxStringEsc {
			return bs.vdoc.ValueStart() + 1, nil
		}
		return bs.vdoc.ValueStart(), nil
	}
	spans, err := bs.ensureSpans()
	if err != nil {
		return 0, err
	}
	if idx >= len(spans) {
		return 0, error_syntax(bs.startPos, bs.src, "internal span sidecar length mismatch")
	}
	return spans[idx].EntryPosition(k), nil
}

// resolveRawSpan returns the exact raw [rawStart, rawEnd) byte range in bs.src
// for logical entry idx.
//
//go:noinline
func (bs *idxBindState) resolveRawSpan(idx int) (int, int, error) {
	if idx < 0 || idx >= len(bs.entries) {
		return 0, 0, error_syntax(bs.startPos, bs.src, "internal index entry out of bounds")
	}
	e := bs.entries[idx]
	k := e & jsonscan.IdxKindMask
	switch k {
	case jsonscan.IdxStringPlain:
		off := int(uint32(e >> 4))
		n := int(e >> 36)
		if off > 0 && off+n < len(bs.src) {
			return off - 1, off + n + 1, nil
		}
	case jsonscan.IdxNumberSpan:
		off := int(uint32(e >> 4))
		n := int(e >> 36)
		if off >= 0 && off+n <= len(bs.src) {
			return off, off + n, nil
		}
	}
	if idx == 0 && bs.vdoc.ValueStart() >= 0 {
		return bs.vdoc.ValueStart(), bs.vdoc.Consumed(), nil
	}
	spans, err := bs.ensureSpans()
	if err != nil {
		return 0, 0, err
	}
	if idx >= len(spans) {
		return 0, 0, error_syntax(bs.startPos, bs.src, "internal span sidecar length mismatch")
	}
	sp := spans[idx]
	return int(sp.RawStart), int(sp.RawEnd), nil
}

//go:noinline
func idxMismatchErr(bs *idxBindState, idx int, vt reflect.Type) error {
	if bs.firstErr != nil {
		return errIdxSuppressed
	}
	pos, err := bs.resolveEntryPos(idx)
	if err != nil {
		bs.firstErr = err
		return err
	}
	merr := MismatchTypeError{
		Pos:  pos,
		Src:  bs.src,
		Type: vt,
	}
	bs.firstErr = merr
	return merr
}

func parseValidSpanToI64(src string, e uint64) (int64, bool) {
	off := int(uint32(e >> 4))
	n := int(e >> 36)
	span := src[off : off+n]
	if span == "-0" {
		return 0, true
	}
	v, err := ParseI64(span)
	return v, err == nil
}

func parseValidSpanToU64(src string, e uint64) (uint64, bool) {
	off := int(uint32(e >> 4))
	n := int(e >> 36)
	span := src[off : off+n]
	v, err := ParseU64(span)
	return v, err == nil
}

func parseValidSpanToF64(src string, e uint64) (float64, bool) {
	off := int(uint32(e >> 4))
	n := int(e >> 36)
	return number.ParseValidJSONFloat64(src[off : off+n])
}

func parseValidSpanToF32(src string, e uint64) (float32, bool) {
	off := int(uint32(e >> 4))
	n := int(e >> 36)
	return number.ParseValidJSONFloat32(src[off : off+n])
}

func decodeIdxString(e uint64, bs *idxBindState) (string, bool) {
	k := e & jsonscan.IdxKindMask
	if k == jsonscan.IdxStringPlain {
		off := int(uint32(e >> 4))
		n := int(e >> 36)
		s := bs.src[off : off+n]
		if bs.copyStr && n != 0 {
			if copied, ok := bs.ctx.copyArenaHit(s); ok {
				return copied, true
			}
			return bs.ctx.copyArenaStringSlow(s), true
		}
		return s, true
	}
	if k == jsonscan.IdxStringEsc {
		uoff := int(uint32(e >> 4))
		ulen := int(e >> 36)
		return bs.ctx.strEscapedBytes(bs.unesc[uoff : uoff+ulen]), true
	}
	return "", false
}

func (d *structDecoder) bindValidatedObj(vp unsafe.Pointer, curIdx int, pairCount int, bs *idxBindState) error {
	var gerr error
	entriesPtr := unsafe.Pointer(unsafe.SliceData(bs.entries))
	srcPtr := unsafe.Pointer(unsafe.StringData(bs.src))
	copyStr := bs.copyStr
	ctx := bs.ctx
	fields := d.fields
	sigs := d.fieldKeySigs
	nfields := len(fields)
	if len(sigs) < nfields {
		nfields = len(sigs)
	}
	fieldsPtr := unsafe.Pointer(unsafe.SliceData(fields))
	sigsPtr := unsafe.Pointer(unsafe.SliceData(sigs))
	expectedIdx := 0

	for i := 0; i < pairCount; i++ {
		kEntry := *(*uint64)(unsafe.Add(entriesPtr, uintptr(curIdx)*8))
		vIdx := curIdx + 1
		vEntry := *(*uint64)(unsafe.Add(entriesPtr, uintptr(vIdx)*8))
		vKind := vEntry & jsonscan.IdxKindMask
		if vKind == jsonscan.IdxObject || vKind == jsonscan.IdxArray {
			curIdx = int(vEntry >> 36)
		} else {
			curIdx = vIdx + 1
		}

		var key string
		if (kEntry & jsonscan.IdxKindMask) == jsonscan.IdxStringPlain {
			kOff := int(uint32(kEntry >> 4))
			kLen := int(kEntry >> 36)
			key = unsafe.String((*byte)(unsafe.Add(srcPtr, kOff)), kLen)
		} else {
			uOff := int(uint32(kEntry >> 4))
			uLen := int(kEntry >> 36)
			key = rt.Mem2Str(bs.unesc[uOff : uOff+uLen])
		}

		var idx int
		matched := false
		if uint(expectedIdx) < uint(nfields) {
			sig := (*fieldKeySig)(unsafe.Add(sigsPtr, uintptr(expectedIdx)*unsafe.Sizeof(fieldKeySig{})))
			kn := len(key)
			if kn == sig.n {
				if kn >= 8 && kn <= 16 {
					matched = readStrU64(key, 0) == sig.head && readStrU64(key, kn-8) == sig.tail
				} else if kn >= 4 && kn < 8 {
					matched = uint64(readStrU32(key, 0)) == sig.head && uint64(readStrU32(key, kn-4)) == sig.tail
				} else if kn >= 17 && kn <= 24 {
					matched = readStrU64(key, 0) == sig.head && readStrU64(key, kn-8) == sig.tail && readStrU64(key, 8) == sig.mid
				} else if kn > 0 && kn < 4 {
					kp := unsafe.Pointer(unsafe.StringData(key))
					matched = (uint64(*(*byte)(kp)) | uint64(*(*byte)(unsafe.Add(kp, kn>>1)))<<8 | uint64(*(*byte)(unsafe.Add(kp, kn-1)))<<16) == sig.head
				} else if kn > 24 {
					feExp := (*fieldEntry)(unsafe.Add(fieldsPtr, uintptr(expectedIdx)*unsafe.Sizeof(fieldEntry{})))
					matched = readStrU64(key, 0) == sig.head && readStrU64(key, kn-8) == sig.tail && readStrU64(key, 8) == sig.mid && key[16:kn-8] == feExp.name[16:kn-8]
				} else {
					matched = true
				}
			}
		}
		if matched {
			idx = expectedIdx
			expectedIdx++
		} else {
			idx = d.fieldMap.Get(key, false)
			if idx == -1 {
				continue
			}
			expectedIdx = idx + 1
		}

		fe := (*fieldEntry)(unsafe.Add(fieldsPtr, uintptr(idx)*unsafe.Sizeof(fieldEntry{})))
		elem := unsafe.Add(vp, uintptr(fe.offset))
		fk := fe.kind

		if vKind == jsonscan.IdxNull {
			if fk <= fieldKindStruct || (fk == fieldKindEface && *(*interface{})(elem) == nil) {
				continue
			}
			if fk >= fieldKindPtrString || fk == fieldKindPtrStruct {
				*(*unsafe.Pointer)(elem) = nil
				continue
			}
			if err := d.bindValidatedFieldCold(fe, idx, elem, vIdx, vEntry, vKind, bs); gerr == nil && err != nil {
				gerr = err
			}
			continue
		}

		switch fk {
		case fieldKindString:
			if vKind == jsonscan.IdxStringPlain {
				off := int(uint32(vEntry >> 4))
				n := int(vEntry >> 36)
				s := unsafe.String((*byte)(unsafe.Add(srcPtr, off)), n)
				if !copyStr || n == 0 {
					*(*string)(elem) = s
					continue
				}
				if copied, ok := ctx.copyArenaHit(s); ok {
					*(*string)(elem) = copied
					continue
				}
				*(*string)(elem) = ctx.copyArenaStringSlow(s)
				continue
			}
		case fieldKindI64:
			if vKind == jsonscan.IdxUintInline {
				*(*int64)(elem) = int64(vEntry >> 4)
				continue
			}
			if vKind == jsonscan.IdxSintInline {
				*(*int64)(elem) = int64(vEntry) >> 4
				continue
			}
		case fieldKindI32:
			if vKind == jsonscan.IdxUintInline {
				uv := vEntry >> 4
				if uv <= math.MaxInt32 {
					*(*int32)(elem) = int32(uv)
					continue
				}
			} else if vKind == jsonscan.IdxSintInline {
				iv := int64(vEntry) >> 4
				if iv >= math.MinInt32 && iv <= math.MaxInt32 {
					*(*int32)(elem) = int32(iv)
					continue
				}
			}
		case fieldKindU64:
			if vKind == jsonscan.IdxUintInline {
				*(*uint64)(elem) = vEntry >> 4
				continue
			}
		case fieldKindU32:
			if vKind == jsonscan.IdxUintInline {
				uv := vEntry >> 4
				if uv <= math.MaxUint32 {
					*(*uint32)(elem) = uint32(uv)
					continue
				}
			}
		case fieldKindBool:
			if vKind == jsonscan.IdxTrue {
				*(*bool)(elem) = true
				continue
			}
			if vKind == jsonscan.IdxFalse {
				*(*bool)(elem) = false
				continue
			}
		case fieldKindF64:
			if vKind == jsonscan.IdxDecimalExact {
				*(*float64)(elem) = unpackExactDecimalF64(vEntry)
				continue
			}
			if vKind == jsonscan.IdxUintInline {
				*(*float64)(elem) = float64(vEntry >> 4)
				continue
			}
			if vKind == jsonscan.IdxSintInline {
				*(*float64)(elem) = sintToFloat64(int64(vEntry) >> 4)
				continue
			}
		case fieldKindF32:
			if vKind == jsonscan.IdxDecimalExact {
				v := vEntry >> 10
				frac := int((vEntry >> 5) & 31)
				if v < (1<<24) && frac <= 10 {
					if v == 0 {
						if (vEntry & 16) != 0 {
							*(*float32)(elem) = math.Float32frombits(1 << 31)
						} else {
							*(*float32)(elem) = 0
						}
					} else {
						fv := float32(v)
						if (vEntry & 16) != 0 {
							fv = -fv
						}
						*(*float32)(elem) = fv / exactPow10F32[frac]
					}
					continue
				}
			} else if vKind == jsonscan.IdxUintInline {
				*(*float32)(elem) = float32(vEntry >> 4)
				continue
			} else if vKind == jsonscan.IdxSintInline {
				*(*float32)(elem) = sintToFloat32(int64(vEntry) >> 4)
				continue
			}
		case fieldKindStruct:
			if vKind == jsonscan.IdxObject {
				cnt := int(uint32(vEntry >> 4))
				if cnt > 0 {
					sd := (*structDecoder)(fe.subDec)
					if err := sd.bindValidatedObj(elem, vIdx+1, cnt, bs); gerr == nil && err != nil {
						gerr = err
					}
				}
				continue
			}
		}

		if err := d.bindValidatedFieldCold(fe, idx, elem, vIdx, vEntry, vKind, bs); gerr == nil && err != nil {
			gerr = err
		}
	}
	return gerr
}

//go:noinline
func (d *structDecoder) bindValidatedFieldCold(
	fe *fieldEntry,
	idx int,
	elem unsafe.Pointer,
	vIdx int,
	vEntry uint64,
	vKind uint64,
	bs *idxBindState,
) error {
	if vKind == jsonscan.IdxNull {
		switch fe.kind {
		case fieldKindEface:
			if *(*interface{})(elem) != nil {
				return bindValidatedEfaceNullReuseOrFallback(d.fieldDecs[idx], elem, vIdx, bs)
			}
		case fieldKindSliceEface:
			*(*[]interface{})(elem) = nil
		case fieldKindSlice:
			((*sliceDecoder)(fe.subDec)).setNil(elem)
		case fieldKindSliceInt:
			*(*[]int)(elem) = nil
		case fieldKindSliceI64:
			*(*[]int64)(elem) = nil
		case fieldKindSliceI32:
			*(*[]int32)(elem) = nil
		case fieldKindSliceUint:
			*(*[]uint)(elem) = nil
		case fieldKindSliceU64:
			*(*[]uint64)(elem) = nil
		case fieldKindSliceU32:
			*(*[]uint32)(elem) = nil
		case fieldKindSliceF64:
			*(*[]float64)(elem) = nil
		case fieldKindSliceF32:
			*(*[]float32)(elem) = nil
		case fieldKindSliceBool:
			*(*[]bool)(elem) = nil
		case fieldKindSliceString:
			*(*[]string)(elem) = nil
		}
		return nil
	}

	if fe.kind >= fieldKindPtrString {
		if d.bindValidatedPtrScalar(fe.kind, elem, vEntry, vKind, bs) {
			return nil
		}
		return d.coldFieldMismatch(idx, elem, vIdx, bs)
	}

	src := bs.src
	switch fe.kind {
	case fieldKindString:
		if vKind == jsonscan.IdxStringEsc {
			uoff := int(uint32(vEntry >> 4))
			ulen := int(vEntry >> 36)
			*(*string)(elem) = bs.ctx.strEscapedBytes(bs.unesc[uoff : uoff+ulen])
			return nil
		}

	case fieldKindI64:
		if vKind == jsonscan.IdxNumberSpan {
			if iv, ok := parseValidSpanToI64(src, vEntry); ok {
				*(*int64)(elem) = iv
				return nil
			}
		}

	case fieldKindI32:
		if vKind == jsonscan.IdxNumberSpan {
			if iv, ok := parseValidSpanToI64(src, vEntry); ok && iv >= math.MinInt32 && iv <= math.MaxInt32 {
				*(*int32)(elem) = int32(iv)
				return nil
			}
		}

	case fieldKindI16:
		if vKind == jsonscan.IdxUintInline {
			uv := vEntry >> 4
			if uv <= math.MaxInt16 {
				*(*int16)(elem) = int16(uv)
				return nil
			}
		} else if vKind == jsonscan.IdxSintInline {
			iv := int64(vEntry) >> 4
			if iv >= math.MinInt16 && iv <= math.MaxInt16 {
				*(*int16)(elem) = int16(iv)
				return nil
			}
		} else if vKind == jsonscan.IdxNumberSpan {
			if iv, ok := parseValidSpanToI64(src, vEntry); ok && iv >= math.MinInt16 && iv <= math.MaxInt16 {
				*(*int16)(elem) = int16(iv)
				return nil
			}
		}

	case fieldKindI8:
		if vKind == jsonscan.IdxUintInline {
			uv := vEntry >> 4
			if uv <= math.MaxInt8 {
				*(*int8)(elem) = int8(uv)
				return nil
			}
		} else if vKind == jsonscan.IdxSintInline {
			iv := int64(vEntry) >> 4
			if iv >= math.MinInt8 && iv <= math.MaxInt8 {
				*(*int8)(elem) = int8(iv)
				return nil
			}
		} else if vKind == jsonscan.IdxNumberSpan {
			if iv, ok := parseValidSpanToI64(src, vEntry); ok && iv >= math.MinInt8 && iv <= math.MaxInt8 {
				*(*int8)(elem) = int8(iv)
				return nil
			}
		}

	case fieldKindU64:
		if vKind == jsonscan.IdxNumberSpan {
			if uv, ok := parseValidSpanToU64(src, vEntry); ok {
				*(*uint64)(elem) = uv
				return nil
			}
		}

	case fieldKindU32:
		if vKind == jsonscan.IdxNumberSpan {
			if uv, ok := parseValidSpanToU64(src, vEntry); ok && uv <= math.MaxUint32 {
				*(*uint32)(elem) = uint32(uv)
				return nil
			}
		}

	case fieldKindU16:
		if vKind == jsonscan.IdxUintInline {
			uv := vEntry >> 4
			if uv <= math.MaxUint16 {
				*(*uint16)(elem) = uint16(uv)
				return nil
			}
		} else if vKind == jsonscan.IdxNumberSpan {
			if uv, ok := parseValidSpanToU64(src, vEntry); ok && uv <= math.MaxUint16 {
				*(*uint16)(elem) = uint16(uv)
				return nil
			}
		}

	case fieldKindU8:
		if vKind == jsonscan.IdxUintInline {
			uv := vEntry >> 4
			if uv <= math.MaxUint8 {
				*(*uint8)(elem) = uint8(uv)
				return nil
			}
		} else if vKind == jsonscan.IdxNumberSpan {
			if uv, ok := parseValidSpanToU64(src, vEntry); ok && uv <= math.MaxUint8 {
				*(*uint8)(elem) = uint8(uv)
				return nil
			}
		}

	case fieldKindF64:
		if vKind == jsonscan.IdxNumberSpan {
			if fv, ok := parseValidSpanToF64(src, vEntry); ok {
				*(*float64)(elem) = fv
				return nil
			}
		}

	case fieldKindF32:
		if vKind == jsonscan.IdxDecimalExact {
			*(*float32)(elem) = unpackExactDecimalF32(vEntry)
			return nil
		}
		if vKind == jsonscan.IdxNumberSpan {
			if fv, ok := parseValidSpanToF32(src, vEntry); ok {
				*(*float32)(elem) = fv
				return nil
			}
		}

	case fieldKindEface:
		if *(*interface{})(elem) == nil {
			val, err := bindValidatedAnyValue(vIdx, vEntry, vKind, bs)
			if err != nil {
				return err
			}
			*(*interface{})(elem) = val
			return nil
		}
		return bindValidatedEfaceReuseOrFallback(d.fieldDecs[idx], elem, vIdx, vEntry, vKind, bs)

	case fieldKindSliceEface:
		if vKind == jsonscan.IdxArray {
			if cap(*(*[]interface{})(elem)) == 0 {
				cnt := int(uint32(vEntry >> 4))
				if cnt == 0 {
					*(*[]interface{})(elem) = []interface{}{}
					return nil
				}
				out, err := bindValidatedAnySliceFresh(vIdx+1, cnt, bs)
				*(*[]interface{})(elem) = out
				return err
			}
			return bindValidatedSliceEfaceReuseOrFallback(d.fieldDecs[idx], elem, vIdx, vEntry, bs)
		}

	case fieldKindPtrStruct:
		if vKind == jsonscan.IdxObject {
			sd := (*structDecoder)(fe.subDec)
			dst := *(*unsafe.Pointer)(elem)
			if dst == nil {
				dst = reflect.New(sd.goType).UnsafePointer()
				*(*unsafe.Pointer)(elem) = dst
			}
			cnt := int(uint32(vEntry >> 4))
			if cnt > 0 {
				return sd.bindValidatedObj(dst, vIdx+1, cnt, bs)
			}
			return nil
		}

	case fieldKindSlice:
		if vKind == jsonscan.IdxArray {
			sd := (*sliceDecoder)(fe.subDec)
			if uint32(vEntry>>4) == 0 {
				sd.setEmpty(elem)
				return nil
			}
			return bindValidatedStructSlice(sd, elem, vIdx, bs)
		}

	case fieldKindSliceInt:
		if vKind == jsonscan.IdxArray {
			if uint32(vEntry>>4) == 0 {
				*(*[]int)(elem) = []int{}
				return nil
			}
			return bindValidatedSliceInt(elem, vIdx, bs)
		}
	case fieldKindSliceI64:
		if vKind == jsonscan.IdxArray {
			if uint32(vEntry>>4) == 0 {
				*(*[]int64)(elem) = []int64{}
				return nil
			}
			return bindValidatedSliceI64(elem, vIdx, bs)
		}
	case fieldKindSliceI32:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceI32(elem, vIdx, bs)
		}
	case fieldKindSliceUint:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceUint(elem, vIdx, bs)
		}
	case fieldKindSliceU64:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceU64(elem, vIdx, bs)
		}
	case fieldKindSliceU32:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceU32(elem, vIdx, bs)
		}
	case fieldKindSliceF64:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceF64(elem, vIdx, bs)
		}
	case fieldKindSliceF32:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceF32(elem, vIdx, bs)
		}
	case fieldKindSliceBool:
		if vKind == jsonscan.IdxArray {
			return bindValidatedSliceBool(elem, vIdx, bs)
		}
	case fieldKindSliceString:
		if vKind == jsonscan.IdxArray {
			if uint32(vEntry>>4) == 0 {
				*(*[]string)(elem) = []string{}
				return nil
			}
			return bindValidatedSliceString(elem, vIdx, bs)
		}
	}
	return d.coldFieldMismatch(idx, elem, vIdx, bs)
}

func bindValidatedAnyValue(vIdx int, vEntry uint64, vKind uint64, bs *idxBindState) (interface{}, error) {
	switch vKind {
	case jsonscan.IdxNull:
		return nil, nil
	case jsonscan.IdxStringPlain:
		return strCommonAnyRaw(bs.ctx, bs.src, int(uint32(vEntry>>4)), int(vEntry>>36), bs.copyStr), nil
	case jsonscan.IdxStringEsc:
		uoff := int(uint32(vEntry >> 4))
		ulen := int(vEntry >> 36)
		return bs.ctx.strEscapedBytes(bs.unesc[uoff : uoff+ulen]), nil
	case jsonscan.IdxTrue:
		return true, nil
	case jsonscan.IdxFalse:
		return false, nil
	case jsonscan.IdxUintInline:
		return uintToFloat64Any(vEntry >> 4), nil
	case jsonscan.IdxSintInline:
		return sintToFloat64Any(int64(vEntry) >> 4), nil
	case jsonscan.IdxDecimalExact:
		return unpackExactDecimalF64(vEntry), nil
	case jsonscan.IdxNumberSpan:
		fv, ok := parseValidSpanToF64(bs.src, vEntry)
		if !ok {
			return nil, idxMismatchErr(bs, vIdx, float64Type)
		}
		return fv, nil
	case jsonscan.IdxObject:
		cnt := int(uint32(vEntry >> 4))
		if cnt == 0 {
			return make(map[string]interface{}), nil
		}
		return bindValidatedAnyMap(vIdx+1, cnt, bs)
	case jsonscan.IdxArray:
		cnt := int(uint32(vEntry >> 4))
		if cnt == 0 {
			return []interface{}{}, nil
		}
		return bindValidatedAnySliceFresh(vIdx+1, cnt, bs)
	default:
		return nil, nil
	}
}

func bindValidatedAnyMap(curIdx int, pairCount int, bs *idxBindState) (map[string]interface{}, error) {
	m := make(map[string]interface{}, pairCount)
	entries := bs.entries
	var gerr error
	for i := 0; i < pairCount; i++ {
		kEntry := entries[curIdx]
		vIdx := curIdx + 1
		vEntry := entries[vIdx]
		vKind := vEntry & jsonscan.IdxKindMask
		if vKind == jsonscan.IdxObject || vKind == jsonscan.IdxArray {
			curIdx = int(vEntry >> 36)
		} else {
			curIdx = vIdx + 1
		}
		key, _ := decodeIdxString(kEntry, bs)
		val, err := bindValidatedAnyValue(vIdx, vEntry, vKind, bs)
		m[key] = val
		if gerr == nil && err != nil {
			gerr = err
		}
	}
	return m, gerr
}

// isOverwritableIdxEface reports whether a non-nil interface{} holds a built-in
// non-pointer Go value that efaceDecoder.FromDom always replaces verbatim with
// the newly decoded value (never decoding in-place through a pointer or invoking
// custom Unmarshaler hooks).
func isOverwritableIdxEface(v interface{}) bool {
	switch v.(type) {
	case bool, float64, string, map[string]interface{}, []interface{},
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, json.Number:
		return true
	default:
		return false
	}
}

//go:noinline
func bindValidatedEfaceNullReuseOrFallback(dec decFunc, elem unsafe.Pointer, vIdx int, bs *idxBindState) error {
	if isOverwritableIdxEface(*(*interface{})(elem)) {
		*(*interface{})(elem) = nil
		return nil
	}
	return bindSubtreeViaFromDom(dec, elem, vIdx, bs)
}

//go:noinline
func bindValidatedEfaceReuseOrFallback(dec decFunc, elem unsafe.Pointer, vIdx int, vEntry uint64, vKind uint64, bs *idxBindState) error {
	if !isOverwritableIdxEface(*(*interface{})(elem)) {
		return bindSubtreeViaFromDom(dec, elem, vIdx, bs)
	}
	val, err := bindValidatedAnyValue(vIdx, vEntry, vKind, bs)
	if err != nil {
		return err
	}
	*(*interface{})(elem) = val
	return nil
}

//go:noinline
func bindValidatedSliceEfaceReuseOrFallback(dec decFunc, elem unsafe.Pointer, vIdx int, vEntry uint64, bs *idxBindState) error {
	cnt := int(uint32(vEntry >> 4))
	if cnt == 0 {
		*(*[]interface{})(elem) = []interface{}{}
		return nil
	}
	cur := *(*[]interface{})(elem)
	if cap(cur) < cnt {
		return bindSubtreeViaFromDom(dec, elem, vIdx, bs)
	}
	dst := cur[:cnt]
	for i := 0; i < cnt; i++ {
		if dst[i] != nil && !isOverwritableIdxEface(dst[i]) {
			return bindSubtreeViaFromDom(dec, elem, vIdx, bs)
		}
	}
	*(*[]interface{})(elem) = dst
	curIdx := vIdx + 1
	entries := bs.entries
	var gerr error
	for i := 0; i < cnt; i++ {
		e := entries[curIdx]
		k := e & jsonscan.IdxKindMask
		itemIdx := curIdx
		if k == jsonscan.IdxObject || k == jsonscan.IdxArray {
			curIdx = int(e >> 36)
		} else {
			curIdx++
		}
		val, err := bindValidatedAnyValue(itemIdx, e, k, bs)
		if err == nil {
			dst[i] = val
		} else if gerr == nil {
			gerr = err
		}
	}
	return gerr
}

func bindValidatedAnySliceFresh(curIdx int, elemCount int, bs *idxBindState) ([]interface{}, error) {
	out := make([]interface{}, elemCount)
	entries := bs.entries
	var gerr error
	for i := 0; i < elemCount; i++ {
		e := entries[curIdx]
		k := e & jsonscan.IdxKindMask
		vIdx := curIdx
		if k == jsonscan.IdxObject || k == jsonscan.IdxArray {
			curIdx = int(e >> 36)
		} else {
			curIdx++
		}
		val, err := bindValidatedAnyValue(vIdx, e, k, bs)
		out[i] = val
		if gerr == nil && err != nil {
			gerr = err
		}
	}
	return out, gerr
}

//go:noinline
func bindSubtreeViaFromDom(dec decFunc, elem unsafe.Pointer, vIdx int, bs *idxBindState) error {
	rawStart, rawEnd, err := bs.resolveRawSpan(vIdx)
	if err != nil {
		if bs.firstErr == nil {
			bs.firstErr = err
		}
		return err
	}
	spanBytes := rawEnd - rawStart
	jsonscan.DiagRecordSubtreeScan(spanBytes)
	if bs.witness != nil {
		bs.witness.IndexSubtreeFallback++
		bs.witness.IndexSubtreeBytes += uint64(spanBytes)
	}
	subOpts := bs.opts &^ (1 << _F_validate_string)
	subP := newSubParserPreValidated(bs.src, rawStart, rawEnd, subOpts, bs.ctx.Utf8Inv)
	defer subP.free()
	ecode := subP.scanDocumentInPlaceWithRole(subOpts, anyType, nil, bs.forceScalar, jsonscan.DiagRoleSubtree)
	if bs.witness != nil {
		bs.witness.SubtreeProducers |= subP.jsMachine.ObservedLexProducers()
	}
	if ecode != SONIC_OK {
		serr := subP.fixError(ecode)
		if bs.firstErr == nil {
			bs.firstErr = serr
		}
		return serr
	}
	subCtx := subP.prepareDecodeContext(bs.opts, anyType)
	subCtx.Doc.Json = bs.src
	derr := dec.FromDom(elem, subCtx.Root(), subCtx)
	if derr != nil && bs.firstErr == nil {
		bs.firstErr = derr
	}
	return derr
}

func (d *structDecoder) bindValidatedPtrScalar(
	fk uint8,
	elem unsafe.Pointer,
	vEntry uint64,
	vKind uint64,
	bs *idxBindState,
) bool {
	src := bs.src
	ctx := bs.ctx
	switch fk {
	case fieldKindPtrString:
		s, ok := decodeIdxString(vEntry, bs)
		if !ok {
			return false
		}
		pp := (**string)(elem)
		dst := *pp
		if dst == nil {
			if len(s) > maxArenaStrLen {
				dst = new(string)
			} else {
				dst = ctx.allocStrPtr(int(d.ptrStrHint))
			}
			*pp = dst
		}
		*dst = s
		return true

	case fieldKindPtrI64, fieldKindPtrInt:
		var iv int64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			iv, ok = int64(vEntry>>4), true
		} else if vKind == jsonscan.IdxSintInline {
			iv, ok = int64(vEntry)>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			iv, ok = parseValidSpanToI64(src, vEntry)
		}
		if !ok {
			return false
		}
		pp := (**int64)(elem)
		dst := *pp
		if dst == nil {
			dst = (*int64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = iv
		return true

	case fieldKindPtrI32:
		var iv int64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			iv, ok = int64(vEntry>>4), true
		} else if vKind == jsonscan.IdxSintInline {
			iv, ok = int64(vEntry)>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			iv, ok = parseValidSpanToI64(src, vEntry)
		}
		if !ok || iv < math.MinInt32 || iv > math.MaxInt32 {
			return false
		}
		pp := (**int32)(elem)
		dst := *pp
		if dst == nil {
			dst = (*int32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = int32(iv)
		return true

	case fieldKindPtrI16:
		var iv int64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			iv, ok = int64(vEntry>>4), true
		} else if vKind == jsonscan.IdxSintInline {
			iv, ok = int64(vEntry)>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			iv, ok = parseValidSpanToI64(src, vEntry)
		}
		if !ok || iv < math.MinInt16 || iv > math.MaxInt16 {
			return false
		}
		pp := (**int16)(elem)
		dst := *pp
		if dst == nil {
			dst = (*int16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = int16(iv)
		return true

	case fieldKindPtrI8:
		var iv int64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			iv, ok = int64(vEntry>>4), true
		} else if vKind == jsonscan.IdxSintInline {
			iv, ok = int64(vEntry)>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			iv, ok = parseValidSpanToI64(src, vEntry)
		}
		if !ok || iv < math.MinInt8 || iv > math.MaxInt8 {
			return false
		}
		pp := (**int8)(elem)
		dst := *pp
		if dst == nil {
			dst = (*int8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = int8(iv)
		return true

	case fieldKindPtrU64, fieldKindPtrUint:
		var uv uint64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			uv, ok = vEntry>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			uv, ok = parseValidSpanToU64(src, vEntry)
		}
		if !ok {
			return false
		}
		pp := (**uint64)(elem)
		dst := *pp
		if dst == nil {
			dst = (*uint64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = uv
		return true

	case fieldKindPtrU32:
		var uv uint64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			uv, ok = vEntry>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			uv, ok = parseValidSpanToU64(src, vEntry)
		}
		if !ok || uv > math.MaxUint32 {
			return false
		}
		pp := (**uint32)(elem)
		dst := *pp
		if dst == nil {
			dst = (*uint32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = uint32(uv)
		return true

	case fieldKindPtrU16:
		var uv uint64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			uv, ok = vEntry>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			uv, ok = parseValidSpanToU64(src, vEntry)
		}
		if !ok || uv > math.MaxUint16 {
			return false
		}
		pp := (**uint16)(elem)
		dst := *pp
		if dst == nil {
			dst = (*uint16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = uint16(uv)
		return true

	case fieldKindPtrU8:
		var uv uint64
		var ok bool
		if vKind == jsonscan.IdxUintInline {
			uv, ok = vEntry>>4, true
		} else if vKind == jsonscan.IdxNumberSpan {
			uv, ok = parseValidSpanToU64(src, vEntry)
		}
		if !ok || uv > math.MaxUint8 {
			return false
		}
		pp := (**uint8)(elem)
		dst := *pp
		if dst == nil {
			dst = (*uint8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = uint8(uv)
		return true

	case fieldKindPtrBool:
		if vKind != jsonscan.IdxTrue && vKind != jsonscan.IdxFalse {
			return false
		}
		pp := (**bool)(elem)
		dst := *pp
		if dst == nil {
			dst = (*bool)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = vKind == jsonscan.IdxTrue
		return true

	case fieldKindPtrF64:
		var fv float64
		var ok bool
		if vKind == jsonscan.IdxDecimalExact {
			fv, ok = unpackExactDecimalF64(vEntry), true
		} else if vKind == jsonscan.IdxNumberSpan {
			fv, ok = parseValidSpanToF64(src, vEntry)
		} else if vKind == jsonscan.IdxUintInline {
			fv, ok = float64(vEntry>>4), true
		} else if vKind == jsonscan.IdxSintInline {
			fv, ok = sintToFloat64(int64(vEntry)>>4), true
		}
		if !ok {
			return false
		}
		pp := (**float64)(elem)
		dst := *pp
		if dst == nil {
			dst = (*float64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = fv
		return true

	case fieldKindPtrF32:
		var fv float32
		var ok bool
		if vKind == jsonscan.IdxDecimalExact {
			fv, ok = unpackExactDecimalF32(vEntry), true
		} else if vKind == jsonscan.IdxNumberSpan {
			fv, ok = parseValidSpanToF32(src, vEntry)
		} else if vKind == jsonscan.IdxUintInline {
			fv, ok = float32(vEntry>>4), true
		} else if vKind == jsonscan.IdxSintInline {
			fv, ok = sintToFloat32(int64(vEntry)>>4), true
		}
		if !ok {
			return false
		}
		pp := (**float32)(elem)
		dst := *pp
		if dst == nil {
			dst = (*float32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
			*pp = dst
		}
		*dst = fv
		return true
	}
	return false
}

//go:noinline
func (d *structDecoder) coldFieldMismatch(fieldIdx int, elem unsafe.Pointer, vIdx int, bs *idxBindState) error {
	fdec := d.fieldDecs[fieldIdx]
	if pd, ok := fdec.(*ptrDecoder); ok {
		if *(*unsafe.Pointer)(elem) == nil {
			if sd, ok := pd.deref.(*structDecoder); ok && sd.goType == pd.typ {
				*(*unsafe.Pointer)(elem) = reflect.New(sd.goType).UnsafePointer()
			} else {
				*(*unsafe.Pointer)(elem) = reflect.New(pd.typ).UnsafePointer()
			}
		}
		return idxMismatchErr(bs, vIdx, decoderReflectType(pd.deref, pd.typ))
	}
	return idxMismatchErr(bs, vIdx, decoderReflectType(fdec, nil))
}

func decoderReflectType(dec decFunc, fallback reflect.Type) reflect.Type {
	switch x := dec.(type) {
	case *stringDecoder:
		return x.typ
	case *i64Decoder:
		return x.typ
	case *i32Decoder:
		return x.typ
	case *i16Decoder:
		return x.typ
	case *i8Decoder:
		return x.typ
	case *u64Decoder:
		return x.typ
	case *u32Decoder:
		return x.typ
	case *u16Decoder:
		return x.typ
	case *u8Decoder:
		return x.typ
	case *boolDecoder:
		return x.typ
	case *f64Decoder:
		return x.typ
	case *f32Decoder:
		return x.typ
	case *structDecoder:
		return x.typ
	case *sliceDecoder:
		return x.typ
	case *sliceEfaceDecoder:
		return sliceEfaceType
	case *sliceIntDecoder:
		return sliceIntType
	case *sliceI64Decoder:
		return sliceI64Type
	case *sliceI32Decoder:
		return sliceI32Type
	case *sliceUintDecoder:
		return sliceUintType
	case *sliceU64Decoder:
		return sliceU64Type
	case *sliceU32Decoder:
		return sliceU32Type
	case *sliceF64Decoder:
		return sliceF64Type
	case *sliceF32Decoder:
		return sliceF32Type
	case *sliceBoolDecoder:
		return sliceBoolType
	case *sliceStringDecoder:
		return sliceStringType
	default:
		return fallback
	}
}

func bindValidatedStructSlice(sd *sliceDecoder, vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		sd.setNil(vp)
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sd.typ)
	}
	size := int(uint32(ae >> 4))
	if size == 0 {
		sd.setEmpty(vp)
		return nil
	}

	rv := reflect.NewAt(sd.typ, vp).Elem()
	if rv.Cap() < size {
		rv.Grow(size - rv.Len())
	}
	rv.SetLen(size)
	elems := rv.UnsafePointer()
	elemSize := sd.size

	var gerr error
	entriesPtr := unsafe.Pointer(unsafe.SliceData(entries))
	cur := arrIdx + 1
	if elemSD := sd.structDec; elemSD != nil {
		for i := 0; i < size; i++ {
			elem := unsafe.Add(elems, uintptr(i)*elemSize)
			e := *(*uint64)(unsafe.Add(entriesPtr, uintptr(cur)*8))
			k := e & jsonscan.IdxKindMask
			if k == jsonscan.IdxObject {
				next := int(e >> 36)
				cnt := int(uint32(e >> 4))
				if cnt > 0 {
					if err := elemSD.bindValidatedObj(elem, cur+1, cnt, bs); gerr == nil && err != nil {
						gerr = err
					}
				}
				cur = next
			} else if k == jsonscan.IdxNull {
				cur++
			} else {
				if gerr == nil {
					gerr = idxMismatchErr(bs, cur, elemSD.typ)
				}
				cur = idxNextSibling(entries, cur)
			}
		}
		return gerr
	}

	psd := sd.ptrStructDec
	ptrs := unsafe.Slice((*unsafe.Pointer)(elems), size)
	for i := 0; i < size; i++ {
		e := *(*uint64)(unsafe.Add(entriesPtr, uintptr(cur)*8))
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxNull {
			ptrs[i] = nil
			cur++
			continue
		}
		dst := ptrs[i]
		if dst == nil {
			dst = reflect.New(psd.goType).UnsafePointer()
			ptrs[i] = dst
		}
		if k == jsonscan.IdxObject {
			next := int(e >> 36)
			cnt := int(uint32(e >> 4))
			if cnt > 0 {
				if err := psd.bindValidatedObj(dst, cur+1, cnt, bs); gerr == nil && err != nil {
					gerr = err
				}
			}
			cur = next
		} else {
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, psd.typ)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	return gerr
}

func bindValidatedSliceI64(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]int64)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceI64Type)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]int64)(vp)
	s := prepareSlice(dst, size)
	if size == 0 {
		*dst = s
		return nil
	}
	entriesPtr := unsafe.Pointer(unsafe.SliceData(entries))
	sPtr := unsafe.Pointer(unsafe.SliceData(s))
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := *(*uint64)(unsafe.Add(entriesPtr, uintptr(cur)*8))
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxUintInline {
			*(*int64)(unsafe.Add(sPtr, uintptr(i)*8)) = int64(e >> 4)
			cur++
		} else if k == jsonscan.IdxSintInline {
			*(*int64)(unsafe.Add(sPtr, uintptr(i)*8)) = int64(e) >> 4
			cur++
		} else if k == jsonscan.IdxNumberSpan {
			if iv, ok := parseValidSpanToI64(src, e); ok {
				*(*int64)(unsafe.Add(sPtr, uintptr(i)*8)) = iv
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, int64Type)
			}
			cur++
		} else if k == jsonscan.IdxNull {
			cur++
		} else {
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, int64Type)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceInt(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]int)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceIntType)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]int)(vp)
	s := prepareSlice(dst, size)
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		switch e & jsonscan.IdxKindMask {
		case jsonscan.IdxUintInline:
			s[i] = int(int64(e >> 4))
			cur++
		case jsonscan.IdxSintInline:
			s[i] = int(int64(e) >> 4)
			cur++
		case jsonscan.IdxNumberSpan:
			if iv, ok := parseValidSpanToI64(src, e); ok {
				s[i] = int(iv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, intType)
			}
			cur++
		case jsonscan.IdxNull:
			cur++
		default:
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, intType)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceI32(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]int32)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceI32Type)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]int32)(vp)
	s := prepareSlice(dst, size)
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		switch e & jsonscan.IdxKindMask {
		case jsonscan.IdxUintInline:
			uv := e >> 4
			if uv <= math.MaxInt32 {
				s[i] = int32(uv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, int32Type)
			}
			cur++
		case jsonscan.IdxSintInline:
			iv := int64(e) >> 4
			if iv >= math.MinInt32 && iv <= math.MaxInt32 {
				s[i] = int32(iv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, int32Type)
			}
			cur++
		case jsonscan.IdxNumberSpan:
			if iv, ok := parseValidSpanToI64(src, e); ok && iv >= math.MinInt32 && iv <= math.MaxInt32 {
				s[i] = int32(iv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, int32Type)
			}
			cur++
		case jsonscan.IdxNull:
			cur++
		default:
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, int32Type)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceU64(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]uint64)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceU64Type)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]uint64)(vp)
	s := prepareSlice(dst, size)
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		switch e & jsonscan.IdxKindMask {
		case jsonscan.IdxUintInline:
			s[i] = e >> 4
			cur++
		case jsonscan.IdxNumberSpan:
			if uv, ok := parseValidSpanToU64(src, e); ok {
				s[i] = uv
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uint64Type)
			}
			cur++
		case jsonscan.IdxNull:
			cur++
		default:
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uint64Type)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceUint(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]uint)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceUintType)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]uint)(vp)
	s := prepareSlice(dst, size)
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		switch e & jsonscan.IdxKindMask {
		case jsonscan.IdxUintInline:
			s[i] = uint(e >> 4)
			cur++
		case jsonscan.IdxNumberSpan:
			if uv, ok := parseValidSpanToU64(src, e); ok {
				s[i] = uint(uv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uintType)
			}
			cur++
		case jsonscan.IdxNull:
			cur++
		default:
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uintType)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceU32(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]uint32)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceU32Type)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]uint32)(vp)
	s := prepareSlice(dst, size)
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		switch e & jsonscan.IdxKindMask {
		case jsonscan.IdxUintInline:
			uv := e >> 4
			if uv <= math.MaxUint32 {
				s[i] = uint32(uv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uint32Type)
			}
			cur++
		case jsonscan.IdxNumberSpan:
			if uv, ok := parseValidSpanToU64(src, e); ok && uv <= math.MaxUint32 {
				s[i] = uint32(uv)
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uint32Type)
			}
			cur++
		case jsonscan.IdxNull:
			cur++
		default:
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, uint32Type)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func unpackExactDecimalF64(e uint64) float64 {
	v := e >> 10
	if v == 0 {
		if (e & 16) != 0 {
			return math.Float64frombits(1 << 63)
		}
		return 0
	}
	fv := float64(v)
	if (e & 16) != 0 {
		fv = -fv
	}
	return fv / exactPow10F64[(e>>5)&31]
}

func unpackExactDecimalF32(e uint64) float32 {
	v := e >> 10
	frac := (e >> 5) & 31
	if v-1 < (1<<24)-1 && frac <= 10 {
		fv := float32(v)
		if (e & 16) != 0 {
			fv = -fv
		}
		return fv / exactPow10F32[frac]
	}
	return unpackExactDecimalF32Slow(e)
}

//go:noinline
func unpackExactDecimalF32Slow(e uint64) float32 {
	v := e >> 10
	if v == 0 {
		if (e & 16) != 0 {
			return math.Float32frombits(1 << 31)
		}
		return 0
	}
	return number.ConvertExactMantFloat32(v, int((e>>5)&31), (e&16) != 0)
}

func bindValidatedSliceF64(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]float64)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceF64Type)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]float64)(vp)
	s := prepareSlice(dst, size)
	if size == 0 {
		*dst = s
		return nil
	}
	entriesPtr := unsafe.Pointer(unsafe.SliceData(entries))
	sPtr := unsafe.Pointer(unsafe.SliceData(s))
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := *(*uint64)(unsafe.Add(entriesPtr, uintptr(cur)*8))
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxDecimalExact {
			*(*float64)(unsafe.Add(sPtr, uintptr(i)*8)) = unpackExactDecimalF64(e)
			cur++
		} else if k == jsonscan.IdxUintInline {
			*(*float64)(unsafe.Add(sPtr, uintptr(i)*8)) = float64(e >> 4)
			cur++
		} else if k == jsonscan.IdxSintInline {
			*(*float64)(unsafe.Add(sPtr, uintptr(i)*8)) = sintToFloat64(int64(e) >> 4)
			cur++
		} else if k == jsonscan.IdxNumberSpan {
			if fv, ok := parseValidSpanToF64(src, e); ok {
				*(*float64)(unsafe.Add(sPtr, uintptr(i)*8)) = fv
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, float64Type)
			}
			cur++
		} else if k == jsonscan.IdxNull {
			cur++
		} else {
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, float64Type)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceF32(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]float32)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceF32Type)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]float32)(vp)
	s := prepareSlice(dst, size)
	if size == 0 {
		*dst = s
		return nil
	}
	entriesPtr := unsafe.Pointer(unsafe.SliceData(entries))
	sPtr := unsafe.Pointer(unsafe.SliceData(s))
	src := bs.src
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := *(*uint64)(unsafe.Add(entriesPtr, uintptr(cur)*8))
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxDecimalExact {
			v := e >> 10
			frac := (e >> 5) & 31
			if v-1 < (1<<24)-1 && frac <= 10 {
				fv := float32(v)
				if (e & 16) != 0 {
					fv = -fv
				}
				*(*float32)(unsafe.Add(sPtr, uintptr(i)*4)) = fv / exactPow10F32[frac]
			} else {
				*(*float32)(unsafe.Add(sPtr, uintptr(i)*4)) = unpackExactDecimalF32Slow(e)
			}
			cur++
		} else if k == jsonscan.IdxUintInline {
			*(*float32)(unsafe.Add(sPtr, uintptr(i)*4)) = float32(e >> 4)
			cur++
		} else if k == jsonscan.IdxSintInline {
			*(*float32)(unsafe.Add(sPtr, uintptr(i)*4)) = sintToFloat32(int64(e) >> 4)
			cur++
		} else if k == jsonscan.IdxNumberSpan {
			if fv, ok := parseValidSpanToF32(src, e); ok {
				*(*float32)(unsafe.Add(sPtr, uintptr(i)*4)) = fv
			} else if gerr == nil {
				gerr = idxMismatchErr(bs, cur, float32Type)
			}
			cur++
		} else if k == jsonscan.IdxNull {
			cur++
		} else {
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, float32Type)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceBool(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]bool)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceBoolType)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]bool)(vp)
	s := prepareSlice(dst, size)
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		switch e & jsonscan.IdxKindMask {
		case jsonscan.IdxTrue:
			s[i] = true
			cur++
		case jsonscan.IdxFalse:
			s[i] = false
			cur++
		case jsonscan.IdxNull:
			cur++
		default:
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, boolType)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}

func bindValidatedSliceString(vp unsafe.Pointer, arrIdx int, bs *idxBindState) error {
	entries := bs.entries
	ae := entries[arrIdx]
	ak := ae & jsonscan.IdxKindMask
	if ak == jsonscan.IdxNull {
		*(*[]string)(vp) = nil
		return nil
	}
	if ak != jsonscan.IdxArray {
		return idxMismatchErr(bs, arrIdx, sliceStringType)
	}
	size := int(uint32(ae >> 4))
	dst := (*[]string)(vp)
	s := prepareSlice(dst, size)
	src := bs.src
	copyStr := bs.copyStr
	ctx := bs.ctx
	var gerr error
	cur := arrIdx + 1
	for i := 0; i < size; i++ {
		e := entries[cur]
		k := e & jsonscan.IdxKindMask
		if k == jsonscan.IdxStringPlain {
			off := int(uint32(e >> 4))
			n := int(e >> 36)
			str := src[off : off+n]
			if copyStr && n != 0 {
				if copied, ok := ctx.copyArenaHit(str); ok {
					str = copied
				} else {
					str = ctx.copyArenaStringSlow(str)
				}
			}
			s[i] = str
			cur++
		} else if k == jsonscan.IdxStringEsc {
			uoff := int(uint32(e >> 4))
			ulen := int(e >> 36)
			s[i] = ctx.strEscapedBytes(bs.unesc[uoff : uoff+ulen])
			cur++
		} else if k == jsonscan.IdxNull {
			cur++
		} else {
			if gerr == nil {
				gerr = idxMismatchErr(bs, cur, stringType)
			}
			cur = idxNextSibling(entries, cur)
		}
	}
	*dst = s
	return gerr
}
