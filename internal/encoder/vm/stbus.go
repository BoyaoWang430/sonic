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
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/prim"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/rt"
)

var compiler func(reflect.Type, ...interface{}) (interface{}, error)

func SetCompiler(c func(reflect.Type, ...interface{}) (interface{}, error)) {
	compiler = c
}

// EncodeValuePointer encodes the value of type vt stored at p.
func EncodeValuePointer(buf *[]byte, vt reflect.Type, p unsafe.Pointer, sb *vars.Stack, fv uint64) error {
	if vt == nil {
		return prim.EncodeNil(buf)
	}
	pp, err := vars.FindOrCompile(vt, (fv&(1<<alg.BitPointerValue)) != 0, compiler)
	if err != nil {
		return err
	}
	return Execute(buf, p, sb, fv, pp.(*ir.Program))
}

const (
	minDirectRootHint = 512
	maxDirectRootHint = 32 << 10
)

type rootEncPlan struct {
	prog          *ir.Program
	boxPool       *sync.Pool
	pureSpecs     []ir.StructFieldSpec
	denseSpecs    []ir.StructFieldSpec
	elemSize      uintptr
	sizeHint      atomic.Uint32
	isDirectPtr   bool
	hasPureSpecs  bool
	hasDenseSpecs bool
	isPureSlice   bool
	allowSizeHint bool
	boxHasPtrs    bool
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

func clampRootSizeHint(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if n > maxDirectRootHint {
		return maxDirectRootHint + 1
	}
	return uint32(n)
}

func growSliceBufAfterFirst(buf []byte, startOff int, n int) []byte {
	if n < 64 {
		return buf
	}
	perElem := (len(buf) - startOff) + 1
	if perElem > 4096 {
		perElem = 4096
	}
	rem := perElem * (n - 1)
	est := len(buf) + rem + (rem >> 3) + 64
	if est > cap(buf) {
		grown := make([]byte, len(buf), est)
		copy(grown, buf)
		return grown
	}
	return buf
}

func encodePureStructSliceRoot(b *[]byte, rv reflect.Value, elemSize uintptr, specs []ir.StructFieldSpec, denseSpecs []ir.StructFieldSpec, hasDense bool, s *vars.Stack, flags uint64) error {
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
	startOff := len(*b)
	buf := append(*b, '[', '{')
	var err error
	sanitize := flags & alg.SanitizeMask
	if sanitize == 0 {
		if hasDense {
			buf, err = encodeStructFieldsDense(buf, dataPtr, denseSpecs, s, flags)
			if err != nil {
				s.LeaveDepth(2)
				return err
			}
			buf = growSliceBufAfterFirst(buf, startOff, n)
			for idx := 1; idx < n; idx++ {
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
			buf = growSliceBufAfterFirst(buf, startOff, n)
			for idx := 1; idx < n; idx++ {
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
			buf = growSliceBufAfterFirst(buf, startOff, n)
			for idx := 1; idx < n; idx++ {
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
			buf = growSliceBufAfterFirst(buf, startOff, n)
			for idx := 1; idx < n; idx++ {
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

func extractPureSliceSpecs(prog *ir.Program) ([]ir.StructFieldSpec, bool) {
	n := len(*prog)
	if (n != 17 && n != 15) || (*prog)[0].Op() != ir.OP_slice_begin || (*prog)[0].Vi() != n {
		return nil, false
	}
	if (*prog)[n-2].Op() != ir.OP_drop || (*prog)[n-1].Op() != ir.OP_byte || (*prog)[n-1].Byte() != ']' {
		return nil, false
	}
	elemEnd := 7
	if n == 15 {
		elemEnd = 6
	}
	if (*prog)[elemEnd].Op() != ir.OP_slice_next {
		return nil, false
	}
	return extractPureStructSpecs(prog, 1, elemEnd)
}

func extractPureStructSpecs(prog *ir.Program, start, end int) ([]ir.StructFieldSpec, bool) {
	if start < 0 || end > len(*prog) || start > end {
		return nil, false
	}
	s := (*prog)[start:end]
	if len(s) == 6 &&
		s[0].Op() == ir.OP_byte && s[0].Byte() == '{' &&
		s[1].Op() == ir.OP_save &&
		s[2].Op() == ir.OP_cond_set &&
		s[3].Op() == ir.OP_struct_fields &&
		s[4].Op() == ir.OP_drop &&
		s[5].Op() == ir.OP_byte && s[5].Byte() == '}' {
		return s[3].VFields(), true
	}
	if len(s) == 5 &&
		s[0].Op() == ir.OP_byte && s[0].Byte() == '{' &&
		s[1].Op() == ir.OP_save &&
		s[2].Op() == ir.OP_cond_set &&
		s[3].Op() == ir.OP_drop &&
		s[4].Op() == ir.OP_byte && s[4].Byte() == '}' {
		return nil, true
	}
	return nil, false
}

func newValueBoxPool(vt reflect.Type) *sync.Pool {
	return &sync.Pool{
		New: func() any {
			return reflect.New(vt)
		},
	}
}

func buildRootEncPlan(vt reflect.Type, _ ...interface{}) (interface{}, error) {
	pp, err := vars.FindOrCompile(vt, false, compiler)
	if err != nil {
		return nil, err
	}
	prog := pp.(*ir.Program)
	switch vt.Kind() {
	case reflect.Ptr, reflect.UnsafePointer:
		n := len(*prog)
		isDirectPtr := vt.Kind() == reflect.Ptr &&
			n >= 6 &&
			(*prog)[0].Op() == ir.OP_is_nil &&
			(*prog)[0].Vr() == nil &&
			(*prog)[1].Op() == ir.OP_save &&
			(*prog)[2].Op() == ir.OP_deref &&
			(*prog)[n-3].Op() == ir.OP_drop &&
			(*prog)[n-2].Op() == ir.OP_goto &&
			(*prog)[n-1].Op() == ir.OP_null
		var pureSpecs, denseSpecs []ir.StructFieldSpec
		var hasPureSpecs, hasDenseSpecs bool
		if isDirectPtr {
			pureSpecs, hasPureSpecs = extractPureStructSpecs(prog, 3, n-3)
			if hasPureSpecs {
				denseSpecs, hasDenseSpecs = ir.BuildDenseSpecs(pureSpecs)
			}
		}
		return &rootEncPlan{
			prog:          prog,
			pureSpecs:     pureSpecs,
			denseSpecs:    denseSpecs,
			isDirectPtr:   isDirectPtr,
			hasPureSpecs:  hasPureSpecs,
			hasDenseSpecs: hasDenseSpecs,
			allowSizeHint: vt.Kind() == reflect.Ptr && vt.Elem().Kind() == reflect.Struct,
		}, nil
	case reflect.Slice:
		if pureSpecs, ok := extractPureSliceSpecs(prog); ok {
			denseSpecs, hasDenseSpecs := ir.BuildDenseSpecs(pureSpecs)
			return &rootEncPlan{
				prog:          prog,
				pureSpecs:     pureSpecs,
				denseSpecs:    denseSpecs,
				elemSize:      vt.Elem().Size(),
				hasDenseSpecs: hasDenseSpecs,
				isPureSlice:   true,
				allowSizeHint: true,
			}, nil
		}
		return &rootEncPlan{
			prog:          prog,
			boxPool:       newValueBoxPool(vt),
			allowSizeHint: vt.Elem().Kind() == reflect.Struct,
			boxHasPtrs:    true,
		}, nil
	default:
		pureSpecs, hasPureSpecs := extractPureStructSpecs(prog, 0, len(*prog))
		var denseSpecs []ir.StructFieldSpec
		var hasDenseSpecs bool
		if hasPureSpecs {
			denseSpecs, hasDenseSpecs = ir.BuildDenseSpecs(pureSpecs)
		}
		return &rootEncPlan{
			prog:          prog,
			boxPool:       newValueBoxPool(vt),
			pureSpecs:     pureSpecs,
			denseSpecs:    denseSpecs,
			hasPureSpecs:  hasPureSpecs,
			hasDenseSpecs: hasDenseSpecs,
			allowSizeHint: vt.Kind() == reflect.Struct,
			boxHasPtrs:    typeHasPointers(vt),
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
	plan, err := getOrBuildRootPlan(rv.Type())
	if err != nil {
		return err
	}
	return encodeWithRootPlan(buf, rv, plan, sb, fv)
}

func getOrBuildRootPlan(vt reflect.Type) (*rootEncPlan, error) {
	if cached := vars.GetRootPlan(vt); cached != nil {
		return cached.(*rootEncPlan), nil
	}
	computed, err := vars.ComputeRootPlan(vt, buildRootEncPlan)
	if err != nil {
		return nil, err
	}
	return computed.(*rootEncPlan), nil
}

func encodeWithRootPlan(buf *[]byte, rv reflect.Value, plan *rootEncPlan, sb *vars.Stack, fv uint64) error {
	if plan.isPureSlice {
		return encodePureStructSliceRoot(buf, rv, plan.elemSize, plan.pureSpecs, plan.denseSpecs, plan.hasDenseSpecs, sb, fv)
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
			if err := ExecuteRange(buf, ptr, sb, fv, plan.prog, 3, len(*plan.prog)-3); err != nil {
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
	box := plan.boxPool.Get().(reflect.Value)
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
	plan.boxPool.Put(box)
	return err
}

// EncodeRoot encodes top-level struct and struct-slice values via per-P pooled scratch buffers
// with adaptive steady-state capacity hints and unzeroed result cloning, avoiding cold heap
// zeroing and sync.Pool Stack traffic on pure/dense struct schemas.
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
	plan, err := getOrBuildRootPlan(rv.Type())
	if err != nil {
		return nil, true, err
	}

	if plan.allowSizeHint {
		if plan.isDirectPtr && rv.UnsafePointer() == nil {
			return []byte{'n', 'u', 'l', 'l'}, true, nil
		}
		if plan.isPureSlice {
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
	}

	buf := vars.NewBytes()
	if plan.allowSizeHint {
		hint := int(plan.sizeHint.Load())
		if hint >= minDirectRootHint && hint <= maxDirectRootHint {
			capHint := hint + (hint >> 5) + 96
			if cap(*buf) < capHint {
				*buf = make([]byte, 0, capHint)
			}
		}
	}
	origBuf := (*buf)[:0]
	var stk *vars.Stack
	if !plan.isPureSlice && !plan.hasPureSpecs {
		stk = vars.NewStack()
	}
	err = encodeWithRootPlan(buf, rv, plan, stk, fv)
	if err != nil {
		if stk != nil {
			vars.ResetStack(stk)
			vars.FreeStack(stk)
		}
		if !rt.CanSizeResue(cap(*buf)) && cap(origBuf) > 0 && rt.CanSizeResue(cap(origBuf)) {
			*buf = origBuf
		}
		vars.FreeBytes(buf)
		runtime.KeepAlive(val)
		return nil, true, err
	}
	if stk != nil {
		vars.FreeStack(stk)
	}
	runtime.KeepAlive(val)

	if plan.allowSizeHint {
		hint := int(plan.sizeHint.Load())
		if (len(*buf) >= minDirectRootHint && hint == 0) || len(*buf) > cap(origBuf) || (hint > 0 && len(*buf)*2 < hint) {
			plan.sizeHint.Store(clampRootSizeHint(len(*buf)))
		}
	}

	var ret []byte
	if rt.CanSizeResue(cap(*buf)) {
		ret = rt.CloneBytesNoZero(*buf)
		vars.FreeBytes(buf)
	} else {
		ret = *buf
		if cap(origBuf) > 0 && rt.CanSizeResue(cap(origBuf)) {
			*buf = origBuf
			vars.FreeBytes(buf)
		} else {
			*buf = nil
		}
	}
	return ret, true, nil
}
