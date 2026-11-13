package optdec

import (
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	caching "github.com/bytedance/sonic/internal/optcaching"
)

const nodeSize = unsafe.Sizeof(node{})

const fastStructStrictExcludeMask = uint64(consts.OptionCaseSensitive) |
	uint64(OptionDisableUnknown)

const fastStructExcludeMask = fastStructStrictExcludeMask |
	(1 << _F_use_number) |
	(1 << _F_use_int64)

const (
	fieldKindGeneric uint8 = iota
	fieldKindString
	fieldKindI64
	fieldKindI32
	fieldKindI16
	fieldKindI8
	fieldKindU64
	fieldKindU32
	fieldKindU16
	fieldKindU8
	fieldKindBool
	fieldKindF64
	fieldKindF32
	fieldKindStruct
	fieldKindPtrStruct
	fieldKindSlice
	fieldKindArray
	fieldKindEface
	fieldKindSliceEface
	fieldKindSliceInt
	fieldKindSliceI64
	fieldKindSliceI32
	fieldKindSliceUint
	fieldKindSliceU64
	fieldKindSliceU32
	fieldKindSliceF64
	fieldKindSliceF32
	fieldKindSliceBool
	fieldKindSliceString
	fieldKindPtrString
	fieldKindPtrI64
	fieldKindPtrInt
	fieldKindPtrI32
	fieldKindPtrI16
	fieldKindPtrI8
	fieldKindPtrU64
	fieldKindPtrUint
	fieldKindPtrU32
	fieldKindPtrU16
	fieldKindPtrU8
	fieldKindPtrBool
	fieldKindPtrF64
	fieldKindPtrF32
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

func newFieldEntry(name string, offset uintptr, ft reflect.Type, dec decFunc) fieldEntry {
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
	case *i16Decoder:
		fe.kind = fieldKindI16
	case *i8Decoder:
		fe.kind = fieldKindI8
	case *u64Decoder:
		fe.kind = fieldKindU64
	case *u32Decoder:
		fe.kind = fieldKindU32
	case *u16Decoder:
		fe.kind = fieldKindU16
	case *u8Decoder:
		fe.kind = fieldKindU8
	case *boolDecoder:
		fe.kind = fieldKindBool
	case *f64Decoder:
		fe.kind = fieldKindF64
	case *f32Decoder:
		fe.kind = fieldKindF32
	case *structDecoder:
		fe.kind = fieldKindStruct
		fe.subDec = unsafe.Pointer(d)
	case *ptrDecoder:
		switch ft {
		case ptrStringType:
			if _, ok := d.deref.(*stringDecoder); ok {
				fe.kind = fieldKindPtrString
			}
		case ptrInt64Type:
			if _, ok := d.deref.(*i64Decoder); ok {
				fe.kind = fieldKindPtrI64
			}
		case ptrIntType:
			if unsafe.Sizeof(int(0)) == 8 {
				if _, ok := d.deref.(*i64Decoder); ok {
					fe.kind = fieldKindPtrInt
				}
			}
		case ptrInt32Type:
			if _, ok := d.deref.(*i32Decoder); ok {
				fe.kind = fieldKindPtrI32
			}
		case ptrInt16Type:
			if _, ok := d.deref.(*i16Decoder); ok {
				fe.kind = fieldKindPtrI16
			}
		case ptrInt8Type:
			if _, ok := d.deref.(*i8Decoder); ok {
				fe.kind = fieldKindPtrI8
			}
		case ptrUint64Type:
			if _, ok := d.deref.(*u64Decoder); ok {
				fe.kind = fieldKindPtrU64
			}
		case ptrUintType:
			if unsafe.Sizeof(uint(0)) == 8 {
				if _, ok := d.deref.(*u64Decoder); ok {
					fe.kind = fieldKindPtrUint
				}
			}
		case ptrUint32Type:
			if _, ok := d.deref.(*u32Decoder); ok {
				fe.kind = fieldKindPtrU32
			}
		case ptrUint16Type:
			if _, ok := d.deref.(*u16Decoder); ok {
				fe.kind = fieldKindPtrU16
			}
		case ptrUint8Type:
			if _, ok := d.deref.(*u8Decoder); ok {
				fe.kind = fieldKindPtrU8
			}
		case ptrBoolType:
			if _, ok := d.deref.(*boolDecoder); ok {
				fe.kind = fieldKindPtrBool
			}
		case ptrFloat64Type:
			if _, ok := d.deref.(*f64Decoder); ok {
				fe.kind = fieldKindPtrF64
			}
		case ptrFloat32Type:
			if _, ok := d.deref.(*f32Decoder); ok {
				fe.kind = fieldKindPtrF32
			}
		default:
			if sd, ok := d.deref.(*structDecoder); ok && d.typ == sd.goType && ft == reflect.PtrTo(sd.goType) {
				fe.kind = fieldKindPtrStruct
				fe.subDec = unsafe.Pointer(sd)
			}
		}
	case *sliceDecoder:
		fe.kind = fieldKindSlice
		fe.subDec = unsafe.Pointer(d)
	case *efaceDecoder:
		fe.kind = fieldKindEface
	case *sliceEfaceDecoder:
		fe.kind = fieldKindSliceEface
	case *sliceIntDecoder:
		fe.kind = fieldKindSliceInt
	case *sliceI64Decoder:
		fe.kind = fieldKindSliceI64
	case *sliceI32Decoder:
		fe.kind = fieldKindSliceI32
	case *sliceUintDecoder:
		fe.kind = fieldKindSliceUint
	case *sliceU64Decoder:
		fe.kind = fieldKindSliceU64
	case *sliceU32Decoder:
		fe.kind = fieldKindSliceU32
	case *sliceF64Decoder:
		fe.kind = fieldKindSliceF64
	case *sliceF32Decoder:
		fe.kind = fieldKindSliceF32
	case *sliceBoolDecoder:
		fe.kind = fieldKindSliceBool
	case *sliceStringDecoder:
		fe.kind = fieldKindSliceString
	case *arrayDecoder:
		fe.kind = fieldKindArray
		fe.subDec = unsafe.Pointer(d)
	}
	return fe
}

type structDecoder struct {
	fieldMap          *caching.FieldCache
	fields            []fieldEntry
	fieldDecs         []decFunc
	wideOffsets       []uintptr
	structName        string
	typ               reflect.Type
	goType            reflect.Type
	sliceType         reflect.Type
	elemSize          uintptr
	maxPtrBatch       uint16
	fast              bool
	flatDirect        bool
	flatPureASCIIKeys bool
	noInterface       bool
	ptrScalarHint     uint8
	ptrStrHint        uint8
	flatPtrMask       uint32
	flatPlan          *flatStructPlan
	flatFailStreak    uint32
	flatProbeTick     uint32
}

func (d *structDecoder) allocPtr(p *Parser) unsafe.Pointer {
	if d.maxPtrBatch < 2 || d.sliceType == nil || p == nil {
		return reflect.New(d.goType).UnsafePointer()
	}
	for i := range p.ptrStructSlabs {
		slot := &p.ptrStructSlabs[i]
		if slot.sd == d {
			if slot.rem > 0 {
				ptr := slot.base
				slot.rem--
				if slot.rem > 0 {
					slot.base = unsafe.Add(ptr, d.elemSize)
				} else {
					slot.base = nil
				}
				return ptr
			}
			return d.allocPtrBatchSlow(slot)
		}
		if slot.sd == nil {
			slot.sd = d
			slot.next = 4
			return reflect.New(d.goType).UnsafePointer()
		}
	}
	for i := range p.ptrStructSlabs {
		slot := &p.ptrStructSlabs[i]
		if slot.rem == 0 {
			slot.sd = d
			slot.next = 4
			return reflect.New(d.goType).UnsafePointer()
		}
	}
	return reflect.New(d.goType).UnsafePointer()
}

//go:noinline
func (d *structDecoder) allocPtrBatchSlow(slot *ptrStructSlabSlot) unsafe.Pointer {
	n := slot.next
	if n < 2 {
		n = 4
	}
	maxBatch := int(d.maxPtrBatch)
	if maxBatch < 2 {
		return reflect.New(d.goType).UnsafePointer()
	}
	if n > maxBatch {
		n = maxBatch
	}
	next := n * 2
	if next > maxBatch {
		next = maxBatch
	}
	slot.next = next
	rv := reflect.MakeSlice(d.sliceType, n, n)
	base := rv.UnsafePointer()
	slot.base = unsafe.Add(base, d.elemSize)
	slot.rem = n - 1
	return base
}

func (d *structDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	vtyp := uint8(node.ptr.typ)
	if vtyp == KNull {
		return nil
	}
	if vtyp != KObject {
		return error_mismatch(node, ctx, d.typ)
	}
	if uint32(node.ptr.val) == 0 {
		return nil
	}

	p := ctx.Parser
	opts := p.options
	if d.fast {
		exclude := fastStructExcludeMask
		if d.noInterface {
			exclude = fastStructStrictExcludeMask
		}
		if opts&exclude == 0 {
			return d.fromObjFast(vp, node.ptr, p, p.Json, opts&(1<<_F_copy_string) != 0, ctx)
		}
	}
	return d.fromObjSlow(vp, node.ptr, p, opts, ctx)
}

func (d *structDecoder) decodePtrScalarField(fk uint8, elem unsafe.Pointer, vnode *node, vtyp uint8, p *Parser, jsonStr string, copyStr bool) bool {
	switch fk {
	case fieldKindPtrString:
		if vtyp == KStringCommon {
			off := int(vnode.typ >> PosBits)
			n := int(vnode.val)
			s := jsonStr[off : off+n]
			if copyStr && n != 0 {
				if copied, ok := p.copyArenaHit(s); ok {
					s = copied
				} else {
					s = p.copyArenaStringSlow(s)
				}
			}
			pp := (**string)(elem)
			dst := *pp
			if dst == nil {
				if len(s) > maxArenaStrLen {
					dst = new(string)
				} else {
					dst = p.allocStrPtr(int(d.ptrStrHint))
				}
				*pp = dst
			}
			*dst = s
			return true
		}
		if vtyp == KStringEscaped {
			s := p.strEscaped(int(vnode.typ>>PosBits), vnode.val)
			pp := (**string)(elem)
			dst := *pp
			if dst == nil {
				if len(s) > maxArenaStrLen {
					dst = new(string)
				} else {
					dst = p.allocStrPtr(int(d.ptrStrHint))
				}
				*pp = dst
			}
			*dst = s
			return true
		}
	case fieldKindPtrI64:
		if (vtyp == KUint && vnode.val <= math.MaxInt64) || vtyp == KSint {
			pp := (**int64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int64)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int64(vnode.val)
			return true
		}
	case fieldKindPtrInt:
		if (vtyp == KUint && vnode.val <= math.MaxInt64) || vtyp == KSint {
			pp := (**int)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int(int64(vnode.val))
			return true
		}
	case fieldKindPtrI32:
		if vtyp == KUint && vnode.val <= math.MaxInt32 {
			pp := (**int32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int32)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int32(vnode.val)
			return true
		}
		if vtyp == KSint {
			iv := int64(vnode.val)
			if iv >= math.MinInt32 && iv <= math.MaxInt32 {
				pp := (**int32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int32)(p.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int32(iv)
				return true
			}
		}
	case fieldKindPtrI16:
		if vtyp == KUint && vnode.val <= math.MaxInt16 {
			pp := (**int16)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int16)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int16(vnode.val)
			return true
		}
		if vtyp == KSint {
			iv := int64(vnode.val)
			if iv >= math.MinInt16 && iv <= math.MaxInt16 {
				pp := (**int16)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int16)(p.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int16(iv)
				return true
			}
		}
	case fieldKindPtrI8:
		if vtyp == KUint && vnode.val <= math.MaxInt8 {
			pp := (**int8)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int8)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int8(vnode.val)
			return true
		}
		if vtyp == KSint {
			iv := int64(vnode.val)
			if iv >= math.MinInt8 && iv <= math.MaxInt8 {
				pp := (**int8)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int8)(p.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int8(iv)
				return true
			}
		}
	case fieldKindPtrU64:
		if vtyp == KUint {
			pp := (**uint64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint64)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = vnode.val
			return true
		}
	case fieldKindPtrUint:
		if vtyp == KUint {
			pp := (**uint)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint(vnode.val)
			return true
		}
	case fieldKindPtrU32:
		if vtyp == KUint && vnode.val <= math.MaxUint32 {
			pp := (**uint32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint32)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint32(vnode.val)
			return true
		}
	case fieldKindPtrU16:
		if vtyp == KUint && vnode.val <= math.MaxUint16 {
			pp := (**uint16)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint16)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint16(vnode.val)
			return true
		}
	case fieldKindPtrU8:
		if vtyp == KUint && vnode.val <= math.MaxUint8 {
			pp := (**uint8)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint8)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint8(vnode.val)
			return true
		}
	case fieldKindPtrBool:
		if vtyp == KTrue || vtyp == KFalse {
			pp := (**bool)(elem)
			dst := *pp
			if dst == nil {
				dst = (*bool)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = vtyp == KTrue
			return true
		}
	case fieldKindPtrF64:
		if vtyp == KReal {
			pp := (**float64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float64)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*(*uint64)(unsafe.Pointer(dst)) = vnode.val
			return true
		}
		if vtyp == KUint {
			pp := (**float64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float64)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = float64(vnode.val)
			return true
		}
		if vtyp == KSint {
			pp := (**float64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float64)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = sintToFloat64(int64(vnode.val))
			return true
		}
	case fieldKindPtrF32:
		if vtyp == KReal {
			fv := math.Float64frombits(vnode.val)
			if fv >= -math.MaxFloat32 && fv <= math.MaxFloat32 {
				pp := (**float32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*float32)(p.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = float32(fv)
				return true
			}
		} else if vtyp == KUint {
			pp := (**float32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float32)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = float32(vnode.val)
			return true
		} else if vtyp == KSint {
			pp := (**float32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float32)(p.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = sintToFloat32(int64(vnode.val))
			return true
		}
	}
	return false
}

func (d *structDecoder) fromObjFast(vp unsafe.Pointer, objPtr *node, p *Parser, jsonStr string, copyStr bool, ctx *context) error {
	var gerr error
	size := int(uint32(objPtr.val))
	fields := d.fields
	nfields := len(fields)
	hasHints := p.hasProjHints
	expectedIdx := 0
	next := PtrOffset(objPtr, 1)

	for i := 0; i < size; i++ {
		knode := next
		vptr := PtrOffset(next, 1)
		vnode := vptr
		vtyp := uint8(vnode.typ)
		if vtyp-KObject > 1 {
			next = PtrOffset(vptr, 1)
		} else {
			next = PtrOffset(vptr, int64(vnode.val>>ConLenBits))
		}

		var idx int
		if hasHints {
			if hint := int((knode.typ >> 8) & 0xFFFFFF); hint != 0 {
				idx = hint - 1
				expectedIdx = idx + 1
				goto fastFieldHit
			}
		}
		{
			var key string
			if uint8(knode.typ) == KStringCommon {
				off := int(knode.typ >> PosBits)
				key = jsonStr[off : off+int(knode.val)]
			} else {
				key = p.strRefEscaped(int(knode.typ>>PosBits), knode.val)
			}
			if uint(expectedIdx) < uint(nfields) && fields[expectedIdx].name == key {
				idx = expectedIdx
				expectedIdx++
			} else {
				idx = d.fieldMap.GetInsensitive(key)
				if idx == -1 {
					continue
				}
				expectedIdx = idx + 1
			}
		}

	fastFieldHit:
		fe := &fields[idx]
		elem := unsafe.Add(vp, uintptr(fe.offset))
		if vtyp == KNull {
			if fe.kind >= fieldKindPtrString {
				*(*unsafe.Pointer)(elem) = nil
				continue
			}
			switch fe.kind {
			case fieldKindPtrStruct:
				*(*unsafe.Pointer)(elem) = nil
				continue
			case fieldKindSlice:
				((*sliceDecoder)(fe.subDec)).setNil(elem)
				continue
			case fieldKindSliceEface:
				*(*[]interface{})(elem) = nil
				continue
			case fieldKindSliceInt:
				*(*[]int)(elem) = nil
				continue
			case fieldKindSliceI64:
				*(*[]int64)(elem) = nil
				continue
			case fieldKindSliceI32:
				*(*[]int32)(elem) = nil
				continue
			case fieldKindSliceUint:
				*(*[]uint)(elem) = nil
				continue
			case fieldKindSliceU64:
				*(*[]uint64)(elem) = nil
				continue
			case fieldKindSliceU32:
				*(*[]uint32)(elem) = nil
				continue
			case fieldKindSliceF64:
				*(*[]float64)(elem) = nil
				continue
			case fieldKindSliceF32:
				*(*[]float32)(elem) = nil
				continue
			case fieldKindSliceBool:
				*(*[]bool)(elem) = nil
				continue
			case fieldKindSliceString:
				*(*[]string)(elem) = nil
				continue
			case fieldKindEface:
				if *(*interface{})(elem) == nil {
					continue
				}
			case fieldKindGeneric:
			default:
				continue
			}
		} else if fe.kind >= fieldKindPtrString {
			if d.decodePtrScalarField(fe.kind, elem, vnode, vtyp, p, jsonStr, copyStr) {
				continue
			}
		} else {
			switch fe.kind {
			case fieldKindString:
				if vtyp == KStringCommon {
					off := int(vnode.typ >> PosBits)
					n := int(vnode.val)
					s := jsonStr[off : off+n]
					if copyStr && n != 0 {
						if copied, ok := p.copyArenaHit(s); ok {
							s = copied
						} else {
							s = p.copyArenaStringSlow(s)
						}
					}
					*(*string)(elem) = s
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
			case fieldKindI16:
				if vtyp == KUint && vnode.val <= math.MaxInt16 {
					*(*int16)(elem) = int16(vnode.val)
					continue
				}
				if vtyp == KSint {
					iv := int64(vnode.val)
					if iv >= math.MinInt16 && iv <= math.MaxInt16 {
						*(*int16)(elem) = int16(iv)
						continue
					}
				}
			case fieldKindI8:
				if vtyp == KUint && vnode.val <= math.MaxInt8 {
					*(*int8)(elem) = int8(vnode.val)
					continue
				}
				if vtyp == KSint {
					iv := int64(vnode.val)
					if iv >= math.MinInt8 && iv <= math.MaxInt8 {
						*(*int8)(elem) = int8(iv)
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
					*(*float64)(elem) = sintToFloat64(int64(vnode.val))
					continue
				}
			case fieldKindF32:
				if vtyp == KReal {
					fv := math.Float64frombits(vnode.val)
					if fv >= -math.MaxFloat32 && fv <= math.MaxFloat32 {
						*(*float32)(elem) = float32(fv)
						continue
					}
				} else if vtyp == KUint {
					*(*float32)(elem) = float32(vnode.val)
					continue
				} else if vtyp == KSint {
					*(*float32)(elem) = sintToFloat32(int64(vnode.val))
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
			case fieldKindU16:
				if vtyp == KUint && vnode.val <= math.MaxUint16 {
					*(*uint16)(elem) = uint16(vnode.val)
					continue
				}
			case fieldKindU8:
				if vtyp == KUint && vnode.val <= math.MaxUint8 {
					*(*uint8)(elem) = uint8(vnode.val)
					continue
				}
			case fieldKindPtrStruct:
				if vtyp == KObject {
					sd := (*structDecoder)(fe.subDec)
					dst := *(*unsafe.Pointer)(elem)
					if dst == nil {
						dst = sd.allocPtr(p)
						*(*unsafe.Pointer)(elem) = dst
					}
					if uint32(vnode.val) != 0 {
						var err error
						if sd.fast {
							err = sd.fromObjFast(dst, vptr, p, jsonStr, copyStr, ctx)
						} else {
							err = sd.FromDom(dst, Node{ptr: vptr}, ctx)
						}
						if gerr == nil && err != nil {
							gerr = err
						}
					}
					continue
				}
			case fieldKindStruct:
				if vtyp == KObject {
					if uint32(vnode.val) != 0 {
						sd := (*structDecoder)(fe.subDec)
						var err error
						if sd.fast {
							err = sd.fromObjFast(elem, vptr, p, jsonStr, copyStr, ctx)
						} else {
							err = sd.FromDom(elem, Node{ptr: vptr}, ctx)
						}
						if gerr == nil && err != nil {
							gerr = err
						}
					}
					continue
				}
			case fieldKindSlice:
				if vtyp == KArray {
					sd := (*sliceDecoder)(fe.subDec)
					if uint32(vnode.val) == 0 {
						sd.setEmpty(elem)
						continue
					}
					err := sd.FromDom(elem, Node{ptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindEface:
				if *(*interface{})(elem) == nil {
					switch vtyp {
					case KStringCommon:
						*(*interface{})(elem) = strCommonAny(p, int(vnode.typ>>PosBits), int(vnode.val), copyStr)
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
					case KUint:
						*(*interface{})(elem) = uintToFloat64Any(vnode.val)
						continue
					case KSint:
						*(*interface{})(elem) = sintToFloat64Any(int64(vnode.val))
						continue
					case KReal:
						*(*interface{})(elem) = math.Float64frombits(vnode.val)
						continue
					}
				}
			case fieldKindSliceEface:
				if vtyp == KArray && len(*(*[]interface{})(elem)) == 0 {
					if uint32(vnode.val) == 0 {
						*(*[]interface{})(elem) = []interface{}{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceEface(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceInt:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]int)(elem) = []int{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceInt(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceI64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]int64)(elem) = []int64{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceI64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceI32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]int32)(elem) = []int32{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceI32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceUint:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]uint)(elem) = []uint{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceUint(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceU64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]uint64)(elem) = []uint64{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceU64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceU32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]uint32)(elem) = []uint32{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceU32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceF64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]float64)(elem) = []float64{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceF64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceF32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]float32)(elem) = []float32{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceF32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceBool:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]bool)(elem) = []bool{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceBool(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceString:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]string)(elem) = []string{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceString(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			}
		}

		err := d.fieldDecs[idx].FromDom(elem, Node{ptr: vptr}, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
	}
	return gerr
}

func (d *structDecoder) fromObjSlow(vp unsafe.Pointer, objPtr *node, p *Parser, opts uint64, ctx *context) error {
	var gerr error
	size := int(uint32(objPtr.val))
	caseSensitive := opts&uint64(consts.OptionCaseSensitive) != 0
	disUnknown := Options(opts)&OptionDisableUnknown != 0
	copyStr := opts&(1<<_F_copy_string) != 0
	jsonStr := p.Json

	fields := d.fields
	nfields := len(fields)
	hasHints := p.hasProjHints
	expectedIdx := 0
	next := PtrOffset(objPtr, 1)
	for i := 0; i < size; i++ {
		knode := next
		vptr := PtrOffset(next, 1)
		vnode := vptr
		vtyp := uint8(vnode.typ)
		if vtyp-KObject > 1 {
			next = PtrOffset(vptr, 1)
		} else {
			next = PtrOffset(vptr, int64(vnode.val>>ConLenBits))
		}

		// find field idx (use pre-matched schema hint when present)
		var idx int
		if hasHints {
			if hint := int((knode.typ >> 8) & 0xFFFFFF); hint != 0 {
				idx = hint - 1
				expectedIdx = idx + 1
				goto slowFieldHit
			}
		}
		{
			var key string
			if uint8(knode.typ) == KStringCommon {
				off := int(knode.typ >> PosBits)
				key = jsonStr[off : off+int(knode.val)]
			} else {
				key, _ = NewNode(knode).AsStrRef(ctx)
			}
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
		}

	slowFieldHit:
		fe := &fields[idx]
		var elem unsafe.Pointer
		if fe.kind == fieldKindWideOffset {
			elem = unsafe.Add(vp, d.wideOffsets[fe.offset])
		} else {
			elem = unsafe.Add(vp, uintptr(fe.offset))
		}
		if vtyp == KNull {
			if fe.kind >= fieldKindPtrString && fe.kind < fieldKindWideOffset {
				*(*unsafe.Pointer)(elem) = nil
				continue
			}
			switch fe.kind {
			case fieldKindPtrStruct:
				*(*unsafe.Pointer)(elem) = nil
				continue
			case fieldKindSlice:
				((*sliceDecoder)(fe.subDec)).setNil(elem)
				continue
			case fieldKindSliceEface:
				*(*[]interface{})(elem) = nil
				continue
			case fieldKindSliceInt:
				*(*[]int)(elem) = nil
				continue
			case fieldKindSliceI64:
				*(*[]int64)(elem) = nil
				continue
			case fieldKindSliceI32:
				*(*[]int32)(elem) = nil
				continue
			case fieldKindSliceUint:
				*(*[]uint)(elem) = nil
				continue
			case fieldKindSliceU64:
				*(*[]uint64)(elem) = nil
				continue
			case fieldKindSliceU32:
				*(*[]uint32)(elem) = nil
				continue
			case fieldKindSliceF64:
				*(*[]float64)(elem) = nil
				continue
			case fieldKindSliceF32:
				*(*[]float32)(elem) = nil
				continue
			case fieldKindSliceBool:
				*(*[]bool)(elem) = nil
				continue
			case fieldKindSliceString:
				*(*[]string)(elem) = nil
				continue
			case fieldKindEface:
				if *(*interface{})(elem) == nil {
					continue
				}
			case fieldKindGeneric, fieldKindWideOffset:
			default:
				continue
			}
		} else if fe.kind >= fieldKindPtrString && fe.kind < fieldKindWideOffset {
			if d.decodePtrScalarField(fe.kind, elem, vnode, vtyp, p, jsonStr, copyStr) {
				continue
			}
		} else {
			switch fe.kind {
			case fieldKindString:
				if vtyp == KStringCommon {
					off := int(vnode.typ >> PosBits)
					n := int(vnode.val)
					s := jsonStr[off : off+n]
					if copyStr && n != 0 {
						if copied, ok := p.copyArenaHit(s); ok {
							s = copied
						} else {
							s = p.copyArenaStringSlow(s)
						}
					}
					*(*string)(elem) = s
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
			case fieldKindI16:
				if vtyp == KUint && vnode.val <= math.MaxInt16 {
					*(*int16)(elem) = int16(vnode.val)
					continue
				}
				if vtyp == KSint {
					iv := int64(vnode.val)
					if iv >= math.MinInt16 && iv <= math.MaxInt16 {
						*(*int16)(elem) = int16(iv)
						continue
					}
				}
			case fieldKindI8:
				if vtyp == KUint && vnode.val <= math.MaxInt8 {
					*(*int8)(elem) = int8(vnode.val)
					continue
				}
				if vtyp == KSint {
					iv := int64(vnode.val)
					if iv >= math.MinInt8 && iv <= math.MaxInt8 {
						*(*int8)(elem) = int8(iv)
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
					*(*float64)(elem) = sintToFloat64(int64(vnode.val))
					continue
				}
			case fieldKindF32:
				if vtyp == KReal {
					fv := math.Float64frombits(vnode.val)
					if fv >= -math.MaxFloat32 && fv <= math.MaxFloat32 {
						*(*float32)(elem) = float32(fv)
						continue
					}
				} else if vtyp == KUint {
					*(*float32)(elem) = float32(vnode.val)
					continue
				} else if vtyp == KSint {
					*(*float32)(elem) = sintToFloat32(int64(vnode.val))
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
			case fieldKindU16:
				if vtyp == KUint && vnode.val <= math.MaxUint16 {
					*(*uint16)(elem) = uint16(vnode.val)
					continue
				}
			case fieldKindU8:
				if vtyp == KUint && vnode.val <= math.MaxUint8 {
					*(*uint8)(elem) = uint8(vnode.val)
					continue
				}
			case fieldKindPtrStruct:
				if vtyp == KObject {
					sd := (*structDecoder)(fe.subDec)
					dst := *(*unsafe.Pointer)(elem)
					if dst == nil {
						dst = sd.allocPtr(p)
						*(*unsafe.Pointer)(elem) = dst
					}
					err := sd.FromDom(dst, Node{ptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindStruct:
				if vtyp == KObject {
					err := ((*structDecoder)(fe.subDec)).FromDom(elem, Node{ptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSlice:
				if vtyp == KArray {
					sd := (*sliceDecoder)(fe.subDec)
					if uint32(vnode.val) == 0 {
						sd.setEmpty(elem)
						continue
					}
					err := sd.FromDom(elem, Node{ptr: vptr}, ctx)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindEface:
				if *(*interface{})(elem) == nil {
					switch vtyp {
					case KStringCommon:
						*(*interface{})(elem) = strCommonAny(p, int(vnode.typ>>PosBits), int(vnode.val), copyStr)
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
				if vtyp == KArray && len(*(*[]interface{})(elem)) == 0 {
					if uint32(vnode.val) == 0 {
						*(*[]interface{})(elem) = []interface{}{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceEface(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceInt:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]int)(elem) = []int{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceInt(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceI64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]int64)(elem) = []int64{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceI64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceI32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]int32)(elem) = []int32{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceI32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceUint:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]uint)(elem) = []uint{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceUint(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceU64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]uint64)(elem) = []uint64{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceU64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceU32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]uint32)(elem) = []uint32{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceU32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceF64:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]float64)(elem) = []float64{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceF64(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceF32:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]float32)(elem) = []float32{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceF32(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceBool:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]bool)(elem) = []bool{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceBool(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			case fieldKindSliceString:
				if vtyp == KArray {
					if uint32(vnode.val) == 0 {
						*(*[]string)(elem) = []string{}
						continue
					}
					n := Node{ptr: vptr}
					err := n.AsSliceString(ctx, elem)
					if gerr == nil && err != nil {
						gerr = err
					}
					continue
				}
			}
		}

		err := d.fieldDecs[idx].FromDom(elem, Node{ptr: vptr}, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
	}
	return gerr
}
