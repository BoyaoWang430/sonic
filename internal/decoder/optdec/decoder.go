package optdec

import (
	"reflect"
	"unsafe"

	"encoding/json"
	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/decoder/errors"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/option"
)

type (
	MismatchTypeError = errors.MismatchTypeError
	SyntaxError       = errors.SyntaxError
)

const (
	_F_allow_control   = consts.F_allow_control
	_F_copy_string     = consts.F_copy_string
	_F_disable_unknown = consts.F_disable_unknown
	_F_disable_urc     = consts.F_disable_urc
	_F_use_int64       = consts.F_use_int64
	_F_use_number      = consts.F_use_number
	_F_validate_string = consts.F_validate_string
)

type Options = consts.Options

const (
	OptionUseInt64         = consts.OptionUseInt64
	OptionUseNumber        = consts.OptionUseNumber
	OptionUseUnicodeErrors = consts.OptionUseUnicodeErrors
	OptionDisableUnknown   = consts.OptionDisableUnknown
	OptionCopyString       = consts.OptionCopyString
	OptionValidateString   = consts.OptionValidateString
)

func Decode(s *string, i *int, f uint64, val interface{}) error {
	return DecodeWithSession(s, i, f, val, nil)
}

func DecodeWithSession(s *string, i *int, f uint64, val interface{}, sessPtr **InputSession) error {
	var sess *InputSession
	if sessPtr != nil {
		sess = *sessPtr
	}
	vv := rt.UnpackEface(val)
	vp := vv.Value

	/* check for nil type */
	if vv.Type == nil {
		return &json.InvalidUnmarshalError{}
	}

	/* must be a non-nil pointer */
	if vp == nil || vv.Type.Kind() != reflect.Ptr {
		return &json.InvalidUnmarshalError{Type: vv.Type.Pack()}
	}

	etp := rt.PtrElem(vv.Type)

	/* check the defined pointer type for issue 379 */
	if vv.Type.IsNamed() {
		newp := vp
		etp = vv.Type
		vp = unsafe.Pointer(&newp)
	}

	dec, err := findOrCompile(etp)
	if err != nil {
		return err
	}

	/* parse into document */
	origPos := *i
	ctx, err := NewContextWithSession(*s, origPos, uint64(f), etp, sess)
	defer ctx.Delete()
	if ctx.Parser.Utf8Inv {
		*s = parserCorrectedSource(ctx.Parser, *s, origPos)
	}
	if err != nil {
		if sess != nil {
			*sess = InputSession{}
		}
		goto fix_error
	}
	err = dec.FromDom(vp, ctx.Root(), &ctx)

fix_error:
	err = fix_error(*s, origPos, err)

	newPos := origPos + ctx.Parser.Pos()
	if sessPtr != nil {
		if err != nil {
			*sessPtr = nil
		} else if hasMoreJSONTokens(*s, newPos) {
			if ctx.Parser.sessionPadded == nil {
				if sess == nil {
					sess = &InputSession{}
					*sessPtr = sess
				}
				sess.src = *s
				sess.base = origPos
				sess.padded = ctx.Parser.padded
				sess.utf8Checked = (f & (1 << _F_validate_string)) != 0
				ctx.Parser.padded = ctx.Parser.paddedBackup
				ctx.Parser.paddedBackup = nil
			}
		} else if sess != nil {
			if sess.padded != nil && uintptr(cap(sess.padded)) <= maxPooledJsonPaddedCap && ctx.Parser.padded == nil {
				ctx.Parser.padded = sess.padded[:0]
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
	programCache.Reset()
}

func pretouchRec(vtm map[reflect.Type]bool, opts option.CompileOptions) error {
	if opts.RecursiveDepth < 0 || len(vtm) == 0 {
		return nil
	}
	seen := make(map[*rt.GoType]struct{}, len(vtm))
	var batch []caching.ProgramBatchItem
	for opts.RecursiveDepth >= 0 && len(vtm) > 0 {
		next := make(map[reflect.Type]bool)
		for vt := range vtm {
			gvt := rt.UnpackType(vt)
			if _, ok := seen[gvt]; ok {
				continue
			}
			seen[gvt] = struct{}{}
			if programCache.Get(gvt) != nil {
				continue
			}
			compiler := newCompiler().apply(opts)
			f, err := compiler.compileType(vt)
			if err != nil {
				return err
			}
			batch = append(batch, caching.ProgramBatchItem{Vt: gvt, Fn: f})
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
