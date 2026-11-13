//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math"
	"math/bits"
	"reflect"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/utf8"
)

type flatStagedField struct {
	val    uint64
	strOff uint32
	strLen uint32
}

type flatLeaf struct {
	offset   uint32
	kind     uint8
	elemKind uint8
	arrLen   uint8
}

type flatStructSliceMeta struct {
	sd          *structDecoder
	keyPrefixes []flatKeyPrefix
	sliceType   reflect.Type
	elemSize    uintptr
	emptySlice  reflect.Value
}

type flatGroup struct {
	sd            *structDecoder
	keyPrefixes   []flatKeyPrefix
	slotStart     uint16
	pureASCIIKeys bool
}

type flatStructPlan struct {
	leaves        []flatLeaf
	groups        []flatGroup
	slots         []int16 // >= 0: leaf index (0..31); < 0: -childGroupIdx into groups
	structSlices  []flatStructSliceMeta
	ptrMask       uint32
	hasContainers bool
}

const (
	flatOptValidateStr uint8 = 1 << iota
	flatOptCaseSensitive
	flatOptDisUnknown
	flatOptStrictURC
	flatOptNoValidateJSON
)

type flatScanState struct {
	staged         [32]flatStagedField
	preNullStaged  *[32]flatStagedField
	nonASCII       uint64
	presentMask    uint32
	strMask        uint32
	nullMask       uint32
	nulledOnceMask uint32
	preNullMask    uint32
	opts           uint8
}

func flatScanOpts(f uint64) uint8 {
	var opts uint8
	if f&(1<<_F_validate_string) != 0 {
		opts |= flatOptValidateStr
	}
	if f&uint64(consts.OptionCaseSensitive) != 0 {
		opts |= flatOptCaseSensitive
	}
	if Options(f)&OptionDisableUnknown != 0 {
		opts |= flatOptDisUnknown
	}
	if f&(1<<_F_disable_urc) != 0 {
		opts |= flatOptStrictURC
	}
	if f&(1<<_F_no_validate_json) != 0 {
		opts |= flatOptNoValidateJSON
	}
	return opts
}

func buildFlatKeyPrefixes(fields []fieldEntry) []flatKeyPrefix {
	out := make([]flatKeyPrefix, len(fields))
	for i := range fields {
		name := fields[i].name
		n := len(name)
		if n == 0 || n > 14 {
			continue
		}
		var buf [16]byte
		copy(buf[:], name)
		buf[n] = '"'
		buf[n+1] = ':'
		n2 := n + 2
		rem := n2
		if rem > 8 {
			rem -= 8
		}
		var maskBuf [8]byte
		for j := 0; j < rem; j++ {
			maskBuf[j] = 0xff
		}
		out[i] = flatKeyPrefix{
			w0:   *(*uint64)(unsafe.Pointer(&buf[0])),
			w1:   *(*uint64)(unsafe.Pointer(&buf[8])),
			mask: *(*uint64)(unsafe.Pointer(&maskBuf[0])),
			n2:   uint8(n2),
		}
	}
	return out
}

func flatMatchKeyFast(strPtr unsafe.Pointer, pos, end int, kp *flatKeyPrefix) (int, bool) {
	n2 := int(kp.n2)
	if n2 != 0 {
		if n2 <= 8 {
			if pos+8 <= end && ((*(*uint64)(unsafe.Add(strPtr, pos))^kp.w0)&kp.mask) == 0 {
				return pos + n2, true
			}
		} else if pos+16 <= end && *(*uint64)(unsafe.Add(strPtr, pos)) == kp.w0 &&
			((*(*uint64)(unsafe.Add(strPtr, pos+8))^kp.w1)&kp.mask) == 0 {
			return pos + n2, true
		}
	}
	return 0, false
}

const (
	flatMaxInputLen      int    = 1 << 20
	flatSingleDocMaxLen  int    = 4096
	flatBackoffThreshold uint32 = 8
	flatMaxNestedDepth   int    = 2
	// UseNumber and UseInt64 only affect untyped interface{} fields, which flat structs never contain.
	flatUnsupportedOpts uint64 = 0
)

func isFlatLeafKind(kind uint8) (ok bool, isPtr bool) {
	switch kind {
	case fieldKindString,
		fieldKindI64, fieldKindI32, fieldKindI16, fieldKindI8,
		fieldKindU64, fieldKindU32, fieldKindU16, fieldKindU8,
		fieldKindBool, fieldKindF64, fieldKindF32:
		return true, false
	case fieldKindPtrString,
		fieldKindPtrI64, fieldKindPtrInt, fieldKindPtrI32, fieldKindPtrI16, fieldKindPtrI8,
		fieldKindPtrU64, fieldKindPtrUint, fieldKindPtrU32, fieldKindPtrU16, fieldKindPtrU8,
		fieldKindPtrBool, fieldKindPtrF64, fieldKindPtrF32:
		return true, true
	default:
		return false, false
	}
}

func isPureASCIIFieldName(name string) bool {
	for j := 0; j < len(name); j++ {
		b := name[j]
		if b < 0x20 || b >= 0x80 || b == '"' || b == '\\' {
			return false
		}
	}
	return true
}

func (sd *structDecoder) initFlatDirect() {
	entries := sd.fields
	if len(sd.wideOffsets) != 0 || len(entries) == 0 {
		return
	}

	hasNestedOrContainer := false
	if len(entries) <= 32 {
		allLeaves := true
		pureASCII := true
		var ptrMask uint32
		for i := range entries {
			ok, isPtr := isFlatLeafKind(entries[i].kind)
			if !ok {
				allLeaves = false
				if entries[i].kind == fieldKindStruct || isFlatContainerField(&entries[i]) {
					hasNestedOrContainer = true
				}
				break
			}
			if isPtr {
				ptrMask |= uint32(1) << uint(i)
			}
			if pureASCII && !isPureASCIIFieldName(entries[i].name) {
				pureASCII = false
			}
		}
		if allLeaves {
			sd.flatDirect = true
			sd.flatPureASCIIKeys = pureASCII
			sd.flatPtrMask = ptrMask
			if pureASCII {
				sd.flatKeyPrefixes = buildFlatKeyPrefixes(entries)
			}
			return
		}
	} else {
		for i := range entries {
			if entries[i].kind == fieldKindStruct || isFlatContainerField(&entries[i]) {
				hasNestedOrContainer = true
				break
			}
		}
	}

	if !hasNestedOrContainer {
		return
	}

	plan := &flatStructPlan{}
	if !buildFlatPlanGroup(plan, sd, 0, 0) || len(plan.leaves) == 0 || len(plan.leaves) > 32 {
		return
	}
	sd.flatDirect = true
	sd.flatPureASCIIKeys = plan.groups[0].pureASCIIKeys
	sd.flatPtrMask = plan.ptrMask
	sd.flatPlan = plan
}

func buildFlatPlanGroup(plan *flatStructPlan, sd *structDecoder, baseOffset uint64, depth int) bool {
	if sd == nil || len(sd.wideOffsets) != 0 || len(sd.fields) == 0 || len(plan.groups) >= 32 {
		return false
	}
	groupIdx := len(plan.groups)
	slotStart := len(plan.slots)
	nfields := len(sd.fields)
	plan.groups = append(plan.groups, flatGroup{
		sd:        sd,
		slotStart: uint16(slotStart),
	})
	plan.slots = append(plan.slots, make([]int16, nfields)...)

	pureASCII := true
	for i := 0; i < nfields; i++ {
		fe := &sd.fields[i]
		if pureASCII && !isPureASCIIFieldName(fe.name) {
			pureASCII = false
		}
		totalOff := baseOffset + uint64(fe.offset)
		if totalOff > math.MaxUint32 {
			return false
		}
		if ok, isPtr := isFlatLeafKind(fe.kind); ok {
			leafIdx := len(plan.leaves)
			if leafIdx >= 32 {
				return false
			}
			plan.leaves = append(plan.leaves, flatLeaf{
				offset: uint32(totalOff),
				kind:   fe.kind,
			})
			if isPtr {
				plan.ptrMask |= uint32(1) << uint(leafIdx)
			}
			plan.slots[slotStart+i] = int16(leafIdx)
			continue
		}
		if fe.kind == fieldKindArray {
			d := (*arrayDecoder)(fe.subDec)
			if d != nil && d.len > 0 && d.len <= flatMaxArrayLen {
				if elemKind, ok := flatScalarElementKind(d.elemDec); ok && d.size == flatScalarKindSize(elemKind) {
					leafIdx := len(plan.leaves)
					if leafIdx >= 32 {
						return false
					}
					plan.leaves = append(plan.leaves, flatLeaf{
						offset:   uint32(totalOff),
						kind:     fieldKindArray,
						elemKind: elemKind,
						arrLen:   uint8(d.len),
					})
					plan.hasContainers = true
					plan.slots[slotStart+i] = int16(leafIdx)
					continue
				}
			}
			return false
		}
		if isFlatScalarSliceKind(fe.kind) {
			leafIdx := len(plan.leaves)
			if leafIdx >= 32 {
				return false
			}
			plan.leaves = append(plan.leaves, flatLeaf{
				offset: uint32(totalOff),
				kind:   fe.kind,
			})
			plan.hasContainers = true
			plan.slots[slotStart+i] = int16(leafIdx)
			continue
		}
		if fe.kind == fieldKindSlice {
			d := (*sliceDecoder)(fe.subDec)
			if d != nil && d.structDec != nil &&
				d.structDec.flatDirect && d.structDec.flatPlan == nil &&
				d.structDec.flatPtrMask == 0 && len(d.structDec.fields) > 0 &&
				len(d.structDec.fields) <= flatMaxArrayLen && len(plan.structSlices) < 256 {
				leafIdx := len(plan.leaves)
				if leafIdx >= 32 {
					return false
				}
				metaIdx := len(plan.structSlices)
				plan.structSlices = append(plan.structSlices, flatStructSliceMeta{
					sd:          d.structDec,
					keyPrefixes: d.structDec.flatKeyPrefixes,
					sliceType:   d.typ,
					elemSize:    d.size,
					emptySlice:  d.emptySlice,
				})
				plan.leaves = append(plan.leaves, flatLeaf{
					offset:   uint32(totalOff),
					kind:     fieldKindSlice,
					elemKind: uint8(metaIdx),
					arrLen:   uint8(len(d.structDec.fields)),
				})
				plan.hasContainers = true
				plan.slots[slotStart+i] = int16(leafIdx)
				continue
			}
			return false
		}
		if fe.kind == fieldKindStruct && depth < flatMaxNestedDepth {
			subSD := (*structDecoder)(fe.subDec)
			if subSD == nil || subSD == sd {
				return false
			}
			childGroupIdx := len(plan.groups)
			if !buildFlatPlanGroup(plan, subSD, totalOff, depth+1) {
				return false
			}
			plan.slots[slotStart+i] = -int16(childGroupIdx)
			continue
		}
		return false
	}
	plan.groups[groupIdx].pureASCIIKeys = pureASCII
	if pureASCII {
		plan.groups[groupIdx].keyPrefixes = buildFlatKeyPrefixes(sd.fields)
	}
	return true
}

// tryDecodeFlatStruct scans s[startPos:] directly into a flat or shallow-nested struct (1..32 leaf fields)
// without borrowing a pooled Parser, copying input into p.padded, or materializing a []node tape.
// Field writes are staged on the stack and committed atomically only after matching the root closing '}',
// so any bailout (escape sequence, type mismatch, overflow, invalid UTF-8, or syntax error) leaves vp
// completely untouched for the standard decoder fallback.
func tryDecodeFlatStruct(s string, startPos int, f uint64, vp unsafe.Pointer, sd *structDecoder, utf8Prechecked, checkTrailing bool) (int, bool) {
	end := len(s)
	maxLen := flatMaxInputLen
	if checkTrailing {
		maxLen = flatSingleDocMaxLen
	}
	if startPos < 0 || startPos >= end || end-startPos > maxLen || end > math.MaxUint32 || f&flatUnsupportedOpts != 0 {
		return 0, false
	}
	streak := atomic.LoadUint32(&sd.flatFailStreak)
	if streak >= flatBackoffThreshold {
		if (atomic.AddUint32(&sd.flatProbeTick, 1) & 255) != 0 {
			return 0, false
		}
	}
	var nextPos int
	var ok bool
	if sd.flatPlan != nil {
		if sd.flatPlan.hasContainers {
			nextPos, ok = tryDecodeFlatContainerPlanBody(s, startPos, end, f, vp, sd.flatPlan, utf8Prechecked, checkTrailing)
		} else {
			nextPos, ok = tryDecodeFlatPlanBody(s, startPos, end, f, vp, sd.flatPlan, utf8Prechecked, checkTrailing)
		}
	} else {
		nextPos, ok = tryDecodeFlatStructBody(s, startPos, end, f, vp, sd, utf8Prechecked, checkTrailing)
	}
	if ok {
		if streak != 0 {
			atomic.StoreUint32(&sd.flatFailStreak, 0)
		}
		return nextPos, true
	}
	if streak < 16 {
		atomic.AddUint32(&sd.flatFailStreak, 1)
	}
	return 0, false
}

func tryDecodeFlatStructBody(s string, startPos, end int, f uint64, vp unsafe.Pointer, sd *structDecoder, utf8Prechecked, checkTrailing bool) (int, bool) {
	strPtr := unsafe.Pointer(unsafe.StringData(s))
	validateStr := f&(1<<_F_validate_string) != 0
	copyStr := f&(1<<_F_copy_string) != 0
	caseSensitive := f&uint64(consts.OptionCaseSensitive) != 0
	disUnknown := Options(f)&OptionDisableUnknown != 0
	ptrMask := sd.flatPtrMask

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

	if pos >= end {
		return 0, false
	}
	c = *(*byte)(unsafe.Add(strPtr, pos))
	if c == '}' {
		pos++
		if checkTrailing && validateStr && pos < end {
			if !flatTrailingUTF8OK(s, strPtr, pos, end) {
				return 0, false
			}
		}
		return pos, true
	}
	if c == '"' {
		pos++
	} else {
		pos, c, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if c == '}' {
			if checkTrailing && validateStr && pos < end {
				if !flatTrailingUTF8OK(s, strPtr, pos, end) {
					return 0, false
				}
			}
			return pos, true
		}
		if c != '"' {
			return 0, false
		}
	}

	var staged [32]flatStagedField
	var preNullStaged *[32]flatStagedField
	var presentMask uint32
	var strMask uint32
	var nullMask uint32
	var nulledOnceMask uint32
	var preNullMask uint32
	var nonASCII uint64

	fields := sd.fields
	nfields := len(fields)
	keyPrefixes := sd.flatKeyPrefixes
	expectedIdx := 0

	for {
		var idx int
		if uint(expectedIdx) < uint(len(keyPrefixes)) {
			if nextPos, ok := flatMatchKeyFast(strPtr, pos, end, &keyPrefixes[expectedIdx]); ok {
				pos = nextPos
				idx = expectedIdx
				expectedIdx++
				goto parseFieldValue
			}
		}

		{
			keyStart := pos
			keyEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, &nonASCII)
			if !ok {
				return 0, false
			}
			key := unsafe.String((*byte)(unsafe.Add(strPtr, keyStart)), keyEnd-keyStart)
			pos = keyEnd + 1
			if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == ':' {
				pos++
			} else {
				var colon byte
				pos, colon, ok = flatSkipWS(strPtr, pos, end)
				if !ok || colon != ':' {
					return 0, false
				}
			}

			if uint(expectedIdx) < uint(nfields) && fields[expectedIdx].name == key {
				idx = expectedIdx
				expectedIdx++
			} else if caseSensitive {
				idx = sd.fieldMap.Get(key, true)
				if idx != -1 {
					expectedIdx = idx + 1
				}
			} else {
				idx = sd.fieldMap.GetInsensitive(key)
				if idx != -1 {
					expectedIdx = idx + 1
				}
			}

			if idx == -1 {
				if disUnknown {
					return 0, false
				}
				var vc byte
				if pos < end {
					vc = *(*byte)(unsafe.Add(strPtr, pos))
					if vc > ' ' {
						pos++
					} else {
						pos, vc, ok = flatSkipWS(strPtr, pos, end)
						if !ok {
							return 0, false
						}
					}
				} else {
					return 0, false
				}
				pos, ok = flatSkipValue(strPtr, vc, pos, end, flatScanOpts(f), &nonASCII)
				if !ok {
					return 0, false
				}
				goto afterFieldValue
			}
		}

	parseFieldValue:
		{
			if pos >= end {
				return 0, false
			}
			vc := *(*byte)(unsafe.Add(strPtr, pos))
			if vc > ' ' {
				pos++
			} else if vc == ' ' && pos+1 < end && *(*byte)(unsafe.Add(strPtr, pos+1)) > ' ' {
				vc = *(*byte)(unsafe.Add(strPtr, pos+1))
				pos += 2
			} else {
				pos, vc, ok = flatSkipWS(strPtr, pos, end)
				if !ok {
					return 0, false
				}
			}

			bit := uint32(1) << uint(idx)
			if vc == 'n' {
				if pos+3 > end || s[pos:pos+3] != "ull" {
					return 0, false
				}
				pos += 3
				if ptrMask&bit != 0 {
					if nulledOnceMask&bit == 0 && presentMask&bit != 0 {
						if preNullStaged == nil {
							preNullStaged = flatAllocPreNullStaged()
						}
						preNullStaged[idx] = staged[idx]
						preNullMask |= bit
					}
					nullMask |= bit
					nulledOnceMask |= bit
					presentMask &^= bit
					strMask &^= bit
				}
				goto afterFieldValue
			}

			nextPos, isStr, ok := flatStageLeafValue(strPtr, s, vc, pos, end, fields[idx].kind, validateStr, &nonASCII, &staged[idx])
			if !ok {
				return 0, false
			}
			pos = nextPos
			presentMask |= bit
			if ptrMask&bit != 0 {
				nullMask &^= bit
			}
			if isStr {
				strMask |= bit
			}
		}

	afterFieldValue:
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
			pos++
			break
		}
		pos, delim, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if delim == '}' {
			break
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

	if ptrMask == 0 {
		var copySlab []byte
		copyOff := 0
		if copyStr && strMask != 0 {
			totalBytes := 0
			for m := strMask; m != 0; m &= m - 1 {
				fi := bits.TrailingZeros32(m)
				totalBytes += int(staged[fi].strLen)
			}
			if totalBytes > 0 {
				copySlab = make([]byte, totalBytes)
			}
		}
		for m := presentMask; m != 0; m &= m - 1 {
			fi := bits.TrailingZeros32(m)
			fe := &fields[fi]
			flatWriteValueScalarOrStr(unsafe.Add(vp, uintptr(fe.offset)), fe.kind, &staged[fi], s, copyStr, copySlab, &copyOff)
		}
		return pos, true
	}

	flatCommitWithPointersRoot(vp, fields, &staged, preNullStaged, presentMask, strMask, nullMask, nulledOnceMask, preNullMask, s, copyStr)
	return pos, true
}

func tryDecodeFlatPlanBody(s string, startPos, end int, f uint64, vp unsafe.Pointer, plan *flatStructPlan, utf8Prechecked, checkTrailing bool) (int, bool) {
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

	var st flatScanState
	st.opts = flatScanOpts(f)

	pos, ok = flatScanPlanGroup(strPtr, s, pos, end, plan, 0, &st)
	if !ok {
		return 0, false
	}

	if validateStr && !utf8Prechecked && st.nonASCII != 0 {
		if !utf8.ValidateString(s[startPos:pos]) {
			return 0, false
		}
	}
	if checkTrailing && validateStr && pos < end {
		if !flatTrailingUTF8OK(s, strPtr, pos, end) {
			return 0, false
		}
	}

	if st.presentMask == 0 && st.nullMask == 0 {
		return pos, true
	}

	flatCommitPlanLeaves(vp, plan.leaves, plan.ptrMask, &st.staged, st.preNullStaged, st.presentMask, st.strMask, st.nullMask, st.nulledOnceMask, st.preNullMask, s, copyStr)
	return pos, true
}

func flatScanPlanGroup(
	strPtr unsafe.Pointer,
	s string,
	pos, end int,
	plan *flatStructPlan,
	groupIdx int,
	st *flatScanState,
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
	keyPrefixes := grp.keyPrefixes
	slotBase := int(grp.slotStart)
	ptrMask := plan.ptrMask
	opts := st.opts
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
				goto parseVal
			}
		}

		{
			keyStart := pos
			keyEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, &st.nonASCII)
			if !ok {
				return 0, false
			}
			key := unsafe.String((*byte)(unsafe.Add(strPtr, keyStart)), keyEnd-keyStart)
			pos = keyEnd + 1
			if pos < end && *(*byte)(unsafe.Add(strPtr, pos)) == ':' {
				pos++
			} else {
				var colon byte
				pos, colon, ok = flatSkipWS(strPtr, pos, end)
				if !ok || colon != ':' {
					return 0, false
				}
			}

			if uint(expectedIdx) < uint(nfields) && fields[expectedIdx].name == key {
				idx = expectedIdx
				expectedIdx++
			} else if caseSensitive {
				idx = sd.fieldMap.Get(key, true)
				if idx != -1 {
					expectedIdx = idx + 1
				}
			} else {
				idx = sd.fieldMap.GetInsensitive(key)
				if idx != -1 {
					expectedIdx = idx + 1
				}
			}

			if idx == -1 {
				if disUnknown {
					return 0, false
				}
				var vc byte
				if pos < end {
					vc = *(*byte)(unsafe.Add(strPtr, pos))
					if vc > ' ' {
						pos++
					} else {
						pos, vc, ok = flatSkipWS(strPtr, pos, end)
						if !ok {
							return 0, false
						}
					}
				} else {
					return 0, false
				}
				pos, ok = flatSkipValue(strPtr, vc, pos, end, st.opts, &st.nonASCII)
				if !ok {
					return 0, false
				}
				goto afterVal
			}
		}

	parseVal:
		{
			if pos >= end {
				return 0, false
			}
			vc := *(*byte)(unsafe.Add(strPtr, pos))
			if vc > ' ' {
				pos++
			} else if vc == ' ' && pos+1 < end && *(*byte)(unsafe.Add(strPtr, pos+1)) > ' ' {
				vc = *(*byte)(unsafe.Add(strPtr, pos+1))
				pos += 2
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
				pos, ok = flatScanPlanGroup(strPtr, s, pos, end, plan, int(-slot), st)
				if !ok {
					return 0, false
				}
				goto afterVal
			}

			leafIdx := int(slot)
			bit := uint32(1) << uint(leafIdx)
			if vc == 'n' {
				if pos+3 > end || s[pos:pos+3] != "ull" {
					return 0, false
				}
				pos += 3
				if ptrMask&bit != 0 {
					if st.nulledOnceMask&bit == 0 && st.presentMask&bit != 0 {
						if st.preNullStaged == nil {
							st.preNullStaged = flatAllocPreNullStaged()
						}
						(*st.preNullStaged)[leafIdx] = st.staged[leafIdx]
						st.preNullMask |= bit
					}
					st.nullMask |= bit
					st.nulledOnceMask |= bit
					st.presentMask &^= bit
					st.strMask &^= bit
				}
				goto afterVal
			}

			nextPos, isStr, ok := flatStageLeafValue(strPtr, s, vc, pos, end, plan.leaves[leafIdx].kind, validateStr, &st.nonASCII, &st.staged[leafIdx])
			if !ok {
				return 0, false
			}
			pos = nextPos
			st.presentMask |= bit
			if ptrMask&bit != 0 {
				st.nullMask &^= bit
			}
			if isStr {
				st.strMask |= bit
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

func flatStageLeafValue(
	strPtr unsafe.Pointer,
	s string,
	vc byte,
	pos, end int,
	kind uint8,
	validateStr bool,
	nonASCII *uint64,
	dst *flatStagedField,
) (int, bool, bool) {
	switch kind {
	case fieldKindString, fieldKindPtrString:
		if vc != '"' {
			return 0, false, false
		}
		strStart := pos
		strEnd, ok := flatScanSimpleString(strPtr, pos, end, validateStr, nonASCII)
		if !ok {
			return 0, false, false
		}
		dst.strOff = uint32(strStart)
		dst.strLen = uint32(strEnd - strStart)
		return strEnd + 1, true, true

	case fieldKindI64, fieldKindPtrI64, fieldKindPtrInt:
		val, nextPos, ok := flatParseSigned(strPtr, vc, pos, end, math.MaxInt64, uint64(math.MaxInt64)+1)
		if !ok {
			return 0, false, false
		}
		dst.val = val
		return nextPos, false, true

	case fieldKindI32, fieldKindPtrI32:
		val, nextPos, ok := flatParseSigned(strPtr, vc, pos, end, math.MaxInt32, uint64(math.MaxInt32)+1)
		if !ok {
			return 0, false, false
		}
		dst.val = uint64(uint32(val))
		return nextPos, false, true

	case fieldKindI16, fieldKindPtrI16:
		val, nextPos, ok := flatParseSigned(strPtr, vc, pos, end, math.MaxInt16, uint64(math.MaxInt16)+1)
		if !ok {
			return 0, false, false
		}
		dst.val = uint64(uint16(val))
		return nextPos, false, true

	case fieldKindI8, fieldKindPtrI8:
		val, nextPos, ok := flatParseSigned(strPtr, vc, pos, end, math.MaxInt8, uint64(math.MaxInt8)+1)
		if !ok {
			return 0, false, false
		}
		dst.val = uint64(uint8(val))
		return nextPos, false, true

	case fieldKindU64, fieldKindPtrU64, fieldKindPtrUint:
		val, nextPos, ok := flatParseUnsigned(strPtr, vc, pos, end, math.MaxUint64)
		if !ok {
			return 0, false, false
		}
		dst.val = val
		return nextPos, false, true

	case fieldKindU32, fieldKindPtrU32:
		val, nextPos, ok := flatParseUnsigned(strPtr, vc, pos, end, math.MaxUint32)
		if !ok {
			return 0, false, false
		}
		dst.val = val
		return nextPos, false, true

	case fieldKindU16, fieldKindPtrU16:
		val, nextPos, ok := flatParseUnsigned(strPtr, vc, pos, end, math.MaxUint16)
		if !ok {
			return 0, false, false
		}
		dst.val = val
		return nextPos, false, true

	case fieldKindU8, fieldKindPtrU8:
		val, nextPos, ok := flatParseUnsigned(strPtr, vc, pos, end, math.MaxUint8)
		if !ok {
			return 0, false, false
		}
		dst.val = val
		return nextPos, false, true

	case fieldKindBool, fieldKindPtrBool:
		if vc == 't' {
			if pos+3 > end || s[pos:pos+3] != "rue" {
				return 0, false, false
			}
			dst.val = 1
			return pos + 3, false, true
		}
		if vc == 'f' {
			if pos+4 > end || s[pos:pos+4] != "alse" {
				return 0, false, false
			}
			dst.val = 0
			return pos + 4, false, true
		}
		return 0, false, false

	case fieldKindF64, fieldKindPtrF64:
		fv, nextPos, ok := flatParseFloat(strPtr, vc, pos, end)
		if !ok {
			return 0, false, false
		}
		dst.val = math.Float64bits(fv)
		return nextPos, false, true

	case fieldKindF32, fieldKindPtrF32:
		fv, nextPos, ok := flatParseFloat(strPtr, vc, pos, end)
		if !ok || fv < -math.MaxFloat32 || fv > math.MaxFloat32 {
			return 0, false, false
		}
		dst.val = uint64(math.Float32bits(float32(fv)))
		return nextPos, false, true

	default:
		return 0, false, false
	}
}

func flatWriteValueScalarOrStr(elem unsafe.Pointer, kind uint8, st *flatStagedField, s string, copyStr bool, copySlab []byte, copyOff *int) {
	switch kind {
	case fieldKindString:
		n := int(st.strLen)
		if n == 0 {
			*(*string)(elem) = ""
		} else if copyStr {
			off := *copyOff
			dst := copySlab[off : off+n]
			*copyOff = off + n
			copy(dst, s[st.strOff:int(st.strOff)+n])
			*(*string)(elem) = unsafe.String(&dst[0], n)
		} else {
			*(*string)(elem) = s[st.strOff : int(st.strOff)+n]
		}
	case fieldKindI64, fieldKindU64, fieldKindF64:
		*(*uint64)(elem) = st.val
	case fieldKindI32, fieldKindU32, fieldKindF32:
		*(*uint32)(elem) = uint32(st.val)
	case fieldKindI16, fieldKindU16:
		*(*uint16)(elem) = uint16(st.val)
	case fieldKindI8, fieldKindU8:
		*(*uint8)(elem) = uint8(st.val)
	case fieldKindBool:
		*(*bool)(elem) = st.val != 0
	}
}

//go:noinline
func flatAllocPreNullStaged() *[32]flatStagedField {
	return new([32]flatStagedField)
}

//go:noinline
func flatCommitPreNullRoot(
	vp unsafe.Pointer,
	fields []fieldEntry,
	preNullStaged *[32]flatStagedField,
	preNullMask uint32,
	s string,
	copyStr bool,
) {
	var copySlabBytes int
	if copyStr {
		for m := preNullMask; m != 0; m &= m - 1 {
			fi := bits.TrailingZeros32(m)
			fe := &fields[fi]
			if fe.kind == fieldKindPtrString && *(*unsafe.Pointer)(unsafe.Add(vp, uintptr(fe.offset))) != nil {
				n := int(preNullStaged[fi].strLen)
				if n <= maxArenaStrLen {
					copySlabBytes += n
				}
			}
		}
	}
	var copySlab []byte
	copyOff := 0
	if copySlabBytes > 0 {
		copySlab = make([]byte, copySlabBytes)
	}
	for m := preNullMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		fe := &fields[fi]
		elem := unsafe.Add(vp, uintptr(fe.offset))
		if *(*unsafe.Pointer)(elem) != nil {
			flatWritePtrLeaf(elem, fe.kind, &preNullStaged[fi], bit, 0, s, copyStr, copySlab, &copyOff, nil, nil, nil, nil)
		}
	}
}

//go:noinline
func flatCommitPreNullLeaves(
	vp unsafe.Pointer,
	leaves []flatLeaf,
	preNullStaged *[32]flatStagedField,
	preNullMask uint32,
	s string,
	copyStr bool,
) {
	var copySlabBytes int
	if copyStr {
		for m := preNullMask; m != 0; m &= m - 1 {
			fi := bits.TrailingZeros32(m)
			lf := &leaves[fi]
			if lf.kind == fieldKindPtrString && *(*unsafe.Pointer)(unsafe.Add(vp, uintptr(lf.offset))) != nil {
				n := int(preNullStaged[fi].strLen)
				if n <= maxArenaStrLen {
					copySlabBytes += n
				}
			}
		}
	}
	var copySlab []byte
	copyOff := 0
	if copySlabBytes > 0 {
		copySlab = make([]byte, copySlabBytes)
	}
	for m := preNullMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		lf := &leaves[fi]
		elem := unsafe.Add(vp, uintptr(lf.offset))
		if *(*unsafe.Pointer)(elem) != nil {
			flatWritePtrLeaf(elem, lf.kind, &preNullStaged[fi], bit, 0, s, copyStr, copySlab, &copyOff, nil, nil, nil, nil)
		}
	}
}

func flatCommitWithPointersRoot(
	vp unsafe.Pointer,
	fields []fieldEntry,
	staged *[32]flatStagedField,
	preNullStaged *[32]flatStagedField,
	presentMask, strMask, nullMask, nulledOnceMask, preNullMask uint32,
	s string,
	copyStr bool,
) {
	if preNullMask != 0 {
		flatCommitPreNullRoot(vp, fields, preNullStaged, preNullMask, s, copyStr)
	}
	for m := nullMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		*(*unsafe.Pointer)(unsafe.Add(vp, uintptr(fields[fi].offset))) = nil
	}
	if presentMask == 0 {
		return
	}

	var nNewScalar, nNewSmallStr, copySlabBytes int
	for m := presentMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		fe := &fields[fi]
		st := &staged[fi]
		if fe.kind == fieldKindString {
			if copyStr {
				copySlabBytes += int(st.strLen)
			}
		} else if fe.kind == fieldKindPtrString {
			n := int(st.strLen)
			if copyStr && n <= maxArenaStrLen {
				copySlabBytes += n
			}
			elem := unsafe.Add(vp, uintptr(fe.offset))
			if *(*unsafe.Pointer)(elem) == nil || nulledOnceMask&bit != 0 {
				if n <= maxArenaStrLen {
					nNewSmallStr++
				}
			}
		} else if fe.kind >= fieldKindPtrI64 {
			elem := unsafe.Add(vp, uintptr(fe.offset))
			if *(*unsafe.Pointer)(elem) == nil || nulledOnceMask&bit != 0 {
				nNewScalar++
			}
		}
	}

	var copySlab []byte
	copyOff := 0
	if copySlabBytes > 0 {
		copySlab = make([]byte, copySlabBytes)
	}
	var scalarWords []uint64
	scalarIdx := 0
	if nNewScalar > 0 {
		scalarWords = make([]uint64, nNewScalar)
	}
	var strSlots []string
	strIdx := 0
	if nNewSmallStr > 0 {
		strSlots = make([]string, nNewSmallStr)
	}

	for m := presentMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		fe := &fields[fi]
		elem := unsafe.Add(vp, uintptr(fe.offset))
		st := &staged[fi]
		if fe.kind < fieldKindPtrString {
			flatWriteValueScalarOrStr(elem, fe.kind, st, s, copyStr, copySlab, &copyOff)
		} else {
			flatWritePtrLeaf(elem, fe.kind, st, bit, nulledOnceMask, s, copyStr, copySlab, &copyOff, scalarWords, &scalarIdx, strSlots, &strIdx)
		}
	}
}

func flatCommitPlanLeaves(
	vp unsafe.Pointer,
	leaves []flatLeaf,
	ptrMask uint32,
	staged *[32]flatStagedField,
	preNullStaged *[32]flatStagedField,
	presentMask, strMask, nullMask, nulledOnceMask, preNullMask uint32,
	s string,
	copyStr bool,
) {
	if ptrMask == 0 {
		var copySlab []byte
		copyOff := 0
		if copyStr && strMask != 0 {
			totalBytes := 0
			for m := strMask; m != 0; m &= m - 1 {
				fi := bits.TrailingZeros32(m)
				totalBytes += int(staged[fi].strLen)
			}
			if totalBytes > 0 {
				copySlab = make([]byte, totalBytes)
			}
		}
		for m := presentMask; m != 0; m &= m - 1 {
			fi := bits.TrailingZeros32(m)
			lf := &leaves[fi]
			flatWriteValueScalarOrStr(unsafe.Add(vp, uintptr(lf.offset)), lf.kind, &staged[fi], s, copyStr, copySlab, &copyOff)
		}
		return
	}

	if preNullMask != 0 {
		flatCommitPreNullLeaves(vp, leaves, preNullStaged, preNullMask, s, copyStr)
	}
	for m := nullMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		*(*unsafe.Pointer)(unsafe.Add(vp, uintptr(leaves[fi].offset))) = nil
	}
	if presentMask == 0 {
		return
	}

	var nNewScalar, nNewSmallStr, copySlabBytes int
	for m := presentMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		lf := &leaves[fi]
		st := &staged[fi]
		if lf.kind == fieldKindString {
			if copyStr {
				copySlabBytes += int(st.strLen)
			}
		} else if lf.kind == fieldKindPtrString {
			n := int(st.strLen)
			if copyStr && n <= maxArenaStrLen {
				copySlabBytes += n
			}
			elem := unsafe.Add(vp, uintptr(lf.offset))
			if *(*unsafe.Pointer)(elem) == nil || nulledOnceMask&bit != 0 {
				if n <= maxArenaStrLen {
					nNewSmallStr++
				}
			}
		} else if lf.kind >= fieldKindPtrI64 {
			elem := unsafe.Add(vp, uintptr(lf.offset))
			if *(*unsafe.Pointer)(elem) == nil || nulledOnceMask&bit != 0 {
				nNewScalar++
			}
		}
	}

	var copySlab []byte
	copyOff := 0
	if copySlabBytes > 0 {
		copySlab = make([]byte, copySlabBytes)
	}
	var scalarWords []uint64
	scalarIdx := 0
	if nNewScalar > 0 {
		scalarWords = make([]uint64, nNewScalar)
	}
	var strSlots []string
	strIdx := 0
	if nNewSmallStr > 0 {
		strSlots = make([]string, nNewSmallStr)
	}

	for m := presentMask; m != 0; m &= m - 1 {
		fi := bits.TrailingZeros32(m)
		bit := uint32(1) << uint(fi)
		lf := &leaves[fi]
		elem := unsafe.Add(vp, uintptr(lf.offset))
		st := &staged[fi]
		if lf.kind < fieldKindPtrString {
			flatWriteValueScalarOrStr(elem, lf.kind, st, s, copyStr, copySlab, &copyOff)
		} else {
			flatWritePtrLeaf(elem, lf.kind, st, bit, nulledOnceMask, s, copyStr, copySlab, &copyOff, scalarWords, &scalarIdx, strSlots, &strIdx)
		}
	}
}

func flatWritePtrLeaf(
	elem unsafe.Pointer,
	kind uint8,
	st *flatStagedField,
	bit, nulledOnceMask uint32,
	s string,
	copyStr bool,
	copySlab []byte,
	copyOff *int,
	scalarWords []uint64,
	scalarIdx *int,
	strSlots []string,
	strIdx *int,
) {
	if kind == fieldKindPtrString {
		n := int(st.strLen)
		var val string
		if n > 0 {
			if copyStr {
				if n <= maxArenaStrLen {
					off := *copyOff
					dst := copySlab[off : off+n]
					*copyOff = off + n
					copy(dst, s[st.strOff:int(st.strOff)+n])
					val = unsafe.String(&dst[0], n)
				} else {
					buf := make([]byte, n)
					copy(buf, s[st.strOff:int(st.strOff)+n])
					val = unsafe.String(&buf[0], n)
				}
			} else {
				val = s[st.strOff : int(st.strOff)+n]
			}
		}
		pp := (**string)(elem)
		dst := *pp
		if dst == nil || nulledOnceMask&bit != 0 {
			if n > maxArenaStrLen {
				dst = new(string)
			} else {
				si := *strIdx
				*strIdx = si + 1
				dst = &strSlots[si]
			}
			*pp = dst
		}
		*dst = val
		return
	}

	ptr := *(*unsafe.Pointer)(elem)
	if ptr == nil || nulledOnceMask&bit != 0 {
		si := *scalarIdx
		*scalarIdx = si + 1
		ptr = unsafe.Pointer(&scalarWords[si])
		*(*unsafe.Pointer)(elem) = ptr
	}
	switch kind {
	case fieldKindPtrI64, fieldKindPtrInt, fieldKindPtrU64, fieldKindPtrUint, fieldKindPtrF64:
		*(*uint64)(ptr) = st.val
	case fieldKindPtrI32, fieldKindPtrU32, fieldKindPtrF32:
		*(*uint32)(ptr) = uint32(st.val)
	case fieldKindPtrI16, fieldKindPtrU16:
		*(*uint16)(ptr) = uint16(st.val)
	case fieldKindPtrI8, fieldKindPtrU8:
		*(*uint8)(ptr) = uint8(st.val)
	case fieldKindPtrBool:
		*(*bool)(ptr) = st.val != 0
	}
}

func flatTrailingUTF8OK(s string, strPtr unsafe.Pointer, pos, end int) bool {
	for pos < end {
		b := *(*byte)(unsafe.Add(strPtr, pos))
		if b > ' ' || (spaceMask&(uint64(1)<<b)) == 0 {
			return utf8.ValidateString(s[pos:])
		}
		pos++
	}
	return true
}

func flatSkipWS(strPtr unsafe.Pointer, pos, end int) (int, byte, bool) {
	for pos < end {
		b := *(*byte)(unsafe.Add(strPtr, pos))
		if b > ' ' {
			return pos + 1, b, true
		}
		if (spaceMask & (uint64(1) << b)) == 0 {
			return pos + 1, b, true
		}
		pos++
		for pos+8 <= end {
			w := *(*uint64)(unsafe.Add(strPtr, pos)) ^ swarSpaces
			if w == 0 {
				pos += 8
				continue
			}
			rel := bits.TrailingZeros64(w) >> 3
			at := pos + rel
			c := *(*byte)(unsafe.Add(strPtr, at))
			if c > ' ' || (spaceMask&(uint64(1)<<c)) == 0 {
				return at + 1, c, true
			}
			pos = at + 1
			break
		}
	}
	return end, 0, false
}

// flatScanSimpleString scans an unescaped string starting at pos (right after opening '"')
// and returns (quotePos, true), bailing out cleanly on '\', '< 0x20' (when validateStr), or EOF.
func flatScanSimpleString(strPtr unsafe.Pointer, pos, end int, validateStr bool, nonASCII *uint64) (int, bool) {
	for pos+8 <= end {
		w := *(*uint64)(unsafe.Add(strPtr, pos))
		*nonASCII |= w & swarHigh
		q := w ^ swarQuote
		qm := (q - swarOnes) &^ q & swarHigh
		b := w ^ swarBS
		em := (b - swarOnes) &^ b & swarHigh
		if validateStr {
			em |= (w - swarCtrl) &^ w & swarHigh
		}
		if qm != 0 {
			if (qm-1)&em != 0 {
				return 0, false
			}
			return pos + (bits.TrailingZeros64(qm) >> 3), true
		}
		if em != 0 {
			return 0, false
		}
		pos += 8
	}
	for pos < end {
		c := *(*byte)(unsafe.Add(strPtr, pos))
		*nonASCII |= uint64(c & 0x80)
		if c == '"' {
			return pos, true
		}
		if c == '\\' || (validateStr && c < 0x20) {
			return 0, false
		}
		pos++
	}
	return 0, false
}

func flatParseSigned(strPtr unsafe.Pointer, c byte, pos, end int, maxPos, maxNeg uint64) (uint64, int, bool) {
	neg := false
	if c == '-' {
		neg = true
		if pos >= end {
			return 0, 0, false
		}
		c = *(*byte)(unsafe.Add(strPtr, pos))
		pos++
	}
	var u uint64
	if c == '0' {
		d := schemaByteAt(strPtr, pos, end)
		if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' {
			return 0, 0, false
		}
	} else if c >= '1' && c <= '9' {
		u = uint64(c - '0')
		digits := 1
		for digits < 19 {
			d := schemaByteAt(strPtr, pos, end)
			if d < '0' || d > '9' {
				if d == '.' || (d|0x20) == 'e' {
					return 0, 0, false
				}
				goto checkRange
			}
			u = u*10 + uint64(d-'0')
			pos++
			digits++
		}
		d := schemaByteAt(strPtr, pos, end)
		if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' {
			return 0, 0, false
		}
	} else {
		return 0, 0, false
	}

checkRange:
	if neg {
		if u > maxNeg {
			return 0, 0, false
		}
		return -u, pos, true
	}
	if u > maxPos {
		return 0, 0, false
	}
	return u, pos, true
}

func flatParseUnsigned(strPtr unsafe.Pointer, c byte, pos, end int, maxVal uint64) (uint64, int, bool) {
	var u uint64
	if c == '0' {
		d := schemaByteAt(strPtr, pos, end)
		if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' {
			return 0, 0, false
		}
		return 0, pos, true
	}
	if c < '1' || c > '9' {
		return 0, 0, false
	}
	u = uint64(c - '0')
	digits := 1
	for digits < 19 {
		d := schemaByteAt(strPtr, pos, end)
		if d < '0' || d > '9' {
			if d == '.' || (d|0x20) == 'e' {
				return 0, 0, false
			}
			if u > maxVal {
				return 0, 0, false
			}
			return u, pos, true
		}
		u = u*10 + uint64(d-'0')
		pos++
		digits++
	}
	d := schemaByteAt(strPtr, pos, end)
	if (d >= '0' && d <= '9') || d == '.' || (d|0x20) == 'e' || u > maxVal {
		return 0, 0, false
	}
	return u, pos, true
}

func flatParseFloat(strPtr unsafe.Pointer, c byte, pos, end int) (float64, int, bool) {
	neg := false
	if c == '-' {
		neg = true
		if pos >= end {
			return 0, 0, false
		}
		c = *(*byte)(unsafe.Add(strPtr, pos))
		pos++
	}

	var mant uint64
	var exp int32
	sigDigits := 0
	var d byte

	if c == '0' {
		d = schemaByteAt(strPtr, pos, end)
		if d >= '0' && d <= '9' {
			return 0, 0, false
		}
		if d == '.' {
			pos++
			f0 := schemaByteAt(strPtr, pos, end)
			if f0 < '0' || f0 > '9' {
				return 0, 0, false
			}
			for schemaByteAt(strPtr, pos, end) == '0' {
				pos++
				exp--
			}
			for {
				fd := schemaByteAt(strPtr, pos, end)
				if fd < '0' || fd > '9' {
					d = fd
					break
				}
				if sigDigits >= 19 {
					return 0, 0, false
				}
				mant = mant*10 + uint64(fd-'0')
				exp--
				sigDigits++
				pos++
			}
		} else if (d | 0x20) != 'e' {
			if neg {
				return math.Copysign(0, -1), pos, true
			}
			return 0, pos, true
		}
	} else if c >= '1' && c <= '9' {
		mant = uint64(c - '0')
		sigDigits = 1
		for {
			id := schemaByteAt(strPtr, pos, end)
			if id < '0' || id > '9' {
				d = id
				break
			}
			if sigDigits >= 19 {
				return 0, 0, false
			}
			mant = mant*10 + uint64(id-'0')
			sigDigits++
			pos++
		}
		if d == '.' {
			pos++
			f0 := schemaByteAt(strPtr, pos, end)
			if f0 < '0' || f0 > '9' {
				return 0, 0, false
			}
			for {
				fd := schemaByteAt(strPtr, pos, end)
				if fd < '0' || fd > '9' {
					d = fd
					break
				}
				if sigDigits >= 19 {
					return 0, 0, false
				}
				mant = mant*10 + uint64(fd-'0')
				exp--
				sigDigits++
				pos++
			}
		}
	} else {
		return 0, 0, false
	}

	if d == 'e' || d == 'E' {
		pos++
		expSign := int32(1)
		ed := schemaByteAt(strPtr, pos, end)
		if ed == '+' {
			pos++
			ed = schemaByteAt(strPtr, pos, end)
		} else if ed == '-' {
			expSign = -1
			pos++
			ed = schemaByteAt(strPtr, pos, end)
		}
		if ed < '0' || ed > '9' {
			return 0, 0, false
		}
		expVal := int32(ed - '0')
		pos++
		expCount := 1
		for {
			ed = schemaByteAt(strPtr, pos, end)
			if ed < '0' || ed > '9' {
				break
			}
			if expCount >= 3 {
				return 0, 0, false
			}
			expVal = expVal*10 + int32(ed-'0')
			expCount++
			pos++
		}
		exp += expSign * expVal
	}

	fv, ok := cParityAtofFast(mant, exp, neg, false)
	if !ok || math.IsInf(fv, 0) {
		return 0, 0, false
	}
	return fv, pos, true
}

func flatSkipContainerRelaxed(strPtr unsafe.Pointer, pos, end int, opts uint8, nonASCII *uint64) (int, bool) {
	nextEnd, err := kernels.SkipContainer(unsafe.String((*byte)(strPtr), end), pos-1)
	if err != 0 {
		return 0, false
	}
	if opts&flatOptValidateStr != 0 {
		*nonASCII = 1
	}
	return nextEnd, true
}

func flatSkipString(strPtr unsafe.Pointer, pos, end int, opts uint8, nonASCII *uint64) (int, bool) {
	checkCtrl := opts&(flatOptValidateStr|flatOptNoValidateJSON) == flatOptValidateStr
	canSIMD := opts&flatOptNoValidateJSON != 0 || (checkCtrl && opts&flatOptStrictURC == 0)
	for {
		limit := end
		simd := false
		if canSIMD && end-pos >= 64 {
			limit = pos + 32
			simd = true
		}
		for pos+8 <= limit {
			w := *(*uint64)(unsafe.Add(strPtr, pos))
			*nonASCII |= w & swarHigh
			q := w ^ swarQuote
			qm := (q - swarOnes) &^ q & swarHigh
			b := w ^ swarBS
			em := (b - swarOnes) &^ b & swarHigh
			if checkCtrl {
				em |= (w - swarCtrl) &^ w & swarHigh
			}
			if qm != 0 {
				if (qm-1)&em != 0 {
					pos += bits.TrailingZeros64(em) >> 3
					break
				}
				return pos + (bits.TrailingZeros64(qm) >> 3) + 1, true
			}
			if em != 0 {
				pos += bits.TrailingZeros64(em) >> 3
				break
			}
			pos += 8
		}
		if simd && pos == limit {
			tail := unsafe.String((*byte)(unsafe.Add(strPtr, pos)), end-pos)
			if opts&flatOptNoValidateJSON != 0 {
				relEnd, err := kernels.SkipStringRelaxed(tail)
				if err != 0 {
					return 0, false
				}
				if opts&flatOptValidateStr != 0 {
					*nonASCII = 1
				}
				return pos + relEnd, true
			}
			relEnd, err := kernels.SkipStringStrict(tail)
			if err != 0 {
				return 0, false
			}
			*nonASCII = 1
			return pos + relEnd, true
		}
		if pos >= end {
			return 0, false
		}
		c := *(*byte)(unsafe.Add(strPtr, pos))
		*nonASCII |= uint64(c & 0x80)
		if c == '"' {
			return pos + 1, true
		}
		if opts&(flatOptValidateStr|flatOptNoValidateJSON) == flatOptValidateStr && c < 0x20 {
			return 0, false
		}
		if c != '\\' {
			pos++
			continue
		}
		if pos+2 > end {
			return 0, false
		}
		if opts&flatOptNoValidateJSON != 0 {
			pos += 2
			continue
		}
		esc := *(*byte)(unsafe.Add(strPtr, pos+1))
		if esc != 'u' {
			if cParityEscaped[esc] == 0 {
				return 0, false
			}
			pos += 2
			continue
		}
		if pos+6 > end {
			return 0, false
		}
		u, ok := cParityReadHex16(unsafe.Add(strPtr, pos+2))
		if !ok {
			return 0, false
		}
		pos += 6
		if opts&flatOptStrictURC != 0 && u >= 0xd800 && u <= 0xdfff {
			if u > 0xdbff || pos+6 > end || *(*byte)(unsafe.Add(strPtr, pos)) != '\\' || *(*byte)(unsafe.Add(strPtr, pos+1)) != 'u' {
				return 0, false
			}
			u2, ok2 := cParityReadHex16(unsafe.Add(strPtr, pos+2))
			if !ok2 || u2 < 0xdc00 || u2 > 0xdfff {
				return 0, false
			}
			pos += 6
		}
	}
}

func flatSkipValue(strPtr unsafe.Pointer, c byte, pos, end int, opts uint8, nonASCII *uint64) (int, bool) {
	var frameBits uint64
	sp := 0
	var ok bool
	var nc, delim, colon, vc byte

valueEntry:
	switch c {
	case '"':
		pos, ok = flatSkipString(strPtr, pos, end, opts, nonASCII)
		if !ok {
			return 0, false
		}
	case '{':
		if opts&flatOptNoValidateJSON != 0 {
			// In relaxed mode outer containers never push to frameBits, so sp == 0 here.
			return flatSkipContainerRelaxed(strPtr, pos, end, opts, nonASCII)
		}
		if sp >= 64 {
			return 0, false
		}
		frameBits <<= 1
		sp++
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if nc == '}' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		goto objKey
	case '[':
		if opts&flatOptNoValidateJSON != 0 {
			return flatSkipContainerRelaxed(strPtr, pos, end, opts, nonASCII)
		}
		if sp >= 64 {
			return 0, false
		}
		frameBits = (frameBits << 1) | 1
		sp++
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if nc == ']' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		c = nc
		goto valueEntry
	case 't':
		if pos+3 > end || unsafe.String((*byte)(unsafe.Add(strPtr, pos)), 3) != "rue" {
			return 0, false
		}
		pos += 3
	case 'f':
		if pos+4 > end || unsafe.String((*byte)(unsafe.Add(strPtr, pos)), 4) != "alse" {
			return 0, false
		}
		pos += 4
	case 'n':
		if pos+3 > end || unsafe.String((*byte)(unsafe.Add(strPtr, pos)), 3) != "ull" {
			return 0, false
		}
		pos += 3
	default:
		pos, ok = skipNumberAt(strPtr, c, pos, end, true)
		if !ok {
			return 0, false
		}
	}

afterValue:
	if sp == 0 {
		return pos, true
	}
	if (frameBits & 1) != 0 {
		pos, delim, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		if delim == ',' {
			pos, vc, ok = flatSkipWS(strPtr, pos, end)
			if !ok {
				return 0, false
			}
			c = vc
			goto valueEntry
		}
		if delim == ']' {
			frameBits >>= 1
			sp--
			goto afterValue
		}
		return 0, false
	}
	pos, delim, ok = flatSkipWS(strPtr, pos, end)
	if !ok {
		return 0, false
	}
	if delim == ',' {
		pos, nc, ok = flatSkipWS(strPtr, pos, end)
		if !ok {
			return 0, false
		}
		goto objKey
	}
	if delim == '}' {
		frameBits >>= 1
		sp--
		goto afterValue
	}
	return 0, false

objKey:
	if nc != '"' {
		return 0, false
	}
	pos, ok = flatSkipString(strPtr, pos, end, opts, nonASCII)
	if !ok {
		return 0, false
	}
	pos, colon, ok = flatSkipWS(strPtr, pos, end)
	if !ok || colon != ':' {
		return 0, false
	}
	pos, vc, ok = flatSkipWS(strPtr, pos, end)
	if !ok {
		return 0, false
	}
	c = vc
	goto valueEntry
}
