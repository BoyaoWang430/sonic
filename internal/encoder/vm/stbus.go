/**
 * Copyright 2024 ByteDance Inc.
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

package vm

import (
	"encoding/json"
	"math"
	"reflect"
	"runtime"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/prim"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/rt"
)

var (
	compiler            func(reflect.Type, ...interface{}) (interface{}, error)
	rootPlanCache       = caching.CreateTypedCache[*rootEncPlan]()
	rootPlanCacheOmitNl = caching.CreateTypedCache[*rootEncPlan]()
)

func init() {
	vars.RegisterCacheReset(func() {
		rootPlanCache.ResetForDiagnostic()
		rootPlanCacheOmitNl.ResetForDiagnostic()
	})
}

func SetCompiler(c func(reflect.Type, ...interface{}) (interface{}, error)) {
	compiler = c
}

// EncodeValuePointer encodes the value of type vt stored at p.
func EncodeValuePointer(buf *[]byte, vt reflect.Type, p unsafe.Pointer, sb *vars.Stack, fv uint64) error {
	if vt == nil {
		return prim.EncodeNil(buf)
	}
	omitNull := (fv & (1 << alg.BitOnlyOmitNull)) != 0
	pp, err := vars.FindOrCompileMode(vt, (fv&(1<<alg.BitPointerValue)) != 0, omitNull, compiler)
	if err != nil {
		return err
	}
	ce := pp.(*ir.CompiledEncoder)
	return Execute(buf, p, sb, fv, &ce.Program)
}

type rootEncPlan struct {
	prog           *ir.Program
	boxPool        *sync.Pool
	pureSpecs      []ir.StructFieldSpec
	denseSpecs     []ir.StructFieldSpec
	elemSize       uintptr
	minElemBytes   int
	builtinMapKind int
	bodyStartPC    int
	bodyEndPC      int
	isDirectPtr    bool
	isDirectMap    bool
	hasPureSpecs   bool
	hasDenseSpecs  bool
	isPureSlice    bool
	isPurePtrSlice bool
	boxHasPtrs     bool
}

func typeHasPointers(vt reflect.Type) bool {
	switch vt.Kind() {
	case reflect.Ptr, reflect.UnsafePointer, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface, reflect.Slice, reflect.String:
		return true
	case reflect.Array:
		return vt.Len() > 0 && typeHasPointers(vt.Elem())
	case reflect.Struct:
		for i := 0; i < vt.NumField(); i++ {
			if typeHasPointers(vt.Field(i).Type) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// maxSliceMinReserve bounds ahead-of-time minimum reservation for pure struct
// slices (separate from option.LimitBufferSize = 1 MiB pool retention cap;
// buffers > 1 MiB are handed directly to the caller on success and never pooled).
// Capacity calculation uses only compile-time provable lower bounds per element
// (never extrapolating from runtime elements or pre-executing callbacks) and
// checks integer overflow before multiplying or adding.
const maxSliceMinReserve = 4 << 20

func reserveSliceMinCap(buf []byte, n int, minElemBytes int) []byte {
	if n <= 1 || minElemBytes <= 0 {
		return buf
	}
	rem := n
	if maxElems := maxSliceMinReserve / minElemBytes; rem > maxElems {
		rem = maxElems
	}
	if rem <= 0 || rem > (math.MaxInt-16)/minElemBytes {
		return buf
	}
	extra := rem * minElemBytes
	if extra <= 0 || len(buf) > math.MaxInt-extra-16 {
		return buf
	}
	need := len(buf) + extra + 16
	if need > cap(buf) {
		grown := make([]byte, len(buf), need)
		copy(grown, buf)
		return grown
	}
	return buf
}

// growSliceRemainingCap grows buf by 1.5x plus the compile-time provable minimum
// bytes for the remaining slice elements (clamped to maxSliceMinReserve) when
// available capacity runs low mid-slice, avoiding repeated 1.25x Go append steps
// on multi-MiB outputs without extrapolating from runtime element sizes.
func growSliceRemainingCap(buf []byte, rem int, minElemBytes int) []byte {
	if rem <= 0 || minElemBytes <= 0 {
		return buf
	}
	if maxElems := maxSliceMinReserve / minElemBytes; rem > maxElems {
		rem = maxElems
	}
	if rem <= 0 || rem > (math.MaxInt-64)/minElemBytes {
		return buf
	}
	extra := rem*minElemBytes + 64
	l := len(buf)
	if l > math.MaxInt-extra {
		return buf
	}
	need := l + extra
	half := cap(buf) >> 1
	if half <= math.MaxInt-need {
		need += half
	} else {
		need = math.MaxInt
	}
	if need > cap(buf) {
		grown := make([]byte, l, need)
		copy(grown, buf)
		return grown
	}
	return buf
}

func encodePureStructSliceRoot(b *[]byte, rv reflect.Value, elemSize uintptr, minElemBytes int, specs []ir.StructFieldSpec, denseSpecs []ir.StructFieldSpec, hasDense bool, s *vars.Stack, flags uint64) error {
	if rv.IsNil() {
		if flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
			*b = append(*b, '[', ']')
		} else {
			*b = append(*b, 'n', 'u', 'l', 'l')
		}
		return nil
	}
	n := rv.Len()
	if n == 0 {
		*b = append(*b, '[', ']')
		return nil
	}
	if !s.EnterDepth(2) {
		return vars.ERR_too_deep
	}
	dataPtr := rv.UnsafePointer()
	buf := reserveSliceMinCap(*b, n, minElemBytes)
	buf = append(buf, '[', '{')
	lowWater := minElemBytes + 64
	var err error
	sanitize := flags & alg.SanitizeMask
	if sanitize == 0 {
		if hasDense {
			buf, err = encodeStructFieldsDense(buf, dataPtr, denseSpecs, s, flags)
			if err != nil {
				s.LeaveDepth(2)
				return err
			}
			for idx := 1; idx < n; idx++ {
				if cap(buf)-len(buf) < lowWater {
					buf = growSliceRemainingCap(buf, n-idx, minElemBytes)
				}
				elemPtr := unsafe.Add(dataPtr, uintptr(idx)*elemSize)
				buf = append(buf, '}', ',', '{')
				buf, err = encodeStructFieldsDense(buf, elemPtr, denseSpecs, s, flags)
				if err != nil {
					s.LeaveDepth(2)
					return err
				}
			}
		} else {
			buf, _, err = encodeStructFieldsDefault(buf, dataPtr, specs, 1<<_S_cond, s, flags)
			if err != nil {
				s.LeaveDepth(2)
				return err
			}
			for idx := 1; idx < n; idx++ {
				if cap(buf)-len(buf) < lowWater {
					buf = growSliceRemainingCap(buf, n-idx, minElemBytes)
				}
				elemPtr := unsafe.Add(dataPtr, uintptr(idx)*elemSize)
				buf = append(buf, '}', ',', '{')
				buf, _, err = encodeStructFieldsDefault(buf, elemPtr, specs, 1<<_S_cond, s, flags)
				if err != nil {
					s.LeaveDepth(2)
					return err
				}
			}
		}
	} else {
		if hasDense {
			buf, err = encodeStructFieldsSanitizedDense(buf, dataPtr, denseSpecs, s, flags, sanitize)
			if err != nil {
				s.LeaveDepth(2)
				return err
			}
			for idx := 1; idx < n; idx++ {
				if cap(buf)-len(buf) < lowWater {
					buf = growSliceRemainingCap(buf, n-idx, minElemBytes)
				}
				elemPtr := unsafe.Add(dataPtr, uintptr(idx)*elemSize)
				buf = append(buf, '}', ',', '{')
				buf, err = encodeStructFieldsSanitizedDense(buf, elemPtr, denseSpecs, s, flags, sanitize)
				if err != nil {
					s.LeaveDepth(2)
					return err
				}
			}
		} else {
			buf, _, err = encodeStructFieldsSanitized(buf, dataPtr, specs, 1<<_S_cond, s, flags, sanitize)
			if err != nil {
				s.LeaveDepth(2)
				return err
			}
			for idx := 1; idx < n; idx++ {
				if cap(buf)-len(buf) < lowWater {
					buf = growSliceRemainingCap(buf, n-idx, minElemBytes)
				}
				elemPtr := unsafe.Add(dataPtr, uintptr(idx)*elemSize)
				buf = append(buf, '}', ',', '{')
				buf, _, err = encodeStructFieldsSanitized(buf, elemPtr, specs, 1<<_S_cond, s, flags, sanitize)
				if err != nil {
					s.LeaveDepth(2)
					return err
				}
			}
		}
	}
	s.LeaveDepth(2)
	*b = append(buf, '}', ']')
	return nil
}

func encodePurePtrStructSliceRoot(b *[]byte, rv reflect.Value, specs []ir.StructFieldSpec, denseSpecs []ir.StructFieldSpec, hasDense bool, s *vars.Stack, flags uint64) error {
	if rv.IsNil() {
		if flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
			*b = append(*b, '[', ']')
		} else {
			*b = append(*b, 'n', 'u', 'l', 'l')
		}
		return nil
	}
	n := rv.Len()
	if n == 0 {
		*b = append(*b, '[', ']')
		return nil
	}
	out, err := encodePtrStructSliceBody(*b, rv.UnsafePointer(), n, specs, denseSpecs, hasDense, s, flags, flags&alg.SanitizeMask)
	if err != nil {
		return err
	}
	*b = out
	return nil
}

func newValueBoxPool(vt reflect.Type) *sync.Pool {
	return &sync.Pool{
		New: func() any {
			v := reflect.New(vt)
			return &v
		},
	}
}

func buildRootEncPlanDefault(vt reflect.Type) (*rootEncPlan, error) {
	return buildRootEncPlanForMode(vt, false)
}

func buildRootEncPlanOmitNull(vt reflect.Type) (*rootEncPlan, error) {
	return buildRootEncPlanForMode(vt, true)
}

func buildRootEncPlanForMode(vt reflect.Type, omitNull bool) (*rootEncPlan, error) {
	pp, err := vars.FindOrCompileMode(vt, false, omitNull, compiler)
	if err != nil {
		return nil, err
	}
	ce := pp.(*ir.CompiledEncoder)
	prog := &ce.Program
	spec := &ce.Root

	switch spec.Kind {
	case ir.RootDirectPtr:
		return &rootEncPlan{
			prog:          prog,
			pureSpecs:     spec.PureSpecs,
			denseSpecs:    spec.DenseSpecs,
			bodyStartPC:   spec.BodyStartPC,
			bodyEndPC:     spec.BodyEndPC,
			isDirectPtr:   true,
			hasPureSpecs:  spec.HasPureSpecs,
			hasDenseSpecs: spec.HasDenseSpecs,
		}, nil
	case ir.RootPureSlice:
		return &rootEncPlan{
			prog:          prog,
			pureSpecs:     spec.PureSpecs,
			denseSpecs:    spec.DenseSpecs,
			elemSize:      spec.ElemSize,
			minElemBytes:  spec.MinElemBytes,
			hasDenseSpecs: spec.HasDenseSpecs,
			isPureSlice:   true,
		}, nil
	case ir.RootPurePtrSlice:
		return &rootEncPlan{
			prog:           prog,
			pureSpecs:      spec.PureSpecs,
			denseSpecs:     spec.DenseSpecs,
			minElemBytes:   spec.MinElemBytes,
			hasDenseSpecs:  spec.HasDenseSpecs,
			isPurePtrSlice: true,
		}, nil
	case ir.RootBuiltinMap:
		return &rootEncPlan{
			prog:           prog,
			builtinMapKind: spec.BuiltinMapKind,
			isDirectMap:    true,
		}, nil
	case ir.RootPureStruct:
		return &rootEncPlan{
			prog:          prog,
			boxPool:       newValueBoxPool(vt),
			pureSpecs:     spec.PureSpecs,
			denseSpecs:    spec.DenseSpecs,
			hasPureSpecs:  true,
			hasDenseSpecs: spec.HasDenseSpecs,
			boxHasPtrs:    typeHasPointers(vt),
		}, nil
	default:
		var boxPool *sync.Pool
		var boxHasPtrs bool
		if vt.Kind() != reflect.Ptr && vt.Kind() != reflect.UnsafePointer {
			boxPool = newValueBoxPool(vt)
			boxHasPtrs = typeHasPointers(vt)
		}
		return &rootEncPlan{
			prog:       prog,
			boxPool:    boxPool,
			boxHasPtrs: boxHasPtrs,
		}, nil
	}
}

// EncodeValue encodes an arbitrary Go interface{} value using only public reflection and typed pointers.
func EncodeValue(buf *[]byte, val interface{}, sb *vars.Stack, fv uint64) error {
	switch v := val.(type) {
	case nil:
		return prim.EncodeNil(buf)
	case string:
		*buf = alg.QuoteFlags(*buf, v, false, fv&alg.SanitizeMask)
		return nil
	case []byte:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		*buf = rt.EncodeBase64(*buf, v)
		return nil
	case json.RawMessage:
		if v == nil {
			*buf = append(*buf, 'n', 'u', 'l', 'l')
			return nil
		}
		return prim.EncodeJsonMarshaler(buf, v, fv)
	case json.Number:
		if v == "" {
			*buf = append(*buf, '0')
		} else if !alg.IsValidNumber(string(v)) {
			return vars.Error_number(v)
		} else {
			*buf = append(*buf, v...)
		}
		return nil
	case bool:
		if v {
			*buf = append(*buf, 't', 'r', 'u', 'e')
		} else {
			*buf = append(*buf, 'f', 'a', 'l', 's', 'e')
		}
		return nil
	case int:
		*buf = alg.I64toa(*buf, int64(v))
		return nil
	case int64:
		*buf = alg.I64toa(*buf, v)
		return nil
	case int32:
		*buf = alg.I64toa(*buf, int64(v))
		return nil
	case int16:
		*buf = alg.I64toa(*buf, int64(v))
		return nil
	case int8:
		*buf = alg.I64toa(*buf, int64(v))
		return nil
	case uint:
		*buf = alg.U64toa(*buf, uint64(v))
		return nil
	case uint64:
		*buf = alg.U64toa(*buf, v)
		return nil
	case uint32:
		*buf = alg.U64toa(*buf, uint64(v))
		return nil
	case uint16:
		*buf = alg.U64toa(*buf, uint64(v))
		return nil
	case uint8:
		*buf = alg.U64toa(*buf, uint64(v))
		return nil
	case uintptr:
		*buf = alg.U64toa(*buf, uint64(v))
		return nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if fv&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
				return nil
			}
			return vars.ERR_nan_or_infinite
		}
		*buf = alg.F64toa(*buf, v)
		return nil
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			if fv&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
				return nil
			}
			return vars.ERR_nan_or_infinite
		}
		*buf = alg.F32toa(*buf, v)
		return nil
	case map[string]any:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '{', '}')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*buf = append(*buf, '{', '}')
			return nil
		}
		if !sb.Save(0, 0, nil, nil) || !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*buf = append(*buf, '{')
		if err := encodeStringAnyMap(buf, v, sb, fv); err != nil {
			return err
		}
		sb.Drop()
		sb.Drop()
		*buf = append(*buf, '}')
		return nil
	case []any:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*buf = append(*buf, '[', ']')
			return nil
		}
		if !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*buf = append(*buf, '[')
		for i, elem := range v {
			if i > 0 {
				*buf = append(*buf, ',')
			}
			if err := encodeAnyValue(buf, elem, sb, fv); err != nil {
				return err
			}
		}
		sb.Drop()
		*buf = append(*buf, ']')
		return nil
	case []float64:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		out, err := appendFloat64Slice(*buf, v, fv)
		if err != nil {
			return err
		}
		*buf = out
		sb.Drop()
		return nil
	case []float32:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		out, err := appendFloat32Slice(*buf, v, fv)
		if err != nil {
			return err
		}
		*buf = out
		sb.Drop()
		return nil
	case []string:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*buf = append(*buf, '[', ']')
			return nil
		}
		if !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		sanitize := fv & alg.SanitizeMask
		*buf = append(*buf, '[')
		*buf = alg.QuoteFlags(*buf, v[0], false, sanitize)
		for i := 1; i < len(v); i++ {
			*buf = append(*buf, ',')
			*buf = alg.QuoteFlags(*buf, v[i], false, sanitize)
		}
		sb.Drop()
		*buf = append(*buf, ']')
		return nil
	case []int:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*buf = append(*buf, '[', ']')
			return nil
		}
		if !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*buf = append(*buf, '[')
		*buf = alg.I64toa(*buf, int64(v[0]))
		for i := 1; i < len(v); i++ {
			*buf = append(*buf, ',')
			*buf = alg.I64toa(*buf, int64(v[i]))
		}
		sb.Drop()
		*buf = append(*buf, ']')
		return nil
	case []int64:
		if v == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '[', ']')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*buf = append(*buf, '[', ']')
			return nil
		}
		if !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*buf = append(*buf, '[')
		*buf = alg.I64toa(*buf, v[0])
		for i := 1; i < len(v); i++ {
			*buf = append(*buf, ',')
			*buf = alg.I64toa(*buf, v[i])
		}
		sb.Drop()
		*buf = append(*buf, ']')
		return nil
	}

	rv := reflect.ValueOf(val)
	if !rv.IsValid() {
		return prim.EncodeNil(buf)
	}
	// Values unpacked from an interface{} are not addressable at top level;
	// pointer types will re-enable pv=true inside compilePtrBody after OP_deref.
	fv &^= (1 << alg.BitPointerValue)
	plan, err := getOrBuildRootPlan(rv.Type(), &fv)
	if err != nil {
		return err
	}
	return encodeWithRootPlan(buf, rv, plan, sb, fv)
}

func getOrBuildRootPlan(vt reflect.Type, fv *uint64) (*rootEncPlan, error) {
	if (*fv & (1 << alg.BitOnlyOmitNull)) != 0 {
		return rootPlanCacheOmitNl.FindOrCompute(vt, buildRootEncPlanOmitNull)
	}
	return rootPlanCache.FindOrCompute(vt, buildRootEncPlanDefault)
}

func encodeWithRootPlan(buf *[]byte, rv reflect.Value, plan *rootEncPlan, sb *vars.Stack, fv uint64) error {
	if plan.isPureSlice {
		return encodePureStructSliceRoot(buf, rv, plan.elemSize, plan.minElemBytes, plan.pureSpecs, plan.denseSpecs, plan.hasDenseSpecs, sb, fv)
	}
	if plan.isPurePtrSlice {
		return encodePurePtrStructSliceRoot(buf, rv, plan.pureSpecs, plan.denseSpecs, plan.hasDenseSpecs, sb, fv)
	}
	if plan.isDirectMap {
		mh := rv.UnsafePointer()
		if mh == nil {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*buf = append(*buf, '{', '}')
			} else {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if rv.Len() == 0 {
			*buf = append(*buf, '{', '}')
			return nil
		}
		if !sb.Save(0, 0, nil, nil) || !sb.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*buf = append(*buf, '{')
		var err error
		if plan.builtinMapKind == vars.MapBuiltinStrAny {
			err = encodeStringAnyMap(buf, *(*map[string]any)(unsafe.Pointer(&mh)), sb, fv)
		} else {
			err = encodeBuiltinMap(buf, unsafe.Pointer(&mh), plan.builtinMapKind, sb, fv)
		}
		if err != nil {
			return err
		}
		sb.Drop()
		sb.Drop()
		*buf = append(*buf, '}')
		return nil
	}
	if plan.boxPool == nil {
		ptr := rv.UnsafePointer()
		if plan.isDirectPtr {
			if ptr == nil {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
				return nil
			}
			if plan.hasDenseSpecs {
				return encodeDenseStructRoot(buf, ptr, plan.denseSpecs, sb, fv)
			}
			if plan.hasPureSpecs {
				return encodePureStructRoot(buf, ptr, plan.pureSpecs, sb, fv)
			}
			if !sb.EnterDepth(1) {
				return vars.ERR_too_deep
			}
			if err := ExecuteRange(buf, ptr, sb, fv, plan.prog, plan.bodyStartPC, plan.bodyEndPC); err != nil {
				return err
			}
			sb.LeaveDepth(1)
			return nil
		}
		slot, idx := sb.PushPtr(ptr)
		err := Execute(buf, slot, sb, fv, plan.prog)
		sb.PopPtr(idx)
		return err
	}
	boxPtr := plan.boxPool.Get().(*reflect.Value)
	box := *boxPtr
	elem := box.Elem()
	elem.Set(rv)
	var err error
	if plan.hasDenseSpecs {
		err = encodeDenseStructRoot(buf, box.UnsafePointer(), plan.denseSpecs, sb, fv)
	} else if plan.hasPureSpecs {
		err = encodePureStructRoot(buf, box.UnsafePointer(), plan.pureSpecs, sb, fv)
	} else {
		err = Execute(buf, box.UnsafePointer(), sb, fv, plan.prog)
	}
	if plan.boxHasPtrs {
		elem.SetZero()
	}
	plan.boxPool.Put(boxPtr)
	return err
}

// EncodeRoot encodes top-level struct, pointer, map, and struct-slice values
// via pooled scratch buffers and detached output slices, with zero global
// history, clock reads, or cross-request concurrency probing.
func EncodeRoot(val interface{}, fv uint64) ([]byte, bool, error) {
	switch val.(type) {
	case nil, string, []byte, json.RawMessage, json.Number, bool,
		int, int64, int32, int16, int8,
		uint, uint64, uint32, uint16, uint8, uintptr,
		float64, float32,
		map[string]any, []any, []float64, []float32, []string, []int, []int64:
		return nil, false, nil
	}

	rv := reflect.ValueOf(val)
	if !rv.IsValid() {
		return []byte{'n', 'u', 'l', 'l'}, true, nil
	}
	fv &^= (1 << alg.BitPointerValue)
	plan, err := getOrBuildRootPlan(rv.Type(), &fv)
	if err != nil {
		return nil, true, err
	}

	if plan.isDirectPtr && rv.UnsafePointer() == nil {
		return []byte{'n', 'u', 'l', 'l'}, true, nil
	}
	if plan.isPureSlice || plan.isPurePtrSlice {
		if rv.IsNil() {
			if fv&(1<<alg.BitNoNullSliceOrMap) != 0 {
				return []byte{'[', ']'}, true, nil
			}
			return []byte{'n', 'u', 'l', 'l'}, true, nil
		}
		if rv.Len() == 0 {
			return []byte{'[', ']'}, true, nil
		}
	}

	execCtx := vars.GetExecCtx()
	origBuf := execCtx.Buf[:0]
	execCtx.Buf = origBuf
	needStk := !plan.isPureSlice && !plan.isPurePtrSlice && !plan.hasPureSpecs
	var stk *vars.Stack
	if needStk {
		stk = &execCtx.Stk
	}
	err = encodeWithRootPlan(&execCtx.Buf, rv, plan, stk, fv)
	if err != nil {
		if needStk {
			vars.ResetStack(stk)
		}
		if !rt.CanSizeResue(cap(execCtx.Buf)) && cap(origBuf) > 0 && rt.CanSizeResue(cap(origBuf)) {
			execCtx.Buf = origBuf
		}
		vars.FreeExecCtx(execCtx, false)
		runtime.KeepAlive(val)
		return nil, true, err
	}
	runtime.KeepAlive(val)

	var ret []byte
	if rt.CanSizeResue(cap(execCtx.Buf)) || (cap(execCtx.Buf) > len(execCtx.Buf)*2 && cap(execCtx.Buf) > 1024) {
		ret = rt.CloneBytesNoZero(execCtx.Buf)
		if !rt.CanSizeResue(cap(execCtx.Buf)) {
			if cap(origBuf) > 0 && rt.CanSizeResue(cap(origBuf)) {
				execCtx.Buf = origBuf
			} else {
				execCtx.Buf = nil
			}
		}
		vars.FreeExecCtx(execCtx, needStk)
	} else {
		ret = execCtx.Buf
		if cap(origBuf) > 0 && rt.CanSizeResue(cap(origBuf)) {
			execCtx.Buf = origBuf
		} else {
			execCtx.Buf = nil
		}
		vars.FreeExecCtx(execCtx, needStk)
	}
	return ret, true, nil
}
