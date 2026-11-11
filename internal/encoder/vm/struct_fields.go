package vm

import (
	"encoding/json"
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

func encodePureStructRoot(b *[]byte, ptr unsafe.Pointer, specs []ir.StructFieldSpec, s *vars.Stack, flags uint64) error {
	if !s.Save(0, 0, nil, nil) {
		return vars.ERR_too_deep
	}
	buf := append(*b, '{')
	var err error
	if sanitize := flags & alg.SanitizeMask; sanitize == 0 {
		buf, _, err = encodeStructFieldsDefault(buf, ptr, specs, 1<<_S_cond, s, flags)
	} else {
		buf, _, err = encodeStructFieldsSanitized(buf, ptr, specs, 1<<_S_cond, s, flags, sanitize)
	}
	s.Drop()
	if err != nil {
		return err
	}
	*b = append(buf, '}')
	return nil
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
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				buf = append(buf, 'n', 'u', 'l', 'l')
				continue
			}
		}
		switch spec.Kind {
		case ir.OP_str:
			v := *(*string)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			if cap(buf)-len(buf) >= 40 {
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
					buf = buf[:len(buf)+keyLen+4]
				} else {
					*(*[5]byte)(vp) = [5]byte{'f', 'a', 'l', 's', 'e'}
					buf = buf[:len(buf)+keyLen+5]
				}
			} else {
				buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
				buf = appendBool(buf, v)
			}
		case ir.OP_eface:
			val := *(*any)(fp)
			if val == nil {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				if cap(buf)-len(buf) >= 36 {
					dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
					dk := (*[4]uint64)(dp)
					sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
					dk[0], dk[1] = sk[0], sk[1]
					if keyLen > 16 {
						dk[2], dk[3] = sk[2], sk[3]
					}
					*(*[4]byte)(unsafe.Add(dp, keyLen)) = [4]byte{'n', 'u', 'l', 'l'}
					buf = buf[:len(buf)+keyLen+4]
				} else {
					buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if err := encodeAnyValue(&buf, val, s, flags); err != nil {
				return buf, f, err
			}
		case ir.OP_struct_begin:
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			f |= 1 << _S_cond
		case ir.OP_struct_end:
			f &^= 1 << _S_cond
			buf = append(buf, '}')
		case ir.OP_slice_struct:
			sliceSpec := ir.GetStructSliceSpec(spec.Aux)
			rv := reflect.NewAt(sliceSpec.SliceType, fp).Elem()
			n := rv.Len()
			if n == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if !rv.IsNil() || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.Save(0, 0, nil, nil) || !s.Save(0, 0, nil, nil) {
				return buf, f, vars.ERR_too_deep
			}
			dataPtr := rv.UnsafePointer()
			buf = append(buf, '[', '{')
			var err error
			buf, _, err = encodeStructFieldsDefault(buf, dataPtr, sliceSpec.Specs, 1<<_S_cond, s, flags)
			if err != nil {
				s.Drop()
				s.Drop()
				return buf, f, err
			}
			for idx := 1; idx < n; idx++ {
				elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
				buf = append(buf, '}', ',', '{')
				buf, _, err = encodeStructFieldsDefault(buf, elemPtr, sliceSpec.Specs, 1<<_S_cond, s, flags)
				if err != nil {
					s.Drop()
					s.Drop()
					return buf, f, err
				}
			}
			s.Drop()
			s.Drop()
			buf = append(buf, '}', ']')
		case ir.OP_slice_eface:
			items := *(*[]any)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if items != nil || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.Save(0, 0, nil, nil) {
				return buf, f, vars.ERR_too_deep
			}
			buf = append(buf, '[')
			for idx, elem := range items {
				if idx > 0 {
					buf = append(buf, ',')
				}
				if err := encodeAnyValue(&buf, elem, s, flags); err != nil {
					s.Drop()
					return buf, f, err
				}
			}
			s.Drop()
			buf = append(buf, ']')
		case ir.OP_slice_i64:
			items := *(*[]int64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if items != nil || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if items != nil || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
		case ir.OP_i32:
			v := *(*int32)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_f64:
			if (sflags&ir.SpecFlagOmitEmpty != 0) && *(*uint64)(fp) == 0 {
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				buf = append(buf, 'n', 'u', 'l', 'l')
				continue
			}
		}
		switch spec.Kind {
		case ir.OP_str:
			v := *(*string)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && len(v) == 0 {
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			if cap(buf)-len(buf) >= 40 {
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
					buf = buf[:len(buf)+keyLen+4]
				} else {
					*(*[5]byte)(vp) = [5]byte{'f', 'a', 'l', 's', 'e'}
					buf = buf[:len(buf)+keyLen+5]
				}
			} else {
				buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
				buf = appendBool(buf, v)
			}
		case ir.OP_eface:
			val := *(*any)(fp)
			if val == nil {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				if cap(buf)-len(buf) >= 36 {
					dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
					dk := (*[4]uint64)(dp)
					sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
					dk[0], dk[1] = sk[0], sk[1]
					if keyLen > 16 {
						dk[2], dk[3] = sk[2], sk[3]
					}
					*(*[4]byte)(unsafe.Add(dp, keyLen)) = [4]byte{'n', 'u', 'l', 'l'}
					buf = buf[:len(buf)+keyLen+4]
				} else {
					buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if err := encodeAnyValue(&buf, val, s, flags); err != nil {
				return buf, f, err
			}
		case ir.OP_struct_begin:
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			f |= 1 << _S_cond
		case ir.OP_struct_end:
			f &^= 1 << _S_cond
			buf = append(buf, '}')
		case ir.OP_slice_struct:
			sliceSpec := ir.GetStructSliceSpec(spec.Aux)
			rv := reflect.NewAt(sliceSpec.SliceType, fp).Elem()
			n := rv.Len()
			if n == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if !rv.IsNil() || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.Save(0, 0, nil, nil) || !s.Save(0, 0, nil, nil) {
				return buf, f, vars.ERR_too_deep
			}
			dataPtr := rv.UnsafePointer()
			buf = append(buf, '[', '{')
			var err error
			buf, _, err = encodeStructFieldsSanitized(buf, dataPtr, sliceSpec.Specs, 1<<_S_cond, s, flags, sanitize)
			if err != nil {
				s.Drop()
				s.Drop()
				return buf, f, err
			}
			for idx := 1; idx < n; idx++ {
				elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
				buf = append(buf, '}', ',', '{')
				buf, _, err = encodeStructFieldsSanitized(buf, elemPtr, sliceSpec.Specs, 1<<_S_cond, s, flags, sanitize)
				if err != nil {
					s.Drop()
					s.Drop()
					return buf, f, err
				}
			}
			s.Drop()
			s.Drop()
			buf = append(buf, '}', ']')
		case ir.OP_slice_eface:
			items := *(*[]any)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if items != nil || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = appendPaddedKey(buf, keyPtr, keyLen)
			if !s.Save(0, 0, nil, nil) {
				return buf, f, vars.ERR_too_deep
			}
			buf = append(buf, '[')
			for idx, elem := range items {
				if idx > 0 {
					buf = append(buf, ',')
				}
				if err := encodeAnyValue(&buf, elem, s, flags); err != nil {
					s.Drop()
					return buf, f, err
				}
			}
			s.Drop()
			buf = append(buf, ']')
		case ir.OP_slice_i64:
			items := *(*[]int64)(fp)
			if len(items) == 0 {
				if sflags&ir.SpecFlagOmitEmpty != 0 {
					continue
				}
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if items != nil || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
				keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
				if f&(1<<_S_cond) != 0 {
					f &^= 1 << _S_cond
					keyPtr, keyLen = spec.FirstKey()
				}
				buf = appendPaddedKey(buf, keyPtr, keyLen)
				if items != nil || flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, "null"...)
				}
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
		case ir.OP_i32:
			v := *(*int32)(fp)
			if (sflags&ir.SpecFlagOmitEmpty != 0) && v == 0 {
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
			if f&(1<<_S_cond) != 0 {
				f &^= 1 << _S_cond
				keyPtr, keyLen = spec.FirstKey()
			}
			buf = alg.AppendPaddedKeyU64(buf, keyPtr, keyLen, uint64(v))
		case ir.OP_f64:
			if (sflags&ir.SpecFlagOmitEmpty != 0) && *(*uint64)(fp) == 0 {
				continue
			}
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
			keyPtr, keyLen := spec.KeyPtr, int(spec.KeyLen)
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
