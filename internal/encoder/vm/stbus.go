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
	"sync"
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

var valueBoxPools sync.Map // map[reflect.Type]*sync.Pool

type rootEncPlan struct {
	prog         *ir.Program
	boxPool      *sync.Pool
	pureSpecs    []ir.StructFieldSpec
	isDirectPtr  bool
	hasPureSpecs bool
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

func getValueBoxPool(vt reflect.Type) *sync.Pool {
	if p, ok := valueBoxPools.Load(vt); ok {
		return p.(*sync.Pool)
	}
	pool := &sync.Pool{
		New: func() any {
			return reflect.New(vt)
		},
	}
	actual, _ := valueBoxPools.LoadOrStore(vt, pool)
	return actual.(*sync.Pool)
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
		var pureSpecs []ir.StructFieldSpec
		var hasPureSpecs bool
		if isDirectPtr {
			pureSpecs, hasPureSpecs = extractPureStructSpecs(prog, 3, n-3)
		}
		return &rootEncPlan{
			prog:         prog,
			pureSpecs:    pureSpecs,
			isDirectPtr:  isDirectPtr,
			hasPureSpecs: hasPureSpecs,
		}, nil
	default:
		pureSpecs, hasPureSpecs := extractPureStructSpecs(prog, 0, len(*prog))
		return &rootEncPlan{
			prog:         prog,
			boxPool:      getValueBoxPool(vt),
			pureSpecs:    pureSpecs,
			hasPureSpecs: hasPureSpecs,
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
	vt := rv.Type()
	var plan *rootEncPlan
	if cached := vars.GetRootPlan(vt); cached != nil {
		plan = cached.(*rootEncPlan)
	} else {
		computed, err := vars.ComputeRootPlan(vt, buildRootEncPlan)
		if err != nil {
			return err
		}
		plan = computed.(*rootEncPlan)
	}
	if plan.boxPool == nil {
		ptr := rv.UnsafePointer()
		if plan.isDirectPtr {
			if ptr == nil {
				*buf = append(*buf, 'n', 'u', 'l', 'l')
				return nil
			}
			if plan.hasPureSpecs {
				return encodePureStructRoot(buf, ptr, plan.pureSpecs, sb, fv)
			}
			if !sb.Save(0, 0, nil, nil) {
				return vars.ERR_too_deep
			}
			if err := ExecuteRange(buf, ptr, sb, fv, plan.prog, 3, len(*plan.prog)-3); err != nil {
				return err
			}
			sb.Drop()
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
	if plan.hasPureSpecs {
		err = encodePureStructRoot(buf, box.UnsafePointer(), plan.pureSpecs, sb, fv)
	} else {
		err = Execute(buf, box.UnsafePointer(), sb, fv, plan.prog)
	}
	elem.SetZero()
	plan.boxPool.Put(box)
	return err
}
