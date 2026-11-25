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

//go:generate go run gen_structs_obj.go

package optdec

import (
	"encoding/binary"
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/native/number"
	caching "github.com/bytedance/sonic/internal/optcaching"
)

const nodeSize = unsafe.Sizeof(node{})

const fastStructExcludeMask = uint64(consts.OptionCaseSensitive) |
	uint64(OptionDisableUnknown)

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

// fieldKeySig is 32 bytes (power-of-two stride: 2 entries per 64B cache line, SHLQ $5 indexing).
type fieldKeySig struct {
	head uint64
	tail uint64
	mid  uint64
	n    int
}

// readStrU64 loads 8 little-endian bytes starting at s[off].
// Caller guarantees 0 <= off && off+8 <= len(s).
func readStrU64(s string, off int) uint64 {
	return binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), off))[:])
}

// readStrU32 loads 4 little-endian bytes starting at s[off].
// Caller guarantees 0 <= off && off+4 <= len(s).
func readStrU32(s string, off int) uint32 {
	return binary.LittleEndian.Uint32((*[4]byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), off))[:])
}

func buildFieldKeySigs(entries []fieldEntry) []fieldKeySig {
	sigs := make([]fieldKeySig, len(entries))
	for i := range entries {
		k := entries[i].name
		n := len(k)
		var sig fieldKeySig
		sig.n = n
		switch {
		case n >= 17:
			sig.head = readStrU64(k, 0)
			sig.tail = readStrU64(k, n-8)
			sig.mid = readStrU64(k, 8)
		case n >= 8:
			sig.head = readStrU64(k, 0)
			sig.tail = readStrU64(k, n-8)
		case n >= 4:
			sig.head = uint64(readStrU32(k, 0))
			sig.tail = uint64(readStrU32(k, n-4))
		case n > 0:
			sig.head = uint64(k[0]) | uint64(k[n>>1])<<8 | uint64(k[n-1])<<16
		}
		sigs[i] = sig
	}
	return sigs
}

type structDecoder struct {
	fieldMap      *caching.FieldCache
	fields        []fieldEntry
	fieldKeySigs  []fieldKeySig
	fieldDecs     []decFunc
	wideOffsets   []uintptr
	structName    string
	typ           reflect.Type
	goType        reflect.Type
	fast          bool
	noInterface   bool
	ptrScalarHint uint8
	ptrStrHint    uint8
}

// FieldCount implements jsonscan.FieldMatcher using the authoritative structDecoder schema.
func (d *structDecoder) FieldCount() int {
	return len(d.fields)
}

// FieldName implements jsonscan.FieldMatcher for sequential field-order matching.
func (d *structDecoder) FieldName(idx int) string {
	return d.fields[idx].name
}

// MatchField implements jsonscan.FieldMatcher via read-only FieldCache lookup.
func (d *structDecoder) MatchField(key string, caseSensitive, keyASCII bool) int {
	return d.fieldMap.GetKnownASCII(key, caseSensitive, keyASCII)
}

// canFastObj reports whether this struct decoder can run fromObjFast under opts.
func (d *structDecoder) canFastObj(opts uint64) bool {
	if d == nil || !d.fast {
		return false
	}
	return opts&fastStructExcludeMask == 0
}

// decodeValueSlot decodes one JSON value at vptr into an existing struct value
// slot (elem) and returns the next sibling *node cursor and any decode error.
func (d *structDecoder) decodeValueSlot(elem unsafe.Pointer, vptr *node, ctx *Context) (*node, error) {
	vtyp := uint8(vptr.typ)
	if vtyp == KObject {
		next := PtrOffset(vptr, int64(vptr.val>>ConLenBits))
		if uint32(vptr.val) == 0 {
			return next, nil
		}
		opts := ctx.options
		if d.canFastObj(opts) {
			return next, d.fromObjFast(elem, vptr, ctx.Doc.Json, opts&(1<<_F_copy_string) != 0, ctx)
		}
		return next, d.fromObjSlow(elem, vptr, opts, ctx)
	}
	val := NewNode(vptr)
	err := d.FromDom(elem, val, ctx)
	return val.Next(), err
}

// decodePtrSlot decodes one JSON value at vptr into a *Struct pointer slot (pp)
// and returns the next sibling *node cursor and any decode error.
func (d *structDecoder) decodePtrSlot(pp *unsafe.Pointer, vptr *node, elemDec decFunc, ctx *Context) (*node, error) {
	vtyp := uint8(vptr.typ)
	if vtyp == KNull {
		*pp = nil
		return PtrOffset(vptr, 1), nil
	}
	if vtyp == KObject {
		next := PtrOffset(vptr, int64(vptr.val>>ConLenBits))
		dst := *pp
		if dst == nil {
			dst = reflect.New(d.goType).UnsafePointer()
			*pp = dst
		}
		if uint32(vptr.val) == 0 {
			return next, nil
		}
		opts := ctx.options
		if d.canFastObj(opts) {
			return next, d.fromObjFast(dst, vptr, ctx.Doc.Json, opts&(1<<_F_copy_string) != 0, ctx)
		}
		return next, d.fromObjSlow(dst, vptr, opts, ctx)
	}
	val := NewNode(vptr)
	err := elemDec.FromDom(unsafe.Pointer(pp), val, ctx)
	return val.Next(), err
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

	opts := ctx.options
	if d.canFastObj(opts) {
		return d.fromObjFast(vp, node.ptr, ctx.Doc.Json, opts&(1<<_F_copy_string) != 0, ctx)
	}
	return d.fromObjSlow(vp, node.ptr, opts, ctx)
}

func (d *structDecoder) decodePtrScalarField(fk uint8, elem unsafe.Pointer, vnode *node, vtyp uint8, ctx *Context, jsonStr string, copyStr bool) bool {
	switch fk {
	case fieldKindPtrString:
		if vtyp == KStringCommon {
			off := int(vnode.typ >> PosBits)
			n := int(vnode.val)
			s := jsonStr[off : off+n]
			if copyStr && n != 0 {
				if copied, ok := ctx.copyArenaHit(s); ok {
					s = copied
				} else {
					s = ctx.copyArenaStringSlow(s)
				}
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
		}
		if vtyp == KStringEscaped {
			s := ctx.strEscaped(int(vnode.typ>>PosBits), vnode.val)
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
		}
	case fieldKindPtrI64:
		if (vtyp == KUint && vnode.val <= math.MaxInt64) || vtyp == KSint {
			pp := (**int64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int64(vnode.val)
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if iv, ok := parseValidRawI64(jsonStr[off : off+int(vnode.val)]); ok {
				pp := (**int64)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = iv
				return true
			}
		}
	case fieldKindPtrInt:
		if (vtyp == KUint && vnode.val <= math.MaxInt64) || vtyp == KSint {
			pp := (**int)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = int(int64(vnode.val))
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if iv, ok := parseValidRawI64(jsonStr[off : off+int(vnode.val)]); ok {
				pp := (**int)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int(iv)
				return true
			}
		}
	case fieldKindPtrI32:
		if vtyp == KUint && vnode.val <= math.MaxInt32 {
			pp := (**int32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*int32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
					dst = (*int32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int32(iv)
				return true
			}
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if iv, ok := parseValidRawI64(jsonStr[off : off+int(vnode.val)]); ok && iv >= math.MinInt32 && iv <= math.MaxInt32 {
				pp := (**int32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
				dst = (*int16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
					dst = (*int16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int16(iv)
				return true
			}
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if iv, ok := parseValidRawI64(jsonStr[off : off+int(vnode.val)]); ok && iv >= math.MinInt16 && iv <= math.MaxInt16 {
				pp := (**int16)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
				dst = (*int8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
					dst = (*int8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = int8(iv)
				return true
			}
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if iv, ok := parseValidRawI64(jsonStr[off : off+int(vnode.val)]); ok && iv >= math.MinInt8 && iv <= math.MaxInt8 {
				pp := (**int8)(elem)
				dst := *pp
				if dst == nil {
					dst = (*int8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
				dst = (*uint64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = vnode.val
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if uv, ok := parseValidRawU64(jsonStr[off : off+int(vnode.val)]); ok {
				pp := (**uint64)(elem)
				dst := *pp
				if dst == nil {
					dst = (*uint64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = uv
				return true
			}
		}
	case fieldKindPtrUint:
		if vtyp == KUint {
			pp := (**uint)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint(vnode.val)
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if uv, ok := parseValidRawU64(jsonStr[off : off+int(vnode.val)]); ok {
				pp := (**uint)(elem)
				dst := *pp
				if dst == nil {
					dst = (*uint)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = uint(uv)
				return true
			}
		}
	case fieldKindPtrU32:
		if vtyp == KUint && vnode.val <= math.MaxUint32 {
			pp := (**uint32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint32(vnode.val)
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if uv, ok := parseValidRawU64(jsonStr[off : off+int(vnode.val)]); ok && uv <= math.MaxUint32 {
				pp := (**uint32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*uint32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = uint32(uv)
				return true
			}
		}
	case fieldKindPtrU16:
		if vtyp == KUint && vnode.val <= math.MaxUint16 {
			pp := (**uint16)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint16(vnode.val)
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if uv, ok := parseValidRawU64(jsonStr[off : off+int(vnode.val)]); ok && uv <= math.MaxUint16 {
				pp := (**uint16)(elem)
				dst := *pp
				if dst == nil {
					dst = (*uint16)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = uint16(uv)
				return true
			}
		}
	case fieldKindPtrU8:
		if vtyp == KUint && vnode.val <= math.MaxUint8 {
			pp := (**uint8)(elem)
			dst := *pp
			if dst == nil {
				dst = (*uint8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = uint8(vnode.val)
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if uv, ok := parseValidRawU64(jsonStr[off : off+int(vnode.val)]); ok && uv <= math.MaxUint8 {
				pp := (**uint8)(elem)
				dst := *pp
				if dst == nil {
					dst = (*uint8)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = uint8(uv)
				return true
			}
		}
	case fieldKindPtrBool:
		if vtyp == KTrue || vtyp == KFalse {
			pp := (**bool)(elem)
			dst := *pp
			if dst == nil {
				dst = (*bool)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
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
				dst = (*float64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*(*uint64)(unsafe.Pointer(dst)) = vnode.val
			return true
		}
		if vtyp == KUint {
			pp := (**float64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = float64(vnode.val)
			return true
		}
		if vtyp == KSint {
			pp := (**float64)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = sintToFloat64(int64(vnode.val))
			return true
		}
		if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if fv, ok := number.ParseValidJSONFloat64(jsonStr[off : off+int(vnode.val)]); ok {
				pp := (**float64)(elem)
				dst := *pp
				if dst == nil {
					dst = (*float64)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = fv
				return true
			}
		}
	case fieldKindPtrF32:
		if vtyp == KReal {
			if f32, ok := realBitsToFloat32(vnode.val, jsonStr, int(vnode.typ>>PosBits)); ok {
				pp := (**float32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*float32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = f32
				return true
			}
		} else if vtyp == KUint {
			pp := (**float32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = float32(vnode.val)
			return true
		} else if vtyp == KSint {
			pp := (**float32)(elem)
			dst := *pp
			if dst == nil {
				dst = (*float32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
				*pp = dst
			}
			*dst = sintToFloat32(int64(vnode.val))
			return true
		} else if vtyp == KRawNumber {
			off := int(vnode.typ >> PosBits)
			if f32, ok := number.ParseValidJSONFloat32(jsonStr[off : off+int(vnode.val)]); ok {
				pp := (**float32)(elem)
				dst := *pp
				if dst == nil {
					dst = (*float32)(ctx.allocScalarPtr(int(d.ptrScalarHint)))
					*pp = dst
				}
				*dst = f32
				return true
			}
		}
	}
	return false
}
