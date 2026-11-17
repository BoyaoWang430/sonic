//go:generate go run gen_struct_fields.go

package vm

import (
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

// inspectTypedSlice accesses an arbitrary struct/ptr-struct slice field via its
// exact reflect.Type without mirroring Go's internal slice header layout.
func inspectTypedSlice(sliceType reflect.Type, fp unsafe.Pointer) (dataPtr unsafe.Pointer, length int, nonNil bool) {
	rv := reflect.NewAt(sliceType, fp).Elem()
	length = rv.Len()
	if length == 0 {
		return nil, 0, !rv.IsNil()
	}
	return rv.UnsafePointer(), length, true
}

func encodeStructFieldsDispatch(buf []byte, ptr unsafe.Pointer, specs []ir.StructFieldSpec, denseSpecs []ir.StructFieldSpec, isDense bool, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	if sanitize == 0 {
		if isDense {
			return encodeStructFieldsDense(buf, ptr, denseSpecs, s, flags)
		}
		var err error
		buf, _, err = encodeStructFieldsDefault(buf, ptr, specs, 1<<_S_cond, s, flags)
		return buf, err
	}
	if isDense {
		return encodeStructFieldsSanitizedDense(buf, ptr, denseSpecs, s, flags, sanitize)
	}
	var err error
	buf, _, err = encodeStructFieldsSanitized(buf, ptr, specs, 1<<_S_cond, s, flags, sanitize)
	return buf, err
}

func encodePtrStructBody(buf []byte, subPtr unsafe.Pointer, ptrSpec *ir.StructPtrSpec, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	if !s.EnterDepth(1) {
		return buf, vars.ERR_too_deep
	}
	buf = append(buf, '{')
	var err error
	buf, err = encodeStructFieldsDispatch(buf, subPtr, ptrSpec.Specs, ptrSpec.DenseSpecs, ptrSpec.IsDense, s, flags, sanitize)
	s.LeaveDepth(1)
	if err != nil {
		return buf, err
	}
	return append(buf, '}'), nil
}

// encodePtrStructDenseField encodes an unconditional *Struct field.
// Kept out-of-line so the dense field loops stay compact in L1i cache.
//
//go:noinline
func encodePtrStructDenseField(buf []byte, fp unsafe.Pointer, spec *ir.StructFieldSpec, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	ptrSpec := (*ir.StructPtrSpec)(spec.Ptr)
	subPtr := *(*unsafe.Pointer)(fp)
	if subPtr == nil {
		return appendKeyAndNull(buf, ptrSpec.KeyPtr, int(spec.KeyLen)), nil
	}
	buf = appendPaddedKey(buf, ptrSpec.KeyPtr, int(spec.KeyLen))
	return encodePtrStructBody(buf, subPtr, ptrSpec, s, flags, sanitize)
}

// encodePtrStructCondField encodes a conditional/omittable *Struct field.
// Kept out-of-line so the conditional field loops stay compact in L1i cache.
//
//go:noinline
func encodePtrStructCondField(buf []byte, fp unsafe.Pointer, spec *ir.StructFieldSpec, f uint64, sflags uint8, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, uint64, error) {
	ptrSpec := (*ir.StructPtrSpec)(spec.Ptr)
	subPtr := *(*unsafe.Pointer)(fp)
	if subPtr == nil && sflags&ir.SpecFlagOmitNil != 0 {
		return buf, f, nil
	}
	keyPtr, keyLen := ptrSpec.KeyPtr, int(spec.KeyLen)
	if f&(1<<_S_cond) != 0 {
		f &^= 1 << _S_cond
		keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
	}
	if subPtr == nil {
		return appendKeyAndNull(buf, keyPtr, keyLen), f, nil
	}
	buf = appendPaddedKey(buf, keyPtr, keyLen)
	var err error
	buf, err = encodePtrStructBody(buf, subPtr, ptrSpec, s, flags, sanitize)
	return buf, f, err
}

// encodeStructSliceDenseField encodes an unconditional []Struct field.
// Kept out-of-line so the dense field loops stay compact in L1i cache.
//
//go:noinline
func encodeStructSliceDenseField(buf []byte, fp unsafe.Pointer, spec *ir.StructFieldSpec, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
	dataPtr, n, nonNil := inspectTypedSlice(sliceSpec.SliceType, fp)
	if n == 0 {
		return appendKeyAndEmptySlice(buf, sliceSpec.KeyPtr, int(spec.KeyLen), nonNil, flags), nil
	}
	buf = appendPaddedKey(buf, sliceSpec.KeyPtr, int(spec.KeyLen))
	return encodeStructSliceBody(buf, dataPtr, n, sliceSpec, s, flags, sanitize)
}

// encodeStructSliceCondField encodes a conditional/omittable []Struct field.
// Kept out-of-line so the conditional field loops stay compact in L1i cache.
//
//go:noinline
func encodeStructSliceCondField(buf []byte, fp unsafe.Pointer, spec *ir.StructFieldSpec, f uint64, sflags uint8, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, uint64, error) {
	sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
	dataPtr, n, nonNil := inspectTypedSlice(sliceSpec.SliceType, fp)
	if n == 0 && sflags&ir.SpecFlagOmitEmpty != 0 {
		return buf, f, nil
	}
	keyPtr, keyLen := sliceSpec.KeyPtr, int(spec.KeyLen)
	if f&(1<<_S_cond) != 0 {
		f &^= 1 << _S_cond
		keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
	}
	if n == 0 {
		return appendKeyAndEmptySlice(buf, keyPtr, keyLen, nonNil, flags), f, nil
	}
	buf = appendPaddedKey(buf, keyPtr, keyLen)
	var err error
	buf, err = encodeStructSliceBody(buf, dataPtr, n, sliceSpec, s, flags, sanitize)
	return buf, f, err
}

func encodeStructSliceBody(buf []byte, dataPtr unsafe.Pointer, n int, sliceSpec *ir.StructSliceSpec, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	if !s.EnterDepth(2) {
		return buf, vars.ERR_too_deep
	}
	buf = reserveSliceMinCap(buf, n, sliceSpec.MinElemBytes)
	buf = append(buf, '[', '{')
	var err error
	buf, err = encodeStructFieldsDispatch(buf, dataPtr, sliceSpec.Specs, sliceSpec.DenseSpecs, sliceSpec.IsDense, s, flags, sanitize)
	if err != nil {
		s.LeaveDepth(2)
		return buf, err
	}
	for idx := 1; idx < n; idx++ {
		elemPtr := unsafe.Add(dataPtr, uintptr(idx)*sliceSpec.ElemSize)
		buf = append(buf, '}', ',', '{')
		buf, err = encodeStructFieldsDispatch(buf, elemPtr, sliceSpec.Specs, sliceSpec.DenseSpecs, sliceSpec.IsDense, s, flags, sanitize)
		if err != nil {
			s.LeaveDepth(2)
			return buf, err
		}
	}
	s.LeaveDepth(2)
	return append(buf, '}', ']'), nil
}

// encodePtrStructSliceDenseField encodes an unconditional []*Struct field.
// Kept out-of-line so the dense field loops stay compact in L1i cache.
//
//go:noinline
func encodePtrStructSliceDenseField(buf []byte, fp unsafe.Pointer, spec *ir.StructFieldSpec, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
	dataPtr, n, nonNil := inspectTypedSlice(sliceSpec.SliceType, fp)
	if n == 0 {
		return appendKeyAndEmptySlice(buf, sliceSpec.KeyPtr, int(spec.KeyLen), nonNil, flags), nil
	}
	buf = appendPaddedKey(buf, sliceSpec.KeyPtr, int(spec.KeyLen))
	return encodePtrStructSliceBody(buf, dataPtr, n, sliceSpec.Specs, sliceSpec.DenseSpecs, sliceSpec.IsDense, s, flags, sanitize)
}

// encodePtrStructSliceCondField encodes a conditional/omittable []*Struct field.
// Kept out-of-line so the conditional field loops stay compact in L1i cache.
//
//go:noinline
func encodePtrStructSliceCondField(buf []byte, fp unsafe.Pointer, spec *ir.StructFieldSpec, f uint64, sflags uint8, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, uint64, error) {
	sliceSpec := (*ir.StructSliceSpec)(spec.Ptr)
	dataPtr, n, nonNil := inspectTypedSlice(sliceSpec.SliceType, fp)
	if n == 0 && sflags&ir.SpecFlagOmitEmpty != 0 {
		return buf, f, nil
	}
	keyPtr, keyLen := sliceSpec.KeyPtr, int(spec.KeyLen)
	if f&(1<<_S_cond) != 0 {
		f &^= 1 << _S_cond
		keyPtr, keyLen = spec.FirstSliceKey(keyPtr)
	}
	if n == 0 {
		return appendKeyAndEmptySlice(buf, keyPtr, keyLen, nonNil, flags), f, nil
	}
	buf = appendPaddedKey(buf, keyPtr, keyLen)
	var err error
	buf, err = encodePtrStructSliceBody(buf, dataPtr, n, sliceSpec.Specs, sliceSpec.DenseSpecs, sliceSpec.IsDense, s, flags, sanitize)
	return buf, f, err
}

func encodePtrStructSliceBody(buf []byte, dataPtr unsafe.Pointer, n int, specs []ir.StructFieldSpec, denseSpecs []ir.StructFieldSpec, isDense bool, s *vars.Stack, flags uint64, sanitize uint64) ([]byte, error) {
	if !s.EnterDepth(2) {
		return buf, vars.ERR_too_deep
	}
	ptrs := unsafe.Slice((*unsafe.Pointer)(dataPtr), n)
	buf = reserveSliceMinCap(buf, n, ir.MinPtrSliceElemBytes)
	buf = append(buf, '[')
	var err error
	for idx, elemPtr := range ptrs {
		if idx > 0 {
			buf = append(buf, ',')
		}
		if elemPtr == nil {
			buf = append(buf, 'n', 'u', 'l', 'l')
			continue
		}
		buf = append(buf, '{')
		buf, err = encodeStructFieldsDispatch(buf, elemPtr, specs, denseSpecs, isDense, s, flags, sanitize)
		if err != nil {
			s.LeaveDepth(2)
			return buf, err
		}
		buf = append(buf, '}')
	}
	s.LeaveDepth(2)
	return append(buf, ']'), nil
}

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
