//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"encoding/json"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/decoder/errors"
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
func DecodeOne(s string, f uint64, val interface{}) (string, int, error) {
	etp, vp, dec, proj, err := unpackAndCompileRoot(val)
	if err != nil {
		return s, 0, err
	}
	if len(s)+len(padding) > largeReadOnlyScanBytes && (proj == nil || !proj.hasRawSubtree || !proj.shouldUseOpts(len(s), f)) {
		return decodeReadOnlyOneCompiled(s, f, etp, vp, dec)
	}

	var activeProj *projPlan
	var p *Parser
	if proj != nil && proj.shouldUseOpts(len(s), f) {
		activeProj = proj
		p = newParserLazy(s, 0, f)
		p.singleDoc = true
	} else {
		p = newParserImpl(s, 0, f)
		p.singleDoc = true
		if len(p.Json) > presizeSingleDocBytes && (proj == nil || !proj.hasRawSubtree) {
			p.presizeNodesOne()
		}
	}
	defer p.free()

	doc, ecode := p.scanDocument(f, etp, activeProj)
	if ecode == errCodeLargeReadOnlyScan {
		return decodeReadOnlyOneCompiled(s, f, etp, vp, dec)
	}
	if doc.Utf8Inv {
		s = doc.Json
	}
	if ecode != SONIC_OK {
		err = p.fixError(ecode)
	} else {
		ctx := p.initDecodeContext(doc, f, etp)
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}
	err = fix_error(s, 0, err)
	endPos := p.Pos()
	if endPos > len(s) {
		endPos = len(s)
	}
	return s, endPos, err
}

func DecodeWithSession(s *string, i *int, f uint64, val interface{}, sessPtr **InputSession) error {
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
	if (sess == nil || sess.padded == nil || sess.src != *s || origPos < sess.base || origPos > len(*s) ||
		((f&(1<<_F_validate_string)) != 0 && !sess.utf8Checked)) &&
		len(*s)-origPos+len(padding) > largeReadOnlyScanBytes &&
		(proj == nil || !proj.hasRawSubtree || !proj.shouldUseOpts(len(*s)-origPos, f)) {
		*i = origPos
		return decodeReadOnlyWithSessionCompiled(s, i, f, etp, vp, dec, sessPtr)
	}

	/* parse into document: multi-value session never presizes from the remaining stream tail */
	var activeProj *projPlan
	var p *Parser
	if proj != nil && proj.shouldUseOpts(len(*s)-origPos, f) {
		activeProj = proj
		p = newParserLazyFromSession(*s, origPos, uint64(f), sess)
	} else {
		p = newParserFromSession(*s, origPos, uint64(f), sess)
	}
	defer p.free()

	doc, ecode := p.scanDocument(uint64(f), etp, activeProj)
	if ecode == errCodeLargeReadOnlyScan {
		*i = origPos
		return decodeReadOnlyWithSessionCompiled(s, i, f, etp, vp, dec, sessPtr)
	}
	if doc.Utf8Inv {
		*s = parserCorrectedSource(p, *s, origPos)
	}
	if ecode != SONIC_OK {
		err = p.fixError(ecode)
		if sess != nil {
			*sess = InputSession{}
		}
		goto fix_error
	}
	{
		ctx := p.initDecodeContext(doc, uint64(f), etp)
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}

fix_error:
	err = fix_error(*s, origPos, err)

	newPos := origPos + p.Pos()
	if newPos > len(*s) {
		newPos = len(*s)
	}
	if sessPtr != nil {
		if err != nil {
			*sessPtr = nil
		} else if hasMoreJSONTokens(*s, newPos) {
			if p.lazyPadded {
				if sess == nil {
					sess = &InputSession{}
					*sessPtr = sess
				}
				if sess.src != *s || origPos < sess.base {
					sess.src = *s
					sess.base = origPos
					sess.padded = nil
					sess.utf8Checked = (f & (1 << _F_validate_string)) != 0
				} else if (f & (1 << _F_validate_string)) != 0 {
					sess.utf8Checked = true
				}
			} else if p.sessionPadded == nil {
				if sess == nil {
					sess = &InputSession{}
					*sessPtr = sess
				}
				sess.src = *s
				sess.base = origPos
				sess.padded = p.padded
				sess.utf8Checked = (f & (1 << _F_validate_string)) != 0
				p.padded = p.paddedBackup
				p.paddedBackup = nil
			}
		} else if sess != nil {
			if sess.padded != nil && cap(sess.padded) <= maxPooledPaddedBufBytes && p.padded == nil {
				p.padded = sess.padded[:0]
			}
			*sessPtr = nil
		}
	}
	*i = newPos
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

// Pretouch compiles vt ahead-of-time to avoid JIT compilation on-the-fly, in
// order to reduce the first-hit latency.
//
// Opts are the compile options, for example, "option.WithCompileRecursiveDepth" is
// a compile option to set the depth of recursive compile for the nested struct type.
func Pretouch(vt reflect.Type, opts ...option.CompileOption) error {
	return PretouchMany([]reflect.Type{vt}, opts...)
}

// PretouchMany compiles all vts ahead-of-time to avoid JIT compilation on-the-fly,
// in order to reduce the first-hit latency.
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
