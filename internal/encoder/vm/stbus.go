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
	}

	rv := reflect.ValueOf(val)
	if !rv.IsValid() {
		return prim.EncodeNil(buf)
	}
	// Values unpacked from an interface{} are not addressable at top level;
	// pointer types will re-enable pv=true inside compilePtrBody after OP_deref.
	fv &^= (1 << alg.BitPointerValue)
	vt := rv.Type()
	switch rv.Kind() {
	case reflect.Ptr, reflect.UnsafePointer:
		slot, idx := sb.PushPtr(rv.UnsafePointer())
		err := EncodeValuePointer(buf, vt, slot, sb, fv)
		sb.PopPtr(idx)
		return err
	default:
		pool := getValueBoxPool(vt)
		box := pool.Get().(reflect.Value)
		elem := box.Elem()
		elem.Set(rv)
		err := EncodeValuePointer(buf, vt, box.UnsafePointer(), sb, fv)
		elem.SetZero()
		pool.Put(box)
		return err
	}
}
