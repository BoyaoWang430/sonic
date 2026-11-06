package optdec

import (
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	caching "github.com/bytedance/sonic/internal/optcaching"
	"github.com/bytedance/sonic/internal/rt"
)

const nodeSize = unsafe.Sizeof(node{})

const (
	fieldKindGeneric uint8 = iota
	fieldKindString
	fieldKindI64
	fieldKindI32
	fieldKindU64
	fieldKindU32
	fieldKindBool
	fieldKindF64
	fieldKindStruct
	fieldKindPtrStruct
	fieldKindSlice
)

type fieldEntry struct {
	offset    uintptr
	fieldDec  decFunc
	ptrType   *rt.GoType
	structDec *structDecoder
	sliceDec  *sliceDecoder
	kind      uint8
}

func newFieldEntry(offset uintptr, dec decFunc) fieldEntry {
	fe := fieldEntry{
		offset:   offset,
		fieldDec: dec,
	}
	switch d := dec.(type) {
	case *stringDecoder:
		fe.kind = fieldKindString
	case *i64Decoder:
		fe.kind = fieldKindI64
	case *i32Decoder:
		fe.kind = fieldKindI32
	case *u64Decoder:
		fe.kind = fieldKindU64
	case *u32Decoder:
		fe.kind = fieldKindU32
	case *boolDecoder:
		fe.kind = fieldKindBool
	case *f64Decoder:
		fe.kind = fieldKindF64
	case *structDecoder:
		fe.kind = fieldKindStruct
		fe.structDec = d
	case *ptrDecoder:
		if sd, ok := d.deref.(*structDecoder); ok {
			fe.kind = fieldKindPtrStruct
			fe.ptrType = d.typ
			fe.structDec = sd
		}
	case *sliceDecoder:
		fe.kind = fieldKindSlice
		fe.sliceDec = d
	}
	return fe
}

type structDecoder struct {
	fieldMap   *caching.FieldCache
	fields     []fieldEntry
	structName string
	typ        reflect.Type
}

func (d *structDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	var gerr error
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	size := obj.Len()
	if size == 0 {
		return nil
	}

	p := ctx.Parser
	opts := p.options
	caseSensitive := opts&uint64(consts.OptionCaseSensitive) != 0
	disUnknown := Options(opts)&OptionDisableUnknown != 0
	copyStr := opts&(1<<_F_copy_string) != 0
	jsonStr := p.Json

	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := ptrCast(next)
		var key string
		if uint8(knode.typ) == KStringCommon {
			koff := int(knode.typ >> PosBits)
			key = jsonStr[koff : koff+int(knode.val)]
		} else {
			key, _ = NewNode(next).AsStrRef(ctx)
		}

		vptr := next + nodeSize
		vnode := ptrCast(vptr)
		vtyp := uint8(vnode.typ)
		if vtyp-KObject > 1 {
			next = vptr + nodeSize
		} else {
			next = vptr + uintptr(vnode.val>>ConLenBits)*nodeSize
		}

		// find field idx
		idx := d.fieldMap.Get(key, caseSensitive)
		if idx == -1 {
			if disUnknown {
				return error_field(key)
			}
			continue
		}

		fe := &d.fields[idx]
		elem := unsafe.Pointer(uintptr(vp) + fe.offset)
		if vtyp == KNull {
			if fe.kind == fieldKindPtrStruct {
				*(*unsafe.Pointer)(elem) = nil
				continue
			}
			if fe.kind == fieldKindSlice {
				*(*rt.GoSlice)(elem) = rt.GoSlice{}
				continue
			}
			if fe.kind != fieldKindGeneric {
				continue
			}
		} else {
			switch fe.kind {
			case fieldKindString:
				if vtyp == KStringCommon {
					soff := int(vnode.typ >> PosBits)
					s := jsonStr[soff : soff+int(vnode.val)]
					if !copyStr {
						*(*string)(elem) = s
					} else {
						*(*string)(elem) = string(rt.Str2Mem(s))
					}
					continue
				}
			case fieldKindI64:
				if vtyp == KUint && vnode.val <= math.MaxInt64 {
					*(*int64)(elem) = int64(vnode.val)
					continue
				}
				if vtyp == KSint {
					*(*int64)(elem) = int64(vnode.val)
					continue
				}
			case fieldKindI32:
				if vtyp == KUint && vnode.val <= math.MaxInt32 {
					*(*int32)(elem) = int32(vnode.val)
					continue
				}
				if vtyp == KSint {
					iv := int64(vnode.val)
					if iv >= math.MinInt32 && iv <= math.MaxInt32 {
						*(*int32)(elem) = int32(iv)
						continue
					}
				}
			case fieldKindBool:
				if vtyp == KTrue {
					*(*bool)(elem) = true
					continue
				}
				if vtyp == KFalse {
					*(*bool)(elem) = false
					continue
				}
			case fieldKindF64:
				if vtyp == KReal {
					*(*uint64)(elem) = vnode.val
					continue
				}
				if vtyp == KUint {
					*(*float64)(elem) = float64(vnode.val)
					continue
				}
				if vtyp == KSint {
					*(*float64)(elem) = float64(int64(vnode.val))
					continue
				}
			case fieldKindU64:
				if vtyp == KUint {
					*(*uint64)(elem) = vnode.val
					continue
				}
			case fieldKindU32:
				if vtyp == KUint && vnode.val <= math.MaxUint32 {
					*(*uint32)(elem) = uint32(vnode.val)
					continue
				}
			case fieldKindPtrStruct:
				if vtyp == KObject {
					dst := *(*unsafe.Pointer)(elem)
					if dst == nil {
						dst = rt.Mallocgc(fe.ptrType.Size, fe.ptrType, true)
						*(*unsafe.Pointer)(elem) = dst
					}
					err := fe.structDec.FromDom(dst, Node{cptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindStruct:
				if vtyp == KObject {
					err := fe.structDec.FromDom(elem, Node{cptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSlice:
				if vtyp == KArray {
					err := fe.sliceDec.FromDom(elem, Node{cptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			}
		}

		err := fe.fieldDec.FromDom(elem, Node{cptr: vptr}, ctx)

		// deal with mismatch type errors
		if gerr == nil && err != nil {
			// TODO: better error info
			gerr = err
		}
	}
	return gerr
}
