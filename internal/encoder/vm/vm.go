// Copyright 2024 CloudWeGo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vm

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/prim"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/rt"
)

const (
	_S_cond = iota
)

func print_instr(buf []byte, pc int, op ir.Op, ins *ir.Instr, p unsafe.Pointer) {
	if len(buf) > 20 {
		fmt.Println(string(buf[len(buf)-20:]))
	} else {
		fmt.Println(string(buf))
	}
	fmt.Printf("pc %04d, op %v, ins %#v, ptr: %x\n", pc, op, ins.Disassemble(), p)
}

func Execute(b *[]byte, p unsafe.Pointer, s *vars.Stack, flags uint64, prog *ir.Program) error {
	return ExecuteRange(b, p, s, flags, prog, 0, len(*prog))
}

func ExecuteRange(b *[]byte, p unsafe.Pointer, s *vars.Stack, flags uint64, prog *ir.Program, startPC int, endPC int) error {
	if startPC >= endPC {
		return nil
	}

	var buf = *b
	var x int
	var q unsafe.Pointer
	var f uint64
	sanitize := flags & alg.SanitizeMask

	var pro = &(*prog)[0]
	for pc := startPC; pc < endPC; {
		ins := (*ir.Instr)(unsafe.Add(unsafe.Pointer(pro), ir.OpSize*uintptr(pc)))
		pc++
		op := ins.Op()

		switch op {
		case ir.OP_goto:
			pc = ins.Vi()
			continue
		case ir.OP_byte:
			v := ins.Byte()
			buf = append(buf, v)
		case ir.OP_text:
			v := ins.Vs()
			if sanitize != 0 && ins.NeedsSanitize() {
				buf = alg.AppendSanitizedLiteral(buf, v, sanitize)
			} else {
				buf = append(buf, v...)
			}
		case ir.OP_struct_fields:
			var err error
			if sanitize == 0 {
				buf, f, err = encodeStructFieldsDefault(buf, p, ins.VFields(), f, s, flags)
			} else {
				buf, f, err = encodeStructFieldsSanitized(buf, p, ins.VFields(), f, s, flags, sanitize)
			}
			if err != nil {
				return err
			}
		case ir.OP_deref:
			p = *(*unsafe.Pointer)(p)
		case ir.OP_index:
			p = unsafe.Add(p, uintptr(ins.I64()))
		case ir.OP_load:
			// NOTICE: load CANNOT change f!
			x, _, p, q = s.Load()
		case ir.OP_save:
			if !s.Save(x, f, p, q) {
				return vars.ERR_too_deep
			}
		case ir.OP_drop:
			x, f, p, q = s.Drop()
		case ir.OP_drop_2:
			s.Drop()
			x, f, p, q = s.Drop()
		case ir.OP_recurse:
			vt, pv := ins.Vp2()
			f := flags
			if pv {
				f |= (1 << alg.BitPointerValue)
			} else {
				f &^= (1 << alg.BitPointerValue)
			}
			*b = buf
			if err := EncodeValuePointer(b, vt, p, s, f); err != nil {
				return err
			}
			buf = *b
		case ir.OP_is_nil:
			if is_nil(p, ins.Vr()) {
				pc = ins.Vi()
				continue
			}
		case ir.OP_is_nil_p1:
			if vt := ins.Vr(); vt != nil {
				if isEmptySlice(p, vt) {
					pc = ins.Vi()
					continue
				}
			} else if len(*(*string)(p)) == 0 {
				pc = ins.Vi()
				continue
			}
		case ir.OP_null:
			buf = append(buf, 'n', 'u', 'l', 'l')
		case ir.OP_str:
			v := *(*string)(p)
			buf = alg.QuoteFlags(buf, v, false, sanitize)
		case ir.OP_bool:
			if *(*bool)(p) {
				buf = append(buf, 't', 'r', 'u', 'e')
			} else {
				buf = append(buf, 'f', 'a', 'l', 's', 'e')
			}
		case ir.OP_i8:
			v := *(*int8)(p)
			buf = alg.I64toa(buf, int64(v))
		case ir.OP_i16:
			v := *(*int16)(p)
			buf = alg.I64toa(buf, int64(v))
		case ir.OP_i32:
			v := *(*int32)(p)
			buf = alg.I64toa(buf, int64(v))
		case ir.OP_i64:
			v := *(*int64)(p)
			buf = alg.I64toa(buf, int64(v))
		case ir.OP_u8:
			v := *(*uint8)(p)
			buf = alg.U64toa(buf, uint64(v))
		case ir.OP_u16:
			v := *(*uint16)(p)
			buf = alg.U64toa(buf, uint64(v))
		case ir.OP_u32:
			v := *(*uint32)(p)
			buf = alg.U64toa(buf, uint64(v))
		case ir.OP_u64:
			v := *(*uint64)(p)
			buf = alg.U64toa(buf, uint64(v))
		case ir.OP_f32:
			v := *(*float32)(p)
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return vars.ERR_nan_or_infinite
			}
			buf = alg.F32toa(buf, v)
		case ir.OP_f64:
			v := *(*float64)(p)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
					buf = append(buf, 'n', 'u', 'l', 'l')
					continue
				}
				return vars.ERR_nan_or_infinite
			}
			buf = alg.F64toa(buf, v)
		case ir.OP_f32_slice:
			var v []float32
			if st := ins.Vr(); st != nil {
				rv := reflect.NewAt(st, p).Elem()
				if rv.IsNil() {
					if has_opts(flags, alg.BitNoNullSliceOrMap) {
						buf = append(buf, '[', ']')
					} else {
						buf = append(buf, 'n', 'u', 'l', 'l')
					}
					continue
				}
				n := rv.Len()
				if n == 0 {
					buf = append(buf, '[', ']')
					continue
				}
				v = unsafe.Slice((*float32)(rv.UnsafePointer()), n)
			} else {
				v = *(*[]float32)(p)
				if v == nil {
					if has_opts(flags, alg.BitNoNullSliceOrMap) {
						buf = append(buf, '[', ']')
					} else {
						buf = append(buf, 'n', 'u', 'l', 'l')
					}
					continue
				}
			}
			if !s.Save(x, f, p, q) {
				return vars.ERR_too_deep
			}
			var err error
			buf, err = appendFloat32Slice(buf, v, flags)
			if err != nil {
				return err
			}
			s.Drop()
		case ir.OP_f64_slice:
			var v []float64
			if st := ins.Vr(); st != nil {
				rv := reflect.NewAt(st, p).Elem()
				if rv.IsNil() {
					if has_opts(flags, alg.BitNoNullSliceOrMap) {
						buf = append(buf, '[', ']')
					} else {
						buf = append(buf, 'n', 'u', 'l', 'l')
					}
					continue
				}
				n := rv.Len()
				if n == 0 {
					buf = append(buf, '[', ']')
					continue
				}
				v = unsafe.Slice((*float64)(rv.UnsafePointer()), n)
			} else {
				v = *(*[]float64)(p)
				if v == nil {
					if has_opts(flags, alg.BitNoNullSliceOrMap) {
						buf = append(buf, '[', ']')
					} else {
						buf = append(buf, 'n', 'u', 'l', 'l')
					}
					continue
				}
			}
			if !s.Save(x, f, p, q) {
				return vars.ERR_too_deep
			}
			var err error
			buf, err = appendFloat64Slice(buf, v, flags)
			if err != nil {
				return err
			}
			s.Drop()
		case ir.OP_bin:
			if vt := ins.Vr(); vt != nil {
				buf = rt.EncodeBase64(buf, reflect.NewAt(vt, p).Elem().Bytes())
				continue
			}
			v := *(*[]byte)(p)
			if v == nil {
				if has_opts(flags, alg.BitNoNullSliceOrMap) {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, 'n', 'u', 'l', 'l')
				}
				continue
			}
			buf = rt.EncodeBase64(buf, v)
		case ir.OP_quote:
			v := *(*string)(p)
			buf = alg.QuoteFlags(buf, v, true, sanitize)
		case ir.OP_number:
			v := *(*json.Number)(p)
			if v == "" {
				buf = append(buf, '0')
			} else if !alg.IsValidNumber(string(v)) {
				return vars.Error_number(v)
			} else {
				buf = append(buf, v...)
			}
		case ir.OP_eface:
			*b = buf
			if err := encodeAnyValue(b, *(*any)(p), s, flags); err != nil {
				return err
			}
			buf = *b
		case ir.OP_iface:
			rv := reflect.NewAt(ins.Vr(), p).Elem()
			if rv.IsNil() {
				buf = append(buf, 'n', 'u', 'l', 'l')
				continue
			}
			*b = buf
			if err := encodeAnyValue(b, rv.Interface(), s, flags); err != nil {
				return err
			}
			buf = *b
		case ir.OP_is_zero_map:
			if reflect.NewAt(ins.Vr(), p).Elem().Len() == 0 {
				pc = ins.Vi()
				continue
			}
		case ir.OP_map_string_any:
			*b = buf
			if err := encodeStringAnyMap(b, *(*map[string]any)(p), s, flags); err != nil {
				return err
			}
			buf = *b
		case ir.OP_map_iter:
			vt := ins.Vr()
			it, err := alg.IteratorStart(vt, p, flags)
			if err != nil {
				return err
			}
			q = unsafe.Pointer(it)
		case ir.OP_map_stop:
			it := (*alg.MapIterator)(q)
			alg.IteratorStop(it)
			q = nil
		case ir.OP_map_value_next:
			it := (*alg.MapIterator)(q)
			p = it.It.V
			alg.IteratorNext(it)
		case ir.OP_map_check_key:
			it := (*alg.MapIterator)(q)
			if it.It.K == nil {
				pc = ins.Vi()
				continue
			}
			p = it.It.K
		case ir.OP_marshal_text:
			vt := ins.Vr()
			rv := reflect.NewAt(vt, p).Elem()
			if vt.Kind() == reflect.Interface && rv.IsNil() {
				buf = append(buf, 'n', 'u', 'l', 'l')
				continue
			}
			if err := prim.EncodeTextMarshaler(&buf, rv.Interface().(encoding.TextMarshaler), flags); err != nil {
				return err
			}
		case ir.OP_marshal_text_p:
			pt := ins.Vr()
			v := reflect.NewAt(pt.Elem(), p).Interface().(encoding.TextMarshaler)
			if err := prim.EncodeTextMarshaler(&buf, v, flags); err != nil {
				return err
			}
		case ir.OP_map_write_key:
			if has_opts(flags, alg.BitSortMapKeys) {
				v := *(*string)(p)
				buf = alg.QuoteFlags(buf, v, false, sanitize)
				pc = ins.Vi()
				continue
			}
		case ir.OP_slice_begin:
			isNil, n, dataPtr := sliceBegin(p, ins.Vr())
			if isNil {
				if has_opts(flags, alg.BitNoNullSliceOrMap) {
					buf = append(buf, '[', ']')
				} else {
					buf = append(buf, 'n', 'u', 'l', 'l')
				}
				pc = ins.Vi()
				continue
			}
			if n == 0 {
				buf = append(buf, '[', ']')
				pc = ins.Vi()
				continue
			}
			if !s.Save(x, f, p, q) {
				return vars.ERR_too_deep
			}
			buf = append(buf, '[')
			x = n - 1
			p = dataPtr
		case ir.OP_slice_next:
			if x == 0 {
				pc = ins.Vi()
				continue
			}
			x--
			p = unsafe.Add(p, uintptr(ins.Vlen()))
			buf = append(buf, ',')
		case ir.OP_cond_set:
			f |= 1 << _S_cond
		case ir.OP_cond_testc:
			if has_opts(f, _S_cond) {
				f &= ^uint64(1 << _S_cond)
				pc = ins.Vi()
				continue
			}
		case ir.OP_is_zero:
			fv := ins.VField()
			if prim.IsZero(p, fv, ins.CanAddr()) {
				pc = ins.Vi()
				continue
			}
		case ir.OP_is_zero_1:
			if *(*uint8)(p) == 0 {
				pc = ins.Vi()
				continue
			}
		case ir.OP_is_zero_2:
			if *(*uint16)(p) == 0 {
				pc = ins.Vi()
				continue
			}
		case ir.OP_is_zero_4:
			if *(*uint32)(p) == 0 {
				pc = ins.Vi()
				continue
			}
		case ir.OP_is_zero_8:
			if *(*uint64)(p) == 0 {
				pc = ins.Vi()
				continue
			}
		case ir.OP_empty_arr:
			if has_opts(flags, alg.BitNoNullSliceOrMap) {
				buf = append(buf, '[', ']')
			} else {
				buf = append(buf, 'n', 'u', 'l', 'l')
			}
		case ir.OP_empty_obj:
			if has_opts(flags, alg.BitNoNullSliceOrMap) {
				buf = append(buf, '{', '}')
			} else {
				buf = append(buf, 'n', 'u', 'l', 'l')
			}
		case ir.OP_marshal:
			vt := ins.Vr()
			rv := reflect.NewAt(vt, p).Elem()
			if vt.Kind() == reflect.Interface && rv.IsNil() {
				buf = append(buf, 'n', 'u', 'l', 'l')
				continue
			}
			if err := prim.EncodeJsonMarshaler(&buf, rv.Interface().(json.Marshaler), flags); err != nil {
				return err
			}
		case ir.OP_marshal_p:
			pt := ins.Vr()
			v := reflect.NewAt(pt.Elem(), p).Interface().(json.Marshaler)
			if err := prim.EncodeJsonMarshaler(&buf, v, flags); err != nil {
				return err
			}
		case ir.OP_marshal_append:
			vt := ins.Vr()
			rv := reflect.NewAt(vt, p).Elem()
			if vt.Kind() == reflect.Interface && rv.IsNil() {
				buf = append(buf, 'n', 'u', 'l', 'l')
				continue
			}
			if err := prim.EncodeJSONAppender(&buf, rv.Interface().(prim.JSONAppender), flags); err != nil {
				return err
			}
		case ir.OP_marshal_append_p:
			pt := ins.Vr()
			v := reflect.NewAt(pt.Elem(), p).Interface().(prim.JSONAppender)
			if err := prim.EncodeJSONAppender(&buf, v, flags); err != nil {
				return err
			}
		case ir.OP_unsupported:
			return vars.Error_unsuppoted(ins.Vt())
		default:
			panic(fmt.Sprintf("not implement %s at %d", ins.Op().String(), pc))
		}
	}

	*b = buf
	return nil
}

func has_opts(opts uint64, bit int) bool {
	return opts&(1<<bit) != 0
}

func is_nil(p unsafe.Pointer, vt reflect.Type) bool {
	if vt == nil {
		return *(*unsafe.Pointer)(p) == nil
	}
	switch vt {
	case vars.EmptyInterfaceType:
		return *(*any)(p) == nil
	case vars.StringAnyMapType:
		return *(*map[string]any)(p) == nil
	case vars.ByteSliceType:
		return *(*[]byte)(p) == nil
	case vars.StringSliceType:
		return *(*[]string)(p) == nil
	case vars.IntSliceType:
		return *(*[]int)(p) == nil
	case vars.Int64SliceType:
		return *(*[]int64)(p) == nil
	case vars.Float64SliceType:
		return *(*[]float64)(p) == nil
	case vars.Float32SliceType:
		return *(*[]float32)(p) == nil
	case vars.AnySliceType:
		return *(*[]any)(p) == nil
	default:
		return reflect.NewAt(vt, p).Elem().IsNil()
	}
}

func isEmptySlice(p unsafe.Pointer, vt reflect.Type) bool {
	switch vt {
	case vars.ByteSliceType:
		return len(*(*[]byte)(p)) == 0
	case vars.StringSliceType:
		return len(*(*[]string)(p)) == 0
	case vars.IntSliceType:
		return len(*(*[]int)(p)) == 0
	case vars.Int64SliceType:
		return len(*(*[]int64)(p)) == 0
	case vars.Float64SliceType:
		return len(*(*[]float64)(p)) == 0
	case vars.Float32SliceType:
		return len(*(*[]float32)(p)) == 0
	case vars.AnySliceType:
		return len(*(*[]any)(p)) == 0
	default:
		return reflect.NewAt(vt, p).Elem().Len() == 0
	}
}

func sliceBegin(p unsafe.Pointer, st reflect.Type) (isNil bool, n int, dataPtr unsafe.Pointer) {
	switch st {
	case vars.StringSliceType:
		v := *(*[]string)(p)
		if v == nil {
			return true, 0, nil
		}
		if len(v) == 0 {
			return false, 0, nil
		}
		return false, len(v), unsafe.Pointer(unsafe.SliceData(v))
	case vars.IntSliceType:
		v := *(*[]int)(p)
		if v == nil {
			return true, 0, nil
		}
		if len(v) == 0 {
			return false, 0, nil
		}
		return false, len(v), unsafe.Pointer(unsafe.SliceData(v))
	case vars.Int64SliceType:
		v := *(*[]int64)(p)
		if v == nil {
			return true, 0, nil
		}
		if len(v) == 0 {
			return false, 0, nil
		}
		return false, len(v), unsafe.Pointer(unsafe.SliceData(v))
	case vars.AnySliceType:
		v := *(*[]any)(p)
		if v == nil {
			return true, 0, nil
		}
		if len(v) == 0 {
			return false, 0, nil
		}
		return false, len(v), unsafe.Pointer(unsafe.SliceData(v))
	default:
		rv := reflect.NewAt(st, p).Elem()
		if rv.IsNil() {
			return true, 0, nil
		}
		length := rv.Len()
		if length == 0 {
			return false, 0, nil
		}
		return false, length, rv.UnsafePointer()
	}
}

func appendPaddedKey(buf []byte, keyPtr *byte, keyLen int) []byte {
	if cap(buf)-len(buf) >= 32 {
		dp := (*[4]uint64)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf)))
		sp := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dp[0], dp[1] = sp[0], sp[1]
		if keyLen > 16 {
			dp[2], dp[3] = sp[2], sp[3]
		}
		return buf[:len(buf)+keyLen]
	}
	return append(buf, unsafe.Slice(keyPtr, keyLen)...)
}

func appendBool(buf []byte, v bool) []byte {
	if cap(buf)-len(buf) >= 8 {
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), len(buf))
		if v {
			*(*[4]byte)(dp) = [4]byte{'t', 'r', 'u', 'e'}
			return buf[:len(buf)+4]
		}
		*(*[5]byte)(dp) = [5]byte{'f', 'a', 'l', 's', 'e'}
		return buf[:len(buf)+5]
	}
	if v {
		return append(buf, "true"...)
	}
	return append(buf, "false"...)
}
