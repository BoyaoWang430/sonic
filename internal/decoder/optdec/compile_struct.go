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
		if val := programCache.Get(vt); val != nil {
			if sd, ok := val.(*structDecoder); ok {
				c.compiled[vt] = sd
				return sd
			}
		}
	}

	maxDepth := c.opts.MaxInlineDepth
	if maxDepth == option.DefaultMaxInlineDepth {
		maxDepth = 16
	}
	if c.depth >= maxDepth+1 {
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
			entries[i] = newFieldEntry(f.Name, f.Path[0].Size, dec)
		}
		decs[i] = dec
	}
	return &structDecoder{
		fieldMap:    caching.NewFieldCache(fv),
		fields:      entries,
		fieldDecs:   decs,
		wideOffsets: wideOffsets,
		structName:  vt.Name(),
		typ:         vt,
		goType:      vt,
	}
}
