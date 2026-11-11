package vm

import (
	"encoding/json"
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/rt"
)

func encodeDenseStructRoot(b *[]byte, ptr unsafe.Pointer, specs []ir.StructFieldSpec, s *vars.Stack, flags uint64) error {
	if !s.EnterDepth(1) {
		return vars.ERR_too_deep
	}
	buf := append(*b, '{')
	var err error
	if sanitize := flags & alg.SanitizeMask; sanitize == 0 {
		buf, err = encodeStructFieldsDense(buf, ptr, specs, s, flags)
	} else {
		buf, err = encodeStructFieldsSanitizedDense(buf, ptr, specs, s, flags, sanitize)
	}
	s.LeaveDepth(1)
	if err != nil {
		return err
	}
	*b = append(buf, '}')
	return nil
}

func encodePureStructRoot(b *[]byte, ptr unsafe.Pointer, specs []ir.StructFieldSpec, s *vars.Stack, flags uint64) error {
	if !s.EnterDepth(1) {
		return vars.ERR_too_deep
	}
	buf := append(*b, '{')
	var err error
	if sanitize := flags & alg.SanitizeMask; sanitize == 0 {
		buf, _, err = encodeStructFieldsDefault(buf, ptr, specs, 1<<_S_cond, s, flags)
	} else {
		buf, _, err = encodeStructFieldsSanitized(buf, ptr, specs, 1<<_S_cond, s, flags, sanitize)
	}
	s.LeaveDepth(1)
	if err != nil {
		return err
	}
	*b = append(buf, '}')
	return nil
}

func appendKeyAndNull(buf []byte, keyPtr *byte, keyLen int) []byte {
	if uint(keyLen) <= 32 && cap(buf)-len(buf) >= 36 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		dk := (*[4]uint64)(dp)
		sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dk[0], dk[1] = sk[0], sk[1]
		if keyLen > 16 {
			dk[2], dk[3] = sk[2], sk[3]
		}
		*(*[4]byte)(unsafe.Add(dp, keyLen)) = [4]byte{'n', 'u', 'l', 'l'}
		return buf[:len(buf)+keyLen+4]
	}
	if keyLen <= 64 && keyLen > 32 && cap(buf)-len(buf) >= keyLen+4 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		*(*[4]uint64)(dp) = *(*[4]uint64)(unsafe.Pointer(keyPtr))
		*(*[4]uint64)(unsafe.Add(dp, keyLen-32)) = *(*[4]uint64)(unsafe.Add(unsafe.Pointer(keyPtr), keyLen-32))
		*(*[4]byte)(unsafe.Add(dp, keyLen)) = [4]byte{'n', 'u', 'l', 'l'}
		return buf[:len(buf)+keyLen+4]
	}
	buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
	return append(buf, "null"...)
}

func appendKeyAndBool(buf []byte, keyPtr *byte, keyLen int, v bool) []byte {
	if uint(keyLen) <= 32 && cap(buf)-len(buf) >= 40 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		dk := (*[4]uint64)(dp)
		sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dk[0], dk[1] = sk[0], sk[1]
		if keyLen > 16 {
			dk[2], dk[3] = sk[2], sk[3]
		}
		vp := unsafe.Add(dp, keyLen)
		if v {
			*(*[4]byte)(vp) = [4]byte{'t', 'r', 'u', 'e'}
			return buf[:len(buf)+keyLen+4]
		}
		*(*[5]byte)(vp) = [5]byte{'f', 'a', 'l', 's', 'e'}
		return buf[:len(buf)+keyLen+5]
	}
	if keyLen <= 64 && keyLen > 32 && cap(buf)-len(buf) >= keyLen+5 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		*(*[4]uint64)(dp) = *(*[4]uint64)(unsafe.Pointer(keyPtr))
		*(*[4]uint64)(unsafe.Add(dp, keyLen-32)) = *(*[4]uint64)(unsafe.Add(unsafe.Pointer(keyPtr), keyLen-32))
		vp := unsafe.Add(dp, keyLen)
		if v {
			*(*[4]byte)(vp) = [4]byte{'t', 'r', 'u', 'e'}
			return buf[:len(buf)+keyLen+4]
		}
		*(*[5]byte)(vp) = [5]byte{'f', 'a', 'l', 's', 'e'}
		return buf[:len(buf)+keyLen+5]
	}
	buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
	return appendBool(buf, v)
}

func appendKeyAndEmptySlice(buf []byte, keyPtr *byte, keyLen int, nonNil bool, flags uint64) []byte {
	isArr := nonNil || flags&(1<<alg.BitNoNullSliceOrMap) != 0
	if uint(keyLen) <= 32 && cap(buf)-len(buf) >= 36 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		dk := (*[4]uint64)(dp)
		sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dk[0], dk[1] = sk[0], sk[1]
		if keyLen > 16 {
			dk[2], dk[3] = sk[2], sk[3]
		}
		vp := unsafe.Add(dp, keyLen)
		if isArr {
			*(*[2]byte)(vp) = [2]byte{'[', ']'}
			return buf[:len(buf)+keyLen+2]
		}
		*(*[4]byte)(vp) = [4]byte{'n', 'u', 'l', 'l'}
		return buf[:len(buf)+keyLen+4]
	}
	if keyLen <= 64 && keyLen > 32 && cap(buf)-len(buf) >= keyLen+4 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		*(*[4]uint64)(dp) = *(*[4]uint64)(unsafe.Pointer(keyPtr))
		*(*[4]uint64)(unsafe.Add(dp, keyLen-32)) = *(*[4]uint64)(unsafe.Add(unsafe.Pointer(keyPtr), keyLen-32))
		vp := unsafe.Add(dp, keyLen)
		if isArr {
			*(*[2]byte)(vp) = [2]byte{'[', ']'}
			return buf[:len(buf)+keyLen+2]
		}
		*(*[4]byte)(vp) = [4]byte{'n', 'u', 'l', 'l'}
		return buf[:len(buf)+keyLen+4]
	}
	buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
	if isArr {
		return append(buf, '[', ']')
	}
	return append(buf, "null"...)
}

func appendBoolSliceBody(buf []byte, items []bool) []byte {
	need := 2 + len(items)*6
	if cap(buf)-len(buf) >= need {
		dp := unsafe.Pointer(unsafe.SliceData(buf))
		pos := len(buf)
		*(*byte)(unsafe.Add(dp, pos)) = '['
		pos++
		if items[0] {
			*(*[4]byte)(unsafe.Add(dp, pos)) = [4]byte{'t', 'r', 'u', 'e'}
			pos += 4
		} else {
			*(*[5]byte)(unsafe.Add(dp, pos)) = [5]byte{'f', 'a', 'l', 's', 'e'}
			pos += 5
		}
		for idx := 1; idx < len(items); idx++ {
			if items[idx] {
				*(*[5]byte)(unsafe.Add(dp, pos)) = [5]byte{',', 't', 'r', 'u', 'e'}
				pos += 5
			} else {
				*(*[6]byte)(unsafe.Add(dp, pos)) = [6]byte{',', 'f', 'a', 'l', 's', 'e'}
				pos += 6
			}
		}
		*(*byte)(unsafe.Add(dp, pos)) = ']'
		return buf[:pos+1]
	}
	buf = append(buf, '[')
	buf = appendBool(buf, items[0])
	for idx := 1; idx < len(items); idx++ {
		buf = append(buf, ',')
		buf = appendBool(buf, items[idx])
	}
	return append(buf, ']')
}

func appendI32SliceBody(buf []byte, items []int32) []byte {
	buf = append(buf, '[')
	buf = alg.I64toa(buf, int64(items[0]))
	for idx := 1; idx < len(items); idx++ {
		buf = append(buf, ',')
		buf = alg.I64toa(buf, int64(items[idx]))
	}
	return append(buf, ']')
}

func appendU64SliceBody(buf []byte, items []uint64) []byte {
	buf = append(buf, '[')
	buf = alg.U64toa(buf, items[0])
	for idx := 1; idx < len(items); idx++ {
		buf = append(buf, ',')
		buf = alg.U64toa(buf, items[idx])
	}
	return append(buf, ']')
}

func appendU32SliceBody(buf []byte, items []uint32) []byte {
	buf = append(buf, '[')
	buf = alg.U64toa(buf, uint64(items[0]))
	for idx := 1; idx < len(items); idx++ {
		buf = append(buf, ',')
		buf = alg.U64toa(buf, uint64(items[idx]))
	}
	return append(buf, ']')
}

func encodeStructFieldsDense(buf []byte, base unsafe.Pointer, specs []ir.StructFieldSpec, s *vars.Stack, flags uint64) ([]byte, error) {
	for i := range specs {
		spec := &specs[i]
		fp := unsafe.Add(base, spec.Offset)
		switch spec.Kind {
		case ir.OP_str:
			buf = alg.QuoteWithPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*string)(fp))
		case ir.OP_i64:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*int64)(fp))
		case ir.OP_bool:
			buf = appendKeyAndBool(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*bool)(fp))
		case ir.OP_eface:
			val := *(*any)(fp)
			if val == nil {
				buf = appendKeyAndNull(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			if err := encodeAnyValue(&buf, val, s, flags); err != nil {
				return buf, err
			}
		case ir.OP_struct_begin:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
		case ir.OP_struct_end:
			buf = append(buf, '}')
		case ir.OP_slice_struct:
			sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
			rv := reflect.NewAt(sliceSpec.SliceType, fp).Elem()
			n := rv.Len()
			if n == 0 {
				buf = appendKeyAndEmptySlice(buf, sliceSpec.KeyPtr, int(spec.KeyLen), !rv.IsNil(), flags)
				continue
			}
			buf = appendPaddedKey(buf, sliceSpec.KeyPtr, int(spec.KeyLen))
			if !s.EnterDepth(2) {
				return buf, vars.ERR_too_deep
			}
			dataPtr := rv.UnsafePointer()
			startOff := len(buf)
			buf = append(buf, '[', '{')
			var err error
			if sliceSpec.IsDense {
				buf, err = encodeStructFieldsDense(buf, dataPtr, sliceSpec.DenseSpecs, s, flags)
				if err != nil {
					s.LeaveDepth(2)
					return buf, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, err = encodeStructFieldsDense(buf, elemPtr, sliceSpec.DenseSpecs, s, flags)
					if err != nil {
						s.LeaveDepth(2)
						return buf, err
					}
				}
			} else {
				buf, _, err = encodeStructFieldsDefault(buf, dataPtr, sliceSpec.Specs, 1<<_S_cond, s, flags)
				if err != nil {
					s.LeaveDepth(2)
					return buf, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, _, err = encodeStructFieldsDefault(buf, elemPtr, sliceSpec.Specs, 1<<_S_cond, s, flags)
					if err != nil {
						s.LeaveDepth(2)
						return buf, err
					}
				}
			}
			s.LeaveDepth(2)
			buf = append(buf, '}', ']')
		case ir.OP_slice_eface:
			items := *(*[]any)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			if !s.EnterDepth(1) {
				return buf, vars.ERR_too_deep
			}
			buf = append(buf, '[')
			for idx, elem := range items {
				if idx > 0 {
					buf = append(buf, ',')
				}
				if err := encodeAnyValue(&buf, elem, s, flags); err != nil {
					s.LeaveDepth(1)
					return buf, err
				}
			}
			s.LeaveDepth(1)
			buf = append(buf, ']')
		case ir.OP_slice_i64:
			items := *(*[]int64)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = append(buf, '[')
			buf = alg.I64toa(buf, items[0])
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.I64toa(buf, items[idx])
			}
			buf = append(buf, ']')
		case ir.OP_slice_str:
			items := *(*[]string)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = append(buf, '[')
			buf = alg.Quote(buf, items[0], false)
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.Quote(buf, items[idx], false)
			}
			buf = append(buf, ']')
		case ir.OP_slice_bool:
			items := *(*[]bool)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendBoolSliceBody(buf, items)
		case ir.OP_f64_slice:
			items := *(*[]float64)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			var err error
			buf, err = appendFloat64Slice(buf, items, flags)
			if err != nil {
				return buf, err
			}
		case ir.OP_f32_slice:
			items := *(*[]float32)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			var err error
			buf, err = appendFloat32Slice(buf, items, flags)
			if err != nil {
				return buf, err
			}
		case ir.OP_slice_i32:
			items := *(*[]int32)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendI32SliceBody(buf, items)
		case ir.OP_slice_u64:
			items := *(*[]uint64)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendU64SliceBody(buf, items)
		case ir.OP_slice_u32:
			items := *(*[]uint32)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendU32SliceBody(buf, items)
		case ir.OP_bin:
			items := *(*[]byte)(fp)
			if items == nil {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), false, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = rt.EncodeBase64(buf, items)
		case ir.OP_f64:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			v := *(*float64)(fp)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, vars.ERR_nan_or_infinite
			}
			buf = alg.F64toa(buf, v)
		case ir.OP_f32:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			v := *(*float32)(fp)
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, vars.ERR_nan_or_infinite
			}
			buf = alg.F32toa(buf, v)
		case ir.OP_i32:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), int64(*(*int32)(fp)))
		case ir.OP_i16:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), int64(*(*int16)(fp)))
		case ir.OP_i8:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), int64(*(*int8)(fp)))
		case ir.OP_u64:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*uint64)(fp))
		case ir.OP_u32:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), uint64(*(*uint32)(fp)))
		case ir.OP_u16:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), uint64(*(*uint16)(fp)))
		case ir.OP_u8:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), uint64(*(*uint8)(fp)))
		case ir.OP_quote:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = alg.Quote(buf, *(*string)(fp), true)
		case ir.OP_number:
			v := *(*json.Number)(fp)
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			if v == "" {
				buf = append(buf, '0')
			} else if !alg.IsValidNumber(string(v)) {
				return buf, vars.Error_number(v)
			} else {
				buf = append(buf, v...)
			}
		}
	}
	return buf, nil
}

func encodeStructFieldsSanitizedDense(buf []byte, base unsafe.Pointer, specs []ir.StructFieldSpec, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	for i := range specs {
		spec := &specs[i]
		fp := unsafe.Add(base, spec.Offset)
		switch spec.Kind {
		case ir.OP_str:
			buf = alg.QuoteWithPaddedKeyFlags(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*string)(fp), sanitize)
		case ir.OP_i64:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*int64)(fp))
		case ir.OP_bool:
			buf = appendKeyAndBool(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*bool)(fp))
		case ir.OP_eface:
			val := *(*any)(fp)
			if val == nil {
				buf = appendKeyAndNull(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			if err := encodeAnyValue(&buf, val, s, flags); err != nil {
				return buf, err
			}
		case ir.OP_struct_begin:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
		case ir.OP_struct_end:
			buf = append(buf, '}')
		case ir.OP_slice_struct:
			sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
			rv := reflect.NewAt(sliceSpec.SliceType, fp).Elem()
			n := rv.Len()
			if n == 0 {
				buf = appendKeyAndEmptySlice(buf, sliceSpec.KeyPtr, int(spec.KeyLen), !rv.IsNil(), flags)
				continue
			}
			buf = appendPaddedKey(buf, sliceSpec.KeyPtr, int(spec.KeyLen))
			if !s.EnterDepth(2) {
				return buf, vars.ERR_too_deep
			}
			dataPtr := rv.UnsafePointer()
			startOff := len(buf)
			buf = append(buf, '[', '{')
			var err error
			if sliceSpec.IsDense {
				buf, err = encodeStructFieldsSanitizedDense(buf, dataPtr, sliceSpec.DenseSpecs, s, flags, sanitize)
				if err != nil {
					s.LeaveDepth(2)
					return buf, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, err = encodeStructFieldsSanitizedDense(buf, elemPtr, sliceSpec.DenseSpecs, s, flags, sanitize)
					if err != nil {
						s.LeaveDepth(2)
						return buf, err
					}
				}
			} else {
				buf, _, err = encodeStructFieldsSanitized(buf, dataPtr, sliceSpec.Specs, 1<<_S_cond, s, flags, sanitize)
				if err != nil {
					s.LeaveDepth(2)
					return buf, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, _, err = encodeStructFieldsSanitized(buf, elemPtr, sliceSpec.Specs, 1<<_S_cond, s, flags, sanitize)
					if err != nil {
						s.LeaveDepth(2)
						return buf, err
					}
				}
			}
			s.LeaveDepth(2)
			buf = append(buf, '}', ']')
		case ir.OP_slice_eface:
			items := *(*[]any)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			if !s.EnterDepth(1) {
				return buf, vars.ERR_too_deep
			}
			buf = append(buf, '[')
			for idx, elem := range items {
				if idx > 0 {
					buf = append(buf, ',')
				}
				if err := encodeAnyValue(&buf, elem, s, flags); err != nil {
					s.LeaveDepth(1)
					return buf, err
				}
			}
			s.LeaveDepth(1)
			buf = append(buf, ']')
		case ir.OP_slice_i64:
			items := *(*[]int64)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = append(buf, '[')
			buf = alg.I64toa(buf, items[0])
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.I64toa(buf, items[idx])
			}
			buf = append(buf, ']')
		case ir.OP_slice_str:
			items := *(*[]string)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = append(buf, '[')
			buf = alg.QuoteFlags(buf, items[0], false, sanitize)
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.QuoteFlags(buf, items[idx], false, sanitize)
			}
			buf = append(buf, ']')
		case ir.OP_slice_bool:
			items := *(*[]bool)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendBoolSliceBody(buf, items)
		case ir.OP_f64_slice:
			items := *(*[]float64)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			var err error
			buf, err = appendFloat64Slice(buf, items, flags)
			if err != nil {
				return buf, err
			}
		case ir.OP_f32_slice:
			items := *(*[]float32)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			var err error
			buf, err = appendFloat32Slice(buf, items, flags)
			if err != nil {
				return buf, err
			}
		case ir.OP_slice_i32:
			items := *(*[]int32)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendI32SliceBody(buf, items)
		case ir.OP_slice_u64:
			items := *(*[]uint64)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendU64SliceBody(buf, items)
		case ir.OP_slice_u32:
			items := *(*[]uint32)(fp)
			if len(items) == 0 {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), items != nil, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = appendU32SliceBody(buf, items)
		case ir.OP_bin:
			items := *(*[]byte)(fp)
			if items == nil {
				buf = appendKeyAndEmptySlice(buf, (*byte)(spec.Ptr), int(spec.KeyLen), false, flags)
				continue
			}
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = rt.EncodeBase64(buf, items)
		case ir.OP_f64:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			v := *(*float64)(fp)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, vars.ERR_nan_or_infinite
			}
			buf = alg.F64toa(buf, v)
		case ir.OP_f32:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			v := *(*float32)(fp)
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, vars.ERR_nan_or_infinite
			}
			buf = alg.F32toa(buf, v)
		case ir.OP_i32:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), int64(*(*int32)(fp)))
		case ir.OP_i16:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), int64(*(*int16)(fp)))
		case ir.OP_i8:
			buf = alg.AppendPaddedKeyI64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), int64(*(*int8)(fp)))
		case ir.OP_u64:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), *(*uint64)(fp))
		case ir.OP_u32:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), uint64(*(*uint32)(fp)))
		case ir.OP_u16:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), uint64(*(*uint16)(fp)))
		case ir.OP_u8:
			buf = alg.AppendPaddedKeyU64(buf, (*byte)(spec.Ptr), int(spec.KeyLen), uint64(*(*uint8)(fp)))
		case ir.OP_quote:
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			buf = alg.QuoteFlags(buf, *(*string)(fp), true, sanitize)
		case ir.OP_number:
			v := *(*json.Number)(fp)
			buf = appendPaddedKey(buf, (*byte)(spec.Ptr), int(spec.KeyLen))
			if v == "" {
				buf = append(buf, '0')
			} else if !alg.IsValidNumber(string(v)) {
				return buf, vars.Error_number(v)
			} else {
				buf = append(buf, v...)
			}
		}
	}
	return buf, nil
}

func encodeStructFieldsDefault(buf []byte, base unsafe.Pointer, specs []ir.StructFieldSpec, f uint64, s *vars.Stack, flags uint64) ([]byte, uint64, error) {
	for i := range specs {
		spec := &specs[i]
		fp := unsafe.Add(base, spec.Offset)
		sflags := spec.Flags
		if sflags&ir.SpecFlagIsPtr != 0 {
			fp = *(*unsafe.Pointer)(fp)
			if fp == nil {
				if sflags&ir.SpecFlagOmitNil != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndNull(buf, keyPtr, keyLen)
				continue
			}
		}
		switch spec.Kind {
		case ir.OP_str:
			v := *(*string)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.QuoteWithPaddedKey(buf, keyPtr, keyLen, v)
		case ir.OP_i64:
			v := *(*int64)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, v)
		case ir.OP_bool:
			v := *(*bool)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && !v {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendKeyAndBool(buf, keyPtr, keyLen, v)
		case ir.OP_eface:
			val := *(*any)(fp)
			if val == nil {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndNull(buf, keyPtr, keyLen)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if err := encodeAnyValue(&buf, val, s, flags); err != nil {
				return buf, f, err
			}
		case ir.OP_struct_begin:
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			f |= 1 << _S_cond
		case ir.OP_struct_end:
			f &^= 1 << _S_cond
			buf = append(buf, '}')
		case ir.OP_slice_struct:
			sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
			rv := reflect.NewAt(sliceSpec.SliceType, fp).Elem()
			n := rv.Len()
			if n == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := sliceSpec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, !rv.IsNil(), flags)
				continue
			}
			keyPtr, keyLen := sliceSpec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.EnterDepth(2) {
				return buf, f, vars.ERR_too_deep
			}
			dataPtr := rv.UnsafePointer()
			startOff := len(buf)
			buf = append(buf, '[', '{')
			var err error
			if sliceSpec.IsDense {
				buf, err = encodeStructFieldsDense(buf, dataPtr, sliceSpec.DenseSpecs, s, flags)
				if err != nil {
					s.LeaveDepth(2)
					return buf, f, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, err = encodeStructFieldsDense(buf, elemPtr, sliceSpec.DenseSpecs, s, flags)
					if err != nil {
						s.LeaveDepth(2)
						return buf, f, err
					}
				}
			} else {
				buf, _, err = encodeStructFieldsDefault(buf, dataPtr, sliceSpec.Specs, 1<<_S_cond, s, flags)
				if err != nil {
					s.LeaveDepth(2)
					return buf, f, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, _, err = encodeStructFieldsDefault(buf, elemPtr, sliceSpec.Specs, 1<<_S_cond, s, flags)
					if err != nil {
						s.LeaveDepth(2)
						return buf, f, err
					}
				}
			}
			s.LeaveDepth(2)
			buf = append(buf, '}', ']')
		case ir.OP_slice_eface:
			items := *(*[]any)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.EnterDepth(1) {
				return buf, f, vars.ERR_too_deep
			}
			buf = append(buf, '[')
			for idx, elem := range items {
				if idx > 0 {
					buf = append(buf, ',')
				}
				if err := encodeAnyValue(&buf, elem, s, flags); err != nil {
					s.LeaveDepth(1)
					return buf, f, err
				}
			}
			s.LeaveDepth(1)
			buf = append(buf, ']')
		case ir.OP_slice_i64:
			items := *(*[]int64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = append(buf, '[')
			buf = alg.I64toa(buf, items[0])
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.I64toa(buf, items[idx])
			}
			buf = append(buf, ']')
		case ir.OP_slice_str:
			items := *(*[]string)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = append(buf, '[')
			buf = alg.Quote(buf, items[0], false)
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.Quote(buf, items[idx], false)
			}
			buf = append(buf, ']')
		case ir.OP_slice_bool:
			items := *(*[]bool)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendBoolSliceBody(buf, items)
		case ir.OP_f64_slice:
			items := *(*[]float64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			var err error
			buf, err = appendFloat64Slice(buf, items, flags)
			if err != nil {
				return buf, f, err
			}
		case ir.OP_f32_slice:
			items := *(*[]float32)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			var err error
			buf, err = appendFloat32Slice(buf, items, flags)
			if err != nil {
				return buf, f, err
			}
		case ir.OP_slice_i32:
			items := *(*[]int32)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendI32SliceBody(buf, items)
		case ir.OP_slice_u64:
			items := *(*[]uint64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendU64SliceBody(buf, items)
		case ir.OP_slice_u32:
			items := *(*[]uint32)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendU32SliceBody(buf, items)
		case ir.OP_bin:
			items := *(*[]byte)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(items) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			if items == nil {
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, false, flags)
				continue
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = rt.EncodeBase64(buf, items)
		case ir.OP_i32:
			v := *(*int32)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, int64(v))
		case ir.OP_i16:
			v := *(*int16)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, int64(v))
		case ir.OP_i8:
			v := *(*int8)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, int64(v))
		case ir.OP_u64:
			v := *(*uint64)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, v)
		case ir.OP_u32:
			v := *(*uint32)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_u16:
			v := *(*uint16)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_u8:
			v := *(*uint8)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_f64:
			if (sflags&ir.SpecFlagOmitEmpty != 0) && *(*uint64)(fp) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			v := *(*float64)(fp)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, f, vars.ERR_nan_or_infinite
			}
			buf = alg.F64toa(buf, v)
		case ir.OP_f32:
			if (sflags&ir.SpecFlagOmitEmpty != 0) && *(*uint32)(fp) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			v := *(*float32)(fp)
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, f, vars.ERR_nan_or_infinite
			}
			buf = alg.F32toa(buf, v)
		case ir.OP_quote:
			v := *(*string)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = alg.Quote(buf, v, true)
		case ir.OP_number:
			v := *(*json.Number)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if v == "" {
				buf = append(buf, '0')
			} else if !alg.IsValidNumber(string(v)) {
				return buf, f, vars.Error_number(v)
			} else {
				buf = append(buf, v...)
			}
		}
	}
	return buf, f, nil
}

func encodeStructFieldsSanitized(buf []byte, base unsafe.Pointer, specs []ir.StructFieldSpec, f uint64, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, uint64, error) {
	for i := range specs {
		spec := &specs[i]
		fp := unsafe.Add(base, spec.Offset)
		sflags := spec.Flags
		if sflags&ir.SpecFlagIsPtr != 0 {
			fp = *(*unsafe.Pointer)(fp)
			if fp == nil {
				if sflags&ir.SpecFlagOmitNil != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndNull(buf, keyPtr, keyLen)
				continue
			}
		}
		switch spec.Kind {
		case ir.OP_str:
			v := *(*string)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.QuoteWithPaddedKeyFlags(buf, keyPtr, keyLen, v, sanitize)
		case ir.OP_i64:
			v := *(*int64)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, v)
		case ir.OP_bool:
			v := *(*bool)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && !v {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendKeyAndBool(buf, keyPtr, keyLen, v)
		case ir.OP_eface:
			val := *(*any)(fp)
			if val == nil {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndNull(buf, keyPtr, keyLen)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if err := encodeAnyValue(&buf, val, s, flags); err != nil {
				return buf, f, err
			}
		case ir.OP_struct_begin:
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			f |= 1 << _S_cond
		case ir.OP_struct_end:
			f &^= 1 << _S_cond
			buf = append(buf, '}')
		case ir.OP_slice_struct:
			sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
			rv := reflect.NewAt(sliceSpec.SliceType, fp).Elem()
			n := rv.Len()
			if n == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := sliceSpec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, !rv.IsNil(), flags)
				continue
			}
			keyPtr, keyLen := sliceSpec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.EnterDepth(2) {
				return buf, f, vars.ERR_too_deep
			}
			dataPtr := rv.UnsafePointer()
			startOff := len(buf)
			buf = append(buf, '[', '{')
			var err error
			if sliceSpec.IsDense {
				buf, err = encodeStructFieldsSanitizedDense(buf, dataPtr, sliceSpec.DenseSpecs, s, flags, sanitize)
				if err != nil {
					s.LeaveDepth(2)
					return buf, f, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, err = encodeStructFieldsSanitizedDense(buf, elemPtr, sliceSpec.DenseSpecs, s, flags, sanitize)
					if err != nil {
						s.LeaveDepth(2)
						return buf, f, err
					}
				}
			} else {
				buf, _, err = encodeStructFieldsSanitized(buf, dataPtr, sliceSpec.Specs, 1<<_S_cond, s, flags, sanitize)
				if err != nil {
					s.LeaveDepth(2)
					return buf, f, err
				}
				buf = growSliceBufAfterFirst(buf, startOff, n)
				for idx := 1; idx < n; idx++ {
					elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
					buf = append(buf, '}', ',', '{')
					buf, _, err = encodeStructFieldsSanitized(buf, elemPtr, sliceSpec.Specs, 1<<_S_cond, s, flags, sanitize)
					if err != nil {
						s.LeaveDepth(2)
						return buf, f, err
					}
				}
			}
			s.LeaveDepth(2)
			buf = append(buf, '}', ']')
		case ir.OP_slice_eface:
			items := *(*[]any)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.EnterDepth(1) {
				return buf, f, vars.ERR_too_deep
			}
			buf = append(buf, '[')
			for idx, elem := range items {
				if idx > 0 {
					buf = append(buf, ',')
				}
				if err := encodeAnyValue(&buf, elem, s, flags); err != nil {
					s.LeaveDepth(1)
					return buf, f, err
				}
			}
			s.LeaveDepth(1)
			buf = append(buf, ']')
		case ir.OP_slice_i64:
			items := *(*[]int64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = append(buf, '[')
			buf = alg.I64toa(buf, items[0])
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.I64toa(buf, items[idx])
			}
			buf = append(buf, ']')
		case ir.OP_slice_str:
			items := *(*[]string)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = append(buf, '[')
			buf = alg.QuoteFlags(buf, items[0], false, sanitize)
			for idx := 1; idx < len(items); idx++ {
				buf = append(buf, ',')
				buf = alg.QuoteFlags(buf, items[idx], false, sanitize)
			}
			buf = append(buf, ']')
		case ir.OP_slice_bool:
			items := *(*[]bool)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendBoolSliceBody(buf, items)
		case ir.OP_f64_slice:
			items := *(*[]float64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			var err error
			buf, err = appendFloat64Slice(buf, items, flags)
			if err != nil {
				return buf, f, err
			}
		case ir.OP_f32_slice:
			items := *(*[]float32)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			var err error
			buf, err = appendFloat32Slice(buf, items, flags)
			if err != nil {
				return buf, f, err
			}
		case ir.OP_slice_i32:
			items := *(*[]int32)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendI32SliceBody(buf, items)
		case ir.OP_slice_u64:
			items := *(*[]uint64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendU64SliceBody(buf, items)
		case ir.OP_slice_u32:
			items := *(*[]uint32)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, items != nil, flags)
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = appendU32SliceBody(buf, items)
		case ir.OP_bin:
			items := *(*[]byte)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(items) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			if items == nil {
				buf = appendKeyAndEmptySlice(buf, keyPtr, keyLen, false, flags)
				continue
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = rt.EncodeBase64(buf, items)
		case ir.OP_i32:
			v := *(*int32)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, int64(v))
		case ir.OP_i16:
			v := *(*int16)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, int64(v))
		case ir.OP_i8:
			v := *(*int8)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyI64(buf, keyPtr, keyLen, int64(v))
		case ir.OP_u64:
			v := *(*uint64)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, v)
		case ir.OP_u32:
			v := *(*uint32)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_u16:
			v := *(*uint16)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_u8:
			v := *(*uint8)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_f64:
			if (sflags&ir.SpecFlagOmitEmpty != 0) && *(*uint64)(fp) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			v := *(*float64)(fp)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, f, vars.ERR_nan_or_infinite
			}
			buf = alg.F64toa(buf, v)
		case ir.OP_f32:
			if (sflags&ir.SpecFlagOmitEmpty != 0) && *(*uint32)(fp) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			v := *(*float32)(fp)
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return buf, f, vars.ERR_nan_or_infinite
			}
			buf = alg.F32toa(buf, v)
		case ir.OP_quote:
			v := *(*string)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			buf = alg.QuoteFlags(buf, v, true, sanitize)
		case ir.OP_number:
			v := *(*json.Number)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := (*byte)(spec.Ptr), int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if v == "" {
				buf = append(buf, '0')
			} else if !alg.IsValidNumber(string(v)) {
				return buf, f, vars.Error_number(v)
			} else {
				buf = append(buf, v...)
			}
		}
	}
	return buf, f, nil
}
