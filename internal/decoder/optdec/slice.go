package optdec

import (
	"reflect"
	"unsafe"
)

type sliceDecoder struct {
	elemType     reflect.Type
	elemDec      decFunc
	structDec    *structDecoder
	ptrStructDec *structDecoder
	size         uintptr
	typ          reflect.Type
	zeroSlice    reflect.Value
	emptySlice   reflect.Value
}

func (d *sliceDecoder) setNil(vp unsafe.Pointer) {
	reflect.NewAt(d.typ, vp).Elem().Set(d.zeroSlice)
}

func (d *sliceDecoder) setEmpty(vp unsafe.Pointer) {
	reflect.NewAt(d.typ, vp).Elem().Set(d.emptySlice)
}

func (d *sliceDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		d.setNil(vp)
		return nil
	}

	arr, ok := node.AsArr()
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	size := arr.Len()
	if size == 0 {
		d.setEmpty(vp)
		return nil
	}

	rv := reflect.NewAt(d.typ, vp).Elem()
	if rv.Cap() < size {
		rv.Grow(size - rv.Len())
	}
	rv.SetLen(size)
	elems := rv.UnsafePointer()

	elemSize := d.size
	next := arr.Children()

	var gerr error
	if sd := d.structDec; sd != nil {
		p := ctx.Parser
		opts := p.options
		if sd.fast && opts&fastStructExcludeMask == 0 {
			jsonStr := p.Json
			copyStr := opts&(1<<_F_copy_string) != 0
			for i := 0; i < size; i++ {
				elem := unsafe.Add(elems, uintptr(i)*elemSize)
				vptr := next
				vtyp := uint8(vptr.typ)
				if vtyp == KObject {
					next = PtrOffset(vptr, int64(vptr.val>>ConLenBits))
					if uint32(vptr.val) != 0 {
						err := sd.fromObjFast(elem, vptr, p, jsonStr, copyStr, ctx)
						if gerr == nil && err != nil {
							gerr = err
						}
					}
					continue
				}
				val := NewNode(vptr)
				err := sd.FromDom(elem, val, ctx)
				if gerr == nil && err != nil {
					gerr = err
				}
				next = val.Next()
			}
			return gerr
		}
		for i := 0; i < size; i++ {
			val := NewNode(next)
			elem := unsafe.Add(elems, uintptr(i)*elemSize)
			err := sd.FromDom(elem, val, ctx)
			if gerr == nil && err != nil {
				gerr = err
			}
			next = val.Next()
		}
	} else if psd := d.ptrStructDec; psd != nil {
		p := ctx.Parser
		opts := p.options
		ptrs := unsafe.Slice((*unsafe.Pointer)(elems), size)
		if psd.fast && opts&fastStructExcludeMask == 0 {
			jsonStr := p.Json
			copyStr := opts&(1<<_F_copy_string) != 0
			for i := 0; i < size; i++ {
				vptr := next
				vtyp := uint8(vptr.typ)
				if vtyp == KObject {
					next = PtrOffset(vptr, int64(vptr.val>>ConLenBits))
					dst := ptrs[i]
					if dst == nil {
						dst = psd.allocPtr(p)
						ptrs[i] = dst
					}
					if uint32(vptr.val) != 0 {
						err := psd.fromObjFast(dst, vptr, p, jsonStr, copyStr, ctx)
						if gerr == nil && err != nil {
							gerr = err
						}
					}
					continue
				}
				if vtyp == KNull {
					ptrs[i] = nil
					next = PtrOffset(vptr, 1)
					continue
				}
				val := NewNode(vptr)
				err := d.elemDec.FromDom(unsafe.Pointer(&ptrs[i]), val, ctx)
				if gerr == nil && err != nil {
					gerr = err
				}
				next = val.Next()
			}
			return gerr
		}
		for i := 0; i < size; i++ {
			val := NewNode(next)
			err := d.elemDec.FromDom(unsafe.Pointer(&ptrs[i]), val, ctx)
			if gerr == nil && err != nil {
				gerr = err
			}
			next = val.Next()
		}
	} else {
		for i := 0; i < size; i++ {
			val := NewNode(next)
			elem := unsafe.Add(elems, uintptr(i)*elemSize)
			err := d.elemDec.FromDom(elem, val, ctx)
			if gerr == nil && err != nil {
				gerr = err
			}
			next = val.Next()
		}
	}
	return gerr
}

type arrayDecoder struct {
	len      int
	elemType reflect.Type
	elemDec  decFunc
	typ      reflect.Type
	size     uintptr
	zeroElem reflect.Value
}

func (d *arrayDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	arr, ok := node.AsArr()
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	next := arr.Children()
	i := 0
	elemSize := d.size

	var gerr error
	for ; i < d.len && i < arr.Len(); i++ {
		elem := unsafe.Add(vp, uintptr(i)*elemSize)
		val := NewNode(next)
		err := d.elemDec.FromDom(elem, val, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
		next = val.Next()
	}

	/* zero rest of array */
	for ; i < d.len; i++ {
		elem := unsafe.Add(vp, uintptr(i)*elemSize)
		reflect.NewAt(d.elemType, elem).Elem().Set(d.zeroElem)
	}
	return gerr
}

var (
	zeroSliceEface  = reflect.Zero(sliceEfaceType)
	emptySliceEface = reflect.MakeSlice(sliceEfaceType, 0, 0)
)

type sliceEfaceDecoder struct {
}

func (d *sliceEfaceDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]interface{})(vp) = nil
		return nil
	}

	/* if slice is empty, just call `AsSliceEface` */
	if len(*(*[]interface{})(vp)) == 0 {
		return node.AsSliceEface(ctx, vp)
	}

	decoder := sliceDecoder{
		elemType:   anyType,
		elemDec:    &efaceDecoder{},
		size:       anyType.Size(),
		typ:        sliceEfaceType,
		zeroSlice:  zeroSliceEface,
		emptySlice: emptySliceEface,
	}

	return decoder.FromDom(vp, node, ctx)
}

type sliceIntDecoder struct {
}

func (d *sliceIntDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]int)(vp) = nil
		return nil
	}

	return node.AsSliceInt(ctx, vp)
}

type sliceI32Decoder struct {
}

func (d *sliceI32Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]int32)(vp) = nil
		return nil
	}

	return node.AsSliceI32(ctx, vp)
}

type sliceI64Decoder struct {
}

func (d *sliceI64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]int64)(vp) = nil
		return nil
	}

	return node.AsSliceI64(ctx, vp)
}

type sliceUintDecoder struct {
}

func (d *sliceUintDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]uint)(vp) = nil
		return nil
	}

	return node.AsSliceUint(ctx, vp)
}

type sliceU32Decoder struct {
}

func (d *sliceU32Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]uint32)(vp) = nil
		return nil
	}

	return node.AsSliceU32(ctx, vp)
}

type sliceU64Decoder struct {
}

func (d *sliceU64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]uint64)(vp) = nil
		return nil
	}

	return node.AsSliceU64(ctx, vp)
}

type sliceStringDecoder struct {
}

func (d *sliceStringDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]string)(vp) = nil
		return nil
	}

	return node.AsSliceString(ctx, vp)
}

type sliceF64Decoder struct {
}

func (d *sliceF64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]float64)(vp) = nil
		return nil
	}

	return node.AsSliceF64(ctx, vp)
}

type sliceF32Decoder struct {
}

func (d *sliceF32Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]float32)(vp) = nil
		return nil
	}

	return node.AsSliceF32(ctx, vp)
}

type sliceBoolDecoder struct {
}

func (d *sliceBoolDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]bool)(vp) = nil
		return nil
	}

	return node.AsSliceBool(ctx, vp)
}

type sliceBytesDecoder struct {
	typ      reflect.Type
	elemType reflect.Type
}

func (d *sliceBytesDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]byte)(vp) = nil
		return nil
	}

	return node.AsSliceBytesInto(ctx, vp, d.typ, d.elemType)
}

type sliceBytesUnmarshalerDecoder struct {
	elemType reflect.Type
	elemDec  decFunc
	typ      reflect.Type
}

func (d *sliceBytesUnmarshalerDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*[]byte)(vp) = nil
		return nil
	}

	/* parse JSON string into `[]byte` */
	if node.IsStr() {
		slice, err := node.AsSliceBytes(ctx)
		if err != nil {
			return err
		}
		*(*[]byte)(vp) = slice
		return nil
	}

	/* parse JSON array into `[]byte` */
	arr, ok := node.AsArr()
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	s := prepareSlice((*[]byte)(vp), arr.Len())
	var elems unsafe.Pointer
	if len(s) > 0 {
		elems = unsafe.Pointer(unsafe.SliceData(s))
	}

	var gerr error
	next := arr.Children()
	for i := 0; i < arr.Len(); i++ {
		child := NewNode(next)
		elem := unsafe.Add(elems, i)
		err := d.elemDec.FromDom(elem, child, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
		next = child.Next()
	}

	*(*[]byte)(vp) = s
	return gerr
}
