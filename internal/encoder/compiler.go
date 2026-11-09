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

package encoder

import (
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/prim"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/encoder/vm"
	"github.com/bytedance/sonic/internal/resolver"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/option"
)

func ForceUseVM() {
	vm.SetCompiler(makeEncoderVM)
	pretouchType = pretouchTypeVM
	encodeValuePointer = vm.EncodeValuePointer
	vars.UseVM = true
}

var encodeValuePointer func(buf *[]byte, vt *rt.GoType, p unsafe.Pointer, sb *vars.Stack, fv uint64) error

func makeEncoderVM(vt *rt.GoType, ex ...interface{}) (interface{}, error) {
	pp, err := NewCompiler().Compile(vt.Pack(), ex[0].(bool))
	if err != nil {
		return nil, err
	}
	return &pp, nil
}

var pretouchType func(_vt reflect.Type, opts option.CompileOptions, v uint8) (map[reflect.Type]uint8, error)

func pretouchTypeVM(_vt reflect.Type, opts option.CompileOptions, v uint8) (map[reflect.Type]uint8, error) {
	/* compile function */
	compiler := NewCompiler().apply(opts)
	encoder := func(vt *rt.GoType, ex ...interface{}) (interface{}, error) {
		pp, err := compiler.Compile(vt.Pack(), ex[0].(bool))
		if err != nil {
			return nil, err
		}
		return &pp, nil
	}

	/* find or compile */
	vt := rt.UnpackType(_vt)
	pv := v == 1
	if val := vars.GetProgram(vt, pv); val != nil {
		return nil, nil
	} else if _, err := vars.ComputeProgram(vt, encoder, pv); err == nil {
		return compiler.rec, nil
	} else {
		return nil, err
	}
}

type vmPretouchKey struct {
	vt *rt.GoType
	pv bool
}

func pretouchRecVMBatch(vtm map[reflect.Type]uint8, opts option.CompileOptions) error {
	if opts.RecursiveDepth < 0 || len(vtm) == 0 {
		return nil
	}
	seen := make(map[vmPretouchKey]struct{}, len(vtm))
	var batchVal []caching.ProgramBatchItem
	var batchPV []caching.ProgramBatchItem

	for opts.RecursiveDepth >= 0 && len(vtm) > 0 {
		next := make(map[reflect.Type]uint8)
		for vt, v := range vtm {
			gvt := rt.UnpackType(vt)
			pv := v == 1
			key := vmPretouchKey{vt: gvt, pv: pv}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if vars.GetProgram(gvt, pv) != nil {
				continue
			}
			compiler := NewCompiler().apply(opts)
			pp, err := compiler.Compile(vt, pv)
			if err != nil {
				return err
			}
			prog := &pp
			if pv {
				batchPV = append(batchPV, caching.ProgramBatchItem{Vt: gvt, Fn: prog})
			} else {
				batchVal = append(batchVal, caching.ProgramBatchItem{Vt: gvt, Fn: prog})
			}
			for svt, sv := range compiler.rec {
				next[svt] = sv
			}
		}
		opts.RecursiveDepth--
		vtm = next
	}
	if len(batchVal) > 0 {
		vars.PutPrograms(batchVal, false)
	}
	if len(batchPV) > 0 {
		vars.PutPrograms(batchPV, true)
	}
	return nil
}

func pretouchRec(vtm map[reflect.Type]uint8, opts option.CompileOptions) error {
	if vars.UseVM {
		return pretouchRecVMBatch(vtm, opts)
	}
	if opts.RecursiveDepth < 0 || len(vtm) == 0 {
		return nil
	}
	next := make(map[reflect.Type]uint8)
	for vt, v := range vtm {
		sub, err := pretouchType(vt, opts, v)
		if err != nil {
			return err
		}
		for svt, v := range sub {
			next[svt] = v
		}
	}
	opts.RecursiveDepth -= 1
	return pretouchRec(next, opts)
}

type Compiler struct {
	opts  option.CompileOptions
	pv    bool
	useVM bool
	tab   map[reflect.Type]bool
	rec   map[reflect.Type]uint8
}

func NewCompiler() *Compiler {
	return &Compiler{
		opts:  option.DefaultCompileOptions(),
		useVM: vars.UseVM,
		tab:   map[reflect.Type]bool{},
		rec:   map[reflect.Type]uint8{},
	}
}

func (self *Compiler) WithVM(useVM bool) *Compiler {
	self.useVM = useVM
	return self
}

func (self *Compiler) apply(opts option.CompileOptions) *Compiler {
	self.opts = opts
	if self.opts.RecursiveDepth > 0 {
		self.rec = map[reflect.Type]uint8{}
	}
	return self
}

func (self *Compiler) rescue(ep *error) {
	if val := recover(); val != nil {
		if err, ok := val.(error); ok {
			*ep = err
		} else {
			panic(val)
		}
	}
}

func (self *Compiler) Compile(vt reflect.Type, pv bool) (ret ir.Program, err error) {
	defer self.rescue(&err)
	self.compileOne(&ret, 0, vt, pv)
	return
}

func (self *Compiler) compileOne(p *ir.Program, sp int, vt reflect.Type, pv bool) {
	if self.tab[vt] {
		p.Vp(ir.OP_recurse, vt, pv)
	} else {
		self.compileRec(p, sp, vt, pv)
	}
}

func (self *Compiler) tryCompileMarshaler(p *ir.Program, vt reflect.Type, pv bool) bool {
	pt := reflect.PtrTo(vt)

	if self.useVM {
		if pv && pt.Implements(prim.JSONAppenderType) {
			self.addMarshalerOp(p, ir.OP_marshal_append_p, pt, prim.JSONAppenderType)
			return true
		}
		if vt.Implements(prim.JSONAppenderType) {
			self.compileMarshaler(p, ir.OP_marshal_append, vt, prim.JSONAppenderType)
			return true
		}
	}

	/* check for addressable `json.Marshaler` with pointer receiver */
	if pv && pt.Implements(vars.JsonMarshalerType) {
		self.addMarshalerOp(p, ir.OP_marshal_p, pt, vars.JsonMarshalerType)
		return true
	}

	/* check for `json.Marshaler` */
	if vt.Implements(vars.JsonMarshalerType) {
		self.compileMarshaler(p, ir.OP_marshal, vt, vars.JsonMarshalerType)
		return true
	}

	/* check for addressable `encoding.TextMarshaler` with pointer receiver */
	if pv && pt.Implements(vars.EncodingTextMarshalerType) {
		self.addMarshalerOp(p, ir.OP_marshal_text_p, pt, vars.EncodingTextMarshalerType)
		return true
	}

	/* check for `encoding.TextMarshaler` */
	if vt.Implements(vars.EncodingTextMarshalerType) {
		self.compileMarshaler(p, ir.OP_marshal_text, vt, vars.EncodingTextMarshalerType)
		return true
	}

	return false
}

func (self *Compiler) compileRec(p *ir.Program, sp int, vt reflect.Type, pv bool) {
	pr := self.pv

	if self.tryCompileMarshaler(p, vt, pv) {
		return
	}

	/* enter the recursion, and compile the type */
	self.pv = pv
	self.tab[vt] = true
	self.compileOps(p, sp, vt)

	/* exit the recursion */
	self.pv = pr
	delete(self.tab, vt)
}

func (self *Compiler) compileOps(p *ir.Program, sp int, vt reflect.Type) {
	switch vt.Kind() {
	case reflect.Bool:
		p.Add(ir.OP_bool)
	case reflect.Int:
		p.Add(ir.OP_int())
	case reflect.Int8:
		p.Add(ir.OP_i8)
	case reflect.Int16:
		p.Add(ir.OP_i16)
	case reflect.Int32:
		p.Add(ir.OP_i32)
	case reflect.Int64:
		p.Add(ir.OP_i64)
	case reflect.Uint:
		p.Add(ir.OP_uint())
	case reflect.Uint8:
		p.Add(ir.OP_u8)
	case reflect.Uint16:
		p.Add(ir.OP_u16)
	case reflect.Uint32:
		p.Add(ir.OP_u32)
	case reflect.Uint64:
		p.Add(ir.OP_u64)
	case reflect.Uintptr:
		p.Add(ir.OP_uintptr())
	case reflect.Float32:
		p.Add(ir.OP_f32)
	case reflect.Float64:
		p.Add(ir.OP_f64)
	case reflect.String:
		self.compileString(p, vt)
	case reflect.Array:
		self.compileArray(p, sp, vt.Elem(), vt.Len())
	case reflect.Interface:
		self.compileInterface(p, vt)
	case reflect.Map:
		self.compileMap(p, sp, vt)
	case reflect.Ptr:
		self.compilePtr(p, sp, vt.Elem())
	case reflect.Slice:
		self.compileSlice(p, sp, vt.Elem())
	case reflect.Struct:
		self.compileStruct(p, sp, vt)
	default:
		self.compileUnsupportedType(p, vt)
	}
}

func (self *Compiler) compileNil(p *ir.Program, sp int, vt reflect.Type, nil_op ir.Op, fn func(*ir.Program, int, reflect.Type)) {
	x := p.PC()
	p.Add(ir.OP_is_nil)
	fn(p, sp, vt)
	e := p.PC()
	p.Add(ir.OP_goto)
	p.Pin(x)
	p.Add(nil_op)
	p.Pin(e)
}

func (self *Compiler) compilePtr(p *ir.Program, sp int, vt reflect.Type) {
	self.compileNil(p, sp, vt, ir.OP_null, self.compilePtrBody)
}

func (self *Compiler) compilePtrBody(p *ir.Program, sp int, vt reflect.Type) {
	p.Tag(sp)
	p.Add(ir.OP_save)
	p.Add(ir.OP_deref)
	self.compileOne(p, sp+1, vt, true)
	p.Add(ir.OP_drop)
}

func (self *Compiler) compileMap(p *ir.Program, sp int, vt reflect.Type) {
	self.compileNil(p, sp, vt, ir.OP_empty_obj, self.compileMapBody)
}

func (self *Compiler) compileMapBody(p *ir.Program, sp int, vt reflect.Type) {
	p.Tag(sp + 1)
	p.Int(ir.OP_byte, '{')
	e := p.PC()
	p.Add(ir.OP_is_zero_map)
	p.Add(ir.OP_save)
	if self.useVM && vt == vars.StringAnyMapType {
		p.Add(ir.OP_save)
		p.Add(ir.OP_map_string_any)
		p.Add(ir.OP_drop_2)
		p.Pin(e)
		p.Int(ir.OP_byte, '}')
		return
	}
	p.Rtt(ir.OP_map_iter, vt)
	p.Add(ir.OP_save)
	i := p.PC()
	p.Add(ir.OP_map_check_key)
	u := p.PC()
	p.Add(ir.OP_map_write_key)
	self.compileMapBodyKey(p, vt.Key())
	p.Pin(u)
	p.Int(ir.OP_byte, ':')
	p.Add(ir.OP_map_value_next)
	self.compileOne(p, sp+2, vt.Elem(), false)
	j := p.PC()
	p.Add(ir.OP_map_check_key)
	p.Int(ir.OP_byte, ',')
	v := p.PC()
	p.Add(ir.OP_map_write_key)
	self.compileMapBodyKey(p, vt.Key())
	p.Pin(v)
	p.Int(ir.OP_byte, ':')
	p.Add(ir.OP_map_value_next)
	self.compileOne(p, sp+2, vt.Elem(), false)
	p.Int(ir.OP_goto, j)
	p.Pin(i)
	p.Pin(j)
	p.Add(ir.OP_map_stop)
	p.Add(ir.OP_drop_2)
	p.Pin(e)
	p.Int(ir.OP_byte, '}')
}

func (self *Compiler) compileMapBodyKey(p *ir.Program, vk reflect.Type) {
	// followed as `encoding/json/emcode.go:resolveKeyName
	if vk.Kind() == reflect.String {
		self.compileString(p, vk)
		return
	}

	if !vk.Implements(vars.EncodingTextMarshalerType) {
		self.compileMapBodyTextKey(p, vk)
	} else {
		self.compileMapBodyUtextKey(p, vk)
	}
}

func (self *Compiler) compileMapBodyTextKey(p *ir.Program, vk reflect.Type) {
	switch vk.Kind() {
	case reflect.Invalid:
		panic("map key is nil")
	case reflect.Bool:
		p.Key(ir.OP_bool)
	case reflect.Int:
		p.Key(ir.OP_int())
	case reflect.Int8:
		p.Key(ir.OP_i8)
	case reflect.Int16:
		p.Key(ir.OP_i16)
	case reflect.Int32:
		p.Key(ir.OP_i32)
	case reflect.Int64:
		p.Key(ir.OP_i64)
	case reflect.Uint:
		p.Key(ir.OP_uint())
	case reflect.Uint8:
		p.Key(ir.OP_u8)
	case reflect.Uint16:
		p.Key(ir.OP_u16)
	case reflect.Uint32:
		p.Key(ir.OP_u32)
	case reflect.Uint64:
		p.Key(ir.OP_u64)
	case reflect.Uintptr:
		p.Key(ir.OP_uintptr())
	case reflect.Float32:
		p.Key(ir.OP_f32)
	case reflect.Float64:
		p.Key(ir.OP_f64)
	case reflect.String:
		self.compileString(p, vk)
	default:
		panic(vars.Error_type(vk))
	}
}

func (self *Compiler) compileMapBodyUtextKey(p *ir.Program, vk reflect.Type) {
	if vk.Kind() != reflect.Ptr {
		self.addMarshalerOp(p, ir.OP_marshal_text, vk, vars.EncodingTextMarshalerType)
	} else {
		self.compileMapBodyUtextPtr(p, vk)
	}
}

func (self *Compiler) compileMapBodyUtextPtr(p *ir.Program, vk reflect.Type) {
	i := p.PC()
	p.Add(ir.OP_is_nil)
	self.addMarshalerOp(p, ir.OP_marshal_text, vk, vars.EncodingTextMarshalerType)
	j := p.PC()
	p.Add(ir.OP_goto)
	p.Pin(i)
	p.Str(ir.OP_text, "\"\"")
	p.Pin(j)
}

func (self *Compiler) compileSlice(p *ir.Program, sp int, vt reflect.Type) {
	self.compileNil(p, sp, vt, ir.OP_empty_arr, self.compileSliceBody)
}

func (self *Compiler) compileSliceBody(p *ir.Program, sp int, vt reflect.Type) {
	if vars.IsSimpleByte(vt) {
		p.Add(ir.OP_bin)
	} else {
		self.compileSliceArray(p, sp, vt)
	}
}

func (self *Compiler) compileSliceArray(p *ir.Program, sp int, vt reflect.Type) {
	if self.compileFloatSlice(p, sp, vt) {
		return
	}
	p.Tag(sp)
	p.Int(ir.OP_byte, '[')
	e := p.PC()
	p.Add(ir.OP_is_nil)
	p.Add(ir.OP_save)
	p.Add(ir.OP_slice_len)
	i := p.PC()
	p.Rtt(ir.OP_slice_next, vt)
	self.compileOne(p, sp+1, vt, true)
	j := p.PC()
	p.Rtt(ir.OP_slice_next, vt)
	p.Int(ir.OP_byte, ',')
	self.compileOne(p, sp+1, vt, true)
	p.Int(ir.OP_goto, j)
	p.Pin(i)
	p.Pin(j)
	p.Add(ir.OP_drop)
	p.Pin(e)
	p.Int(ir.OP_byte, ']')
}

func (self *Compiler) compileArray(p *ir.Program, sp int, vt reflect.Type, nb int) {
	p.Tag(sp)
	p.Int(ir.OP_byte, '[')
	p.Add(ir.OP_save)

	/* first item */
	if nb != 0 {
		self.compileOne(p, sp+1, vt, self.pv)
		p.Add(ir.OP_load)
	}

	/* remaining items */
	for i := 1; i < nb; i++ {
		p.Int(ir.OP_byte, ',')
		p.Int(ir.OP_index, i*int(vt.Size()))
		self.compileOne(p, sp+1, vt, self.pv)
		p.Add(ir.OP_load)
	}

	/* end of array */
	p.Add(ir.OP_drop)
	p.Int(ir.OP_byte, ']')
}

func (self *Compiler) compileString(p *ir.Program, vt reflect.Type) {
	if vt != vars.JsonNumberType {
		p.Add(ir.OP_str)
	} else {
		p.Add(ir.OP_number)
	}
}

func (self *Compiler) compileStruct(p *ir.Program, sp int, vt reflect.Type) {
	maxDepth := self.opts.MaxInlineDepth
	if self.useVM && maxDepth == option.DefaultMaxInlineDepth {
		maxDepth = 16
	}
	if sp >= maxDepth || p.PC() >= vars.MAX_ILBUF || (!self.useVM && sp > 0 && vt.NumField() >= vars.MAX_FIELDS) {
		p.Vp(ir.OP_recurse, vt, self.pv)
		if self.opts.RecursiveDepth > 0 {
			if self.pv {
				self.rec[vt] = 1
			} else {
				self.rec[vt] = 0
			}
		}
	} else {
		self.compileStructBody(p, sp, vt)
	}
}

func (self *Compiler) isCustomMarshaler(vt reflect.Type, pv bool) bool {
	pt := reflect.PtrTo(vt)
	if self.useVM && ((pv && pt.Implements(prim.JSONAppenderType)) || vt.Implements(prim.JSONAppenderType)) {
		return true
	}
	if pv && pt.Implements(vars.JsonMarshalerType) {
		return true
	}
	if vt.Implements(vars.JsonMarshalerType) {
		return true
	}
	if pv && pt.Implements(vars.EncodingTextMarshalerType) {
		return true
	}
	if vt.Implements(vars.EncodingTextMarshalerType) {
		return true
	}
	return false
}

func specNoCommaSkip(hasGuaranteedField bool) uint8 {
	if hasGuaranteedField {
		return 0
	}
	return 1
}

func (self *Compiler) tryCompileStructFieldSpec(fv *resolver.FieldMeta, baseOffset uintptr, hasGuaranteedField bool) (ir.StructFieldSpec, bool) {
	if !self.useVM || fv.KeyNeedsSanitize || len(fv.PaddedCommaKey) > 32 {
		return ir.StructFieldSpec{}, false
	}
	if (fv.Opts & resolver.F_omitzero) != 0 {
		return ir.StructFieldSpec{}, false
	}
	offset := baseOffset
	for _, o := range fv.Path {
		if o.Kind == resolver.F_deref {
			return ir.StructFieldSpec{}, false
		}
		offset += o.Size
	}
	ft := fv.Type
	if self.tab[ft] || self.isCustomMarshaler(ft, self.pv) {
		return ir.StructFieldSpec{}, false
	}
	isPtr := false
	if ft.Kind() == reflect.Ptr && (fv.Opts&resolver.F_stringize) == 0 {
		et := ft.Elem()
		if !self.tab[et] && !self.isCustomMarshaler(ft, true) && !self.isCustomMarshaler(et, true) {
			switch et.Kind() {
			case reflect.Bool,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
				reflect.Float32, reflect.Float64:
				ft = et
				isPtr = true
			case reflect.String:
				if et != vars.JsonNumberType {
					ft = et
					isPtr = true
				}
			}
		}
	}
	var kind ir.Op
	if (fv.Opts & resolver.F_stringize) != 0 {
		if ft != vars.JsonNumberType && ft.Kind() == reflect.String {
			kind = ir.OP_quote
		} else {
			return ir.StructFieldSpec{}, false
		}
	} else {
		switch ft.Kind() {
		case reflect.Bool:
			kind = ir.OP_bool
		case reflect.Int:
			kind = ir.OP_int()
		case reflect.Int8:
			kind = ir.OP_i8
		case reflect.Int16:
			kind = ir.OP_i16
		case reflect.Int32:
			kind = ir.OP_i32
		case reflect.Int64:
			kind = ir.OP_i64
		case reflect.Uint:
			kind = ir.OP_uint()
		case reflect.Uint8:
			kind = ir.OP_u8
		case reflect.Uint16:
			kind = ir.OP_u16
		case reflect.Uint32:
			kind = ir.OP_u32
		case reflect.Uint64:
			kind = ir.OP_u64
		case reflect.Uintptr:
			kind = ir.OP_uintptr()
		case reflect.Float32:
			kind = ir.OP_f32
		case reflect.Float64:
			kind = ir.OP_f64
		case reflect.String:
			if ft != vars.JsonNumberType {
				kind = ir.OP_str
			} else {
				kind = ir.OP_number
			}
		case reflect.Interface:
			if ft.NumMethod() == 0 {
				kind = ir.OP_eface
			} else {
				return ir.StructFieldSpec{}, false
			}
		case reflect.Slice:
			if (fv.Opts&resolver.F_omitempty) != 0 && self.opts.EncOnlyOmitNull {
				return ir.StructFieldSpec{}, false
			}
			et := ft.Elem()
			if self.tab[et] || self.isCustomMarshaler(et, true) {
				return ir.StructFieldSpec{}, false
			}
			switch et.Kind() {
			case reflect.Interface:
				if et.NumMethod() == 0 {
					kind = ir.OP_slice_eface
				} else {
					return ir.StructFieldSpec{}, false
				}
			case reflect.Int:
				if ir.OP_int() == ir.OP_i64 {
					kind = ir.OP_slice_i64
				} else {
					return ir.StructFieldSpec{}, false
				}
			case reflect.Int64:
				kind = ir.OP_slice_i64
			case reflect.String:
				if et != vars.JsonNumberType {
					kind = ir.OP_slice_str
				} else {
					return ir.StructFieldSpec{}, false
				}
			default:
				return ir.StructFieldSpec{}, false
			}
		default:
			return ir.StructFieldSpec{}, false
		}
	}
	var flags uint8
	if isPtr {
		flags |= ir.SpecFlagIsPtr
		if (fv.Opts & resolver.F_omitempty) != 0 {
			flags |= ir.SpecFlagOmitNil
		}
	} else if (fv.Opts&resolver.F_omitempty) != 0 && (!self.opts.EncOnlyOmitNull || kind == ir.OP_eface) {
		flags |= ir.SpecFlagOmitEmpty
	}
	return ir.StructFieldSpec{
		CommaKey:    fv.PaddedCommaKey,
		Offset:      offset,
		NoCommaSkip: specNoCommaSkip(hasGuaranteedField),
		Kind:        kind,
		Flags:       flags,
	}, true
}

func (self *Compiler) tryFlattenNestedStruct(fv *resolver.FieldMeta, baseOffset uintptr, sp int) (uintptr, bool) {
	maxDepth := self.opts.MaxInlineDepth
	if self.useVM && maxDepth == option.DefaultMaxInlineDepth {
		maxDepth = 16
	}
	if !self.useVM || sp+1 >= maxDepth || fv.PaddedStructBeginKey == "" {
		return 0, false
	}
	ft := fv.Type
	if ft.Kind() != reflect.Struct || (fv.Opts&(resolver.F_omitempty|resolver.F_omitzero|resolver.F_stringize)) != 0 {
		return 0, false
	}
	offset := baseOffset
	for _, o := range fv.Path {
		if o.Kind == resolver.F_deref {
			return 0, false
		}
		offset += o.Size
	}
	if self.tab[ft] || self.isCustomMarshaler(ft, self.pv) {
		return 0, false
	}
	return offset, true
}

func (self *Compiler) compileStructBody(p *ir.Program, sp int, vt reflect.Type) {
	p.Tag(sp)
	p.Int(ir.OP_byte, '{')
	p.Add(ir.OP_save)
	p.Add(ir.OP_cond_set)

	var specs []ir.StructFieldSpec
	flushSpecs := func() {
		if len(specs) > 0 {
			p.Fields(specs)
			specs = nil
		}
	}

	var compileFields func(curType reflect.Type, baseOffset uintptr, depth int)
	compileFields = func(curType reflect.Type, baseOffset uintptr, depth int) {
		fvs := resolver.ResolveStruct(curType)
		hasPreviousField := false
		hasGuaranteedField := false
		for i, fv := range fvs {
			var s []int

			/* "omitempty" for arrays */
			if fv.Type.Kind() == reflect.Array {
				if fv.Type.Len() == 0 && (fv.Opts&resolver.F_omitempty) != 0 {
					continue
				}
			}

			if childOffset, ok := self.tryFlattenNestedStruct(&fvs[i], baseOffset, depth); ok {
				specs = append(specs, ir.StructFieldSpec{
					CommaKey:    fv.PaddedStructBeginKey,
					NoCommaSkip: specNoCommaSkip(hasGuaranteedField),
					Kind:        ir.OP_struct_begin,
				})
				self.tab[fv.Type] = true
				compileFields(fv.Type, childOffset, depth+1)
				delete(self.tab, fv.Type)
				specs = append(specs, ir.StructFieldSpec{
					Kind: ir.OP_struct_end,
				})
				hasPreviousField = true
				hasGuaranteedField = true
				continue
			}

			if spec, ok := self.tryCompileStructFieldSpec(&fvs[i], baseOffset, hasGuaranteedField); ok {
				specs = append(specs, spec)
				hasPreviousField = true
				if spec.Flags&(ir.SpecFlagOmitEmpty|ir.SpecFlagOmitNil) == 0 {
					hasGuaranteedField = true
				}
				continue
			}
			flushSpecs()

			/* index to the field relative to the saved root struct pointer */
			for idx, o := range fv.Path {
				off := int(o.Size)
				if idx == 0 {
					off += int(baseOffset)
				}
				if p.Int(ir.OP_index, off); o.Kind == resolver.F_deref {
					s = append(s, p.PC())
					p.Add(ir.OP_is_nil)
					p.Add(ir.OP_deref)
				}
			}

			/* check for "omitempty" option */
			if fv.Type.Kind() != reflect.Struct && fv.Type.Kind() != reflect.Array && (fv.Opts&resolver.F_omitempty) != 0 {
				if self.opts.EncOnlyOmitNull {
					x := p.PC()
					if self.compileStructFieldOmitNilPtr(p, fv.Type) {
						s = append(s, x)
					}
				} else {
					s = append(s, p.PC())
					self.compileStructFieldEmpty(p, fv.Type)
				}
			}
			/* check for "omitzero" option */
			if fv.Opts&resolver.F_omitzero != 0 {
				s = append(s, p.PC())
				p.VField(ir.OP_is_zero, &fvs[i])
			}

			/* A non-skippable field guarantees a comma before every later field. */
			key := fv.PaddedCommaKey[1:]
			if hasGuaranteedField {
				key = fv.PaddedCommaKey
			} else if hasPreviousField || len(s) != 0 {
				/* Earlier fields may all have been omitted. */
				j := p.PC()
				p.Add(ir.OP_cond_testc)
				p.Int(ir.OP_byte, ',')
				p.Pin(j)
			}
			hasPreviousField = true
			if len(s) == 0 {
				hasGuaranteedField = true
			}

			/* compile the key and value */
			ft := fv.Type
			p.Str(ir.OP_text, key)

			/* check for "stringnize" option */
			if (fv.Opts & resolver.F_stringize) == 0 {
				self.compileOne(p, sp+1, ft, self.pv)
			} else {
				self.compileStructFieldStr(p, sp+1, ft)
			}

			/* patch the skipping jumps and reload the struct pointer */
			p.Rel(s)
			p.Add(ir.OP_load)
		}
	}

	compileFields(vt, 0, sp)
	flushSpecs()

	/* end of object */
	p.Add(ir.OP_drop)
	p.Int(ir.OP_byte, '}')
}

func (self *Compiler) compileStructFieldStr(p *ir.Program, sp int, vt reflect.Type) {
	// NOTICE: according to encoding/json, Marshaler type has higher priority than string option
	// see issue:
	if self.tryCompileMarshaler(p, vt, self.pv) {
		return
	}

	pc := -1
	ft := vt
	sv := false

	/* dereference the pointer if needed */
	if ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}

	/* check if it can be stringized */
	switch ft.Kind() {
	case reflect.Bool:
		sv = true
	case reflect.Int:
		sv = true
	case reflect.Int8:
		sv = true
	case reflect.Int16:
		sv = true
	case reflect.Int32:
		sv = true
	case reflect.Int64:
		sv = true
	case reflect.Uint:
		sv = true
	case reflect.Uint8:
		sv = true
	case reflect.Uint16:
		sv = true
	case reflect.Uint32:
		sv = true
	case reflect.Uint64:
		sv = true
	case reflect.Uintptr:
		sv = true
	case reflect.Float32:
		sv = true
	case reflect.Float64:
		sv = true
	case reflect.String:
		sv = true
	}

	/* if it's not, ignore the "string" and follow the regular path */
	if !sv {
		self.compileOne(p, sp, vt, self.pv)
		return
	}

	/* dereference the pointer */
	if vt.Kind() == reflect.Ptr {
		pc = p.PC()
		vt = vt.Elem()
		p.Add(ir.OP_is_nil)
		p.Add(ir.OP_deref)
	}

	/* special case of a double-quoted string */
	if ft != vars.JsonNumberType && ft.Kind() == reflect.String {
		p.Add(ir.OP_quote)
	} else {
		self.compileStructFieldQuoted(p, sp, vt)
	}

	/* the "null" case of the pointer */
	if pc != -1 {
		e := p.PC()
		p.Add(ir.OP_goto)
		p.Pin(pc)
		p.Add(ir.OP_null)
		p.Pin(e)
	}
}

func (self *Compiler) compileStructFieldEmpty(p *ir.Program, vt reflect.Type) {
	switch vt.Kind() {
	case reflect.Bool:
		p.Add(ir.OP_is_zero_1)
	case reflect.Int:
		p.Add(ir.OP_is_zero_ints())
	case reflect.Int8:
		p.Add(ir.OP_is_zero_1)
	case reflect.Int16:
		p.Add(ir.OP_is_zero_2)
	case reflect.Int32:
		p.Add(ir.OP_is_zero_4)
	case reflect.Int64:
		p.Add(ir.OP_is_zero_8)
	case reflect.Uint:
		p.Add(ir.OP_is_zero_ints())
	case reflect.Uint8:
		p.Add(ir.OP_is_zero_1)
	case reflect.Uint16:
		p.Add(ir.OP_is_zero_2)
	case reflect.Uint32:
		p.Add(ir.OP_is_zero_4)
	case reflect.Uint64:
		p.Add(ir.OP_is_zero_8)
	case reflect.Uintptr:
		p.Add(ir.OP_is_nil)
	case reflect.Float32:
		p.Add(ir.OP_is_zero_4)
	case reflect.Float64:
		p.Add(ir.OP_is_zero_8)
	case reflect.String:
		p.Add(ir.OP_is_nil_p1)
	case reflect.Interface:
		p.Add(ir.OP_is_nil)
	case reflect.Map:
		p.Add(ir.OP_is_zero_map)
	case reflect.Ptr:
		p.Add(ir.OP_is_nil)
	case reflect.Slice:
		p.Add(ir.OP_is_nil_p1)
	default:
		panic(vars.Error_type(vt))
	}
}

func (self *Compiler) compileStructFieldOmitNilPtr(p *ir.Program, vt reflect.Type) bool {
	switch vt.Kind() {
	case reflect.Interface:
		p.Add(ir.OP_is_nil)
		return true
	case reflect.Map:
		p.Add(ir.OP_is_nil)
		return true
	case reflect.Ptr:
		p.Add(ir.OP_is_nil)
		return true
	case reflect.Slice:
		p.Add(ir.OP_is_nil)
		return true
	default:
		return false
	}
}

func (self *Compiler) compileStructFieldQuoted(p *ir.Program, sp int, vt reflect.Type) {
	p.Int(ir.OP_byte, '"')
	self.compileOne(p, sp, vt, self.pv)
	p.Int(ir.OP_byte, '"')
}

func (self *Compiler) compileInterface(p *ir.Program, vt reflect.Type) {
	/* iface and efaces are different */
	if vt.NumMethod() == 0 {
		p.Add(ir.OP_eface)
		return
	}

	x := p.PC()
	p.Add(ir.OP_is_nil_p1)
	p.Add(ir.OP_iface)

	/* the "null" value */
	e := p.PC()
	p.Add(ir.OP_goto)
	p.Pin(x)
	p.Add(ir.OP_null)
	p.Pin(e)
}

func (self *Compiler) compileUnsupportedType(p *ir.Program, vt reflect.Type) {
	p.Rtt(ir.OP_unsupported, vt)
}

func (self *Compiler) compileMarshaler(p *ir.Program, op ir.Op, vt reflect.Type, mt reflect.Type) {
	pc := p.PC()
	vk := vt.Kind()

	/* direct receiver */
	if vk != reflect.Ptr {
		self.addMarshalerOp(p, op, vt, mt)
		return
	}
	/* value receiver with a pointer type, check for nil before calling the marshaler */
	p.Add(ir.OP_is_nil)

	self.addMarshalerOp(p, op, vt, mt)

	i := p.PC()
	p.Add(ir.OP_goto)
	p.Pin(pc)
	p.Add(ir.OP_null)
	p.Pin(i)
}

func (self *Compiler) addMarshalerOp(p *ir.Program, op ir.Op, vt reflect.Type, mt reflect.Type) {
	if self.useVM {
		itab := rt.GetItab(rt.IfaceType(rt.UnpackType(mt)), rt.UnpackType(vt), true)
		p.Vtab(op, vt, itab)
	} else {
		// OPT: get itab here
		p.Rtt(op, vt)
	}
}
