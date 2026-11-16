//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

//go:generate go run gen_structs_obj.go

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
// The trailing padding is spelled out because go/types before Go 1.21 (used
// by go vet / go test on Go 1.20) omits implicit trailing padding from
// unsafe.Sizeof and would otherwise reject the size assertions below.
type fieldEntry struct {
	name   string
	subDec unsafe.Pointer
	offset uint32
	kind   uint8
	_      [3]byte
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

// flatKeyPrefix holds a pre-encoded host-endian byte representation of `name":`
// (without the opening quote) for fast in-bounds ASCII key comparison up to 14 bytes;
// longer field names leave n2 == 0 and fall back to exact length/name comparison.
type flatKeyPrefix struct {
	w0   uint64
	w1   uint64
	mask uint64
	n2   uint8
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
	flatKeyPrefixes   []flatKeyPrefix
	_                 [64]byte
	flatFailStreak    uint32
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

// canFastObj reports whether this struct decoder can run fromObjFast under opts.
func (d *structDecoder) canFastObj(opts uint64) bool {
	if d == nil || !d.fast {
		return false
	}
	exclude := fastStructExcludeMask
	if d.noInterface {
		exclude = fastStructStrictExcludeMask
	}
	return opts&exclude == 0
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
	if d.canFastObj(opts) {
		return d.fromObjFast(vp, node.ptr, p, p.Json, opts&(1<<_F_copy_string) != 0, ctx)
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
			if f32, ok := realBitsToFloat32(vnode.val, jsonStr, int(vnode.typ>>PosBits)); ok {
				pp := (**float32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*float32)(p.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = f32
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
