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
	fieldKindEface
	fieldKindSliceEface
	fieldKindSliceI64
	fieldKindSliceI32
	fieldKindSliceU64
	fieldKindSliceU32
	fieldKindSliceString
	fieldKindWideOffset
)

// fieldEntry is 32 bytes (2 entries per 64B cache line).
// Cold fallback decFunc interfaces are stored in structDecoder.fieldDecs.
type fieldEntry struct {
	name   string
	subDec unsafe.Pointer
	offset uint32
	kind   uint8
}

var _ [32 - unsafe.Sizeof(fieldEntry{})]byte
var _ [unsafe.Sizeof(fieldEntry{}) - 32]byte

func newFieldEntry(name string, offset uintptr, dec decFunc) fieldEntry {
	if offset > math.MaxUint32 {
		return fieldEntry{name: name}
	}
	fe := fieldEntry{
		name:   name,
		offset: uint32(offset),
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
		fe.subDec = unsafe.Pointer(d)
	case *ptrDecoder:
		if sd, ok := d.deref.(*structDecoder); ok && d.typ == sd.goType {
			fe.kind = fieldKindPtrStruct
			fe.subDec = unsafe.Pointer(sd)
		}
	case *sliceDecoder:
		fe.kind = fieldKindSlice
		fe.subDec = unsafe.Pointer(d)
	case *efaceDecoder:
		fe.kind = fieldKindEface
	case *sliceEfaceDecoder:
		fe.kind = fieldKindSliceEface
	case *sliceI64Decoder:
		fe.kind = fieldKindSliceI64
	case *sliceI32Decoder:
		fe.kind = fieldKindSliceI32
	case *sliceU64Decoder:
		fe.kind = fieldKindSliceU64
	case *sliceU32Decoder:
		fe.kind = fieldKindSliceU32
	case *sliceStringDecoder:
		fe.kind = fieldKindSliceString
	}
	return fe
}

type structDecoder struct {
	fieldMap   *caching.FieldCache
	fields     []fieldEntry
	fieldDecs   []decFunc
	wideOffsets []uintptr
	structName string
	typ        reflect.Type
	goType     *rt.GoType
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

	fields := d.fields
	nfields := len(fields)
	expectedIdx := 0
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := ptrCast(next)
		var key string
		if uint8(knode.typ) == KStringCommon {
			key = p.strRefCommon(int(knode.typ>>PosBits), int(knode.val))
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
		var idx int
		if expectedIdx < nfields && fields[expectedIdx].name == key {
			idx = expectedIdx
			expectedIdx++
		} else {
			idx = d.fieldMap.Get(key, caseSensitive)
			if idx == -1 {
				if disUnknown {
					return error_field(key)
				}
				continue
			}
			expectedIdx = idx + 1
		}

		fe := &fields[idx]
		var elem unsafe.Pointer
		if fe.kind == fieldKindWideOffset {
			elem = unsafe.Pointer(uintptr(vp) + d.wideOffsets[fe.offset])
		} else {
			elem = unsafe.Pointer(uintptr(vp) + uintptr(fe.offset))
		}
		if vtyp == KNull {
			switch fe.kind {
			case fieldKindPtrStruct:
				*(*unsafe.Pointer)(elem) = nil
				continue
			case fieldKindSlice, fieldKindSliceEface, fieldKindSliceI64, fieldKindSliceI32, fieldKindSliceU64, fieldKindSliceU32, fieldKindSliceString:
				*(*rt.GoSlice)(elem) = rt.GoSlice{}
				continue
			case fieldKindEface:
				if (*rt.GoEface)(elem).Type == nil {
					continue
				}
			case fieldKindGeneric, fieldKindWideOffset:
			default:
				continue
			}
		} else {
			switch fe.kind {
			case fieldKindString:
				if vtyp == KStringCommon {
					*(*string)(elem) = p.strCommon(int(vnode.typ>>PosBits), int(vnode.val), copyStr)
					continue
				}
				if vtyp == KStringEscaped {
					*(*string)(elem) = p.strEscaped(int(vnode.typ>>PosBits), vnode.val)
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
					sd := (*structDecoder)(fe.subDec)
					dst := *(*unsafe.Pointer)(elem)
					if dst == nil {
						dst = rt.Mallocgc(sd.goType.Size, sd.goType, true)
						*(*unsafe.Pointer)(elem) = dst
					}
					err := sd.FromDom(dst, Node{cptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindStruct:
				if vtyp == KObject {
					err := ((*structDecoder)(fe.subDec)).FromDom(elem, Node{cptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSlice:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					err := ((*sliceDecoder)(fe.subDec)).FromDom(elem, Node{cptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindEface:
				if (*rt.GoEface)(elem).Value == nil {
					switch vtyp {
					case KStringCommon:
						*(*interface{})(elem) = p.strCommon(int(vnode.typ>>PosBits), int(vnode.val), copyStr)
						continue
					case KStringEscaped:
						*(*interface{})(elem) = p.strEscaped(int(vnode.typ>>PosBits), vnode.val)
						continue
					case KTrue:
						*(*interface{})(elem) = true
						continue
					case KFalse:
						*(*interface{})(elem) = false
						continue
					}
				}
			case fieldKindSliceEface:
				if vtyp == KArray && (*rt.GoSlice)(elem).Len == 0 {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					n := Node{cptr: vptr}
					err := n.AsSliceEface(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceI64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					n := Node{cptr: vptr}
					err := n.AsSliceI64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceI32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					n := Node{cptr: vptr}
					err := n.AsSliceI32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceU64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					n := Node{cptr: vptr}
					err := n.AsSliceU64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceU32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					n := Node{cptr: vptr}
					err := n.AsSliceU32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceString:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*rt.GoSlice)(elem) = rt.EmptySlice
						continue
					}
					n := Node{cptr: vptr}
					err := n.AsSliceString(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			}
		}

		err := d.fieldDecs[idx].FromDom(elem, Node{cptr: vptr}, ctx)

		// deal with mismatch type errors
		if gerr == nil && err != nil {
			// TODO: better error info
			gerr = err
		}
	}
	return gerr
}
