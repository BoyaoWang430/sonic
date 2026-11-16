//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"reflect"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/resolver"
)

// decodeRootFromDom validates any deferred float64-overflow number tokens
// before mutating the target or invoking custom UnmarshalJSON methods, then
// executes dec.FromDom. In 99.9999%+ of documents hasDeferredFloatInf is false
// and this adds a single predicted branch per root decode.
func decodeRootFromDom(vp unsafe.Pointer, dec decFunc, ctx *Context) error {
	p := ctx.Parser
	if p != nil && p.hasDeferredFloatInf && p.options&(1<<_F_use_number) == 0 {
		if errPos, ok := checkDeferredFloatInf(vp, dec, ctx.Root(), ctx); !ok {
			if errPos < 0 {
				errPos = 0
			} else if errPos > len(p.Json) {
				errPos = len(p.Json)
			}
			return error_syntax(errPos, p.Json, ParsingErrors[SONIC_FLOAT_INF])
		}
	}
	return dec.FromDom(vp, ctx.Root(), ctx)
}

func firstRawNumberEndMinusOne(nd Node) (int, bool) {
	cur := nd.ptr
	end := nd.Next()
	for p := cur; uintptr(unsafe.Pointer(p)) < uintptr(unsafe.Pointer(end)); p = PtrOffset(p, 1) {
		if uint8(p.typ) == KRawNumber {
			pos := int(p.typ>>PosBits) + int(p.val) - 1
			if pos < 0 {
				pos = 0
			}
			return pos, true
		}
	}
	return 0, false
}

// checkDeferredFloatInf walks (vp, dec, nd) without mutating vp and verifies
// that every deferred KRawNumber (emitted only on SONIC_FLOAT_INF when
// _F_use_number == 0) resides inside a raw JSON consumer (json.RawMessage,
// NoCopyRawMessage, non-string json.Unmarshaler, or json.Number).
func checkDeferredFloatInf(vp unsafe.Pointer, dec decFunc, nd Node, ctx *Context) (int, bool) {
	if dec == nil {
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true
	}

	switch d := dec.(type) {
	case *rawMessageDecoder, *numberDecoder:
		return 0, true

	case *noCopyRawMessageDecoder:
		if !d.strOpt {
			return 0, true
		}
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true

	case *unmarshalJSONDecoder:
		if !d.strOpt {
			return 0, true
		}
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true

	case *ptrDecoder:
		if nd.IsNull() {
			return 0, true
		}
		var subVP unsafe.Pointer
		if vp != nil {
			subVP = *(*unsafe.Pointer)(vp)
		}
		return checkDeferredFloatInf(subVP, d.deref, nd, ctx)

	case *embeddedFieldPtrDecoder:
		if nd.IsNull() {
			return 0, true
		}
		subVP := vp
		if subVP != nil {
			subVP = unsafe.Add(subVP, -int(d.field.Path[0].Size))
			for _, f := range d.field.Path {
				if subVP == nil {
					break
				}
				subVP = unsafe.Add(subVP, f.Size)
				if f.Kind == resolver.F_deref {
					subVP = *(*unsafe.Pointer)(subVP)
				}
			}
		}
		return checkDeferredFloatInf(subVP, d.fieldDec, nd, ctx)

	case *recuriveDecoder:
		var subDec decFunc
		if ptr := atomic.LoadPointer(&d.dec); ptr != nil {
			subDec = *(*decFunc)(ptr)
		} else {
			var err error
			subDec, err = findOrCompile(d.typ)
			if err != nil {
				if pos, has := firstRawNumberEndMinusOne(nd); has {
					return pos, false
				}
				return 0, true
			}
		}
		return checkDeferredFloatInf(vp, subDec, nd, ctx)

	case *efaceDecoder:
		if vp != nil {
			cur := *(*interface{})(vp)
			if cur != nil {
				rv := reflect.ValueOf(cur)
				vt := rv.Type()
				if vt.Kind() == reflect.Ptr && !rv.IsNil() && rv.UnsafePointer() != vp {
					if nd.IsNull() && vt.Elem().Kind() != reflect.Ptr {
						return 0, true
					}
					etp := vt.Elem()
					subVP := rv.UnsafePointer()
					if vt.Name() != "" {
						etp = vt
						subVP = nil
					}
					if subDec, err := findOrCompile(etp); err == nil {
						return checkDeferredFloatInf(subVP, subDec, nd, ctx)
					}
				}
			}
		}
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true

	case *ifaceDecoder:
		if nd.IsNull() {
			return 0, true
		}
		if vp != nil {
			iv := reflect.NewAt(d.typ, vp).Elem()
			if !iv.IsNil() {
				ev := iv.Elem()
				vt := ev.Type()
				if vt.Kind() == reflect.Ptr && !ev.IsNil() {
					etp := vt.Elem()
					subVP := ev.UnsafePointer()
					if vt.Name() != "" {
						etp = vt
						subVP = nil
					}
					if subDec, err := findOrCompile(etp); err == nil {
						return checkDeferredFloatInf(subVP, subDec, nd, ctx)
					}
				}
			}
		}
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true

	case *structDecoder:
		if nd.IsNull() {
			return 0, true
		}
		obj, ok := nd.AsObj()
		if !ok {
			if pos, has := firstRawNumberEndMinusOne(nd); has {
				return pos, false
			}
			return 0, true
		}
		caseSensitive := ctx.Parser.options&uint64(consts.OptionCaseSensitive) != 0
		size := obj.Len()
		next := obj.Children()
		for i := 0; i < size; i++ {
			knode := NewNode(next)
			vnode := NewNode(PtrOffset(next, 1))
			next = vnode.Next()

			key, _ := knode.AsStrRef(ctx)
			var idx int
			if caseSensitive {
				idx = d.fieldMap.Get(key, true)
			} else {
				idx = d.fieldMap.GetInsensitive(key)
			}
			if idx < 0 {
				if pos, has := firstRawNumberEndMinusOne(vnode); has {
					return pos, false
				}
				continue
			}
			var fieldVP unsafe.Pointer
			if vp != nil {
				fe := &d.fields[idx]
				if fe.kind == fieldKindWideOffset {
					fieldVP = unsafe.Add(vp, d.wideOffsets[fe.offset])
				} else {
					fieldVP = unsafe.Add(vp, uintptr(fe.offset))
				}
			}
			if pos, ok := checkDeferredFloatInf(fieldVP, d.fieldDecs[idx], vnode, ctx); !ok {
				return pos, false
			}
		}
		return 0, true

	case *sliceDecoder:
		if nd.IsNull() {
			return 0, true
		}
		arr, ok := nd.AsArr()
		if !ok {
			if pos, has := firstRawNumberEndMinusOne(nd); has {
				return pos, false
			}
			return 0, true
		}
		size := arr.Len()
		next := arr.Children()
		for i := 0; i < size; i++ {
			elemNode := NewNode(next)
			next = elemNode.Next()
			if pos, ok := checkDeferredFloatInf(nil, d.elemDec, elemNode, ctx); !ok {
				return pos, false
			}
		}
		return 0, true

	case *sliceBytesUnmarshalerDecoder:
		if nd.IsNull() || nd.IsStr() {
			return 0, true
		}
		arr, ok := nd.AsArr()
		if !ok {
			if pos, has := firstRawNumberEndMinusOne(nd); has {
				return pos, false
			}
			return 0, true
		}
		size := arr.Len()
		next := arr.Children()
		for i := 0; i < size; i++ {
			elemNode := NewNode(next)
			next = elemNode.Next()
			if pos, ok := checkDeferredFloatInf(nil, d.elemDec, elemNode, ctx); !ok {
				return pos, false
			}
		}
		return 0, true

	case *arrayDecoder:
		if nd.IsNull() {
			return 0, true
		}
		arr, ok := nd.AsArr()
		if !ok {
			if pos, has := firstRawNumberEndMinusOne(nd); has {
				return pos, false
			}
			return 0, true
		}
		size := arr.Len()
		next := arr.Children()
		for i := 0; i < size; i++ {
			elemNode := NewNode(next)
			next = elemNode.Next()
			if i >= d.len {
				if pos, has := firstRawNumberEndMinusOne(elemNode); has {
					return pos, false
				}
				continue
			}
			var elemVP unsafe.Pointer
			if vp != nil {
				elemVP = unsafe.Add(vp, uintptr(i)*d.size)
			}
			if pos, ok := checkDeferredFloatInf(elemVP, d.elemDec, elemNode, ctx); !ok {
				return pos, false
			}
		}
		return 0, true

	case *mapStrKeyDecoder:
		return checkDeferredFloatInfMap(d.elemDec, nd, ctx)
	case *mapI32KeyDecoder:
		return checkDeferredFloatInfMap(d.elemDec, nd, ctx)
	case *mapI64KeyDecoder:
		return checkDeferredFloatInfMap(d.elemDec, nd, ctx)
	case *mapU32KeyDecoder:
		return checkDeferredFloatInfMap(d.elemDec, nd, ctx)
	case *mapU64KeyDecoder:
		return checkDeferredFloatInfMap(d.elemDec, nd, ctx)
	case *mapDecoder:
		return checkDeferredFloatInfMap(d.elemDec, nd, ctx)

	default:
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true
	}
}

func checkDeferredFloatInfMap(elemDec decFunc, nd Node, ctx *Context) (int, bool) {
	if nd.IsNull() {
		return 0, true
	}
	obj, ok := nd.AsObj()
	if !ok {
		if pos, has := firstRawNumberEndMinusOne(nd); has {
			return pos, false
		}
		return 0, true
	}
	size := obj.Len()
	next := obj.Children()
	for i := 0; i < size; i++ {
		vnode := NewNode(PtrOffset(next, 1))
		next = vnode.Next()
		if pos, ok := checkDeferredFloatInf(nil, elemDec, vnode, ctx); !ok {
			return pos, false
		}
	}
	return 0, true
}
