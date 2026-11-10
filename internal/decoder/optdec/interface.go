package optdec

import (
	"encoding"
	"encoding/json"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

type efaceDecoder struct {
}

func (d *efaceDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	cur := *(*interface{})(vp)
	if cur == nil {
		ret, err := node.AsEface(ctx)
		if err != nil {
			return err
		}
		*(*interface{})(vp) = ret
		return nil
	}

	/*
		 not pointer type, or nil pointer, or self-pointed interface{}, such as
			```go
			var v interface{}
			v = &v
			return v
			``` see `issue758_test.go`.
	*/
	rv := reflect.ValueOf(cur)
	vt := rv.Type()
	if vt.Kind() != reflect.Ptr || rv.IsNil() || rv.UnsafePointer() == vp {
		ret, err := node.AsEface(ctx)
		if err != nil {
			return err
		}
		*(*interface{})(vp) = ret
		return nil
	}

	if node.IsNull() {
		if vt.Elem().Kind() != reflect.Ptr {
			*(*interface{})(vp) = nil
			return nil
		}
	}

	etp := vt.Elem()
	dst := rv.UnsafePointer()

	/* check the defined pointer type for issue 379 */
	if vt.Name() != "" {
		// check named pointer type, avoid call its `Unmarshaler`
		ptrSlot := reflect.New(vt)
		*(*unsafe.Pointer)(ptrSlot.UnsafePointer()) = dst
		etp = vt
		dst = ptrSlot.UnsafePointer()
	}

	dec, err := findOrCompile(etp)
	if err != nil {
		return err
	}

	return dec.FromDom(dst, node, ctx)
}

type ifaceDecoder struct {
	typ     reflect.Type
	zeroVal reflect.Value
}

func (d *ifaceDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	iv := reflect.NewAt(d.typ, vp).Elem()
	if node.IsNull() {
		iv.Set(d.zeroVal)
		return nil
	}

	if iv.IsNil() {
		return error_type(d.typ)
	}

	ev := iv.Elem()
	vt := ev.Type()
	if vt.Kind() != reflect.Ptr || ev.IsNil() {
		return error_type(d.typ)
	}

	etp := vt.Elem()
	dst := ev.UnsafePointer()

	/* check the defined pointer type for issue 379 */
	if vt.Name() != "" {
		ptrSlot := reflect.New(vt)
		*(*unsafe.Pointer)(ptrSlot.UnsafePointer()) = dst
		etp = vt
		dst = ptrSlot.UnsafePointer()
	}

	dec, err := findOrCompile(etp)
	if err != nil {
		return err
	}

	return dec.FromDom(dst, node, ctx)
}

type unmarshalTextDecoder struct {
	typ      reflect.Type
	elemType reflect.Type
}

func (d *unmarshalTextDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if d.typ.Kind() == reflect.Interface {
		iv := reflect.NewAt(d.typ, vp).Elem()
		if node.IsNull() {
			iv.Set(reflect.Zero(d.typ))
			return nil
		}
		if iv.IsNil() {
			return error_type(d.typ)
		}
		ev := iv.Elem()
		if ev.Kind() != reflect.Ptr || ev.IsNil() {
			return error_type(d.typ)
		}
		txt, ok := node.AsStringText(ctx)
		if !ok {
			return error_mismatch(node, ctx, d.typ)
		}
		return iv.Interface().(encoding.TextUnmarshaler).UnmarshalText(txt)
	}

	if node.IsNull() {
		if d.elemType != nil {
			switch d.elemType.Kind() {
			case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface:
				reflect.NewAt(d.elemType, vp).Elem().Set(reflect.Zero(d.elemType))
			}
		}
		return nil
	}

	txt, ok := node.AsStringText(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	return reflect.NewAt(d.elemType, vp).Interface().(encoding.TextUnmarshaler).UnmarshalText(txt)
}

var (
	noCopyRawMessageType    reflect.Type
	noCopyRawMessagePtrType reflect.Type
	jsonRawMessageType      = reflect.TypeOf(json.RawMessage(nil))
)

// RegisterNoCopyRawMessageType registers sonic.NoCopyRawMessage exact type identity.
func RegisterNoCopyRawMessageType(vt reflect.Type) {
	noCopyRawMessageType = vt
	noCopyRawMessagePtrType = reflect.PtrTo(vt)
}

type rawMessageDecoder struct{}

func (d *rawMessageDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	raw := node.AsRaw(ctx)
	*(*[]byte)(vp) = append((*(*[]byte)(vp))[:0], raw...)
	return nil
}

type noCopyRawMessageDecoder struct {
	strOpt bool
}

func (d *noCopyRawMessageDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if d.strOpt {
		if node.IsNull() {
			*(*[]byte)(vp) = []byte("null")
			return nil
		}
		s, ok := node.AsStringText(ctx)
		if !ok {
			return error_mismatch(node, ctx, noCopyRawMessageType)
		}
		*(*[]byte)(vp) = s
		return nil
	}
	raw := node.AsRaw(ctx)
	if ctx.Parser.options&(1<<_F_copy_string) != 0 {
		*(*[]byte)(vp) = []byte(raw)
	} else {
		*(*[]byte)(vp) = rt.Str2Mem(raw)
	}
	return nil
}

type unmarshalJSONDecoder struct {
	typ      reflect.Type
	elemType reflect.Type
	strOpt   bool
}

func (d *unmarshalJSONDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if d.typ.Kind() == reflect.Interface {
		iv := reflect.NewAt(d.typ, vp).Elem()
		if node.IsNull() {
			iv.Set(reflect.Zero(d.typ))
			return nil
		}
		if iv.IsNil() {
			return error_type(d.typ)
		}
		ev := iv.Elem()
		if ev.Kind() != reflect.Ptr || ev.IsNil() {
			return error_type(d.typ)
		}
		if noCopyRawMessagePtrType != nil && ev.Type() == noCopyRawMessagePtrType {
			return (&noCopyRawMessageDecoder{strOpt: d.strOpt}).FromDom(ev.UnsafePointer(), node, ctx)
		}
		var input []byte
		if d.strOpt {
			s, ok := node.AsStringText(ctx)
			if !ok {
				return error_mismatch(node, ctx, d.typ)
			}
			input = s
		} else {
			input = []byte(node.AsRaw(ctx))
		}
		return iv.Interface().(json.Unmarshaler).UnmarshalJSON(input)
	}

	var input []byte
	if d.strOpt && node.IsNull() {
		input = []byte("null")
	} else if d.strOpt {
		s, ok := node.AsStringText(ctx)
		if !ok {
			return error_mismatch(node, ctx, d.typ)
		}
		input = s
	} else {
		input = []byte(node.AsRaw(ctx))
	}

	return reflect.NewAt(d.elemType, vp).Interface().(json.Unmarshaler).UnmarshalJSON(input)
}
