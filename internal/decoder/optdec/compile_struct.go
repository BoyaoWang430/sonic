//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
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

package optdec

import (
	"fmt"
	"math"
	"reflect"

	caching "github.com/bytedance/sonic/internal/optcaching"
	"github.com/bytedance/sonic/internal/resolver"
	"github.com/bytedance/sonic/option"
)

const (
	_MAX_FIELDS = 50 // cutoff at 50 fields struct
)

func (c *compiler) compileIntStringOption(vt reflect.Type) decFunc {
	switch vt.Size() {
	case 4:
		switch vt.Kind() {
		case reflect.Uint:
			fallthrough
		case reflect.Uintptr:
			return &u32StringDecoder{}
		case reflect.Int:
			return &i32StringDecoder{}
		}
	case 8:
		switch vt.Kind() {
		case reflect.Uint:
			fallthrough
		case reflect.Uintptr:
			return &u64StringDecoder{}
		case reflect.Int:
			return &i64StringDecoder{}
		}
	default:
		panic("not supported pointer size: " + fmt.Sprint(vt.Size()))
	}
	panic("unreachable")
}

func isInteger(vt reflect.Type) bool {
	switch vt.Kind() {
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint, reflect.Uintptr, reflect.Int:
		return true
	default:
		return false
	}
}

func (c *compiler) assertStringOptTypes(vt reflect.Type) {
	if c.depth > _CompileMaxDepth {
		panic(*stackOverflow)
	}

	c.depth += 1
	defer func() {
		c.depth -= 1
	}()

	if isInteger(vt) {
		return
	}

	switch vt.Kind() {
	case reflect.String, reflect.Bool, reflect.Float32, reflect.Float64:
		return
	case reflect.Ptr:
		c.assertStringOptTypes(vt.Elem())
	default:
		panicForInvalidStrType(vt)
	}
}

func (c *compiler) compileFieldStringOption(vt reflect.Type) decFunc {
	c.assertStringOptTypes(vt)
	unmDec := c.tryCompilePtrUnmarshaler(vt, true)
	if unmDec != nil {
		return unmDec
	}

	switch vt.Kind() {
	case reflect.String:
		if vt == jsonNumberType {
			return &numberStringDecoder{}
		}
		return &strStringDecoder{}
	case reflect.Bool:
		return &boolStringDecoder{}
	case reflect.Int8:
		return &i8StringDecoder{}
	case reflect.Int16:
		return &i16StringDecoder{}
	case reflect.Int32:
		return &i32StringDecoder{}
	case reflect.Int64:
		return &i64StringDecoder{}
	case reflect.Uint8:
		return &u8StringDecoder{}
	case reflect.Uint16:
		return &u16StringDecoder{}
	case reflect.Uint32:
		return &u32StringDecoder{}
	case reflect.Uint64:
		return &u64StringDecoder{}
	case reflect.Float32:
		return &f32StringDecoder{}
	case reflect.Float64:
		return &f64StringDecoder{}
	case reflect.Uint:
		fallthrough
	case reflect.Uintptr:
		fallthrough
	case reflect.Int:
		return c.compileIntStringOption(vt)
	case reflect.Ptr:
		return &ptrStrDecoder{
			typ:   vt.Elem(),
			deref: c.compileFieldStringOption(vt.Elem()),
		}
	default:
		panicForInvalidStrType(vt)
		return nil
	}
}

func (c *compiler) compileStruct(vt reflect.Type) decFunc {
	c.enter(vt)
	defer c.exit(vt)
	if c.namedPtr {
		c.namedPtr = false
		return c.compileStructBody(vt)
	}
	if dec, ok := c.compiled[vt]; ok {
		return dec
	}
	if c.depth > 1 && c.opts.MaxInlineDepth == option.DefaultMaxInlineDepth {
		if val, ok := programCache.Get(vt); ok {
			if sd, ok := val.(*structDecoder); ok {
				c.compiled[vt] = sd
				return sd
			}
		}
	}

	if c.depth >= c.opts.MaxInlineDepth+1 {
		return c.newRecursiveDecoder(vt)
	}
	dec := c.compileStructBody(vt)
	c.compiled[vt] = dec
	return dec
}

func (c *compiler) compileStructBody(vt reflect.Type) decFunc {
	fv := resolver.ResolveStruct(vt)
	entries := make([]fieldEntry, len(fv))
	decs := make([]decFunc, len(fv))
	var wideOffsets []uintptr
	var ptrScalarCount int
	var ptrStrCount int

	for i, f := range fv {
		var dec decFunc
		/* dealt with field tag options */
		if f.Opts&resolver.F_stringize != 0 {
			dec = c.compileFieldStringOption(f.Type)
		} else {
			dec = c.compile(f.Type)
		}

		/* deal with embedded pointer fields */
		if f.Path[0].Kind == resolver.F_deref {
			dec = &embeddedFieldPtrDecoder{
				field:     f,
				fieldDec:  dec,
				fieldName: f.Name,
			}
		}

		if f.Path[0].Size > math.MaxUint32 {
			entries[i] = fieldEntry{
				name:   f.Name,
				offset: uint32(len(wideOffsets)),
				kind:   fieldKindWideOffset,
			}
			wideOffsets = append(wideOffsets, f.Path[0].Size)
		} else {
			fe := newFieldEntry(f.Name, f.Path[0].Size, f.Type, dec)
			entries[i] = fe
			switch fe.kind {
			case fieldKindPtrString:
				ptrStrCount++
			case fieldKindPtrI64, fieldKindPtrInt, fieldKindPtrI32, fieldKindPtrI16, fieldKindPtrI8,
				fieldKindPtrU64, fieldKindPtrUint, fieldKindPtrU32, fieldKindPtrU16, fieldKindPtrU8,
				fieldKindPtrBool, fieldKindPtrF64, fieldKindPtrF32:
				ptrScalarCount++
			case fieldKindStruct, fieldKindPtrStruct:
				sd := (*structDecoder)(fe.subDec)
				ptrScalarCount += int(sd.ptrScalarHint)
				ptrStrCount += int(sd.ptrStrHint)
			}
		}
		decs[i] = dec
	}
	if ptrScalarCount > 16 {
		ptrScalarCount = 16
	}
	if ptrStrCount > 16 {
		ptrStrCount = 16
	}
	sd := &structDecoder{
		fieldMap:      caching.NewFieldCache(fv),
		fields:        entries,
		fieldKeySigs:  buildFieldKeySigs(entries),
		fieldDecs:     decs,
		wideOffsets:   wideOffsets,
		structName:    vt.Name(),
		typ:           vt,
		goType:        vt,
		fast:          len(wideOffsets) == 0,
		noInterface:   !typeHasInterface(vt),
		ptrScalarHint: uint8(ptrScalarCount),
		ptrStrHint:    uint8(ptrStrCount),
	}
	return sd
}

func typeHasInterface(vt reflect.Type) bool {
	return typeHasInterfaceSeen(vt, nil)
}

func typeHasInterfaceSeen(vt reflect.Type, seen map[reflect.Type]bool) bool {
	for vt.Kind() == reflect.Ptr {
		vt = vt.Elem()
	}
	switch vt.Kind() {
	case reflect.Interface:
		return true
	case reflect.Slice, reflect.Array:
		return typeHasInterfaceSeen(vt.Elem(), seen)
	case reflect.Map:
		return typeHasInterfaceSeen(vt.Key(), seen) || typeHasInterfaceSeen(vt.Elem(), seen)
	case reflect.Struct:
		if seen == nil {
			seen = make(map[reflect.Type]bool)
		}
		if seen[vt] {
			return false
		}
		seen[vt] = true
		for i := 0; i < vt.NumField(); i++ {
			if typeHasInterfaceSeen(vt.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}
