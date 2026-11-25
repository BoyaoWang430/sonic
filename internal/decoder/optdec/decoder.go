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
	stdutf8 "unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/decoder/errors"
	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/option"
)

type (
	MismatchTypeError = errors.MismatchTypeError
	SyntaxError       = errors.SyntaxError
)

const (
	_F_allow_control    = consts.F_allow_control
	_F_copy_string      = consts.F_copy_string
	_F_disable_unknown  = consts.F_disable_unknown
	_F_disable_urc      = consts.F_disable_urc
	_F_use_int64        = consts.F_use_int64
	_F_use_number       = consts.F_use_number
	_F_validate_string  = consts.F_validate_string
	_F_no_validate_json = consts.F_no_validate_json
)

type Options = consts.Options

const (
	OptionUseInt64         = consts.OptionUseInt64
	OptionUseNumber        = consts.OptionUseNumber
	OptionUseUnicodeErrors = consts.OptionUseUnicodeErrors
	OptionDisableUnknown   = consts.OptionDisableUnknown
	OptionCopyString       = consts.OptionCopyString
	OptionValidateString   = consts.OptionValidateString
	OptionNoValidateJSON   = consts.OptionNoValidateJSON
)

type rootPtrInfo struct {
	etp     reflect.Type
	dec     decFunc
	proj    *projPlan
	isNamed bool
}

var (
	rootPtrCache      = caching.CreateTypedCache[*rootPtrInfo]()
	efaceRootDec      = &efaceDecoder{}
	mapEfaceRootDec   = &mapEfaceDecoder{}
	sliceEfaceRootDec = &sliceEfaceDecoder{}
	mapStringRootDec  = &mapStringDecoder{}
	stringRootDec     = &stringDecoder{typ: stringType}
	intRootDec        = (&compiler{}).compileInt(intType)
	int64RootDec      = &i64Decoder{typ: int64Type}
	float64RootDec    = &f64Decoder{typ: float64Type}
	boolRootDec       = &boolDecoder{typ: boolType}
)

func unpackRootValue(val interface{}) (etp reflect.Type, vp unsafe.Pointer, err error) {
	etp, vp, _, _, err = unpackAndCompileRoot(val)
	return
}

func unpackAndCompileRoot(val interface{}) (etp reflect.Type, vp unsafe.Pointer, dec decFunc, proj *projPlan, err error) {
	if val == nil {
		return nil, nil, nil, nil, &json.InvalidUnmarshalError{}
	}
	switch v := val.(type) {
	case *interface{}:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return anyType, unsafe.Pointer(v), efaceRootDec, nil, nil
	case *map[string]interface{}:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return mapEfaceType, unsafe.Pointer(v), mapEfaceRootDec, nil, nil
	case *[]interface{}:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return sliceEfaceType, unsafe.Pointer(v), sliceEfaceRootDec, nil, nil
	case *map[string]string:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return mapStringType, unsafe.Pointer(v), mapStringRootDec, nil, nil
	case *string:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return stringType, unsafe.Pointer(v), stringRootDec, projScalarPlan, nil
	case *int:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return intType, unsafe.Pointer(v), intRootDec, projScalarPlan, nil
	case *int64:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return int64Type, unsafe.Pointer(v), int64RootDec, projScalarPlan, nil
	case *float64:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return float64Type, unsafe.Pointer(v), float64RootDec, projScalarPlan, nil
	case *bool:
		if v == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)}
		}
		return boolType, unsafe.Pointer(v), boolRootDec, projScalarPlan, nil
	}

	rv := reflect.ValueOf(val)
	vt := rv.Type()
	if info, ok := rootPtrCache.Get(vt); ok {
		vp = rv.UnsafePointer()
		if vp == nil {
			return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: vt}
		}
		if info.isNamed {
			ptrSlot := reflect.New(vt)
			*(*unsafe.Pointer)(ptrSlot.UnsafePointer()) = vp
			vp = ptrSlot.UnsafePointer()
		}
		return info.etp, vp, info.dec, info.proj, nil
	}

	if vt.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, nil, nil, nil, &json.InvalidUnmarshalError{Type: vt}
	}

	vp = rv.UnsafePointer()
	etp = vt.Elem()
	isNamed := vt.Name() != ""

	/* check the defined pointer type for issue 379 */
	if isNamed {
		ptrSlot := reflect.New(vt)
		*(*unsafe.Pointer)(ptrSlot.UnsafePointer()) = vp
		etp = vt
		vp = ptrSlot.UnsafePointer()
	}

	dec, err = findOrCompile(etp)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	proj = compileProjPlan(dec)
	rootPtrCache.PutMany([]caching.TypedBatchItem[*rootPtrInfo]{{
		Vt: vt,
		Val: &rootPtrInfo{
			etp:     etp,
			dec:     dec,
			proj:    proj,
			isNamed: isNamed,
		},
	}})
	return etp, vp, dec, proj, nil
}

func Decode(s *string, i *int, f uint64, val interface{}) error {
	return DecodeWithSession(s, i, f, val, nil)
}

// DecodeOne decodes a single JSON value starting at s[0] without heap-escaping a Decoder or Context.
// Both parseSchemaRoot (shared jsonscan.Machine) and parseImpl (read-only LexBatch)
// operate directly over read-only source slices at all sizes.
func DecodeOne(s string, f uint64, val interface{}) (string, int, error) {
	return decodeOneWithControl(s, f, val, nil)
}

func decodeOneWithControl(s string, f uint64, val interface{}, ctrl *decodeTestControl) (string, int, error) {
	etp, vp, dec, proj, err := unpackAndCompileRoot(val)
	if err != nil {
		return s, 0, err
	}

	var p *Parser
	if ctrl != nil && ctrl.pinnedParser != nil {
		p = initParserLazy(ctrl.pinnedParser, s, 0, f)
		p.singleDoc = true
		defer p.reset()
	} else {
		p = newParserLazy(s, 0, f)
		p.singleDoc = true
		defer p.free()
	}

	preparedLen := len(p.Json)
	var activeProj *projPlan
	useIdx := false
	if (ctrl == nil || ctrl.forceRep == repAuto) && proj != nil && proj.shouldUseOpts(preparedLen, f) {
		activeProj = proj
		jsonscan.DiagRecordSchemaRoute()
		if ctrl != nil && ctrl.witness != nil {
			ctrl.witness.SchemaRouteSelected++
		}
	} else if proj.canAdmitPreparedIdx(preparedLen, f, ctrl) {
		useIdx = true
		jsonscan.DiagRecordIndexRoute()
		if ctrl != nil && ctrl.witness != nil {
			ctrl.witness.IndexRouteSelected++
		}
		if preparedLen > presizeSingleDocBytes && (ctrl == nil || ctrl.initialIdxCap == 0) {
			p.presizeIdxOne()
		}
	}

	utf8Inv := p.Utf8Inv
	if useIdx {
		fallbackTape, idxErr := p.decodeViaValidatedIndex(f, etp, vp, proj.idxPlan, ctrl)
		if fallbackTape {
			useIdx = false
			p.pos = 0
			p.end = preparedLen
			p.errOffsetPlus1 = 0
			p.nbuf.init(p.nodes)
		} else {
			err = idxErr
		}
	}
	if !useIdx {
		if activeProj == nil {
			jsonscan.DiagRecordTapeRoute()
			if ctrl != nil && ctrl.witness != nil {
				ctrl.witness.TapeRouteSelected++
			}
			if preparedLen > presizeSingleDocBytes && (proj == nil || !proj.hasRawSubtree) {
				p.presizeNodesOne()
			}
		}
		forceScalar := ctrl != nil && ctrl.forceScalar
		ecode := p.scanDocumentInPlaceMode(f, etp, activeProj, forceScalar)
		if ctrl != nil && ctrl.witness != nil {
			ctrl.witness.RequestedForceScalar = forceScalar
			ctrl.witness.ObservedProducers |= p.jsMachine.ObservedLexProducers()
		}
		if ecode != SONIC_OK {
			err = p.fixError(ecode)
		} else {
			ctx := p.prepareDecodeContext(f, etp)
			err = dec.FromDom(vp, ctx.Root(), ctx)
		}
	}
	err = fix_error_orig(s, 0, utf8Inv, err)
	endPos := p.Pos()
	if utf8Inv {
		endPos = mapRepairedOffsetToOrig(s, endPos)
	}
	if endPos > len(s) {
		endPos = len(s)
	}
	return s, endPos, err
}

func DecodeWithSession(s *string, i *int, f uint64, val interface{}, sessPtr **InputSession) error {
	return decodeSessionWithControl(s, i, f, val, sessPtr, nil)
}

func decodeSessionWithControl(s *string, i *int, f uint64, val interface{}, sessPtr **InputSession, ctrl *decodeTestControl) error {
	var sess *InputSession
	if sessPtr != nil {
		sess = *sessPtr
	}

	etp, vp, dec, proj, err := unpackAndCompileRoot(val)
	if err != nil {
		return err
	}

	origPos := *i
	if origPos < 0 {
		origPos = 0
	} else if origPos > len(*s) {
		origPos = len(*s)
	}

	/* parse into document: multi-value session never presizes from the remaining stream tail */
	var p *Parser
	if ctrl != nil && ctrl.pinnedParser != nil {
		if sess != nil && sess.src == *s && origPos >= sess.base && origPos <= len(*s) &&
			((uint64(f)&(1<<_F_validate_string)) == 0 || sess.utf8Checked) {
			p = ctrl.pinnedParser
			p.Json = (*s)[origPos:]
			p.pos = 0
			p.end = len(p.Json)
			p.options = uint64(f)
			p.nbuf.init(p.nodes)
		} else {
			p = initParserLazy(ctrl.pinnedParser, *s, origPos, uint64(f))
		}
		defer p.reset()
	} else {
		p = newParserFromSession(*s, origPos, uint64(f), sess)
		defer p.free()
	}

	origFull := *s
	origScanPos := p.pos
	origScanEnd := p.end
	preparedRem := len(p.Json) - origScanPos
	utf8Inv := p.Utf8Inv
	if utf8Inv {
		*s = parserCorrectedSource(p, *s, origPos)
	}

	var activeProj *projPlan
	useIdx := false
	if (ctrl == nil || ctrl.forceRep == repAuto) && proj != nil && proj.shouldUseOpts(preparedRem, f) {
		activeProj = proj
		jsonscan.DiagRecordSchemaRoute()
		if ctrl != nil && ctrl.witness != nil {
			ctrl.witness.SchemaRouteSelected++
		}
	} else if proj.canAdmitPreparedIdx(preparedRem, uint64(f), ctrl) {
		useIdx = true
		jsonscan.DiagRecordIndexRoute()
		if ctrl != nil && ctrl.witness != nil {
			ctrl.witness.IndexRouteSelected++
		}
	}

	if useIdx {
		fallbackTape, idxErr := p.decodeViaValidatedIndex(uint64(f), etp, vp, proj.idxPlan, ctrl)
		if fallbackTape {
			useIdx = false
			p.pos = origScanPos
			p.end = origScanEnd
			p.errOffsetPlus1 = 0
			p.nbuf.init(p.nodes)
		} else {
			err = idxErr
			if err != nil {
				if _, isSyntax := err.(SyntaxError); isSyntax && sess != nil {
					*sess = InputSession{}
				}
				goto fix_error
			}
		}
	}
	if !useIdx {
		if activeProj == nil {
			jsonscan.DiagRecordTapeRoute()
			if ctrl != nil && ctrl.witness != nil {
				ctrl.witness.TapeRouteSelected++
			}
		}
		forceScalar := ctrl != nil && ctrl.forceScalar
		ecode := p.scanDocumentInPlaceMode(uint64(f), etp, activeProj, forceScalar)
		if ctrl != nil && ctrl.witness != nil {
			ctrl.witness.RequestedForceScalar = forceScalar
			ctrl.witness.ObservedProducers |= p.jsMachine.ObservedLexProducers()
		}
		if ecode != SONIC_OK {
			err = p.fixError(ecode)
			if sess != nil {
				*sess = InputSession{}
			}
			goto fix_error
		}
		ctx := p.prepareDecodeContext(uint64(f), etp)
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}

fix_error:
	err = fix_error_orig(origFull, origPos, utf8Inv, err)

	newPos := origPos + p.Pos()
	if newPos > len(*s) {
		newPos = len(*s)
	}
	if sessPtr != nil {
		if err != nil {
			*sessPtr = nil
		} else if hasMoreJSONTokens(*s, newPos) {
			if sess == nil {
				sess = &InputSession{}
				*sessPtr = sess
			}
			if sess.src != *s || origPos < sess.base {
				sess.src = *s
				sess.base = origPos
				sess.utf8Checked = (f & (1 << _F_validate_string)) != 0
			} else if (f & (1 << _F_validate_string)) != 0 {
				sess.utf8Checked = true
			}
		} else if sess != nil {
			*sessPtr = nil
		}
	}
	*i = newPos
	return err
}

// mapRepairedOffsetToOrig converts a byte offset inside utf8.CorrectWith(orig, "\ufffd")
// back to the exact byte offset in the unmutated original string orig.
func mapRepairedOffsetToOrig(orig string, repPos int) int {
	if repPos <= 0 {
		return 0
	}
	origIdx := 0
	repIdx := 0
	for origIdx < len(orig) && repIdx < repPos {
		if orig[origIdx] < stdutf8.RuneSelf {
			origIdx++
			repIdx++
			continue
		}
		r, sz := stdutf8.DecodeRuneInString(orig[origIdx:])
		if r == stdutf8.RuneError && sz == 1 {
			if repPos < repIdx+3 {
				return origIdx
			}
			origIdx++
			repIdx += 3
			continue
		}
		if repPos < repIdx+sz {
			return origIdx + (repPos - repIdx)
		}
		origIdx += sz
		repIdx += sz
	}
	return origIdx
}

func fix_error_orig(origFull string, origBase int, utf8Inv bool, err error) error {
	if err == nil {
		return nil
	}
	if !utf8Inv {
		return fix_error(origFull, origBase, err)
	}
	origSub := origFull[origBase:]
	switch e := err.(type) {
	case SyntaxError:
		return SyntaxError{
			Pos: origBase + mapRepairedOffsetToOrig(origSub, e.Pos),
			Src: origFull,
			Msg: e.Msg,
		}
	case *SyntaxError:
		if e != nil {
			return SyntaxError{
				Pos: origBase + mapRepairedOffsetToOrig(origSub, e.Pos),
				Src: origFull,
				Msg: e.Msg,
			}
		}
	case MismatchTypeError:
		return &MismatchTypeError{
			Pos:  origBase + mapRepairedOffsetToOrig(origSub, e.Pos),
			Src:  origFull,
			Type: e.Type,
		}
	case *MismatchTypeError:
		if e != nil {
			return &MismatchTypeError{
				Pos:  origBase + mapRepairedOffsetToOrig(origSub, e.Pos),
				Src:  origFull,
				Type: e.Type,
			}
		}
	}
	return err
}

func fix_error(json string, pos int, err error) error {
	if e, ok := err.(SyntaxError); ok {
		return SyntaxError{
			Pos: int(e.Pos) + pos,
			Src: json,
			Msg: e.Msg,
		}
	}

	if e, ok := err.(MismatchTypeError); ok {
		return &MismatchTypeError{
			Pos:  int(e.Pos) + pos,
			Src:  json,
			Type: e.Type,
		}
	}

	return err
}

// Pretouch compiles vt's decoder type plan ahead-of-time to warm schema caches
// and reduce first-request latency.
//
// Opts are the compile options, for example, "option.WithCompileRecursiveDepth" is
// a compile option to set the depth of recursive compile for the nested struct type.
func Pretouch(vt reflect.Type, opts ...option.CompileOption) error {
	return PretouchMany([]reflect.Type{vt}, opts...)
}

// PretouchMany compiles decoder type plans for all vts ahead-of-time to warm
// schema caches and reduce first-request latency.
func PretouchMany(vts []reflect.Type, opts ...option.CompileOption) error {
	if len(vts) == 0 {
		return nil
	}

	cfg := option.DefaultCompileOptions()
	for _, opt := range opts {
		opt(&cfg)
	}

	vtm := make(map[reflect.Type]bool, len(vts))
	for _, vt := range vts {
		vtm[vt] = true
	}
	return pretouchRec(vtm, cfg)
}

func ResetProgramCache() {
	programCache.ResetForDiagnostic()
	rootPtrCache.ResetForDiagnostic()
}

func pretouchRec(vtm map[reflect.Type]bool, opts option.CompileOptions) error {
	if opts.RecursiveDepth < 0 || len(vtm) == 0 {
		return nil
	}
	seen := make(map[reflect.Type]struct{}, len(vtm))
	var batch []caching.TypedBatchItem[decFunc]
	for opts.RecursiveDepth >= 0 && len(vtm) > 0 {
		next := make(map[reflect.Type]bool)
		for vt := range vtm {
			if _, ok := seen[vt]; ok {
				continue
			}
			seen[vt] = struct{}{}
			if _, ok := programCache.Get(vt); ok {
				continue
			}
			compiler := newCompiler().apply(opts)
			f, err := compiler.compileType(vt)
			if err != nil {
				return err
			}
			batch = append(batch, caching.TypedBatchItem[decFunc]{Vt: vt, Val: f})
			for svt := range compiler.visited {
				next[svt] = true
			}
		}
		opts.RecursiveDepth--
		vtm = next
	}
	programCache.PutMany(batch)
	return nil
}
