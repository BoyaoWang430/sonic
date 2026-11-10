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
	_S_init
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
	pl := len(*prog)
	if pl <= 0 {
		return nil
	}

	var buf = *b
	var x int
	var q unsafe.Pointer
	var f uint64
	sanitize := flags & alg.SanitizeMask

	var pro = &(*prog)[0]
	for pc := 0; pc < pl; {
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
			base := p
			specs := ins.VFields()
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.I64toa(buf, v)
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
					*b = buf
					if err := encodeAnyValue(b, val, s, flags); err != nil {
						return err
					}
					buf = *b
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
						return vars.ERR_too_deep
					}
					buf = append(buf, '[')
					*b = buf
					for idx, elem := range items {
						if idx > 0 {
							*b = append(*b, ',')
						}
						if err := encodeAnyValue(b, elem, s, flags); err != nil {
							return err
						}
					}
					buf = *b
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.I64toa(buf, int64(v))
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.I64toa(buf, int64(v))
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.I64toa(buf, int64(v))
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.U64toa(buf, v)
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.U64toa(buf, uint64(v))
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.U64toa(buf, uint64(v))
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
					buf = appendPaddedKey(buf, keyPtr, keyLen)
					buf = alg.U64toa(buf, uint64(v))
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
						return vars.ERR_nan_or_infinite
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
						return vars.ERR_nan_or_infinite
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
						return vars.Error_number(v)
					} else {
						buf = append(buf, v...)
					}
				}
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
				if reflect.NewAt(vt, p).Elem().Len() == 0 {
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
			var err error
			buf, err = appendFloat32Slice(buf, *(*[]float32)(p), flags)
			if err != nil {
				return err
			}
		case ir.OP_f64_slice:
			var err error
			buf, err = appendFloat64Slice(buf, *(*[]float64)(p), flags)
			if err != nil {
				return err
			}
		case ir.OP_bin:
			v := *(*[]byte)(p)
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
		case ir.OP_slice_len:
			rv := reflect.NewAt(ins.Vr(), p).Elem()
			x = rv.Len()
			p = rv.UnsafePointer()
			f |= 1 << _S_init
		case ir.OP_slice_next:
			if x == 0 {
				pc = ins.Vi()
				continue
			}
			x--
			if has_opts(f, _S_init) {
				f &= ^uint64(1 << _S_init)
			} else {
				p = unsafe.Add(p, uintptr(ins.Vlen()))
			}
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
	default:
		return reflect.NewAt(vt, p).Elem().IsNil()
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
