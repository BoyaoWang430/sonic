/*
 * Copyright 2021 ByteDance Inc.
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

package ir

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/resolver"
	"github.com/bytedance/sonic/internal/rt"
)

const (
	_InsFlagNeedsSanitize uint64 = 1 << 48
	_InsFlagCanAddr       uint64 = 1 << 48
	OpSize                       = unsafe.Sizeof(Instr{})
)

type Op uint8

const (
	OP_null Op = iota
	OP_empty_arr
	OP_empty_obj
	OP_bool
	OP_i8
	OP_i16
	OP_i32
	OP_i64
	OP_u8
	OP_u16
	OP_u32
	OP_u64
	OP_f32
	OP_f64
	OP_f32_slice
	OP_f64_slice
	OP_str
	OP_bin
	OP_quote
	OP_number
	OP_eface
	OP_iface
	OP_byte
	OP_text
	OP_deref
	OP_index
	OP_load
	OP_save
	OP_drop
	OP_drop_2
	OP_recurse
	OP_map_iter
	OP_map_stop
	OP_map_value_next
	OP_map_string_any
	OP_marshal
	OP_marshal_p
	OP_marshal_text
	OP_marshal_text_p
	OP_marshal_append
	OP_marshal_append_p
	OP_cond_set
	OP_struct_fields
	OP_struct_begin
	OP_struct_end
	OP_slice_eface
	OP_slice_i64
	OP_slice_str
	OP_slice_struct
	OP_unsupported

	// TheFollowing belongs to Branch Op Basic Block
	OP_goto
	OP_map_check_key
	OP_map_write_key
	OP_slice_begin
	OP_slice_next
	OP_cond_testc
	OP_is_nil
	OP_is_nil_p1
	OP_is_zero_1
	OP_is_zero_2
	OP_is_zero_4
	OP_is_zero_8
	OP_is_zero_map
	OP_is_zero
)

var _OpNames = [256]string{
	OP_null:             "null",
	OP_empty_arr:        "empty_arr",
	OP_empty_obj:        "empty_obj",
	OP_bool:             "bool",
	OP_i8:               "i8",
	OP_i16:              "i16",
	OP_i32:              "i32",
	OP_i64:              "i64",
	OP_u8:               "u8",
	OP_u16:              "u16",
	OP_u32:              "u32",
	OP_u64:              "u64",
	OP_f32:              "f32",
	OP_f64:              "f64",
	OP_f32_slice:        "f32_slice",
	OP_f64_slice:        "f64_slice",
	OP_str:              "str",
	OP_bin:              "bin",
	OP_quote:            "quote",
	OP_number:           "number",
	OP_eface:            "eface",
	OP_iface:            "iface",
	OP_byte:             "byte",
	OP_text:             "text",
	OP_deref:            "deref",
	OP_index:            "index",
	OP_load:             "load",
	OP_save:             "save",
	OP_drop:             "drop",
	OP_drop_2:           "drop_2",
	OP_recurse:          "recurse",
	OP_is_nil:           "is_nil",
	OP_is_nil_p1:        "is_nil_p1",
	OP_is_zero_1:        "is_zero_1",
	OP_is_zero_2:        "is_zero_2",
	OP_is_zero_4:        "is_zero_4",
	OP_is_zero_8:        "is_zero_8",
	OP_is_zero_map:      "is_zero_map",
	OP_is_zero:          "is_zero",
	OP_goto:             "goto",
	OP_map_iter:         "map_iter",
	OP_map_stop:         "map_stop",
	OP_map_check_key:    "map_check_key",
	OP_map_write_key:    "map_write_key",
	OP_map_value_next:   "map_value_next",
	OP_map_string_any:   "map_string_any",
	OP_slice_begin:      "slice_begin",
	OP_slice_next:       "slice_next",
	OP_marshal:          "marshal",
	OP_marshal_p:        "marshal_p",
	OP_marshal_text:     "marshal_text",
	OP_marshal_text_p:   "marshal_text_p",
	OP_marshal_append:   "marshal_append",
	OP_marshal_append_p: "marshal_append_p",
	OP_cond_set:         "cond_set",
	OP_cond_testc:       "cond_testc",
	OP_struct_fields:    "struct_fields",
	OP_struct_begin:     "struct_begin",
	OP_struct_end:       "struct_end",
	OP_slice_eface:      "slice_eface",
	OP_slice_i64:        "slice_i64",
	OP_slice_str:        "slice_str",
	OP_slice_struct:     "slice_struct",
	OP_unsupported:      "unsupported",
}

func (self Op) String() string {
	if ret := _OpNames[self]; ret != "" {
		return ret
	} else {
		return "<invalid>"
	}
}

func OP_int() Op {
	switch unsafe.Sizeof(int(0)) {
	case 4:
		return OP_i32
	case 8:
		return OP_i64
	default:
		panic("unsupported int size")
	}
}

func OP_uint() Op {
	switch unsafe.Sizeof(uint(0)) {
	case 4:
		return OP_u32
	case 8:
		return OP_u64
	default:
		panic("unsupported uint size")
	}
}

func OP_uintptr() Op {
	switch unsafe.Sizeof(uintptr(0)) {
	case 4:
		return OP_u32
	case 8:
		return OP_u64
	default:
		panic("unsupported uintptr size")
	}
}

func OP_is_zero_ints() Op {
	switch unsafe.Sizeof(int(0)) {
	case 4:
		return OP_is_zero_4
	case 8:
		return OP_is_zero_8
	default:
		panic("unsupported int size")
	}
}

type Instr struct {
	u  uint64         // union {op: 8, _: 7, pv: 1, vi: 48}
	p  unsafe.Pointer // string data *byte, *resolver.FieldMeta, or *StructFieldSpec
	vt reflect.Type
}

func packOp(op Op) uint64 {
	return uint64(op) << 56
}

func newInsOp(op Op) Instr {
	return Instr{u: packOp(op)}
}

func newInsVi(op Op, vi int) Instr {
	return Instr{u: packOp(op) | rt.PackInt(vi)}
}

func literalNeedsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '<' || c == '>' || c == '&' || c >= 0x80 {
			return true
		}
	}
	return false
}

func newInsVs(op Op, vs string) Instr {
	var ptr unsafe.Pointer
	if len(vs) > 0 {
		ptr = unsafe.Pointer(unsafe.StringData(vs))
	}
	u := packOp(op) | rt.PackInt(len(vs))
	if literalNeedsSanitize(vs) {
		u |= _InsFlagNeedsSanitize
	}
	return Instr{
		u: u,
		p: ptr,
	}
}

func newInsVt(op Op, vt reflect.Type) Instr {
	u := packOp(op)
	if op == OP_slice_next && vt != nil {
		if sz := vt.Size(); sz > 0 && sz < 256 {
			u |= uint64(sz) << 48
		}
	}
	return Instr{
		u:  u,
		vt: vt,
	}
}

const (
	SpecFlagOmitEmpty uint8 = 1 << iota
	SpecFlagIsPtr
	SpecFlagOmitNil
)

type StructFieldSpec struct {
	KeyPtr      *byte
	Offset      uintptr
	Aux         uint32
	KeyLen      uint8
	NoCommaSkip uint8
	Kind        Op
	Flags       uint8
}

type StructSliceSpec struct {
	SliceType reflect.Type
	ElemSize  uintptr
	Specs     []StructFieldSpec
}

var (
	sliceSpecsMu  sync.Mutex
	sliceSpecsPtr atomic.Pointer[[]StructSliceSpec]
)

func RegisterStructSliceSpec(spec StructSliceSpec) uint32 {
	sliceSpecsMu.Lock()
	defer sliceSpecsMu.Unlock()
	var old []StructSliceSpec
	if p := sliceSpecsPtr.Load(); p != nil {
		old = *p
	}
	idx := uint32(len(old))
	next := make([]StructSliceSpec, idx+1)
	copy(next, old)
	next[idx] = spec
	sliceSpecsPtr.Store(&next)
	return idx
}

func GetStructSliceSpec(idx uint32) *StructSliceSpec {
	s := *sliceSpecsPtr.Load()
	return &s[idx]
}

// FirstKey returns the key pointer and length without its leading comma when NoCommaSkip == 1.
func (s *StructFieldSpec) FirstKey() (*byte, int) {
	skip := s.NoCommaSkip
	return (*byte)(unsafe.Add(unsafe.Pointer(s.KeyPtr), skip)), int(s.KeyLen - skip)
}

func newInsFields(specs []StructFieldSpec) Instr {
	cp := make([]StructFieldSpec, len(specs))
	copy(cp, specs)
	var ptr unsafe.Pointer
	if len(cp) > 0 {
		ptr = unsafe.Pointer(unsafe.SliceData(cp))
	}
	return Instr{
		u: packOp(OP_struct_fields) | rt.PackInt(len(cp)),
		p: ptr,
	}
}

func newInsVp(op Op, vt reflect.Type, pv bool) Instr {
	ins := newInsVt(op, vt)
	if pv {
		ins.u |= 1
	}
	return ins
}

func newInsVField(op Op, fv *resolver.FieldMeta) Instr {
	return Instr{
		u: packOp(op),
		p: unsafe.Pointer(fv),
	}
}

func newInsVFieldAddr(op Op, fv *resolver.FieldMeta, canAddr bool) Instr {
	u := packOp(op)
	if canAddr {
		u |= _InsFlagCanAddr
	}
	return Instr{
		u: u,
		p: unsafe.Pointer(fv),
	}
}

func (self Instr) Op() Op {
	return Op(self.u >> 56)
}

func (self Instr) Vi() int {
	return rt.UnpackInt(self.u)
}

func (self Instr) VField() *resolver.FieldMeta {
	return (*resolver.FieldMeta)(self.p)
}

func (self Instr) CanAddr() bool {
	return self.u&_InsFlagCanAddr != 0
}

func (self Instr) VFields() []StructFieldSpec {
	n := self.Vi()
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*StructFieldSpec)(self.p), n)
}

func (self Instr) Vs() string {
	n := self.Vi()
	if n == 0 {
		return ""
	}
	return unsafe.String((*byte)(self.p), n)
}

func (self Instr) Vk() reflect.Kind {
	return self.vt.Kind()
}

func (self Instr) Vt() reflect.Type {
	return self.vt
}

func (self Instr) Vr() reflect.Type {
	return self.vt
}

func (self Instr) Vp() (vt reflect.Type, pv bool) {
	return self.vt, (self.u & 1) == 1
}

func (self Instr) Vp2() (vt reflect.Type, pv bool) {
	return self.vt, (self.u & 1) == 1
}

func (self Instr) Byte() byte {
	return byte(self.Vi())
}

func (self Instr) I64() int64 {
	return int64(self.Vi())
}

func (self Instr) NeedsSanitize() bool {
	return self.u&_InsFlagNeedsSanitize != 0
}

func (self Instr) Vlen() int {
	if sz := int(uint8(self.u >> 48)); sz != 0 {
		return sz
	}
	return int(self.vt.Size())
}

func (self Instr) IsBranch() bool {
	op := self.Op()
	return op >= OP_goto && op <= OP_is_zero
}

func (self Instr) Disassemble() string {
	switch self.Op() {
	case OP_byte:
		return fmt.Sprintf("%-18s%s", self.Op(), strconv.QuoteRune(rune(self.Vi())))
	case OP_text:
		return fmt.Sprintf("%-18s%q", self.Op(), self.Vs())
	case OP_index:
		return fmt.Sprintf("%-18s%d bytes", self.Op(), self.Vi())
	case OP_is_nil,
		OP_is_nil_p1,
		OP_is_zero_1,
		OP_is_zero_2,
		OP_is_zero_4,
		OP_is_zero_8,
		OP_is_zero_map,
		OP_goto,
		OP_map_check_key,
		OP_map_write_key,
		OP_cond_testc:
		return fmt.Sprintf("%-18sL_%d", self.Op(), self.Vi())
	case OP_is_zero:
		return fmt.Sprintf("%-18s%s, L_%d", self.Op(), self.VField().Type.String(), self.Vi())
	case OP_slice_begin, OP_slice_next:
		return fmt.Sprintf("%-18sL_%d, %s", self.Op(), self.Vi(), self.Vt())
	case OP_marshal,
		OP_marshal_p,
		OP_marshal_text,
		OP_marshal_text_p,
		OP_marshal_append,
		OP_marshal_append_p,
		OP_map_iter,
		OP_iface,
		OP_unsupported:
		return fmt.Sprintf("%-18s%s", self.Op(), self.Vt())
	case OP_recurse:
		vt, pv := self.Vp()
		return fmt.Sprintf("%-18s%s, pv=%v", self.Op(), vt, pv)
	default:
		return self.Op().String()
	}
}

type Program []Instr

func (self Program) PC() int {
	return len(self)
}

func (self *Program) Add(op Op) {
	*self = append(*self, newInsOp(op))
}

func (self *Program) Key(op Op) {
	*self = append(*self, newInsVi(OP_byte, '"'), newInsOp(op), newInsVi(OP_byte, '"'))
}

func (self *Program) Int(op Op, vi int) {
	*self = append(*self, newInsVi(op, vi))
}

func (self *Program) Str(op Op, vs string) {
	*self = append(*self, newInsVs(op, vs))
}

func (self *Program) Rtt(op Op, vt reflect.Type) {
	*self = append(*self, newInsVt(op, vt))
}

func (self *Program) Fields(specs []StructFieldSpec) {
	*self = append(*self, newInsFields(specs))
}

func (self *Program) VField(op Op, fv *resolver.FieldMeta) {
	*self = append(*self, newInsVField(op, fv))
}

func (self *Program) VFieldAddr(op Op, fv *resolver.FieldMeta, canAddr bool) {
	*self = append(*self, newInsVFieldAddr(op, fv, canAddr))
}

func (self *Program) Vp(op Op, vt reflect.Type, pv bool) {
	*self = append(*self, newInsVp(op, vt, pv))
}

func (self *Program) Pin(i int) {
	v := &(*self)[i]
	v.u &= 0xffff000000000000
	v.u |= rt.PackInt(self.PC())
}

func (self *Program) Rel(v []int) {
	for _, i := range v {
		self.Pin(i)
	}
}

func (self *Program) Tag(n int) {
	if n >= vars.MaxStack {
		panic(vars.ERR_too_deep)
	}
}

func (self Program) Disassemble() string {
	nb := len(self)
	tab := make([]bool, nb+1)
	ret := make([]string, 0, nb+1)

	/*Mark the jump targets */
	for _, ins := range self {
		if ins.IsBranch() {
			tab[ins.Vi()] = true
		}
	}

	/* Disassemble each instruction */
	for i, ins := range self {
		if tab[i] {
			ret = append(ret, fmt.Sprintf("L_%d:", i))
		}
		ret = append(ret, "\t"+ins.Disassemble())
	}

	/* Add an end label if needed */
	if tab[nb] {
		ret = append(ret, fmt.Sprintf("L_%d:", nb))
	}

	/* Join all lines with LF */
	return strings.Join(ret, "\n")
}
