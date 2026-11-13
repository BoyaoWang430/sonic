//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"math/bits"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/utf8"
)

const (
	flatMaxArrayLen    = 16
	flatMaxElemPool    = 64
	flatMaxStructMasks = 16
)

func isFlatScalarOrPtrKind(kind uint8) bool {
	return (kind >= fieldKindString && kind <= fieldKindF32) ||
		(kind >= fieldKindPtrString && kind <= fieldKindPtrF32)
}

func isFlatScalarSliceKind(kind uint8) bool {
	switch kind {
	case fieldKindSliceI64, fieldKindSliceI32,
		fieldKindSliceU64, fieldKindSliceU32,
		fieldKindSliceF64, fieldKindSliceF32,
		fieldKindSliceBool, fieldKindSliceString:
		return true
	case fieldKindSliceInt, fieldKindSliceUint:
		return unsafe.Sizeof(int(0)) == 8
	default:
		return false
	}
}

func flatScalarElementKind(dec decFunc) (uint8, bool) {
	switch dec.(type) {
	case *stringDecoder:
		return fieldKindString, true
	case *i64Decoder:
		return fieldKindI64, true
	case *i32Decoder:
		return fieldKindI32, true
	case *i16Decoder:
		return fieldKindI16, true
	case *i8Decoder:
		return fieldKindI8, true
	case *u64Decoder:
		return fieldKindU64, true
	case *u32Decoder:
		return fieldKindU32, true
	case *u16Decoder:
		return fieldKindU16, true
	case *u8Decoder:
		return fieldKindU8, true
	case *boolDecoder:
		return fieldKindBool, true
	case *f64Decoder:
		return fieldKindF64, true
	case *f32Decoder:
		return fieldKindF32, true
	default:
		return 0, false
	}
}

func flatScalarKindSize(kind uint8) uintptr {
	switch kind {
	case fieldKindString:
		return unsafe.Sizeof("")
	case fieldKindI64, fieldKindU64, fieldKindF64:
		return 8
	case fieldKindI32, fieldKindU32, fieldKindF32:
		return 4
	case fieldKindI16, fieldKindU16:
		return 2
	case fieldKindI8, fieldKindU8, fieldKindBool:
		return 1
	default:
		return 0
	}
}

func flatSliceElemKind(kind uint8) uint8 {
	switch kind {
	case fieldKindSliceInt, fieldKindSliceI64:
		return fieldKindI64
	case fieldKindSliceI32:
		return fieldKindI32
	case fieldKindSliceUint, fieldKindSliceU64:
		return fieldKindU64
	case fieldKindSliceU32:
		return fieldKindU32
	case fieldKindSliceF64:
		return fieldKindF64
	case fieldKindSliceF32:
		return fieldKindF32
	case fieldKindSliceBool:
		return fieldKindBool
	case fieldKindSliceString:
		return fieldKindString
	default:
		return 0
	}
}

func isFlatContainerField(fe *fieldEntry) bool {
	if isFlatScalarSliceKind(fe.kind) {
		return true
	}
	if fe.kind == fieldKindArray {
		d := (*arrayDecoder)(fe.subDec)
		if d != nil && d.len > 0 && d.len <= flatMaxArrayLen {
			if ek, ok := flatScalarElementKind(d.elemDec); ok && d.size == flatScalarKindSize(ek) {
				return true
			}
		}
		return false
	}
	if fe.kind == fieldKindSlice {
		d := (*sliceDecoder)(fe.subDec)
		return d != nil && d.structDec != nil &&
			d.structDec.flatDirect && d.structDec.flatPlan == nil &&
			d.structDec.flatPtrMask == 0 && len(d.structDec.fields) > 0 &&
			len(d.structDec.fields) <= flatMaxArrayLen
	}
	return false
}

func flatPackElemField(st *flatStagedField, kind uint8) uint64 {
	if kind == fieldKindString {
		return (uint64(st.strOff) << 32) | uint64(st.strLen)
	}
	return st.val
}

func flatUnpackElemField(packed uint64, kind uint8) flatStagedField {
	if kind == fieldKindString {
		return flatStagedField{
			strOff: uint32(packed >> 32),
			strLen: uint32(packed),
		}
	}
	return flatStagedField{val: packed}
}

func flatResolveStr(st *flatStagedField, s string, copyStr bool, copySlab []byte, copyOff *int) string {
	n := int(st.strLen)
	if n == 0 {
		return ""
	}
	if copyStr {
		off := *copyOff
		dst := copySlab[off : off+n]
		*copyOff = off + n
		copy(dst, s[st.strOff:int(st.strOff)+n])
		return unsafe.String(&dst[0], n)
	}
	return s[st.strOff : int(st.strOff)+n]
}

func flatScanGroupKeySlow(
	strPtr unsafe.Pointer,
	pos, end int,
	sd *structDecoder,
	expectedIdx *int,
	caseSensitive, validateStr bool,
	nonASCII *uint64,
) (int, int, bool) {
	fields := sd.fields
	nfields := len(fields)
	exp := *expectedIdx
	keyStart := pos
	keyEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, nonASCII)
	if !ok {
		return 0, 0, false
	}
	key := unsafe.String((*byte)(unsafe.Add(strPtr, keyStart)), keyEnd-keyStart)
	pos = keyEnd + 1
	if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == ':' {
		pos++
	} else {
		var colon byte
		pos, colon, ok = flatSkipWS(strPtr, pos, end)
		if !ok || colon != ':' {
			return 0, 0, false
		}
	}

	var idx int
	if uint(exp) < uint(nfields) && fields[exp].name == key {
		idx = exp
		*expectedIdx = exp + 1
	} else if caseSensitive {
		idx = sd.fieldMap.Get(key, true)
		if idx != -1 {
			*expectedIdx = idx + 1
		}
	} else {
		idx = sd.fieldMap.GetInsensitive(key)
		if idx != -1 {
			*expectedIdx = idx + 1
		}
	}
	return idx, pos, true
}

func flatNextNonWSByte(strPtr unsafe.Pointer, pos, end int) (int, byte, bool) {
	if pos >= end {
		return 0, 0, false
	}
	c := *(*byte)(unsafe.Add(strPtr, pos))
	if c > ' ' {
		return pos + 1, c, true
	}
	if c == ' ' && pos+1 < end {
		c2 := *(*byte)(unsafe.Add(strPtr, pos+1))
		if c2 > ' ' {
			return pos + 2, c2, true
		}
	}
	return flatSkipWS(strPtr, pos, end)
}

func tryDecodeFlatContainerPlanBody(s string, startPos, end int, f uint64, vp unsafe.Pointer, plan *flatStructPlan, utf8Prechecked, checkTrailing bool) (int, bool) {
	strPtr := unsafe.Pointer(unsafe.StringData(s))
	validateStr := f&(1<<_F_validate_string) != 0
	copyStr := f&(1<<_F_copy_string) != 0
	caseSensitive := f&uint64(consts.OptionCaseSensitive) != 0
	disUnknown := Options(f)&OptionDisableUnknown != 0
	strictURC := f&(1<<_F_disable_urc) != 0
	noValidateJSON := f&(1<<_F_no_validate_json) != 0

	pos, c, ok := flatSkipWS(strPtr, startPos, end)
	if !ok {
		return 0, false
	}
	if c == 'n' {
		if pos+3 > end || s[pos:pos+3] != "ull" {
			return 0, false
		}
		pos += 3
		if checkTrailing && validateStr && pos < end {
			if !flatTrailingUTF8OK(s, strPtr, pos, end) {
				return 0, false
			}
		}
		return pos, true
	}
	if c != '{' {
		return 0, false
	}

	var staged [32]flatStagedField
	var preNullStaged *[32]flatStagedField
	var elemPool [flatMaxElemPool]uint64
	var structMasks [flatMaxStructMasks]uint16
	var presentMask uint32
	var strMask uint32
	var nullMask uint32
	var nulledOnceMask uint32
	var preNullMask uint32
	var nonASCII uint64
	elemPoolPos := 0
	structMaskPos := 0

	pos, ok = flatScanContainerPlanGroup(
		strPtr, s, pos, end, plan, 0,
		validateStr, caseSensitive, disUnknown, strictURC, noValidateJSON,
		&nonASCII, &staged, &preNullStaged,
		&elemPool, &elemPoolPos, &structMasks, &structMaskPos,
		&presentMask, &strMask, &nullMask, &nulledOnceMask, &preNullMask,
	)
	if !ok {
		return 0, false
	}

	if validateStr && !utf8Prechecked && nonASCII != 0 {
		if !utf8.ValidateString(s[startPos:pos]) {
			return 0, false
		}
	}
	if checkTrailing && validateStr && pos < end {
		if !flatTrailingUTF8OK(s, strPtr, pos, end) {
			return 0, false
		}
	}

	if presentMask == 0 && nullMask == 0 {
		return pos, true
	}

	flatCommitContainerPlanLeaves(
		vp, plan, &staged, preNullStaged, &elemPool, &structMasks,
		presentMask, strMask, nullMask, nulledOnceMask, preNullMask, s, copyStr,
	)
	return pos, true
}

func flatScanContainerPlanGroup(
	strPtr unsafe.Pointer,
	s string,
	pos, end int,
	plan *flatStructPlan,
	groupIdx int,
	validateStr, caseSensitive, disUnknown, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	staged *[32]flatStagedField,
	preNullStaged **[32]flatStagedField,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
	structMasks *[flatMaxStructMasks]uint16,
	structMaskPos *int,
	presentMask, strMask, nullMask, nulledOnceMask, preNullMask *uint32,
) (int, bool) {
	if pos >= end {
		return 0, false
	}
	c := *(*byte)(unsafe.Add(strPtr, pos))
	if c == '}' {
		return pos + 1, true
	}
	var ok bool
	if c == '"' {
		pos++
	} else {
		pos, c, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if c == '}' {
			return pos, true
		}
		if c != '"' {
			return 0, false
		}
	}

	grp := &plan.groups[groupIdx]
	sd := grp.sd
	fields := sd.fields
	nfields := len(fields)
	slotBase := int(grp.slotStart)
	pureASCIIKeys := grp.pureASCIIKeys
	ptrMask := plan.ptrMask
	expectedIdx := 0

	for {
		var idx int
		if pureASCIIKeys && uint(expectedIdx) < uint(nfields) {
			fe := &fields[expectedIdx]
			n := len(fe.name)
			if pos+n+2 <= end && *(*uint16)(unsafe.Add(strPtr, pos+n)) == 0x3a22 &&
				unsafe.String((*byte)(unsafe.Add(strPtr, pos)), n) == fe.name {
				pos += n + 2
				idx = expectedIdx
				expectedIdx++
				goto keyMatched
			}
		}
		idx, pos, ok = flatScanGroupKeySlow(strPtr, pos, end, sd, &expectedIdx, caseSensitive, validateStr, nonASCII)
		if !ok {
			return 0, false
		}

	keyMatched:
		if idx == -1 {
			if disUnknown {
				return 0, false
			}
			var vc byte
			if pos >= end {
				return 0, false
			}
			vc = *(*byte)(unsafe.Add(strPtr, pos))
			if vc > ' ' {
				pos++
			} else {
				pos, vc, ok = flatSkipWS(strPtr, pos, end)
				if !ok {
					return 0, false
				}
			}
			pos, ok = flatSkipValue(s, strPtr, vc, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
			if !ok {
				return 0, false
			}
			goto afterVal
		}

		{
			var vc byte
			if pos >= end {
				return 0, false
			}
			vc = *(*byte)(unsafe.Add(strPtr, pos))
			if vc > ' ' {
				pos++
			} else {
				pos, vc, ok = flatSkipWS(strPtr, pos, end)
				if !ok {
					return 0, false
				}
			}

			slot := plan.slots[slotBase+idx]
			if slot < 0 {
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					goto afterVal
				}
				if vc != '{' {
					return 0, false
				}
				pos, ok = flatScanContainerPlanGroup(
					strPtr, s, pos, end, plan, int(-slot),
					validateStr, caseSensitive, disUnknown, strictURC, noValidateJSON,
					nonASCII, staged, preNullStaged,
					elemPool, elemPoolPos, structMasks, structMaskPos,
					presentMask, strMask, nullMask, nulledOnceMask, preNullMask,
				)
				if !ok {
					return 0, false
				}
				goto afterVal
			}

			leafIdx := int(slot)
			bit := uint32(1) << uint(leafIdx)
			lf := &plan.leaves[leafIdx]

			switch {
			case isFlatScalarOrPtrKind(lf.kind):
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					if ptrMask&bit != 0 {
						if *nulledOnceMask&bit == 0 && *presentMask&bit != 0 {
							if *preNullStaged == nil {
								*preNullStaged = flatAllocPreNullStaged()
							}
							(*preNullStaged)[leafIdx] = staged[leafIdx]
							*preNullMask |= bit
						}
						*nullMask |= bit
						*nulledOnceMask |= bit
						*presentMask &^= bit
						*strMask &^= bit
					}
					goto afterVal
				}
				nextPos, isStr, ok := flatStageLeafValue(strPtr, s, vc, pos, end, lf.kind, validateStr, nonASCII, &staged[leafIdx])
				if !ok {
					return 0, false
				}
				pos = nextPos
				*presentMask |= bit
				if ptrMask&bit != 0 {
					*nullMask &^= bit
				}
				if isStr {
					*strMask |= bit
				}

			case lf.kind == fieldKindArray:
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					goto afterVal
				}
				if vc != '[' {
					return 0, false
				}
				var startOff, count int
				pos, startOff, count, ok = flatStageScalarArray(
					strPtr, s, pos, end, lf.elemKind, int(lf.arrLen),
					validateStr, strictURC, noValidateJSON, nonASCII,
					elemPool, elemPoolPos,
				)
				if !ok {
					return 0, false
				}
				staged[leafIdx].strOff = uint32(startOff)
				staged[leafIdx].strLen = uint32(count)
				*presentMask |= bit
				if lf.elemKind == fieldKindString {
					*strMask |= bit
				}

			case isFlatScalarSliceKind(lf.kind):
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					*nullMask |= bit
					*presentMask &^= bit
					*strMask &^= bit
					goto afterVal
				}
				if vc != '[' {
					return 0, false
				}
				var startOff, count int
				pos, startOff, count, ok = flatStageScalarArray(
					strPtr, s, pos, end, flatSliceElemKind(lf.kind), -1,
					validateStr, strictURC, noValidateJSON, nonASCII,
					elemPool, elemPoolPos,
				)
				if !ok {
					return 0, false
				}
				staged[leafIdx].strOff = uint32(startOff)
				staged[leafIdx].strLen = uint32(count)
				*presentMask |= bit
				*nullMask &^= bit
				if lf.kind == fieldKindSliceString {
					*strMask |= bit
				}

			case lf.kind == fieldKindSlice:
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					*nullMask |= bit
					*presentMask &^= bit
					*strMask &^= bit
					goto afterVal
				}
				if vc != '[' {
					return 0, false
				}
				var elemStart, maskStart, count int
				pos, elemStart, maskStart, count, ok = flatStageStructSlice(
					strPtr, s, pos, end, &plan.structSlices[lf.elemKind],
					validateStr, caseSensitive, disUnknown, strictURC, noValidateJSON, nonASCII,
					elemPool, elemPoolPos, structMasks, structMaskPos,
				)
				if !ok {
					return 0, false
				}
				staged[leafIdx].strOff = uint32(elemStart)
				staged[leafIdx].strLen = uint32(count)
				staged[leafIdx].val = uint64(maskStart)
				*presentMask |= bit
				*nullMask &^= bit
				*strMask |= bit

			default:
				return 0, false
			}
		}

	afterVal:
		if pos >= end {
			return 0, false
		}
		delim := *(*byte)(unsafe.Add(strPtr, pos))
		if delim == ',' {
			pos++
			if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == '"' {
				pos++
				continue
			}
			var nc byte
			pos, nc, ok = flatSkipWS(strPtr, pos, end)
			if !ok || nc != '"' {
				return 0, false
			}
			continue
		}
		if delim == '}' {
			return pos + 1, true
		}
		pos, delim, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if delim == '}' {
			return pos, true
		}
		if delim != ',' {
			return 0, false
		}
		var nc byte
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok || nc != '"' {
			return 0, false
		}
	}
}

func flatArrayNextElemSlow(strPtr unsafe.Pointer, pos, end int) (int, byte, bool, bool) {
	pos, delim, ok := flatSkipWS(strPtr, pos, end)
	if !ok {
		return 0, 0, false, false
	}
	if delim == ']' {
		return pos, 0, true, true
	}
	if delim != ',' {
		return 0, 0, false, false
	}
	var ec byte
	pos, ec, ok = flatSkipWS(strPtr, pos, end)
	if !ok {
		return 0, 0, false, false
	}
	return pos, ec, false, true
}

func flatSkipArrayRest(
	s string,
	strPtr unsafe.Pointer,
	pos, end int,
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
) (int, bool) {
	var ec byte
	var ok bool
	for {
		if pos >= end {
			return 0, false
		}
		delim := *(*byte)(unsafe.Add(strPtr, pos))
		if delim == ']' {
			return pos + 1, true
		}
		if delim == ',' {
			pos++
		} else {
			pos, delim, ok = flatSkipWS(strPtr, pos, end)
			if !ok {
				return 0, false
			}
			if delim == ']' {
				return pos, true
			}
			if delim != ',' {
				return 0, false
			}
		}
		pos, ec, ok = flatNextNonWSByte(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		pos, ok = flatSkipValue(s, strPtr, ec, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
		if !ok {
			return 0, false
		}
	}
}

func flatStageSignedArray(
	s string,
	strPtr unsafe.Pointer,
	ec byte,
	pos, end int,
	maxPos uint64,
	maxNeg uint64,
	arrCap int,
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
) (int, int, int, bool) {
	poolStart := *elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		val, nextPos, ok := flatParseSigned(strPtr, ec, pos, end, maxPos, maxNeg)
		if !ok {
			return 0, 0, 0, false
		}
		elemPool[pIdx] = val
		pIdx++
		pos = nextPos
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			pos, ok = flatSkipArrayRest(s, strPtr, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
			if !ok {
				return 0, 0, 0, false
			}
			*elemPoolPos = pIdx
			return pos, poolStart, arrCap, true
		}
		if pos >= end {
			return 0, 0, 0, false
		}
		d := *(*byte)(unsafe.Add(strPtr, pos))
		if d == ',' && pos+1 < end {
			if c2 := *(*byte)(unsafe.Add(strPtr, pos+1)); c2 > ' ' {
				ec = c2
				pos += 2
				continue
			}
		} else if d == ']' {
			*elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			*elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageUnsignedArray(
	s string,
	strPtr unsafe.Pointer,
	ec byte,
	pos, end int,
	maxU uint64,
	arrCap int,
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
) (int, int, int, bool) {
	poolStart := *elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		val, nextPos, ok := flatParseUnsigned(strPtr, ec, pos, end, maxU)
		if !ok {
			return 0, 0, 0, false
		}
		elemPool[pIdx] = val
		pIdx++
		pos = nextPos
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			pos, ok = flatSkipArrayRest(s, strPtr, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
			if !ok {
				return 0, 0, 0, false
			}
			*elemPoolPos = pIdx
			return pos, poolStart, arrCap, true
		}
		if pos >= end {
			return 0, 0, 0, false
		}
		d := *(*byte)(unsafe.Add(strPtr, pos))
		if d == ',' && pos+1 < end {
			if c2 := *(*byte)(unsafe.Add(strPtr, pos+1)); c2 > ' ' {
				ec = c2
				pos += 2
				continue
			}
		} else if d == ']' {
			*elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			*elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageFloatArray(
	s string,
	strPtr unsafe.Pointer,
	ec byte,
	pos, end int,
	is32 bool,
	arrCap int,
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
) (int, int, int, bool) {
	poolStart := *elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		fv, nextPos, ok := flatParseFloat(strPtr, ec, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if is32 {
			fv32 := float32(fv)
			if math.IsInf(float64(fv32), 0) && !math.IsInf(fv, 0) {
				return 0, 0, 0, false
			}
			elemPool[pIdx] = uint64(math.Float32bits(fv32))
		} else {
			elemPool[pIdx] = math.Float64bits(fv)
		}
		pIdx++
		pos = nextPos
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			pos, ok = flatSkipArrayRest(s, strPtr, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
			if !ok {
				return 0, 0, 0, false
			}
			*elemPoolPos = pIdx
			return pos, poolStart, arrCap, true
		}
		if pos >= end {
			return 0, 0, 0, false
		}
		d := *(*byte)(unsafe.Add(strPtr, pos))
		if d == ',' && pos+1 < end {
			if c2 := *(*byte)(unsafe.Add(strPtr, pos+1)); c2 > ' ' {
				ec = c2
				pos += 2
				continue
			}
		} else if d == ']' {
			*elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			*elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageBoolArray(
	s string,
	strPtr unsafe.Pointer,
	ec byte,
	pos, end int,
	arrCap int,
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
) (int, int, int, bool) {
	poolStart := *elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		if ec == 't' {
			if pos+3 > end || s[pos:pos+3] != "rue" {
				return 0, 0, 0, false
			}
			elemPool[pIdx] = 1
			pos += 3
		} else if ec == 'f' {
			if pos+4 > end || s[pos:pos+4] != "alse" {
				return 0, 0, 0, false
			}
			elemPool[pIdx] = 0
			pos += 4
		} else {
			return 0, 0, 0, false
		}
		pIdx++
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			var ok bool
			pos, ok = flatSkipArrayRest(s, strPtr, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
			if !ok {
				return 0, 0, 0, false
			}
			*elemPoolPos = pIdx
			return pos, poolStart, arrCap, true
		}
		if pos >= end {
			return 0, 0, 0, false
		}
		d := *(*byte)(unsafe.Add(strPtr, pos))
		if d == ',' && pos+1 < end {
			if c2 := *(*byte)(unsafe.Add(strPtr, pos+1)); c2 > ' ' {
				ec = c2
				pos += 2
				continue
			}
		} else if d == ']' {
			*elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done, ok bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			*elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageStringArray(
	s string,
	strPtr unsafe.Pointer,
	ec byte,
	pos, end int,
	arrCap int,
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
) (int, int, int, bool) {
	poolStart := *elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool || ec != '"' {
			return 0, 0, 0, false
		}
		strStart := pos
		strEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, nonASCII)
		if !ok {
			return 0, 0, 0, false
		}
		elemPool[pIdx] = (uint64(strStart) << 32) | uint64(strEnd-strStart)
		pIdx++
		pos = strEnd + 1
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			pos, ok = flatSkipArrayRest(s, strPtr, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
			if !ok {
				return 0, 0, 0, false
			}
			*elemPoolPos = pIdx
			return pos, poolStart, arrCap, true
		}
		if pos >= end {
			return 0, 0, 0, false
		}
		d := *(*byte)(unsafe.Add(strPtr, pos))
		if d == ',' && pos+1 < end {
			if c2 := *(*byte)(unsafe.Add(strPtr, pos+1)); c2 > ' ' {
				ec = c2
				pos += 2
				continue
			}
		} else if d == ']' {
			*elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			*elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageScalarArray(
	strPtr unsafe.Pointer,
	s string,
	pos, end int,
	elemKind uint8,
	arrCap int, // >= 0 for fixed arrays [N]T; -1 for slices []T
	validateStr, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
) (int, int, int, bool) {
	if pos >= end {
		return 0, 0, 0, false
	}
	ec := *(*byte)(unsafe.Add(strPtr, pos))
	if ec > ' ' {
		pos++
	} else {
		var ok bool
		pos, ec, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
	}
	if ec == ']' {
		return pos, *elemPoolPos, 0, true
	}
	if arrCap == 0 {
		pos, ok := flatSkipArrayRest(s, strPtr, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
		return pos, *elemPoolPos, 0, ok
	}

	switch elemKind {
	case fieldKindI64:
		return flatStageSignedArray(s, strPtr, ec, pos, end, math.MaxInt64, uint64(math.MaxInt64)+1, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindI32:
		return flatStageSignedArray(s, strPtr, ec, pos, end, math.MaxInt32, uint64(math.MaxInt32)+1, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindI16:
		return flatStageSignedArray(s, strPtr, ec, pos, end, math.MaxInt16, uint64(math.MaxInt16)+1, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindI8:
		return flatStageSignedArray(s, strPtr, ec, pos, end, math.MaxInt8, uint64(math.MaxInt8)+1, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindU64:
		return flatStageUnsignedArray(s, strPtr, ec, pos, end, math.MaxUint64, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindU32:
		return flatStageUnsignedArray(s, strPtr, ec, pos, end, math.MaxUint32, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindU16:
		return flatStageUnsignedArray(s, strPtr, ec, pos, end, math.MaxUint16, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindU8:
		return flatStageUnsignedArray(s, strPtr, ec, pos, end, math.MaxUint8, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindF64:
		return flatStageFloatArray(s, strPtr, ec, pos, end, false, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindF32:
		return flatStageFloatArray(s, strPtr, ec, pos, end, true, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindBool:
		return flatStageBoolArray(s, strPtr, ec, pos, end, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	case fieldKindString:
		return flatStageStringArray(s, strPtr, ec, pos, end, arrCap, validateStr, strictURC, noValidateJSON, nonASCII, elemPool, elemPoolPos)
	default:
		return 0, 0, 0, false
	}
}

func flatStageStructSlice(
	strPtr unsafe.Pointer,
	s string,
	pos, end int,
	meta *flatStructSliceMeta,
	validateStr, caseSensitive, disUnknown, strictURC, noValidateJSON bool,
	nonASCII *uint64,
	elemPool *[flatMaxElemPool]uint64,
	elemPoolPos *int,
	structMasks *[flatMaxStructMasks]uint16,
	structMaskPos *int,
) (int, int, int, int, bool) {
	pos, ec, ok := flatNextNonWSByte(strPtr, pos, end)
	if !ok {
		return 0, 0, 0, 0, false
	}
	elemStart := *elemPoolPos
	maskStart := *structMaskPos
	if ec == ']' {
		return pos, elemStart, maskStart, 0, true
	}

	sd := meta.sd
	fields := sd.fields
	nf := len(fields)
	pureASCIIKeys := sd.flatPureASCIIKeys
	count := 0
	var st flatStagedField

	for {
		if ec != '{' {
			return 0, 0, 0, 0, false
		}
		mIdx := *structMaskPos
		base := *elemPoolPos
		if mIdx >= flatMaxStructMasks || base+nf > flatMaxElemPool {
			return 0, 0, 0, 0, false
		}
		*structMaskPos = mIdx + 1
		*elemPoolPos = base + nf

		var subMask uint16
		var nc byte
		pos, nc, ok = flatNextNonWSByte(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, 0, false
		}
		if nc != '}' {
			expectedIdx := 0
			for {
				if nc != '"' {
					return 0, 0, 0, 0, false
				}
				var fi int
				if pureASCIIKeys && uint(expectedIdx) < uint(nf) {
					fe := &fields[expectedIdx]
					n := len(fe.name)
					if pos+n+2 <= end && *(*uint16)(unsafe.Add(strPtr, pos+n)) == 0x3a22 &&
						unsafe.String((*byte)(unsafe.Add(strPtr, pos)), n) == fe.name {
						pos += n + 2
						fi = expectedIdx
						expectedIdx++
						goto subKeyMatched
					}
				}
				fi, pos, ok = flatScanGroupKeySlow(strPtr, pos, end, sd, &expectedIdx, caseSensitive, validateStr, nonASCII)
				if !ok {
					return 0, 0, 0, 0, false
				}
			subKeyMatched:
				var vc byte
				if pos >= end {
					return 0, 0, 0, 0, false
				}
				vc = *(*byte)(unsafe.Add(strPtr, pos))
				if vc > ' ' {
					pos++
				} else {
					pos, vc, ok = flatSkipWS(strPtr, pos, end)
					if !ok {
						return 0, 0, 0, 0, false
					}
				}
				if fi == -1 {
					if disUnknown {
						return 0, 0, 0, 0, false
					}
					pos, ok = flatSkipValue(s, strPtr, vc, pos, end, validateStr, strictURC, noValidateJSON, nonASCII)
					if !ok {
						return 0, 0, 0, 0, false
					}
				} else if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, 0, 0, 0, false
					}
					pos += 3
				} else {
					ek := fields[fi].kind
					nextPos, _, ok := flatStageLeafValue(strPtr, s, vc, pos, end, ek, validateStr, nonASCII, &st)
					if !ok {
						return 0, 0, 0, 0, false
					}
					pos = nextPos
					elemPool[base+fi] = flatPackElemField(&st, ek)
					subMask |= uint16(1) << uint(fi)
				}

				if pos >= end {
					return 0, 0, 0, 0, false
				}
				delim := *(*byte)(unsafe.Add(strPtr, pos))
				if delim == ',' {
					pos++
					pos, nc, ok = flatNextNonWSByte(strPtr, pos, end)
					if !ok {
						return 0, 0, 0, 0, false
					}
					continue
				}
				if delim == '}' {
					pos++
					break
				}
				pos, delim, ok = flatSkipWS(strPtr, pos, end)
				if !ok {
					return 0, 0, 0, 0, false
				}
				if delim == '}' {
					break
				}
				if delim != ',' {
					return 0, 0, 0, 0, false
				}
				pos, nc, ok = flatNextNonWSByte(strPtr, pos, end)
				if !ok {
					return 0, 0, 0, 0, false
				}
			}
		}

		structMasks[mIdx] = subMask
		count++

		if pos >= end {
			return 0, 0, 0, 0, false
		}
		delim := *(*byte)(unsafe.Add(strPtr, pos))
		if delim == ',' {
			pos++
		} else if delim == ']' {
			return pos + 1, elemStart, maskStart, count, true
		} else {
			pos, delim, ok = flatSkipWS(strPtr, pos, end)
			if !ok {
				return 0, 0, 0, 0, false
			}
			if delim == ']' {
				return pos, elemStart, maskStart, count, true
			}
			if delim != ',' {
				return 0, 0, 0, 0, false
			}
		}
		pos, ec, ok = flatNextNonWSByte(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, 0, false
		}
	}
}

func flatClearSliceNull(elem unsafe.Pointer, lf *flatLeaf, plan *flatStructPlan) {
	switch lf.kind {
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
	case fieldKindSlice:
		reflect.NewAt(plan.structSlices[lf.elemKind].sliceType, elem).Elem().SetZero()
	}
}

func flatPrepareScalarSlice[T any](dst *[]T, n int) ([]T, bool) {
	if n == 0 {
		if cap(*dst) > 0 {
			*dst = (*dst)[:0]
		} else {
			*dst = []T{}
		}
		return nil, false
	}
	if cap(*dst) >= n {
		return (*dst)[:n], true
	}
	return make([]T, n), true
}

func flatCommitContainerPlanLeaves(
	vp unsafe.Pointer,
	plan *flatStructPlan,
	staged *[32]flatStagedField,
	preNullStaged *[32]flatStagedField,
	elemPool *[flatMaxElemPool]uint64,
	structMasks *[flatMaxStructMasks]uint16,
	presentMask, strMask, nullMask, nulledOnceMask, preNullMask uint32,
	s string,
	copyStr bool,
) {
	leaves := plan.leaves
	ptrMask := plan.ptrMask

	if ptrPreNull := preNullMask & ptrMask; ptrPreNull != 0 {
		flatCommitPreNullLeaves(vp, leaves, preNullStaged, ptrPreNull, s, copyStr)
	}
	for m := nullMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		lf := &leaves[fi]
		elem := unsafe.Add(vp, uintptr(lf.offset))
		if ptrMask&bit != 0 {
			*(*unsafe.Pointer)(elem) = nil
		} else {
			flatClearSliceNull(elem, lf, plan)
		}
	}
	if presentMask == 0 {
		return
	}

	var copySlab []byte
	var scalarWords []uint64
	var strSlots []string
	copyOff, scalarIdx, strIdx := 0, 0, 0

	var prepMask uint32
	if copyStr {
		prepMask = presentMask & (strMask | ptrMask)
	} else {
		prepMask = presentMask & ptrMask
	}
	if prepMask != 0 {
		var nNewScalar, nNewSmallStr, copySlabBytes int
		for m := prepMask; m != 0; m &= m - 1 {
			fi := bits.TrailingZeros32(m)
			bit := uint32(1) << uint(fi)
			lf := &leaves[fi]
			st := &staged[fi]
			elem := unsafe.Add(vp, uintptr(lf.offset))

			switch lf.kind {
			case fieldKindString:
				copySlabBytes += int(st.strLen)
			case fieldKindPtrString:
				n := int(st.strLen)
				if copyStr && n <= maxArenaStrLen {
					copySlabBytes += n
				}
				if *(*unsafe.Pointer)(elem) == nil || nulledOnceMask&bit != 0 {
					if n <= maxArenaStrLen {
						nNewSmallStr++
					}
				}
			case fieldKindArray:
				start := int(st.strOff)
				count := int(st.strLen)
				for j := 0; j < count; j++ {
					copySlabBytes += int(uint32(elemPool[start+j]))
				}
			case fieldKindSliceString:
				start := int(st.strOff)
				count := int(st.strLen)
				for j := 0; j < count; j++ {
					copySlabBytes += int(uint32(elemPool[start+j]))
				}
			case fieldKindSlice:
				meta := &plan.structSlices[lf.elemKind]
				subFields := meta.sd.fields
				nf := len(subFields)
				elemStart := int(st.strOff)
				maskStart := int(st.val)
				count := int(st.strLen)
				for j := 0; j < count; j++ {
					for sm := uint32(structMasks[maskStart+j]); sm != 0; sm &= sm - 1 {
						si := bits.TrailingZeros32(sm)
						if subFields[si].kind == fieldKindString {
							copySlabBytes += int(uint32(elemPool[elemStart+j*nf+si]))
						}
					}
				}
			default:
				if lf.kind >= fieldKindPtrI64 && lf.kind <= fieldKindPtrF32 {
					if *(*unsafe.Pointer)(elem) == nil || nulledOnceMask&bit != 0 {
						nNewScalar++
					}
				}
			}
		}
		if copySlabBytes > 0 {
			copySlab = make([]byte, copySlabBytes)
		}
		if nNewScalar > 0 {
			scalarWords = make([]uint64, nNewScalar)
		}
		if nNewSmallStr > 0 {
			strSlots = make([]string, nNewSmallStr)
		}
	}

	for m := presentMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		lf := &leaves[fi]
		elem := unsafe.Add(vp, uintptr(lf.offset))
		st := &staged[fi]

		switch {
		case lf.kind <= fieldKindF32:
			flatWriteValueScalarOrStr(elem, lf.kind, st, s, copyStr, copySlab, &copyOff)
		case lf.kind >= fieldKindPtrString && lf.kind <= fieldKindPtrF32:
			flatWritePtrLeaf(elem, lf.kind, st, bit, nulledOnceMask, s, copyStr, copySlab, &copyOff, scalarWords, &scalarIdx, strSlots, &strIdx)
		case lf.kind == fieldKindArray:
			elemKind := lf.elemKind
			elemSize := flatScalarKindSize(elemKind)
			arrLen := int(lf.arrLen)
			start := int(st.strOff)
			count := int(st.strLen)
			for i := 0; i < count; i++ {
				slot := unsafe.Add(elem, uintptr(i)*elemSize)
				est := flatUnpackElemField(elemPool[start+i], elemKind)
				flatWriteValueScalarOrStr(slot, elemKind, &est, s, copyStr, copySlab, &copyOff)
			}
			for i := count; i < arrLen; i++ {
				slot := unsafe.Add(elem, uintptr(i)*elemSize)
				flatWriteZeroScalarOrStr(slot, elemKind)
			}
		case lf.kind == fieldKindSliceInt:
			dst := (*[]int)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = int(int64(u))
			}
			*dst = out
		case lf.kind == fieldKindSliceI64:
			dst := (*[]int64)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = int64(u)
			}
			*dst = out
		case lf.kind == fieldKindSliceI32:
			dst := (*[]int32)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = int32(u)
			}
			*dst = out
		case lf.kind == fieldKindSliceUint:
			dst := (*[]uint)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = uint(u)
			}
			*dst = out
		case lf.kind == fieldKindSliceU64:
			dst := (*[]uint64)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			copy(out, elemPool[int(st.strOff):int(st.strOff)+n])
			*dst = out
		case lf.kind == fieldKindSliceU32:
			dst := (*[]uint32)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = uint32(u)
			}
			*dst = out
		case lf.kind == fieldKindSliceF64:
			dst := (*[]float64)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = math.Float64frombits(u)
			}
			*dst = out
		case lf.kind == fieldKindSliceF32:
			dst := (*[]float32)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = math.Float32frombits(uint32(u))
			}
			*dst = out
		case lf.kind == fieldKindSliceBool:
			dst := (*[]bool)(elem)
			n := int(st.strLen)
			out, ok := flatPrepareScalarSlice(dst, n)
			if !ok {
				continue
			}
			vals := elemPool[int(st.strOff) : int(st.strOff)+n]
			for i, u := range vals {
				out[i] = u != 0
			}
			*dst = out
		case lf.kind == fieldKindSliceString:
			dst := (*[]string)(elem)
			n := int(st.strLen)
			if n == 0 {
				if cap(*dst) > 0 {
					clear((*dst)[:cap(*dst)])
					*dst = (*dst)[:0]
				} else {
					*dst = []string{}
				}
				continue
			}
			var out []string
			if cap(*dst) >= n {
				oldLen := len(*dst)
				if oldLen > n {
					clear((*dst)[n:oldLen])
				}
				out = (*dst)[:n]
			} else {
				out = make([]string, n)
			}
			start := int(st.strOff)
			for i := 0; i < n; i++ {
				est := flatUnpackElemField(elemPool[start+i], fieldKindString)
				out[i] = flatResolveStr(&est, s, copyStr, copySlab, &copyOff)
			}
			*dst = out
		case lf.kind == fieldKindSlice:
			meta := &plan.structSlices[lf.elemKind]
			n := int(st.strLen)
			rv := reflect.NewAt(meta.sliceType, elem).Elem()
			if n == 0 {
				if rv.Cap() > 0 {
					oldLen := rv.Len()
					for j := 0; j < oldLen; j++ {
						rv.Index(j).SetZero()
					}
					rv.SetLen(0)
				} else {
					rv.Set(meta.emptySlice)
				}
				continue
			}
			oldLen := rv.Len()
			if rv.Cap() < n {
				if oldLen > 0 {
					for j := 0; j < oldLen; j++ {
						rv.Index(j).SetZero()
					}
					rv.SetLen(0)
				}
				rv.Grow(n)
				rv.SetLen(n)
			} else {
				if oldLen > n {
					for j := n; j < oldLen; j++ {
						rv.Index(j).SetZero()
					}
				}
				rv.SetLen(n)
				if n > oldLen {
					for j := oldLen; j < n; j++ {
						rv.Index(j).SetZero()
					}
				}
			}
			elemsPtr := rv.UnsafePointer()
			subFields := meta.sd.fields
			nf := len(subFields)
			elemStart := int(st.strOff)
			maskStart := int(st.val)
			for j := 0; j < n; j++ {
				ePtr := unsafe.Add(elemsPtr, uintptr(j)*meta.elemSize)
				for sm := uint32(structMasks[maskStart+j]); sm != 0; sm &= sm - 1 {
					si := bits.TrailingZeros32(sm)
					sf := &subFields[si]
					est := flatUnpackElemField(elemPool[elemStart+j*nf+si], sf.kind)
					flatWriteValueScalarOrStr(unsafe.Add(ePtr, uintptr(sf.offset)), sf.kind, &est, s, copyStr, copySlab, &copyOff)
				}
			}
		}
	}
}

func flatWriteZeroScalarOrStr(elem unsafe.Pointer, kind uint8) {
	switch kind {
	case fieldKindString:
		*(*string)(elem) = ""
	case fieldKindI64, fieldKindU64, fieldKindF64:
		*(*uint64)(elem) = 0
	case fieldKindI32, fieldKindU32, fieldKindF32:
		*(*uint32)(elem) = 0
	case fieldKindI16, fieldKindU16:
		*(*uint16)(elem) = 0
	case fieldKindI8, fieldKindU8:
		*(*uint8)(elem) = 0
	case fieldKindBool:
		*(*bool)(elem) = false
	}
}
