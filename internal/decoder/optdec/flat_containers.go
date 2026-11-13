//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"math/bits"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/utf8"
)

const (
	flatMaxArrayLen    = 16
	flatMaxElemPool    = 64
	flatMaxStructMasks = 16
)

type flatContainerScanState struct {
	flatScanState
	elemNullMask  uint64
	elemPool      [flatMaxElemPool]uint64
	structMasks   [flatMaxStructMasks]uint16
	elemPoolPos   int
	structMaskPos int
}

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

	var cs flatContainerScanState
	cs.opts = flatScanOpts(f)

	pos, ok = flatScanContainerPlanGroup(strPtr, s, pos, end, plan, 0, &cs)
	if !ok {
		return 0, false
	}

	if validateStr && !utf8Prechecked && cs.nonASCII != 0 {
		if !utf8.ValidateString(s[startPos:pos]) {
			return 0, false
		}
	}
	if checkTrailing && validateStr && pos < end {
		if !flatTrailingUTF8OK(s, strPtr, pos, end) {
			return 0, false
		}
	}

	if cs.presentMask == 0 && cs.nullMask == 0 {
		return pos, true
	}

	flatCommitContainerPlanLeaves(vp, plan, &cs, s, copyStr)
	return pos, true
}

func flatScanContainerPlanGroup(
	strPtr unsafe.Pointer,
	s string,
	pos, end int,
	plan *flatStructPlan,
	groupIdx int,
	cs *flatContainerScanState,
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
	keyPrefixes := grp.keyPrefixes
	slotBase := int(grp.slotStart)
	ptrMask := plan.ptrMask
	opts := cs.opts
	validateStr := opts&flatOptValidateStr != 0
	caseSensitive := opts&flatOptCaseSensitive != 0
	disUnknown := opts&flatOptDisUnknown != 0
	expectedIdx := 0

	for {
		var idx int
		if uint(expectedIdx) < uint(len(keyPrefixes)) {
			if nextPos, ok := flatMatchKeyFast(strPtr, pos, end, &keyPrefixes[expectedIdx]); ok {
				pos = nextPos
				idx = expectedIdx
				expectedIdx++
				goto keyMatched
			}
		}
		idx, pos, ok = flatScanGroupKeySlow(strPtr, pos, end, sd, &expectedIdx, caseSensitive, validateStr, &cs.nonASCII)
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
			pos, ok = flatSkipValue(strPtr, vc, pos, end, cs.opts, &cs.nonASCII)
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
				pos, ok = flatScanContainerPlanGroup(strPtr, s, pos, end, plan, int(-slot), cs)
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
						if cs.nulledOnceMask&bit == 0 && cs.presentMask&bit != 0 {
							if cs.preNullStaged == nil {
								cs.preNullStaged = flatAllocPreNullStaged()
							}
							(*cs.preNullStaged)[leafIdx] = cs.staged[leafIdx]
							cs.preNullMask |= bit
						}
						cs.nullMask |= bit
						cs.nulledOnceMask |= bit
						cs.presentMask &^= bit
						cs.strMask &^= bit
					}
					goto afterVal
				}
				nextPos, isStr, ok := flatStageLeafValue(strPtr, s, vc, pos, end, lf.kind, validateStr, &cs.nonASCII, &cs.staged[leafIdx])
				if !ok {
					return 0, false
				}
				pos = nextPos
				cs.presentMask |= bit
				if ptrMask&bit != 0 {
					cs.nullMask &^= bit
				}
				if isStr {
					cs.strMask |= bit
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
				pos, startOff, count, ok = flatStageScalarArray(strPtr, s, pos, end, lf.elemKind, int(lf.arrLen), cs)
				if !ok {
					return 0, false
				}
				cs.staged[leafIdx].strOff = uint32(startOff)
				cs.staged[leafIdx].strLen = uint32(count)
				cs.presentMask |= bit
				if lf.elemKind == fieldKindString {
					cs.strMask |= bit
				}

			case isFlatScalarSliceKind(lf.kind):
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					cs.nullMask |= bit
					cs.nulledOnceMask |= bit
					cs.presentMask &^= bit
					cs.strMask &^= bit
					goto afterVal
				}
				if vc != '[' {
					return 0, false
				}
				var startOff, count int
				pos, startOff, count, ok = flatStageScalarArray(strPtr, s, pos, end, flatSliceElemKind(lf.kind), -1, cs)
				if !ok {
					return 0, false
				}
				cs.staged[leafIdx].strOff = uint32(startOff)
				cs.staged[leafIdx].strLen = uint32(count)
				cs.presentMask |= bit
				cs.nullMask &^= bit
				if lf.kind == fieldKindSliceString {
					cs.strMask |= bit
				}

			case lf.kind == fieldKindSlice:
				if vc == 'n' {
					if pos+3 > end || s[pos:pos+3] != "ull" {
						return 0, false
					}
					pos += 3
					cs.nullMask |= bit
					cs.nulledOnceMask |= bit
					cs.presentMask &^= bit
					cs.strMask &^= bit
					goto afterVal
				}
				if vc != '[' {
					return 0, false
				}
				var elemStart, maskStart, count int
				pos, elemStart, maskStart, count, ok = flatStageStructSlice(strPtr, s, pos, end, &plan.structSlices[lf.elemKind], cs)
				if !ok {
					return 0, false
				}
				cs.staged[leafIdx].strOff = uint32(elemStart)
				cs.staged[leafIdx].strLen = uint32(count)
				cs.staged[leafIdx].val = uint64(maskStart)
				cs.presentMask |= bit
				cs.nullMask &^= bit
				cs.strMask |= bit

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

func flatSkipArrayRest(strPtr unsafe.Pointer, s string, pos, end int, cs *flatContainerScanState) (int, bool) {
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
		pos, ok = flatSkipValue(strPtr, ec, pos, end, cs.opts, &cs.nonASCII)
		if !ok {
			return 0, false
		}
	}
}

func flatStageNullElem(s string, pos, end, pIdx int, cs *flatContainerScanState) (int, bool) {
	if pos+3 > end || s[pos:pos+3] != "ull" {
		return 0, false
	}
	cs.elemPool[pIdx] = 0
	cs.elemNullMask |= uint64(1) << uint(pIdx)
	return pos + 3, true
}

func flatStageSignedArray(
	strPtr unsafe.Pointer,
	s string,
	ec byte,
	pos, end int,
	maxPos uint64,
	arrCap int,
	cs *flatContainerScanState,
) (int, int, int, bool) {
	maxNeg := maxPos + 1
	poolStart := cs.elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		if ec != 'n' {
			val, nextPos, ok := flatParseSigned(strPtr, ec, pos, end, maxPos, maxNeg)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPool[pIdx] = val
			pos = nextPos
		} else {
			var ok bool
			pos, ok = flatStageNullElem(s, pos, end, pIdx, cs)
			if !ok {
				return 0, 0, 0, false
			}
		}
		pIdx++
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			var ok bool
			pos, ok = flatSkipArrayRest(strPtr, s, pos, end, cs)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPoolPos = pIdx
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
			cs.elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done, ok bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			cs.elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageUnsignedArray(
	strPtr unsafe.Pointer,
	s string,
	ec byte,
	pos, end int,
	maxU uint64,
	arrCap int,
	cs *flatContainerScanState,
) (int, int, int, bool) {
	poolStart := cs.elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		if ec != 'n' {
			val, nextPos, ok := flatParseUnsigned(strPtr, ec, pos, end, maxU)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPool[pIdx] = val
			pos = nextPos
		} else {
			var ok bool
			pos, ok = flatStageNullElem(s, pos, end, pIdx, cs)
			if !ok {
				return 0, 0, 0, false
			}
		}
		pIdx++
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			var ok bool
			pos, ok = flatSkipArrayRest(strPtr, s, pos, end, cs)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPoolPos = pIdx
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
			cs.elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done, ok bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			cs.elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageFloatArray(
	strPtr unsafe.Pointer,
	s string,
	ec byte,
	pos, end int,
	is32 bool,
	arrCap int,
	cs *flatContainerScanState,
) (int, int, int, bool) {
	poolStart := cs.elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		if ec != 'n' {
			fv, nextPos, ok := flatParseFloat(strPtr, ec, pos, end)
			if !ok {
				return 0, 0, 0, false
			}
			if is32 {
				fv32 := float32(fv)
				if math.IsInf(float64(fv32), 0) && !math.IsInf(fv, 0) {
					return 0, 0, 0, false
				}
				cs.elemPool[pIdx] = uint64(math.Float32bits(fv32))
			} else {
				cs.elemPool[pIdx] = math.Float64bits(fv)
			}
			pos = nextPos
		} else {
			var ok bool
			pos, ok = flatStageNullElem(s, pos, end, pIdx, cs)
			if !ok {
				return 0, 0, 0, false
			}
		}
		pIdx++
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			var ok bool
			pos, ok = flatSkipArrayRest(strPtr, s, pos, end, cs)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPoolPos = pIdx
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
			cs.elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done, ok bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			cs.elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageBoolArray(
	strPtr unsafe.Pointer,
	s string,
	ec byte,
	pos, end int,
	arrCap int,
	cs *flatContainerScanState,
) (int, int, int, bool) {
	poolStart := cs.elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		if ec == 't' {
			if pos+3 > end || s[pos:pos+3] != "rue" {
				return 0, 0, 0, false
			}
			cs.elemPool[pIdx] = 1
			pos += 3
		} else if ec == 'f' {
			if pos+4 > end || s[pos:pos+4] != "alse" {
				return 0, 0, 0, false
			}
			cs.elemPool[pIdx] = 0
			pos += 4
		} else if ec == 'n' {
			var ok bool
			pos, ok = flatStageNullElem(s, pos, end, pIdx, cs)
			if !ok {
				return 0, 0, 0, false
			}
		} else {
			return 0, 0, 0, false
		}
		pIdx++
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			var ok bool
			pos, ok = flatSkipArrayRest(strPtr, s, pos, end, cs)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPoolPos = pIdx
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
			cs.elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done, ok bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			cs.elemPoolPos = pIdx
			return pos, poolStart, pIdx - poolStart, true
		}
	}
}

func flatStageStringArray(
	strPtr unsafe.Pointer,
	s string,
	ec byte,
	pos, end int,
	arrCap int,
	cs *flatContainerScanState,
) (int, int, int, bool) {
	validateStr := cs.opts&flatOptValidateStr != 0
	poolStart := cs.elemPoolPos
	pIdx := poolStart
	for {
		if pIdx >= flatMaxElemPool {
			return 0, 0, 0, false
		}
		if ec == '"' {
			strStart := pos
			strEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, &cs.nonASCII)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPool[pIdx] = (uint64(strStart) << 32) | uint64(strEnd-strStart)
			pos = strEnd + 1
		} else if ec == 'n' {
			var ok bool
			pos, ok = flatStageNullElem(s, pos, end, pIdx, cs)
			if !ok {
				return 0, 0, 0, false
			}
		} else {
			return 0, 0, 0, false
		}
		pIdx++
		if arrCap >= 0 && pIdx-poolStart == arrCap {
			var ok bool
			pos, ok = flatSkipArrayRest(strPtr, s, pos, end, cs)
			if !ok {
				return 0, 0, 0, false
			}
			cs.elemPoolPos = pIdx
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
			cs.elemPoolPos = pIdx
			return pos + 1, poolStart, pIdx - poolStart, true
		}
		var done, ok bool
		pos, ec, done, ok = flatArrayNextElemSlow(strPtr, pos, end)
		if !ok {
			return 0, 0, 0, false
		}
		if done {
			cs.elemPoolPos = pIdx
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
	cs *flatContainerScanState,
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
		return pos, cs.elemPoolPos, 0, true
	}
	if arrCap == 0 {
		return 0, 0, 0, false
	}

	switch elemKind {
	case fieldKindI64:
		return flatStageSignedArray(strPtr, s, ec, pos, end, math.MaxInt64, arrCap, cs)
	case fieldKindI32:
		return flatStageSignedArray(strPtr, s, ec, pos, end, math.MaxInt32, arrCap, cs)
	case fieldKindI16:
		return flatStageSignedArray(strPtr, s, ec, pos, end, math.MaxInt16, arrCap, cs)
	case fieldKindI8:
		return flatStageSignedArray(strPtr, s, ec, pos, end, math.MaxInt8, arrCap, cs)
	case fieldKindU64:
		return flatStageUnsignedArray(strPtr, s, ec, pos, end, math.MaxUint64, arrCap, cs)
	case fieldKindU32:
		return flatStageUnsignedArray(strPtr, s, ec, pos, end, math.MaxUint32, arrCap, cs)
	case fieldKindU16:
		return flatStageUnsignedArray(strPtr, s, ec, pos, end, math.MaxUint16, arrCap, cs)
	case fieldKindU8:
		return flatStageUnsignedArray(strPtr, s, ec, pos, end, math.MaxUint8, arrCap, cs)
	case fieldKindF64:
		return flatStageFloatArray(strPtr, s, ec, pos, end, false, arrCap, cs)
	case fieldKindF32:
		return flatStageFloatArray(strPtr, s, ec, pos, end, true, arrCap, cs)
	case fieldKindBool:
		return flatStageBoolArray(strPtr, s, ec, pos, end, arrCap, cs)
	case fieldKindString:
		return flatStageStringArray(strPtr, s, ec, pos, end, arrCap, cs)
	default:
		return 0, 0, 0, false
	}
}

func flatStageStructSlice(
	strPtr unsafe.Pointer,
	s string,
	pos, end int,
	meta *flatStructSliceMeta,
	cs *flatContainerScanState,
) (int, int, int, int, bool) {
	pos, ec, ok := flatNextNonWSByte(strPtr, pos, end)
	if !ok {
		return 0, 0, 0, 0, false
	}
	elemStart := cs.elemPoolPos
	maskStart := cs.structMaskPos
	if ec == ']' {
		return pos, elemStart, maskStart, 0, true
	}

	sd := meta.sd
	fields := sd.fields
	keyPrefixes := meta.keyPrefixes
	nf := len(fields)
	opts := cs.opts
	validateStr := opts&flatOptValidateStr != 0
	caseSensitive := opts&flatOptCaseSensitive != 0
	disUnknown := opts&flatOptDisUnknown != 0
	count := 0
	var st flatStagedField

	for {
		if ec != '{' {
			return 0, 0, 0, 0, false
		}
		mIdx := cs.structMaskPos
		base := cs.elemPoolPos
		if mIdx >= flatMaxStructMasks || base+nf > flatMaxElemPool {
			return 0, 0, 0, 0, false
		}
		cs.structMaskPos = mIdx + 1
		cs.elemPoolPos = base + nf

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
				if uint(expectedIdx) < uint(len(keyPrefixes)) {
					if nextPos, matched := flatMatchKeyFast(strPtr, pos, end, &keyPrefixes[expectedIdx]); matched {
						pos = nextPos
						fi = expectedIdx
						expectedIdx++
						goto subKeyMatched
					}
				}
				fi, pos, ok = flatScanGroupKeySlow(strPtr, pos, end, sd, &expectedIdx, caseSensitive, validateStr, &cs.nonASCII)
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
					pos, ok = flatSkipValue(strPtr, vc, pos, end, cs.opts, &cs.nonASCII)
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
					nextPos, _, ok := flatStageLeafValue(strPtr, s, vc, pos, end, ek, validateStr, &cs.nonASCII, &st)
					if !ok {
						return 0, 0, 0, 0, false
					}
					pos = nextPos
					cs.elemPool[base+fi] = flatPackElemField(&st, ek)
					subMask |= uint16(1) << uint(fi)
				}

				if pos >= end {
					return 0, 0, 0, 0, false
				}
				delim := *(*byte)(unsafe.Add(strPtr, pos))
				if delim == ',' {
					pos++
					if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == '"' {
						pos++
						nc = '"'
						continue
					}
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

		cs.structMasks[mIdx] = subMask
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

func flatSliceElemNullBits(elemNullMask uint64, start, n int) uint64 {
	if n <= 0 {
		return 0
	}
	return (elemNullMask >> uint(start)) & (^uint64(0) >> uint(64-n))
}

func flatPrepareCommitSlice[T any](dst *[]T, n int, nulled bool) []T {
	if !nulled && n > 0 {
		if old := *dst; cap(old) >= n {
			out := old[:n]
			*dst = out
			return out
		}
	}
	if nulled {
		*dst = nil
	}
	if n == 0 {
		*dst = []T{}
		return nil
	}
	old := *dst
	s := make([]T, n)
	if len(old) > 0 {
		copy(s, old)
	}
	*dst = s
	return s
}

func flatSliceNullBitsIfAny(elemNullMask uint64, start, n int) uint64 {
	if elemNullMask == 0 || n <= 0 {
		return 0
	}
	return (elemNullMask >> uint(start)) & (^uint64(0) >> uint(64-n))
}

func flatCommitContainerPlanLeaves(
	vp unsafe.Pointer,
	plan *flatStructPlan,
	cs *flatContainerScanState,
	s string,
	copyStr bool,
) {
	leaves := plan.leaves
	ptrMask := plan.ptrMask
	staged := &cs.staged
	preNullStaged := cs.preNullStaged
	elemPool := &cs.elemPool
	structMasks := &cs.structMasks
	presentMask := cs.presentMask
	strMask := cs.strMask
	nullMask := cs.nullMask
	nulledOnceMask := cs.nulledOnceMask
	preNullMask := cs.preNullMask
	elemNullMask := cs.elemNullMask

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
				if lf.elemKind == fieldKindString {
					start := int(st.strOff)
					count := int(st.strLen)
					nullBits := flatSliceElemNullBits(elemNullMask, start, count)
					for j := 0; j < count; j++ {
						if nullBits&(uint64(1)<<uint(j)) == 0 {
							copySlabBytes += int(uint32(elemPool[start+j]))
						}
					}
				}
			case fieldKindSliceString:
				start := int(st.strOff)
				count := int(st.strLen)
				nullBits := flatSliceElemNullBits(elemNullMask, start, count)
				for j := 0; j < count; j++ {
					if nullBits&(uint64(1)<<uint(j)) == 0 {
						copySlabBytes += int(uint32(elemPool[start+j]))
					}
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
			nullBits := flatSliceElemNullBits(elemNullMask, start, count)
			for i := 0; i < count; i++ {
				if nullBits&(uint64(1)<<uint(i)) != 0 {
					continue
				}
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
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = int(int64(u))
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = int(int64(u))
						}
					}
				}
			}
		case lf.kind == fieldKindSliceI64:
			dst := (*[]int64)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = int64(u)
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = int64(u)
						}
					}
				}
			}
		case lf.kind == fieldKindSliceI32:
			dst := (*[]int32)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = int32(u)
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = int32(u)
						}
					}
				}
			}
		case lf.kind == fieldKindSliceUint:
			dst := (*[]uint)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = uint(u)
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = uint(u)
						}
					}
				}
			}
		case lf.kind == fieldKindSliceU64:
			dst := (*[]uint64)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					copy(out, vals)
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = u
						}
					}
				}
			}
		case lf.kind == fieldKindSliceU32:
			dst := (*[]uint32)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = uint32(u)
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = uint32(u)
						}
					}
				}
			}
		case lf.kind == fieldKindSliceF64:
			dst := (*[]float64)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = math.Float64frombits(u)
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = math.Float64frombits(u)
						}
					}
				}
			}
		case lf.kind == fieldKindSliceF32:
			dst := (*[]float32)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = math.Float32frombits(uint32(u))
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = math.Float32frombits(uint32(u))
						}
					}
				}
			}
		case lf.kind == fieldKindSliceBool:
			dst := (*[]bool)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				vals := elemPool[start : start+n]
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i, u := range vals {
						out[i] = u != 0
					}
				} else {
					for i, u := range vals {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							out[i] = u != 0
						}
					}
				}
			}
		case lf.kind == fieldKindSliceString:
			dst := (*[]string)(elem)
			n := int(st.strLen)
			start := int(st.strOff)
			if out := flatPrepareCommitSlice(dst, n, nulledOnceMask&bit != 0); out != nil {
				if nullBits := flatSliceNullBitsIfAny(elemNullMask, start, n); nullBits == 0 {
					for i := 0; i < n; i++ {
						est := flatUnpackElemField(elemPool[start+i], fieldKindString)
						out[i] = flatResolveStr(&est, s, copyStr, copySlab, &copyOff)
					}
				} else {
					for i := 0; i < n; i++ {
						if nullBits&(uint64(1)<<uint(i)) == 0 {
							est := flatUnpackElemField(elemPool[start+i], fieldKindString)
							out[i] = flatResolveStr(&est, s, copyStr, copySlab, &copyOff)
						}
					}
				}
			}
		case lf.kind == fieldKindSlice:
			meta := &plan.structSlices[lf.elemKind]
			n := int(st.strLen)
			rv := reflect.NewAt(meta.sliceType, elem).Elem()
			if nulledOnceMask&bit != 0 {
				rv.SetZero()
			}
			if n == 0 {
				rv.Set(meta.emptySlice)
				continue
			}
			if rv.Cap() < n {
				rv.Grow(n - rv.Len())
			}
			rv.SetLen(n)
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
