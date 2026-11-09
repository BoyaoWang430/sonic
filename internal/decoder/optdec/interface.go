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
	/* check the defined pointer type for issue 379 */
	eface := (*rt.GoEface)(vp)

	/*
		 not pointer type, or nil pointer, or self-pointed interface{}, such as
			```go
			var v interface{}
			v = &v
			return v
			``` see `issue758_test.go`.
	*/
	if eface.Value == nil || eface.Type.Kind() != reflect.Ptr || eface.Value == vp {
		ret, err := node.AsEface(ctx)
		if err != nil {
			return err
		}
		*(*interface{})(vp) = ret
		return nil
	}

	if node.IsNull() {
		if eface.Type.Indirect() || (!eface.Type.Indirect() && eface.Type.Pack().Elem().Kind() != reflect.Ptr) {
			*(*interface{})(vp) = nil
			return nil
		}
	}

	etp := rt.PtrElem(eface.Type)
	vp = eface.Value

	if eface.Type.IsNamed() {
		// check named pointer type, avoid call its `Unmarshaler`
		newp := vp
		etp = eface.Type
		vp = unsafe.Pointer(&newp)
	} else if !eface.Type.Indirect() {
		// check direct value
		etp = rt.UnpackType(eface.Type.Pack().Elem())
	}

	dec, err := findOrCompile(etp)
	if err != nil {
		return err
	}

	return dec.FromDom(vp, node, ctx)
}

type ifaceDecoder struct {
	typ *rt.GoType
}

func (d *ifaceDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*unsafe.Pointer)(vp) = nil
		return nil
	}

	iface := *(*rt.GoIface)(vp)
	if iface.Itab == nil {
		return error_type(d.typ)
	}

	vt := iface.Itab.Vt
	if vt.Kind() != reflect.Ptr || iface.Value == nil {
		return error_type(d.typ)
	}

	etp := rt.PtrElem(vt)
	vp = iface.Value

	/* check the defined pointer type for issue 379 */
	if vt.IsNamed() {
		newp := vp
		etp = vt
		vp = unsafe.Pointer(&newp)
	}

	dec, err := findOrCompile(etp)
	if err != nil {
		return err
	}

	return dec.FromDom(vp, node, ctx)
}

type unmarshalTextDecoder struct {
	typ  *rt.GoType
	itab *rt.GoItab
}

func (d *unmarshalTextDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*unsafe.Pointer)(vp) = nil
		return nil
	}

	txt, ok := node.AsStringText(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ.Pack())
	}

	if d.itab != nil {
		it := rt.GoIface{Itab: d.itab, Value: vp}
		return (*(*encoding.TextUnmarshaler)(unsafe.Pointer(&it))).UnmarshalText(txt)
	}

	v := *(*interface{})(unsafe.Pointer(&rt.GoEface{
		Type:  d.typ,
		Value: vp,
	}))

	// fast path
	if u, ok := v.(encoding.TextUnmarshaler); ok {
		return u.UnmarshalText(txt)
	}

	// slow path
	rv := reflect.ValueOf(v)
	if u, ok := rv.Interface().(encoding.TextUnmarshaler); ok {
		return u.UnmarshalText(txt)
	}

	return error_type(d.typ)
}

var (
	noCopyRawMessageType    *rt.GoType
	noCopyRawMessagePtrType *rt.GoType
	jsonRawMessageGoType    = reflect.TypeOf(json.RawMessage(nil))
)

// RegisterNoCopyRawMessageType registers sonic.NoCopyRawMessage exact type identity.
func RegisterNoCopyRawMessageType(vt reflect.Type) {
	noCopyRawMessageType = rt.UnpackType(vt)
	noCopyRawMessagePtrType = rt.UnpackType(reflect.PtrTo(vt))
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
			return error_mismatch(node, ctx, noCopyRawMessageType.Pack())
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
	typ    *rt.GoType
	itab   *rt.GoItab
	strOpt bool
}

func (d *unmarshalJSONDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if d.typ.Kind() == reflect.Interface {
		if node.IsNull() {
			*(*unsafe.Pointer)(vp) = nil
			return nil
		}
		iface := *(*rt.GoIface)(vp)
		if iface.Itab == nil || iface.Itab.Vt.Kind() != reflect.Ptr || iface.Value == nil {
			return error_type(d.typ)
		}
		if noCopyRawMessagePtrType != nil && iface.Itab.Vt == noCopyRawMessagePtrType {
			return (&noCopyRawMessageDecoder{strOpt: d.strOpt}).FromDom(iface.Value, node, ctx)
		}
		var input []byte
		if d.strOpt {
			s, ok := node.AsStringText(ctx)
			if !ok {
				return error_mismatch(node, ctx, d.typ.Pack())
			}
			input = s
		} else {
			input = []byte(node.AsRaw(ctx))
		}
		return (*(*json.Unmarshaler)(vp)).UnmarshalJSON(input)
	}

	var input []byte
	if d.strOpt && node.IsNull() {
		input = []byte("null")
	} else if d.strOpt {
		s, ok := node.AsStringText(ctx)
		if !ok {
			return error_mismatch(node, ctx, d.typ.Pack())
		}
		input = s
	} else {
		input = []byte(node.AsRaw(ctx))
	}

	if d.itab != nil {
		it := rt.GoIface{Itab: d.itab, Value: vp}
		return (*(*json.Unmarshaler)(unsafe.Pointer(&it))).UnmarshalJSON(input)
	}

	v := *(*interface{})(unsafe.Pointer(&rt.GoEface{
		Type:  d.typ,
		Value: vp,
	}))

	// fast path
	if u, ok := v.(json.Unmarshaler); ok {
		return u.UnmarshalJSON(input)
	}

	// slow path
	rv := reflect.ValueOf(v)
	if u, ok := rv.Interface().(json.Unmarshaler); ok {
		return u.UnmarshalJSON(input)
	}

	return error_type(d.typ)
}
