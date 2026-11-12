package optdec

import (
	"encoding"
	"math"
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

const is32BitInt = ^uint(0)>>63 == 0

/** Decoder for most common map types: map[string]interface{}, map[string]string **/

type mapEfaceDecoder struct {
}

func (d *mapEfaceDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*map[string]interface{})(vp) = nil
		return nil
	}

	return node.AsMapEface(ctx, vp)
}

type mapStringDecoder struct {
}

func (d *mapStringDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*map[string]string)(vp) = nil
		return nil
	}

	return node.AsMapString(ctx, vp)
}

type mapStrIntDecoder struct{}

func (d *mapStrIntDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[string]int)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapStrIntType)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[string]int, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsI64(ctx)
		if !ok || (is32BitInt && (ret > math.MaxInt32 || ret < math.MinInt32)) {
			m[key] = 0
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, intType)
			}
			next = val.Next()
		} else {
			m[key] = int(ret)
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapStrI64Decoder struct{}

func (d *mapStrI64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[string]int64)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapStrI64Type)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[string]int64, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsI64(ctx)
		if !ok {
			m[key] = 0
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, int64Type)
			}
			next = val.Next()
		} else {
			m[key] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapStrU64Decoder struct{}

func (d *mapStrU64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[string]uint64)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapStrU64Type)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[string]uint64, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsU64(ctx)
		if !ok {
			m[key] = 0
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, uint64Type)
			}
			next = val.Next()
		} else {
			m[key] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapStrF64Decoder struct{}

func (d *mapStrF64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[string]float64)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapStrF64Type)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[string]float64, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsF64(ctx)
		if !ok {
			m[key] = 0
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, float64Type)
			}
			next = val.Next()
		} else {
			m[key] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapStrBoolDecoder struct{}

func (d *mapStrBoolDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[string]bool)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapStrBoolType)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[string]bool, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsBool()
		if !ok {
			m[key] = false
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, boolType)
			}
			next = val.Next()
		} else {
			m[key] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapIntStrDecoder struct{}

func (d *mapIntStrDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[int]string)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapIntStrType)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[int]string, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		k, ok := knode.ParseI64(ctx)
		if !ok || (is32BitInt && (k > math.MaxInt32 || k < math.MinInt32)) {
			if gerr == nil {
				gerr = error_mismatch(knode, ctx, mapIntStrType)
			}
			val := NewNode(PtrOffset(next, 1))
			next = val.Next()
			continue
		}
		key := int(k)
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsStr(ctx)
		if !ok {
			m[key] = ""
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, stringType)
			}
			next = val.Next()
		} else {
			m[key] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapI64StrDecoder struct{}

func (d *mapI64StrDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	dst := (*map[int64]string)(vp)
	if node.IsNull() {
		*dst = nil
		return nil
	}
	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, mapI64StrType)
	}
	size := obj.Len()
	m := *dst
	if m == nil {
		m = make(map[int64]string, size)
	}
	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, ok := knode.ParseI64(ctx)
		if !ok {
			if gerr == nil {
				gerr = error_mismatch(knode, ctx, mapI64StrType)
			}
			val := NewNode(PtrOffset(next, 1))
			next = val.Next()
			continue
		}
		val := NewNode(PtrOffset(next, 1))
		ret, ok := val.AsStr(ctx)
		if !ok {
			m[key] = ""
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = error_mismatch(val, ctx, stringType)
			}
			next = val.Next()
		} else {
			m[key] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}
	*dst = m
	return gerr
}

type mapReflectSlots struct {
	kv reflect.Value
	ev reflect.Value
	ep unsafe.Pointer
}

func acquireMapSlots(pool *sync.Pool, keyType, elemType reflect.Type, freshElem bool) (kv, ev reflect.Value, ep unsafe.Pointer, pooled *mapReflectSlots) {
	if !freshElem {
		if v := pool.Get(); v != nil {
			s := v.(*mapReflectSlots)
			return s.kv, s.ev, s.ep, s
		}
		evVal := reflect.New(elemType).Elem()
		s := &mapReflectSlots{
			kv: reflect.New(keyType).Elem(),
			ev: evVal,
			ep: evVal.Addr().UnsafePointer(),
		}
		return s.kv, s.ev, s.ep, s
	}
	return reflect.New(keyType).Elem(), reflect.Value{}, nil, nil
}

func releaseMapSlots(pool *sync.Pool, pooled *mapReflectSlots, zeroKey, zeroElem reflect.Value) {
	if pooled != nil {
		pooled.kv.Set(zeroKey)
		pooled.ev.Set(zeroElem)
		pool.Put(pooled)
	}
}

func decodeMapEntry(mv, kv, ev, zeroElem reflect.Value, ep unsafe.Pointer, elemType reflect.Type, freshElem bool, elemDec decFunc, valn Node, ctx *context) error {
	if freshElem {
		ev = reflect.New(elemType).Elem()
		ep = ev.Addr().UnsafePointer()
	} else {
		ev.Set(zeroElem)
	}
	err := elemDec.FromDom(ep, valn, ctx)
	mv.SetMapIndex(kv, ev)
	return err
}

/** Decoder for map with string key **/

type mapStrKeyDecoder struct {
	mapType   reflect.Type
	keyType   reflect.Type
	elemType  reflect.Type
	zeroMap   reflect.Value
	zeroKey   reflect.Value
	zeroElem  reflect.Value
	elemDec   decFunc
	typ       reflect.Type
	freshElem bool
	slotPool  sync.Pool
}

func (d *mapStrKeyDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	rv := reflect.NewAt(d.mapType, vp).Elem()
	if node.IsNull() {
		rv.Set(d.zeroMap)
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, d.mapType)
	}

	mv := rv
	wasNil := mv.IsNil()
	if wasNil {
		mv = reflect.MakeMapWithSize(d.mapType, obj.Len())
	}

	if obj.Len() == 0 {
		if wasNil {
			rv.Set(mv)
		}
		return nil
	}

	kv, ev, ep, pooled := acquireMapSlots(&d.slotPool, d.keyType, d.elemType, d.freshElem)

	var gerr error
	next := obj.Children()
	for i := 0; i < obj.Len(); i++ {
		keyn := NewNode(next)
		key, _ := keyn.AsStr(ctx)
		kv.SetString(key)

		valn := NewNode(PtrOffset(next, 1))
		err := decodeMapEntry(mv, kv, ev, d.zeroElem, ep, d.elemType, d.freshElem, d.elemDec, valn, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
		next = valn.Next()
	}

	releaseMapSlots(&d.slotPool, pooled, d.zeroKey, d.zeroElem)
	if wasNil {
		rv.Set(mv)
	}
	return gerr
}

/** Decoder for map with int32 or int64 key **/

type mapI32KeyDecoder struct {
	mapType   reflect.Type
	keyType   reflect.Type
	elemType  reflect.Type
	zeroMap   reflect.Value
	zeroKey   reflect.Value
	zeroElem  reflect.Value
	elemDec   decFunc
	freshElem bool
	slotPool  sync.Pool
}

func (d *mapI32KeyDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	rv := reflect.NewAt(d.mapType, vp).Elem()
	if node.IsNull() {
		rv.Set(d.zeroMap)
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, d.mapType)
	}

	mv := rv
	wasNil := mv.IsNil()
	if wasNil {
		mv = reflect.MakeMapWithSize(d.mapType, obj.Len())
	}

	if obj.Len() == 0 {
		if wasNil {
			rv.Set(mv)
		}
		return nil
	}

	kv, ev, ep, pooled := acquireMapSlots(&d.slotPool, d.keyType, d.elemType, d.freshElem)

	next := obj.Children()
	var gerr error
	for i := 0; i < obj.Len(); i++ {
		keyn := NewNode(next)
		k, ok := keyn.ParseI64(ctx)
		if !ok || k > math.MaxInt32 || k < math.MinInt32 {
			if gerr == nil {
				gerr = error_mismatch(keyn, ctx, d.mapType)
			}
			valn := NewNode(PtrOffset(next, 1))
			next = valn.Next()
			continue
		}

		kv.SetInt(k)
		valn := NewNode(PtrOffset(next, 1))
		err := decodeMapEntry(mv, kv, ev, d.zeroElem, ep, d.elemType, d.freshElem, d.elemDec, valn, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}

		next = valn.Next()
	}

	releaseMapSlots(&d.slotPool, pooled, d.zeroKey, d.zeroElem)
	if wasNil {
		rv.Set(mv)
	}
	return gerr
}

type mapI64KeyDecoder struct {
	mapType   reflect.Type
	keyType   reflect.Type
	elemType  reflect.Type
	zeroMap   reflect.Value
	zeroKey   reflect.Value
	zeroElem  reflect.Value
	elemDec   decFunc
	freshElem bool
	slotPool  sync.Pool
}

func (d *mapI64KeyDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	rv := reflect.NewAt(d.mapType, vp).Elem()
	if node.IsNull() {
		rv.Set(d.zeroMap)
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, d.mapType)
	}

	mv := rv
	wasNil := mv.IsNil()
	if wasNil {
		mv = reflect.MakeMapWithSize(d.mapType, obj.Len())
	}

	if obj.Len() == 0 {
		if wasNil {
			rv.Set(mv)
		}
		return nil
	}

	kv, ev, ep, pooled := acquireMapSlots(&d.slotPool, d.keyType, d.elemType, d.freshElem)

	var gerr error
	next := obj.Children()
	for i := 0; i < obj.Len(); i++ {
		keyn := NewNode(next)
		key, ok := keyn.ParseI64(ctx)

		if !ok {
			if gerr == nil {
				gerr = error_mismatch(keyn, ctx, d.mapType)
			}
			valn := NewNode(PtrOffset(next, 1))
			next = valn.Next()
			continue
		}

		kv.SetInt(key)
		valn := NewNode(PtrOffset(next, 1))
		err := decodeMapEntry(mv, kv, ev, d.zeroElem, ep, d.elemType, d.freshElem, d.elemDec, valn, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
		next = valn.Next()
	}

	releaseMapSlots(&d.slotPool, pooled, d.zeroKey, d.zeroElem)
	if wasNil {
		rv.Set(mv)
	}
	return gerr
}

/** Decoder for map with unt32 or uint64 key **/

type mapU32KeyDecoder struct {
	mapType   reflect.Type
	keyType   reflect.Type
	elemType  reflect.Type
	zeroMap   reflect.Value
	zeroKey   reflect.Value
	zeroElem  reflect.Value
	elemDec   decFunc
	freshElem bool
	slotPool  sync.Pool
}

func (d *mapU32KeyDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	rv := reflect.NewAt(d.mapType, vp).Elem()
	if node.IsNull() {
		rv.Set(d.zeroMap)
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, d.mapType)
	}

	mv := rv
	wasNil := mv.IsNil()
	if wasNil {
		mv = reflect.MakeMapWithSize(d.mapType, obj.Len())
	}

	if obj.Len() == 0 {
		if wasNil {
			rv.Set(mv)
		}
		return nil
	}

	kv, ev, ep, pooled := acquireMapSlots(&d.slotPool, d.keyType, d.elemType, d.freshElem)

	var gerr error
	next := obj.Children()
	for i := 0; i < obj.Len(); i++ {
		keyn := NewNode(next)
		k, ok := keyn.ParseU64(ctx)
		if !ok || k > math.MaxUint32 {
			if gerr == nil {
				gerr = error_mismatch(keyn, ctx, d.mapType)
			}
			valn := NewNode(PtrOffset(next, 1))
			next = valn.Next()
			continue
		}

		kv.SetUint(k)
		valn := NewNode(PtrOffset(next, 1))
		err := decodeMapEntry(mv, kv, ev, d.zeroElem, ep, d.elemType, d.freshElem, d.elemDec, valn, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
		next = valn.Next()
	}

	releaseMapSlots(&d.slotPool, pooled, d.zeroKey, d.zeroElem)
	if wasNil {
		rv.Set(mv)
	}
	return gerr
}

type mapU64KeyDecoder struct {
	mapType   reflect.Type
	keyType   reflect.Type
	elemType  reflect.Type
	zeroMap   reflect.Value
	zeroKey   reflect.Value
	zeroElem  reflect.Value
	elemDec   decFunc
	freshElem bool
	slotPool  sync.Pool
}

func (d *mapU64KeyDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	rv := reflect.NewAt(d.mapType, vp).Elem()
	if node.IsNull() {
		rv.Set(d.zeroMap)
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return error_mismatch(node, ctx, d.mapType)
	}

	mv := rv
	wasNil := mv.IsNil()
	if wasNil {
		mv = reflect.MakeMapWithSize(d.mapType, obj.Len())
	}

	if obj.Len() == 0 {
		if wasNil {
			rv.Set(mv)
		}
		return nil
	}

	kv, ev, ep, pooled := acquireMapSlots(&d.slotPool, d.keyType, d.elemType, d.freshElem)

	var gerr error
	next := obj.Children()
	for i := 0; i < obj.Len(); i++ {
		keyn := NewNode(next)
		key, ok := keyn.ParseU64(ctx)
		if !ok {
			if gerr == nil {
				gerr = error_mismatch(keyn, ctx, d.mapType)
			}
			valn := NewNode(PtrOffset(next, 1))
			next = valn.Next()
			continue
		}

		kv.SetUint(key)
		valn := NewNode(PtrOffset(next, 1))
		err := decodeMapEntry(mv, kv, ev, d.zeroElem, ep, d.elemType, d.freshElem, d.elemDec, valn, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
		next = valn.Next()
	}

	releaseMapSlots(&d.slotPool, pooled, d.zeroKey, d.zeroElem)
	if wasNil {
		rv.Set(mv)
	}
	return gerr
}

/** Decoder for generic cases */

type decKey func(dec *mapDecoder, raw string, kv reflect.Value) error

func decodeKeyU8(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	ret, err := ParseU64(key)
	if err != nil {
		return err
	}
	if ret > math.MaxUint8 {
		return error_value(key, dec.keyType)
	}
	kv.SetUint(ret)
	return nil
}

func decodeKeyU16(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	ret, err := ParseU64(key)
	if err != nil {
		return err
	}
	if ret > math.MaxUint16 {
		return error_value(key, dec.keyType)
	}
	kv.SetUint(ret)
	return nil
}

func decodeKeyI8(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	ret, err := ParseI64(key)
	if err != nil {
		return err
	}
	if ret > math.MaxInt8 || ret < math.MinInt8 {
		return error_value(key, dec.keyType)
	}
	kv.SetInt(ret)
	return nil
}

func decodeKeyI16(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	ret, err := ParseI64(key)
	if err != nil {
		return err
	}
	if ret > math.MaxInt16 || ret < math.MinInt16 {
		return error_value(key, dec.keyType)
	}
	kv.SetInt(ret)
	return nil
}

func decodeKeyTextUnmarshaler(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	kt := dec.keyType

	// For pointer key types (e.g. map[*K]V), decode directly into *K.
	if kt.Kind() == reflect.Ptr {
		ret := reflect.New(kt.Elem())
		err = ret.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(key))
		if err != nil {
			return err
		}
		kv.Set(ret)
		return nil
	}

	// For value key types (e.g. map[K]V), allocate a fresh key per entry so callbacks retaining their receiver do not alias across entries.
	ret := reflect.New(kt)
	err = ret.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(key))
	if err != nil {
		return err
	}
	kv.Set(ret.Elem())
	return nil
}

func decodeFloat32Key(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	ret, err := ParseF64(key)
	if err != nil {
		return err
	}
	if ret > math.MaxFloat32 || ret < -math.MaxFloat32 {
		return error_value(key, dec.keyType)
	}
	kv.SetFloat(ret)
	return nil
}

func decodeFloat64Key(dec *mapDecoder, raw string, kv reflect.Value) error {
	key, err := Unquote(raw)
	if err != nil {
		return err
	}
	ret, err := ParseF64(key)
	if err != nil {
		return err
	}
	kv.SetFloat(ret)
	return nil
}

func decodeJsonNumberKey(dec *mapDecoder, raw string, kv reflect.Value) error {
	// skip the quote
	raw = raw[1 : len(raw)-1]
	end, ok := SkipNumberFast(raw, 0)

	// check trailing chars
	if !ok || end != len(raw) {
		return error_value(raw, jsonNumberType)
	}

	kv.SetString(string(rt.Str2Mem(raw[0:end])))
	return nil
}

type mapDecoder struct {
	mapType   reflect.Type
	keyType   reflect.Type
	elemType  reflect.Type
	zeroMap   reflect.Value
	zeroElem  reflect.Value
	keyDec    decKey
	elemDec   decFunc
	freshElem bool
}

func (d *mapDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	rv := reflect.NewAt(d.mapType, vp).Elem()
	if node.IsNull() {
		rv.Set(d.zeroMap)
		return nil
	}

	obj, ok := node.AsObj()
	if !ok || d.keyDec == nil {
		return error_mismatch(node, ctx, d.mapType)
	}

	mv := rv
	wasNil := mv.IsNil()
	if wasNil {
		mv = reflect.MakeMapWithSize(d.mapType, obj.Len())
	}

	if obj.Len() == 0 {
		if wasNil {
			rv.Set(mv)
		}
		return nil
	}

	kv := reflect.New(d.keyType).Elem()
	var ev reflect.Value
	var ep unsafe.Pointer
	if !d.freshElem {
		ev = reflect.New(d.elemType).Elem()
		ep = ev.Addr().UnsafePointer()
	}

	next := obj.Children()
	var gerr error
	for i := 0; i < obj.Len(); i++ {
		keyn := NewNode(next)
		raw := keyn.AsRaw(ctx)

		err := d.keyDec(d, raw, kv)
		if err != nil {
			if gerr == nil {
				gerr = error_mismatch(keyn, ctx, d.mapType)
			}
			valn := NewNode(PtrOffset(next, 1))
			next = valn.Next()
			continue
		}

		valn := NewNode(PtrOffset(next, 1))
		err = decodeMapEntry(mv, kv, ev, d.zeroElem, ep, d.elemType, d.freshElem, d.elemDec, valn, ctx)
		if gerr == nil && err != nil {
			gerr = err
		}

		next = valn.Next()
	}

	if wasNil {
		rv.Set(mv)
	}
	return gerr
}
