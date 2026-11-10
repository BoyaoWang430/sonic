package optdec

import (
	"fmt"
	"reflect"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/option"
)

var (
	programCache = caching.CreateProgramCache()
)

func findOrCompile(vt reflect.Type) (decFunc, error) {
	makeDecoder := func(vt reflect.Type, _ ...interface{}) (interface{}, error) {
		ret, err := newCompiler().compileType(vt)
		return ret, err
	}
	if val := programCache.Get(vt); val != nil {
		return val.(decFunc), nil
	} else if ret, err := programCache.Compute(vt, makeDecoder); err == nil {
		return ret.(decFunc), nil
	} else {
		return nil, err
	}
}

type compiler struct {
	visited  map[reflect.Type]bool
	compiled map[reflect.Type]decFunc
	recDecs  []*recuriveDecoder
	depth    int
	counts   int
	opts     option.CompileOptions
	namedPtr bool
}

func newCompiler() *compiler {
	return &compiler{
		visited:  make(map[reflect.Type]bool),
		compiled: make(map[reflect.Type]decFunc),
		opts:     option.DefaultCompileOptions(),
	}
}

func (self *compiler) apply(opts option.CompileOptions) *compiler {
	self.opts = opts
	return self
}

const _CompileMaxDepth = 4096

func (c *compiler) enter(vt reflect.Type) {
	c.visited[vt] = true
	c.depth += 1

	if c.depth > _CompileMaxDepth {
		panic(*stackOverflow)
	}
}

func (c *compiler) exit(vt reflect.Type) {
	c.visited[vt] = false
	c.depth -= 1
}

func (c *compiler) compileInt(vt reflect.Type) decFunc {
	switch vt.Size() {
	case 4:
		switch vt.Kind() {
		case reflect.Uint:
			fallthrough
		case reflect.Uintptr:
			return &u32Decoder{}
		case reflect.Int:
			return &i32Decoder{}
		}
	case 8:
		switch vt.Kind() {
		case reflect.Uint:
			fallthrough
		case reflect.Uintptr:
			return &u64Decoder{}
		case reflect.Int:
			return &i64Decoder{}
		}
	default:
		panic("not supported pointer size: " + fmt.Sprint(vt.Size()))
	}
	panic("unreachable")
}

func (c *compiler) rescue(ep *error) {
	if val := recover(); val != nil {
		if err, ok := val.(error); ok {
			*ep = err
		} else {
			panic(val)
		}
	}
}

func (c *compiler) newRecursiveDecoder(vt reflect.Type) *recuriveDecoder {
	r := &recuriveDecoder{
		typ: vt,
	}
	c.recDecs = append(c.recDecs, r)
	return r
}

func (c *compiler) compileType(vt reflect.Type) (ret decFunc, err error) {
	defer c.rescue(&err)
	ret = c.compile(vt)
	for _, r := range c.recDecs {
		if target, ok := c.compiled[r.typ]; ok {
			r.bind(target)
		}
	}
	return ret, err
}

func (c *compiler) compile(vt reflect.Type) decFunc {
	if c.visited[vt] {
		return c.newRecursiveDecoder(vt)
	}

	dec := c.tryCompilePtrUnmarshaler(vt, false)
	if dec != nil {
		return dec
	}

	return c.compileBasic(vt)
}

func (c *compiler) compileBasic(vt reflect.Type) decFunc {
	defer func() {
		c.counts += 1
	}()
	switch vt.Kind() {
	case reflect.Bool:
		return &boolDecoder{}
	case reflect.Int8:
		return &i8Decoder{}
	case reflect.Int16:
		return &i16Decoder{}
	case reflect.Int32:
		return &i32Decoder{}
	case reflect.Int64:
		return &i64Decoder{}
	case reflect.Uint8:
		return &u8Decoder{}
	case reflect.Uint16:
		return &u16Decoder{}
	case reflect.Uint32:
		return &u32Decoder{}
	case reflect.Uint64:
		return &u64Decoder{}
	case reflect.Float32:
		return &f32Decoder{}
	case reflect.Float64:
		return &f64Decoder{}
	case reflect.Uint:
		fallthrough
	case reflect.Uintptr:
		fallthrough
	case reflect.Int:
		return c.compileInt(vt)
	case reflect.String:
		return c.compileString(vt)
	case reflect.Array:
		return c.compileArray(vt)
	case reflect.Interface:
		return c.compileInterface(vt)
	case reflect.Map:
		return c.compileMap(vt)
	case reflect.Ptr:
		return c.compilePtr(vt)
	case reflect.Slice:
		return c.compileSlice(vt)
	case reflect.Struct:
		return c.compileStruct(vt)
	default:
		return &unsupportedTypeDecoder{
			typ: vt,
		}
	}
}

func (c *compiler) compilePtr(vt reflect.Type) decFunc {
	c.enter(vt)
	defer c.exit(vt)

	// special logic for Named Ptr, issue 379
	if reflect.PtrTo(vt.Elem()) != vt {
		c.namedPtr = true
		return &ptrDecoder{
			typ:   vt.Elem(),
			deref: c.compileBasic(vt.Elem()),
		}
	}

	return &ptrDecoder{
		typ:   vt.Elem(),
		deref: c.compile(vt.Elem()),
	}
}

func (c *compiler) compileArray(vt reflect.Type) decFunc {
	c.enter(vt)
	defer c.exit(vt)
	return &arrayDecoder{
		len:      vt.Len(),
		elemType: vt.Elem(),
		elemDec:  c.compile(vt.Elem()),
		typ:      vt,
		size:     vt.Elem().Size(),
		zeroElem: reflect.Zero(vt.Elem()),
	}
}

func (c *compiler) compileString(vt reflect.Type) decFunc {
	if vt == jsonNumberType {
		return &numberDecoder{}
	}
	return &stringDecoder{}

}

func (c *compiler) tryCompileSliceUnmarshaler(vt reflect.Type) decFunc {
	pt := reflect.PtrTo(vt.Elem())
	if pt.Implements(jsonUnmarshalerType) {
		return &sliceDecoder{
			elemType:   vt.Elem(),
			elemDec:    c.compile(vt.Elem()),
			size:       vt.Elem().Size(),
			typ:        vt,
			zeroSlice:  reflect.Zero(vt),
			emptySlice: reflect.MakeSlice(vt, 0, 0),
		}
	}

	if pt.Implements(encodingTextUnmarshalerType) {
		return &sliceDecoder{
			elemType:   vt.Elem(),
			elemDec:    c.compile(vt.Elem()),
			size:       vt.Elem().Size(),
			typ:        vt,
			zeroSlice:  reflect.Zero(vt),
			emptySlice: reflect.MakeSlice(vt, 0, 0),
		}
	}
	return nil
}

func (c *compiler) compileSlice(vt reflect.Type) decFunc {
	c.enter(vt)
	defer c.exit(vt)

	// Some common slice, use a decoder, to avoid function calls
	et := vt.Elem()

	/* first checking `[]byte` */
	if et.Kind() == reflect.Uint8 /* []byte */ {
		return c.compileSliceBytes(vt)
	}

	dec := c.tryCompileSliceUnmarshaler(vt)
	if dec != nil {
		return dec
	}

	if vt == sliceEfaceType {
		return &sliceEfaceDecoder{}
	}
	switch et.Kind() {
	case reflect.Int32:
		return &sliceI32Decoder{}
	case reflect.Int64:
		return &sliceI64Decoder{}
	case reflect.Int:
		if et.Size() == 8 {
			return &sliceI64Decoder{}
		}
		return &sliceI32Decoder{}
	case reflect.Uint32:
		return &sliceU32Decoder{}
	case reflect.Uint64:
		return &sliceU64Decoder{}
	case reflect.Uint, reflect.Uintptr:
		if et.Size() == 8 {
			return &sliceU64Decoder{}
		}
		return &sliceU32Decoder{}
	}
	if et.Kind() == reflect.String && et != jsonNumberType {
		return &sliceStringDecoder{}
	}

	ed := c.compile(et)
	sd, _ := ed.(*structDecoder)
	return &sliceDecoder{
		elemType:   et,
		elemDec:    ed,
		structDec:  sd,
		size:       et.Size(),
		typ:        vt,
		zeroSlice:  reflect.Zero(vt),
		emptySlice: reflect.MakeSlice(vt, 0, 0),
	}
}

func (c *compiler) compileSliceBytes(vt reflect.Type) decFunc {
	ep := reflect.PtrTo(vt.Elem())

	if ep.Implements(jsonUnmarshalerType) {
		return &sliceBytesUnmarshalerDecoder{
			elemType: vt.Elem(),
			elemDec:  c.compile(vt.Elem()),
			typ:      vt,
		}
	}

	if ep.Implements(encodingTextUnmarshalerType) {
		return &sliceBytesUnmarshalerDecoder{
			elemType: vt.Elem(),
			elemDec:  c.compile(vt.Elem()),
			typ:      vt,
		}
	}

	return &sliceBytesDecoder{}
}

func (c *compiler) compileInterface(vt reflect.Type) decFunc {
	c.enter(vt)
	defer c.exit(vt)
	if vt.NumMethod() == 0 {
		return &efaceDecoder{}
	}

	if vt.Implements(jsonUnmarshalerType) {
		return &unmarshalJSONDecoder{
			typ: vt,
		}
	}

	if vt.Implements(encodingTextUnmarshalerType) {
		return &unmarshalTextDecoder{
			typ: vt,
		}
	}

	return &ifaceDecoder{
		typ:     vt,
		zeroVal: reflect.Zero(vt),
	}
}

func typeNeedsFreshElemSlot(vt reflect.Type) bool {
	return typeNeedsFreshElemSlotSeen(vt, nil)
}

func typeNeedsFreshElemSlotSeen(vt reflect.Type, seen map[reflect.Type]bool) bool {
	if vt == nil || vt.Kind() == reflect.Interface {
		return false
	}
	if vt.Implements(jsonUnmarshalerType) || vt.Implements(encodingTextUnmarshalerType) {
		return true
	}
	if vt.Kind() != reflect.Ptr && (reflect.PtrTo(vt).Implements(jsonUnmarshalerType) || reflect.PtrTo(vt).Implements(encodingTextUnmarshalerType)) {
		return true
	}
	switch vt.Kind() {
	case reflect.Struct:
		if seen == nil {
			seen = make(map[reflect.Type]bool)
		}
		if seen[vt] {
			return false
		}
		seen[vt] = true
		for i := 0; i < vt.NumField(); i++ {
			sf := vt.Field(i)
			if !sf.IsExported() && !sf.Anonymous {
				continue
			}
			if sf.Tag.Get("json") == "-" {
				continue
			}
			if typeNeedsFreshElemSlotSeen(sf.Type, seen) {
				return true
			}
		}
	case reflect.Array:
		return typeNeedsFreshElemSlotSeen(vt.Elem(), seen)
	}
	return false
}

func (c *compiler) compileMap(vt reflect.Type) decFunc {
	c.enter(vt)
	defer c.exit(vt)
	kt := vt.Key()
	et := vt.Elem()
	freshElem := typeNeedsFreshElemSlot(et)

	// check the key unmarshaler at first
	decKey := tryCompileKeyUnmarshaler(vt)
	if decKey != nil {
		return &mapDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			keyDec:    decKey,
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	}

	// Most common map, use a decoder, to avoid function calls
	if vt == mapEfaceType {
		return &mapEfaceDecoder{}
	} else if vt == mapStringType {
		return &mapStringDecoder{}
	}

	if kt.Kind() == reflect.String && kt != jsonNumberType {
		return &mapStrKeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			typ:       vt,
			freshElem: freshElem,
		}
	}

	switch kt.Kind() {
	case reflect.Int64:
		return &mapI64KeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	case reflect.Int32:
		return &mapI32KeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	case reflect.Int:
		if kt.Size() == 8 {
			return &mapI64KeyDecoder{
				mapType:   vt,
				keyType:   kt,
				elemType:  et,
				zeroMap:   reflect.Zero(vt),
				zeroElem:  reflect.Zero(et),
				elemDec:   c.compile(et),
				freshElem: freshElem,
			}
		}
		return &mapI32KeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	case reflect.Uint64:
		return &mapU64KeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	case reflect.Uint32:
		return &mapU32KeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	case reflect.Uint, reflect.Uintptr:
		if kt.Size() == 8 {
			return &mapU64KeyDecoder{
				mapType:   vt,
				keyType:   kt,
				elemType:  et,
				zeroMap:   reflect.Zero(vt),
				zeroElem:  reflect.Zero(et),
				elemDec:   c.compile(et),
				freshElem: freshElem,
			}
		}
		return &mapU32KeyDecoder{
			mapType:   vt,
			keyType:   kt,
			elemType:  et,
			zeroMap:   reflect.Zero(vt),
			zeroElem:  reflect.Zero(et),
			elemDec:   c.compile(et),
			freshElem: freshElem,
		}
	}

	// Generic map
	return &mapDecoder{
		mapType:   vt,
		keyType:   kt,
		elemType:  et,
		zeroMap:   reflect.Zero(vt),
		zeroElem:  reflect.Zero(et),
		keyDec:    c.compileMapKey(vt),
		elemDec:   c.compile(et),
		freshElem: freshElem,
	}
}

func tryCompileKeyUnmarshaler(vt reflect.Type) decKey {
	kt := vt.Key()

	/* map key type itself implements encoding.TextUnmarshaler */
	if kt.Kind() != reflect.Interface && kt.Implements(encodingTextUnmarshalerType) {
		return decodeKeyTextUnmarshaler
	}

	/* check for `encoding.TextUnmarshaler` with pointer receiver */
	if kt.Kind() != reflect.Interface && kt.Kind() != reflect.Ptr && reflect.PtrTo(kt).Implements(encodingTextUnmarshalerType) {
		return decodeKeyTextUnmarshaler
	}

	/* NOTE: encoding/json not support map key with `json.Unmarshaler` */
	return nil
}

func (c *compiler) compileMapKey(vt reflect.Type) decKey {
	switch vt.Key().Kind() {
	case reflect.Int8:
		return decodeKeyI8
	case reflect.Int16:
		return decodeKeyI16
	case reflect.Uint8:
		return decodeKeyU8
	case reflect.Uint16:
		return decodeKeyU16
	// NOTE: actually, encoding/json can't use float as map key
	case reflect.Float32:
		return decodeFloat32Key
	case reflect.Float64:
		return decodeFloat64Key
	case reflect.String:
		if vt.Key() == jsonNumberType {
			return decodeJsonNumberKey
		}
		fallthrough
	default:
		return nil
	}
}

// maybe vt is a named type, and not a pointer receiver, see issue 379
func (c *compiler) tryCompilePtrUnmarshaler(vt reflect.Type, strOpt bool) decFunc {
	if noCopyRawMessageType != nil && vt == noCopyRawMessageType {
		return &noCopyRawMessageDecoder{strOpt: strOpt}
	}
	if vt == jsonRawMessageType && !strOpt {
		return &rawMessageDecoder{}
	}
	pt := reflect.PtrTo(vt)

	/* check for `json.Unmarshaler` with pointer receiver */
	if pt.Implements(jsonUnmarshalerType) {
		return &unmarshalJSONDecoder{
			typ:      pt,
			elemType: vt,
			strOpt:   strOpt,
		}
	}

	/* check for `encoding.TextMarshaler` with pointer receiver */
	if pt.Implements(encodingTextUnmarshalerType) {
		/* TextUnmarshal not support, string tag */
		if strOpt {
			panicForInvalidStrType(vt)
		}
		return &unmarshalTextDecoder{
			typ:      pt,
			elemType: vt,
		}
	}

	return nil
}

func panicForInvalidStrType(vt reflect.Type) {
	panic(error_type(vt))
}
