package optdec

import (
	"encoding/json"
	"math"
	"unsafe"

	"github.com/bytedance/sonic/internal/envs"
	"github.com/bytedance/sonic/internal/rt"
)

type Context struct {
	Parser    *Parser
	efacePool *efacePool
	Stack     boundedStack
	Utf8Inv   bool
}

func (ctx *Context) Options() uint64 {
	return ctx.Parser.options
}

/************************* Stack and Pool Helper *******************/

type parentStat struct {
	con    unsafe.Pointer
	remain uint64
}
type boundedStack struct {
	stack []parentStat
	index int
}

func newStack(size int) boundedStack {
	return boundedStack{
		stack: make([]parentStat, size+2),
		index: 0,
	}
}

//go:nosplit
func (s *boundedStack) Pop() (unsafe.Pointer, int, bool) {
	s.index--
	con := s.stack[s.index].con
	remain := s.stack[s.index].remain &^ (uint64(1) << 63)
	isObj := (s.stack[s.index].remain & (uint64(1) << 63)) != 0
	s.stack[s.index].con = nil
	s.stack[s.index].remain = 0
	return con, int(remain), isObj
}

//go:nosplit
func (s *boundedStack) Push(p unsafe.Pointer, remain int, isObj bool) {
	s.stack[s.index].con = p
	s.stack[s.index].remain = uint64(remain)
	if isObj {
		s.stack[s.index].remain |= (uint64(1) << 63)
	}
	s.index++
}

type efacePool struct {
	t64        rt.T64Pool
	tslice     rt.TslicePool
	tstring    rt.TstringPool
	efaceSlice rt.SlicePool
}

func newEfacePool(stat *jsonStat, useNumber bool) *efacePool {
	strs := int(stat.str)
	nums := 0
	if useNumber {
		strs += int(stat.number)
	} else {
		nums = int(stat.number)
	}

	return &efacePool{
		t64:        rt.NewT64Pool(nums),
		tslice:     rt.NewTslicePool(int(stat.array)),
		tstring:    rt.NewTstringPool(strs),
		efaceSlice: rt.NewPool(rt.AnyType, int(stat.array_elems)),
	}
}

func (self *efacePool) GetMap(hint int) unsafe.Pointer {
	m := make(map[string]interface{}, hint)
	return *(*unsafe.Pointer)(unsafe.Pointer(&m))
}

func (self *efacePool) GetSlice(hint int) unsafe.Pointer {
	return unsafe.Pointer(self.efaceSlice.GetSlice(hint))
}

func (self *efacePool) ConvTSlice(val rt.GoSlice, typ *rt.GoType, dst unsafe.Pointer) {
	self.tslice.Conv(val, typ, (*interface{})(dst))
}

func (self *efacePool) ConvF64(val float64, dst unsafe.Pointer) {
	self.t64.Conv(castU64(val), rt.Float64Type, (*interface{})(dst))
}

func (self *efacePool) ConvTstring(val string, dst unsafe.Pointer) {
	self.tstring.Conv(val, (*interface{})(dst))
}

func (self *efacePool) ConvTnum(val json.Number, dst unsafe.Pointer) {
	self.tstring.ConvNum(val, (*interface{})(dst))
}

/********************************************************/

func canUseFastMap(opts uint64, root *rt.GoType) bool {
	return envs.UseFastMap && (opts&(1<<_F_use_int64)) == 0 && (root == rt.AnyType || root == rt.MapEfaceType || root == rt.SliceEfaceType)
}

func NewContext(json string, pos int, opts uint64, root *rt.GoType) (Context, error) {
	ctx := Context{
		Parser: newParser(json, pos, opts),
	}
	if root == rt.AnyType || root == rt.MapEfaceType || root == rt.SliceEfaceType {
		ctx.Parser.isEface = true
	}

	ecode := ctx.Parser.parse()

	if ecode != 0 {
		return ctx, ctx.Parser.fixError(ecode)
	}

	useNumber := (opts & (1 << _F_use_number)) != 0
	if canUseFastMap(opts, root) {
		ctx.efacePool = newEfacePool(&ctx.Parser.nbuf.stat, useNumber)
		ctx.Stack = newStack(int(ctx.Parser.nbuf.stat.max_depth))
	}

	return ctx, nil
}

func (ctx *Context) Delete() {
	ctx.Parser.free()
	ctx.Parser = nil
}

type Node struct {
	cptr uintptr
}

func NewNode(cptr uintptr) Node {
	return Node{cptr: cptr}
}

type Dom struct {
	cdom uintptr
}

func (ctx *Context) Root() Node {
	root := (uintptr)(((*rt.GoSlice)(unsafe.Pointer(&ctx.Parser.nodes))).Ptr)
	return Node{cptr: root}
}

type Array struct {
	cptr uintptr
}

type Object struct {
	cptr uintptr
}

func (obj Object) Len() int {
	cobj := ptrCast(obj.cptr)
	return int(uint64(cobj.val) & ConLenMask)
}

func (arr Array) Len() int {
	carr := ptrCast(arr.cptr)
	return int(uint64(carr.val) & ConLenMask)
}

// / Helper functions to eliminate CGO calls
func (val Node) Type() uint8 {
	ctype := ptrCast(val.cptr)
	return uint8(ctype.typ & TypeMask)
}

func (val Node) Next() uintptr {
	cobj := ptrCast(val.cptr)
	if uint8(cobj.typ)-KObject > 1 {
		return val.cptr + unsafe.Sizeof(node{})
	}
	return val.cptr + uintptr(cobj.val>>ConLenBits)*unsafe.Sizeof(node{})
}

func (val *Node) next() {
	*val = NewNode(val.Next())
}

type NodeIter struct {
	next uintptr
}

func NewNodeIter(node Node) NodeIter {
	return NodeIter{next: node.cptr}
}

func (iter *NodeIter) Next() Node {
	ret := NewNode(iter.next)
	iter.next = PtrOffset(iter.next, 1)
	return ret
}

func (iter *NodeIter) Peek() Node {
	return NewNode(iter.next)
}

func (val Node) U64() uint64 {
	cnum := ptrCast(val.cptr)
	return *(*uint64)((unsafe.Pointer)(&(cnum.val)))
}

func (val Node) I64() int64 {
	cnum := ptrCast(val.cptr)
	return *(*int64)((unsafe.Pointer)(&(cnum.val)))
}

func (val Node) IsNull() bool {
	return val.Type() == KNull
}

func (val Node) IsNumber() bool {
	return val.Type()&KNumber != 0
}

func (val Node) F64() float64 {
	cnum := ptrCast(val.cptr)
	return *(*float64)((unsafe.Pointer)(&(cnum.val)))
}

func (val Node) Bool() bool {
	return val.Type() == KTrue
}

func (self Node) AsU64(ctx *Context) (uint64, bool) {
	if self.Type() == KUint {
		return self.U64(), true
	} else if self.Type() == KRawNumber {
		num, err := ParseU64(self.Raw(ctx))
		if err != nil {
			return 0, false
		}
		return num, true
	} else {
		return 0, false
	}
}

func (val *Node) AsObj() (Object, bool) {
	var ret Object
	if val.Type() != KObject {
		return ret, false
	}
	return Object{
		cptr: val.cptr,
	}, true
}

func (val Node) Obj() Object {
	return Object{cptr: val.cptr}
}

func (val Node) Arr() Array {
	return Array{cptr: val.cptr}
}

func (val *Node) AsArr() (Array, bool) {
	var ret Array
	if val.Type() != KArray {
		return ret, false
	}
	return Array{
		cptr: val.cptr,
	}, true
}

func (self Node) AsI64(ctx *Context) (int64, bool) {
	typ := self.Type()
	if typ == KUint && self.U64() <= math.MaxInt64 {
		return int64(self.U64()), true
	} else if typ == KSint {
		return self.I64(), true
	} else if typ == KRawNumber {
		val, err := self.Number(ctx).Int64()
		if err != nil {
			return 0, false
		}
		return val, true
	} else {
		return 0, false
	}
}

func (self Node) AsByte(ctx *Context) (uint8, bool) {
	typ := self.Type()
	if typ == KUint && self.U64() <= math.MaxUint8 {
		return uint8(self.U64()), true
	} else if typ == KSint && self.I64() == 0 {
		return 0, true
	} else {
		return 0, false
	}
}

/********* Parse Node String into Value ***************/

func (val Node) ParseI64(ctx *Context) (int64, bool) {
	s, ok := val.AsStrRef(ctx)
	if !ok {
		return 0, false
	}

	if s == "null" {
		return 0, true
	}

	i, err := ParseI64(s)
	if err != nil {
		return 0, false
	}
	return i, true
}

func (val Node) ParseBool(ctx *Context) (bool, bool) {
	s, ok := val.AsStrRef(ctx)
	if !ok {
		return false, false
	}

	if s == "null" {
		return false, true
	}

	b, err := ParseBool(s)
	if err != nil {
		return false, false
	}
	return b, true
}

func (val Node) ParseU64(ctx *Context) (uint64, bool) {
	s, ok := val.AsStrRef(ctx)
	if !ok {
		return 0, false
	}

	if s == "null" {
		return 0, true
	}

	i, err := ParseU64(s)
	if err != nil {
		return 0, false
	}
	return i, true
}

func (val Node) ParseF64(ctx *Context) (float64, bool) {
	s, ok := val.AsStrRef(ctx)
	if !ok {
		return 0, false
	}

	if s == "null" {
		return 0, true
	}

	i, err := ParseF64(s)
	if err != nil {
		return 0, false
	}
	return i, true
}

func (val Node) ParseString(ctx *Context) (string, bool) {
	// should not use AsStrRef
	s, ok := val.AsStr(ctx)
	if !ok {
		return "", false
	}

	if s == "null" {
		return "", true
	}

	s, err := Unquote(s)
	if err != nil {
		return "", false
	}
	return s, true
}

func (val Node) ParseNumber(ctx *Context) (json.Number, bool) {
	// should not use AsStrRef
	s, ok := val.AsStr(ctx)
	if !ok {
		return json.Number(""), false
	}

	if s == "null" {
		return json.Number(""), true
	}

	end, ok := SkipNumberFast(s, 0)
	// has error or trailing chars
	if !ok || end != len(s) {
		return json.Number(""), false
	}
	return json.Number(s), true
}

func (val Node) AsF64(ctx *Context) (float64, bool) {
	switch val.Type() {
	case KUint:
		return float64(val.U64()), true
	case KSint:
		return float64(val.I64()), true
	case KReal:
		return float64(val.F64()), true
	case KRawNumber:
		f, err := val.Number(ctx).Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func (val Node) AsBool() (bool, bool) {
	switch val.Type() {
	case KTrue:
		return true, true
	case KFalse:
		return false, true
	default:
		return false, false
	}
}

func (val Node) AsStr(ctx *Context) (string, bool) {
	cnode := ptrCast(val.cptr)
	t := uint8(cnode.typ)
	if t == KStringCommon {
		offset := int(cnode.typ >> PosBits)
		s := ctx.Parser.Json[offset : offset+int(cnode.val)]
		if ctx.Parser.options&(1<<_F_copy_string) == 0 {
			return s, true
		}
		return string(rt.Str2Mem(s)), true
	}
	if t == KStringEscaped {
		offset := int(cnode.typ >> PosBits)
		return string(ctx.Parser.JsonBytes()[offset : offset+int(cnode.val)]), true
	}
	return "", false
}

func (val Node) AsStrRef(ctx *Context) (string, bool) {
	cnode := ptrCast(val.cptr)
	t := uint8(cnode.typ)
	if t == KStringCommon {
		offset := int(cnode.typ >> PosBits)
		return ctx.Parser.Json[offset : offset+int(cnode.val)], true
	}
	if t == KStringEscaped {
		offset := int(cnode.typ >> PosBits)
		return rt.Mem2Str(ctx.Parser.JsonBytes()[offset : offset+int(cnode.val)]), true
	}
	return "", false
}

func (val Node) AsStringText(ctx *Context) ([]byte, bool) {
	if !val.IsStr() {
		return nil, false
	}

	// clone to new bytes
	s, b := val.AsStrRef(ctx)
	return []byte(s), b
}

func (val Node) IsStr() bool {
	return (val.Type() == KStringCommon) || (val.Type() == KStringEscaped)
}

func (val Node) IsRawNumber() bool {
	return val.Type() == KRawNumber
}

func (val Node) Number(ctx *Context) json.Number {
	return json.Number(val.Raw(ctx))
}

func (val Node) Raw(ctx *Context) string {
	node := ptrCast(val.cptr)
	len := int(node.val)
	offset := val.Position()
	return ctx.Parser.Json[offset:int(offset+len)]
}

func (val Node) Position() int {
	node := ptrCast(val.cptr)
	return int(node.typ >> PosBits)
}

func (val Node) AsNumber(ctx *Context) (json.Number, bool) {
	// parse JSON string as number
	if val.IsStr() {
		s, _ := val.AsStr(ctx)
		if !ValidNumberFast(s) {
			return "", false
		} else {
			return json.Number(s), true
		}
	}

	return val.NonstrAsNumber(ctx)
}

func (val Node) NonstrAsNumber(ctx *Context) (json.Number, bool) {
	// deal with raw number
	if val.IsRawNumber() {
		return val.Number(ctx), true
	}

	// deal with parse number
	if !val.IsNumber() {
		return json.Number(""), false
	}

	start := val.Position()
	end, ok := SkipNumberFast(ctx.Parser.Json, start)
	if !ok {
		return "", false
	}
	return json.Number(ctx.Parser.Json[start:end]), true
}

func (val Node) AsRaw(ctx *Context) string {
	// fast path for unescaped strings
	switch val.Type() {
	case KNull:
		return "null"
	case KTrue:
		return "true"
	case KFalse:
		return "false"
	case KStringCommon:
		node := ptrCast(val.cptr)
		len := int(node.val)
		offset := val.Position()
		// add start and end quote
		ref := rt.Str2Mem(ctx.Parser.Json)[offset-1 : offset+len+1]
		return rt.Mem2Str(ref)
	case KRawNumber:
		fallthrough
	case KRaw:
		return val.Raw(ctx)
	case KStringEscaped:
		raw, _ := SkipOneFast(ctx.Parser.Json, val.Position()-1)
		return raw
	default:
		raw, err := SkipOneFast(ctx.Parser.Json, val.Position())
		if err != nil {
			break
		}
		return raw
	}
	panic("should always be valid json here")
}

// reference from the input JSON as possible
func (val Node) StringRef(ctx *Context) string {
	return val.Raw(ctx)
}

//go:nocheckptr
func ptrCast(p uintptr) *node {
	return (*node)(unsafe.Pointer(p))
}

func (val Node) StringCopyEsc(ctx *Context) string {
	// check whether there are in padded
	node := ptrCast(val.cptr)
	len := int(node.val)
	offset := val.Position()
	return string(ctx.Parser.JsonBytes()[offset : offset+len])
}

func (val Node) Object() Object {
	return Object{cptr: val.cptr}
}

func (val Node) Array() Array {
	return Array{cptr: val.cptr}
}

func (val *Array) Children() uintptr {
	return PtrOffset(val.cptr, 1)
}

func (val *Object) Children() uintptr {
	return PtrOffset(val.cptr, 1)
}

func (val *Node) Equal(ctx *Context, lhs string) bool {
	// check whether escaped
	cstr := ptrCast(val.cptr)
	offset := int(val.Position())
	len := int(cstr.val)
	return lhs == ctx.Parser.Json[offset:offset+len]
}

func (node *Node) AsMapEface(ctx *Context, vp unsafe.Pointer) error {
	if node.IsNull() {
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return newUnmatched(node.Position(), rt.MapEfaceType)
	}

	var err, gerr error
	size := obj.Len()

	var m map[string]interface{}
	if *(*unsafe.Pointer)(vp) == nil {
		if ctx.efacePool != nil {
			p := ctx.efacePool.GetMap(size)
			m = *(*map[string]interface{})(unsafe.Pointer(&p))
		} else {
			m = make(map[string]interface{}, size)
		}
	} else {
		m = *(*map[string]interface{})(vp)
	}

	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		m[key], err = val.AsEface(ctx)
		next = val.cptr
		if gerr == nil && err != nil {
			gerr = err
		}
	}

	*(*map[string]interface{})(vp) = m
	return gerr
}

func (node *Node) AsMapString(ctx *Context, vp unsafe.Pointer) error {
	obj, ok := node.AsObj()
	if !ok {
		return newUnmatched(node.Position(), rt.MapStringType)
	}

	size := obj.Len()

	var m map[string]string
	if *(*unsafe.Pointer)(vp) == nil {
		m = make(map[string]string, size)
	} else {
		m = *(*map[string]string)(vp)
	}

	var gerr error
	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		m[key], ok = val.AsStr(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), rt.StringType)
			}
			next = val.Next()
		} else {
			next = PtrOffset(val.cptr, 1)
		}
	}

	*(*map[string]string)(vp) = m
	return gerr
}

func (node *Node) AsSliceEface(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), rt.SliceEfaceType)
	}

	size := arr.Len()
	var s []interface{}
	if size != 0 && ctx.efacePool != nil {
		slice := rt.GoSlice{
			Ptr: ctx.efacePool.GetSlice(size),
			Len: size,
			Cap: size,
		}
		*(*rt.GoSlice)(unsafe.Pointer(&s)) = slice
	} else {
		s = *(*[]interface{})((unsafe.Pointer)(rt.MakeSlice(vp, rt.AnyType, size)))
	}

	*node = NewNode(arr.Children())

	var err, gerr error
	for i := 0; i < size; i++ {
		s[i], err = node.AsEface(ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
	}

	*(*[]interface{})(vp) = s
	return nil
}

func (node *Node) AsSliceI32(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), rt.SliceI32Type)
	}

	size := arr.Len()
	s := *(*[]int32)((unsafe.Pointer)(rt.MakeSlice(vp, rt.Int32Type, size)))
	next := arr.Children()

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsI64(ctx)
		if !ok || ret > math.MaxInt32 || ret < math.MinInt32 {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), rt.Int32Type)
			}
			next = val.Next()
		} else {
			s[i] = int32(ret)
			next = PtrOffset(val.cptr, 1)
		}
	}

	*(*[]int32)(vp) = s
	return gerr
}

func (node *Node) AsSliceI64(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), rt.SliceI64Type)
	}

	size := arr.Len()
	s := *(*[]int64)((unsafe.Pointer)(rt.MakeSlice(vp, rt.Int64Type, size)))
	next := arr.Children()

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)

		ret, ok := val.AsI64(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), rt.Int64Type)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.cptr, 1)
		}
	}

	*(*[]int64)(vp) = s
	return gerr
}

func (node *Node) AsSliceU32(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), rt.SliceU32Type)
	}

	size := arr.Len()
	next := arr.Children()
	s := *(*[]uint32)((unsafe.Pointer)(rt.MakeSlice(vp, rt.Uint32Type, size)))

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsU64(ctx)
		if !ok || ret > math.MaxUint32 {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), rt.Uint32Type)
			}
			next = val.Next()
		} else {
			s[i] = uint32(ret)
			next = PtrOffset(val.cptr, 1)
		}
	}

	*(*[]uint32)(vp) = s
	return gerr
}

func (node *Node) AsSliceU64(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), rt.SliceU64Type)
	}

	size := arr.Len()
	next := arr.Children()

	s := *(*[]uint64)((unsafe.Pointer)(rt.MakeSlice(vp, rt.Uint64Type, size)))
	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsU64(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), rt.Uint64Type)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.cptr, 1)
		}
	}

	*(*[]uint64)(vp) = s
	return gerr
}

func (node *Node) AsSliceString(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), rt.SliceStringType)
	}

	size := arr.Len()
	next := arr.Children()
	s := *(*[]string)((unsafe.Pointer)(rt.MakeSlice(vp, rt.StringType, size)))

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsStr(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), rt.StringType)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.cptr, 1)
		}
	}

	*(*[]string)(vp) = s
	return gerr
}

func (val *Node) AsSliceBytes(ctx *Context) ([]byte, error) {
	var origin []byte
	switch val.Type() {
	case KStringEscaped:
		node := ptrCast(val.cptr)
		offset := val.Position()
		len := int(node.val)
		origin = ctx.Parser.JsonBytes()[offset : offset+len]
	case KStringCommon:
		origin = rt.Str2Mem(val.StringRef(ctx))
	case KArray:
		arr := val.Array()
		size := arr.Len()
		a := make([]byte, size)
		elem := NewNode(arr.Children())
		var gerr error
		var ok bool
		for i := 0; i < size; i++ {
			a[i], ok = elem.AsByte(ctx)
			if !ok && gerr == nil {
				gerr = newUnmatched(val.Position(), rt.BytesType)
			}
			elem = NewNode(PtrOffset(elem.cptr, 1))
		}
		return a, gerr
	default:
		return nil, newUnmatched(val.Position(), rt.BytesType)
	}

	b64, err := rt.DecodeBase64(origin)
	if err != nil {
		return nil, newUnmatched(val.Position(), rt.BytesType)
	}
	return b64, nil
}

// AsEface will always ok, because we have parse in native.
func (node *Node) AsEface(ctx *Context) (interface{}, error) {
	if ctx.efacePool != nil {
		iter := NewNodeIter(*node)
		v := AsEfaceFast(&iter, ctx)
		*node = iter.Peek()
		return v, nil
	}
	if ctx.Parser != nil && ctx.Parser.isEface && (ctx.Parser.options&(1<<_F_use_int64)) == 0 && node.cptr == ctx.Root().cptr {
		useNumber := (ctx.Parser.options & (1 << _F_use_number)) != 0
		ctx.efacePool = newEfacePool(&ctx.Parser.nbuf.stat, useNumber)
		if len(ctx.Stack.stack) <= int(ctx.Parser.nbuf.stat.max_depth)+1 {
			ctx.Stack = newStack(int(ctx.Parser.nbuf.stat.max_depth))
		}
		iter := NewNodeIter(*node)
		v := AsEfaceFast(&iter, ctx)
		*node = iter.Peek()
		ctx.efacePool = nil
		return v, nil
	}
	return node.AsEfaceFallback(ctx)
}

func parseSingleNode(node Node, ctx *Context) interface{} {
	var v interface{}
	switch node.Type() {
	case KObject:
		v = map[string]interface{}{}
	case KArray:
		v = []interface{}{}
	case KStringCommon:
		v = node.StringRef(ctx)
	case KStringEscaped:
		v = node.StringCopyEsc(ctx)
	case KTrue:
		v = true
	case KFalse:
		v = false
	case KNull:
		v = nil
	case KUint:
		v = float64(node.U64())
	case KSint:
		v = float64(node.I64())
	case KReal:
		v = float64(node.F64())
	case KRawNumber:
		v = node.Number(ctx)
	default:
		panic("unreachable for as eface")
	}
	return v
}

func castU64(val float64) uint64 {
	return *((*uint64)(unsafe.Pointer((&val))))
}

func AsEfaceFast(iter *NodeIter, ctx *Context) interface{} {
	var mp, sp, parent unsafe.Pointer // current container pointer
	var node Node
	var size int
	var isObj bool
	var slice rt.GoSlice
	var val unsafe.Pointer
	var vt **rt.GoType
	var vp *unsafe.Pointer
	var rootM unsafe.Pointer
	var rootS rt.GoSlice
	var root interface{}
	var key string

	node = iter.Next()

	switch node.Type() {
	case KObject:
		size = node.Object().Len()
		if size != 0 {
			ctx.Stack.Push(nil, 0, true)
			mp = ctx.efacePool.GetMap(size)
			rootM = mp
			isObj = true
			goto _object_key
		} else {
			return rt.GoEface{
				Type:  rt.MapEfaceType,
				Value: ctx.efacePool.GetMap(0),
			}.Pack()
		}
	case KArray:
		size = node.Array().Len()
		if size != 0 {
			ctx.Stack.Push(nil, 0, false)
			sp = ctx.efacePool.GetSlice(size)
			slice = rt.GoSlice{
				Ptr: sp,
				Len: size,
				Cap: size,
			}
			rootS = slice
			isObj = false
			val = sp
			goto _arr_val
		} else {
			ctx.efacePool.ConvTSlice(rt.EmptySlice, rt.SliceEfaceType, unsafe.Pointer(&root))
		}
	case KStringCommon:
		s, _ := node.AsStr(ctx)
		ctx.efacePool.ConvTstring(s, unsafe.Pointer(&root))
	case KStringEscaped:
		ctx.efacePool.ConvTstring(node.StringCopyEsc(ctx), unsafe.Pointer(&root))
	case KTrue:
		root = true
	case KFalse:
		root = false
	case KNull:
		root = nil
	case KUint:
		ctx.efacePool.ConvF64(float64(node.U64()), unsafe.Pointer(&root))
	case KSint:
		ctx.efacePool.ConvF64(float64(node.I64()), unsafe.Pointer(&root))
	case KReal:
		ctx.efacePool.ConvF64(node.F64(), unsafe.Pointer(&root))
	case KRawNumber:
		ctx.efacePool.ConvTnum(node.Number(ctx), unsafe.Pointer(&root))
	default:
		panic("unreachable for as eface")
	}
	return root

_object_key:
	node = iter.Next()
	key, _ = node.AsStr(ctx)

	// interface{} slot in map bucket
	val = rt.Mapassign_faststr(rt.MapEfaceMapType, mp, key)
	vt = &(*rt.GoEface)(val).Type
	vp = &(*rt.GoEface)(val).Value

	// parse value node
	node = iter.Next()
	switch node.Type() {
	case KObject:
		newSize := node.Object().Len()
		newMp := ctx.efacePool.GetMap(newSize)
		*vt = rt.MapEfaceType
		*vp = newMp
		remain := size - 1
		isObj = true
		if newSize != 0 {
			if remain > 0 {
				ctx.Stack.Push(mp, remain, true)
			}
			mp = newMp
			size = newSize
			goto _object_key
		}
	case KArray:
		newSize := node.Array().Len()
		if newSize == 0 {
			ctx.efacePool.ConvTSlice(rt.EmptySlice, rt.SliceEfaceType, val)
			break
		}

		newSp := ctx.efacePool.GetSlice(newSize)
		// pack to []interface{}
		ctx.efacePool.ConvTSlice(rt.GoSlice{
			Ptr: newSp,
			Len: newSize,
			Cap: newSize,
		}, rt.SliceEfaceType, val)
		remain := size - 1
		if remain > 0 {
			ctx.Stack.Push(mp, remain, true)
		}
		val = newSp
		isObj = false
		size = newSize
		goto _arr_val
	case KStringCommon:
		s, _ := node.AsStr(ctx)
		ctx.efacePool.ConvTstring(s, val)
	case KStringEscaped:
		ctx.efacePool.ConvTstring(node.StringCopyEsc(ctx), val)
	case KTrue:
		rt.ConvTBool(true, (*interface{})(val))
	case KFalse:
		rt.ConvTBool(false, (*interface{})(val))
	case KNull:
		// Write nil explicitly. A fresh slot is already zero, but for a
		// duplicate key mapassign returns the *existing* slot, which still
		// holds the previous value; falling through would wrongly keep it
		// instead of overwriting it with null like encoding/json does.
		*(*any)(val) = nil
	case KUint:
		ctx.efacePool.ConvF64(float64(node.U64()), val)
	case KSint:
		ctx.efacePool.ConvF64(float64(node.I64()), val)
	case KReal:
		ctx.efacePool.ConvF64(node.F64(), val)
	case KRawNumber:
		ctx.efacePool.ConvTnum(node.Number(ctx), val)
	default:
		panic("unreachable for as eface")
	}

	// check size
	size -= 1
	if size != 0 {
		goto _object_key
	}

	parent, size, isObj = ctx.Stack.Pop()

	// parent is empty
	if parent == nil {
		if isObj {
			return rt.GoEface{
				Type:  rt.MapEfaceType,
				Value: rootM,
			}.Pack()
		} else {
			ctx.efacePool.ConvTSlice(rootS, rt.SliceEfaceType, (unsafe.Pointer)(&root))
			return root
		}
	}

	// continue to parse parent
	if isObj {
		mp = parent
		goto _object_key
	} else {
		val = rt.PtrAdd(parent, rt.AnyType.Size)
		goto _arr_val
	}

_arr_val:
	// interface{} slot in slice
	vt = &(*rt.GoEface)(val).Type
	vp = &(*rt.GoEface)(val).Value

	// parse value node
	node = iter.Next()
	switch node.Type() {
	case KObject:
		newSize := node.Object().Len()
		newMp := ctx.efacePool.GetMap(newSize)
		*vt = rt.MapEfaceType
		*vp = newMp
		remain := size - 1
		if newSize != 0 {
			// push next array elem into stack
			if remain > 0 {
				ctx.Stack.Push(val, remain, false)
			}
			mp = newMp
			size = newSize
			isObj = true
			goto _object_key
		}
	case KArray:
		newSize := node.Array().Len()
		if newSize == 0 {
			ctx.efacePool.ConvTSlice(rt.EmptySlice, rt.SliceEfaceType, val)
			break
		}

		newSp := ctx.efacePool.GetSlice(newSize)
		// pack to []interface{}
		ctx.efacePool.ConvTSlice(rt.GoSlice{
			Ptr: newSp,
			Len: newSize,
			Cap: newSize,
		}, rt.SliceEfaceType, val)

		remain := size - 1
		if remain > 0 {
			ctx.Stack.Push(val, remain, false)
		}

		val = newSp
		isObj = false
		size = newSize
		goto _arr_val
	case KStringCommon:
		s, _ := node.AsStr(ctx)
		ctx.efacePool.ConvTstring(s, val)
	case KStringEscaped:
		ctx.efacePool.ConvTstring(node.StringCopyEsc(ctx), val)
	case KTrue:
		rt.ConvTBool(true, (*interface{})(val))
	case KFalse:
		rt.ConvTBool(false, (*interface{})(val))
	case KNull:
		// Write nil explicitly. A fresh slot is already zero, but for a
		// duplicate key mapassign returns the *existing* slot, which still
		// holds the previous value; falling through would wrongly keep it
		// instead of overwriting it with null like encoding/json does.
		*(*any)(val) = nil
	case KUint:
		ctx.efacePool.ConvF64(float64(node.U64()), val)
	case KSint:
		ctx.efacePool.ConvF64(float64(node.I64()), val)
	case KReal:
		ctx.efacePool.ConvF64(node.F64(), val)
	case KRawNumber:
		ctx.efacePool.ConvTnum(node.Number(ctx), val)
	default:
		panic("unreachable for as eface")
	}

	// check size
	size -= 1
	if size != 0 {
		val = rt.PtrAdd(val, rt.AnyType.Size)
		goto _arr_val
	}

	parent, size, isObj = ctx.Stack.Pop()

	// parent is empty
	if parent == nil {
		if isObj {
			return rt.GoEface{
				Type:  rt.MapEfaceType,
				Value: rootM,
			}.Pack()
		} else {
			ctx.efacePool.ConvTSlice(rootS, rt.SliceEfaceType, unsafe.Pointer(&root))
			return root
		}
	}

	// continue to parse parent
	if isObj {
		mp = parent
		goto _object_key
	} else {
		val = rt.PtrAdd(parent, rt.AnyType.Size)
		goto _arr_val
	}
}

func (node *Node) AsEfaceFallback(ctx *Context) (interface{}, error) {
	p := ctx.Parser
	opts := p.options
	copyStr := opts&(1<<_F_copy_string) != 0
	numSpecial := opts&((1<<_F_use_number)|(1<<_F_use_int64)) != 0
	jsonStr := p.Json

	switch node.Type() {
	case KObject:
		obj := node.Object()
		size := obj.Len()
		m := make(map[string]interface{}, size)
		cur := obj.Children()
		var gerr, err error
		for i := 0; i < size; i++ {
			knode := ptrCast(cur)
			var key string
			if uint8(knode.typ) == KStringCommon {
				koff := int(knode.typ >> PosBits)
				s := jsonStr[koff : koff+int(knode.val)]
				if !copyStr {
					key = s
				} else {
					key = string(rt.Str2Mem(s))
				}
			} else {
				koff := int(knode.typ >> PosBits)
				key = string(p.JsonBytes()[koff : koff+int(knode.val)])
			}
			vptr := cur + unsafe.Sizeof(struct{ typ, val uint64 }{})
			if !numSpecial {
				vnode := ptrCast(vptr)
				switch uint8(vnode.typ) {
				case KNull:
					m[key] = nil
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KTrue:
					m[key] = true
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KFalse:
					m[key] = false
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KStringCommon:
					soff := int(vnode.typ >> PosBits)
					s := jsonStr[soff : soff+int(vnode.val)]
					if !copyStr {
						m[key] = s
					} else {
						m[key] = string(rt.Str2Mem(s))
					}
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KStringEscaped:
					soff := int(vnode.typ >> PosBits)
					m[key] = string(p.JsonBytes()[soff : soff+int(vnode.val)])
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KUint:
					m[key] = float64(vnode.val)
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KSint:
					m[key] = float64(int64(vnode.val))
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KReal:
					m[key] = *(*float64)(unsafe.Pointer(&vnode.val))
					cur = vptr + unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				}
			}
			*node = Node{cptr: vptr}
			m[key], err = node.AsEfaceFallback(ctx)
			cur = node.cptr
			if gerr == nil && err != nil {
				gerr = err
			}
		}
		*node = Node{cptr: cur}
		return m, gerr
	case KArray:
		arr := node.Array()
		size := arr.Len()
		a := make([]interface{}, size)
		cur := arr.Children()
		var gerr, err error
		for i := 0; i < size; i++ {
			if !numSpecial {
				vnode := ptrCast(cur)
				switch uint8(vnode.typ) {
				case KNull:
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KTrue:
					a[i] = true
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KFalse:
					a[i] = false
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KStringCommon:
					soff := int(vnode.typ >> PosBits)
					s := jsonStr[soff : soff+int(vnode.val)]
					if !copyStr {
						a[i] = s
					} else {
						a[i] = string(rt.Str2Mem(s))
					}
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KStringEscaped:
					soff := int(vnode.typ >> PosBits)
					a[i] = string(p.JsonBytes()[soff : soff+int(vnode.val)])
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KUint:
					a[i] = float64(vnode.val)
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KSint:
					a[i] = float64(int64(vnode.val))
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				case KReal:
					a[i] = *(*float64)(unsafe.Pointer(&vnode.val))
					cur += unsafe.Sizeof(struct{ typ, val uint64 }{})
					continue
				}
			}
			*node = Node{cptr: cur}
			a[i], err = node.AsEfaceFallback(ctx)
			cur = node.cptr
			if gerr == nil && err != nil {
				gerr = err
			}
		}
		*node = Node{cptr: cur}
		return a, gerr
	case KStringCommon:
		str, _ := node.AsStr(ctx)
		*node = NewNode(PtrOffset(node.cptr, 1))
		return str, nil
	case KStringEscaped:
		str := node.StringCopyEsc(ctx)
		*node = NewNode(PtrOffset(node.cptr, 1))
		return str, nil
	case KTrue:
		*node = NewNode(PtrOffset(node.cptr, 1))
		return true, nil
	case KFalse:
		*node = NewNode(PtrOffset(node.cptr, 1))
		return false, nil
	case KNull:
		*node = NewNode(PtrOffset(node.cptr, 1))
		return nil, nil
	default:
		// use float64
		if ctx.Parser.options&(1<<_F_use_number) != 0 {
			num, ok := node.AsNumber(ctx)
			if !ok {
				// skip the unmatched type
				*node = NewNode(node.Next())
				return nil, newUnmatched(node.Position(), rt.JsonNumberType)
			} else {
				*node = NewNode(PtrOffset(node.cptr, 1))
				return num, nil
			}
		} else if ctx.Parser.options&(1<<_F_use_int64) != 0 {
			// first try int64
			i, ok := node.AsI64(ctx)
			if ok {
				*node = NewNode(PtrOffset(node.cptr, 1))
				return i, nil
			}

			// is not integer, then use float64
			f, ok := node.AsF64(ctx)
			if ok {
				*node = NewNode(PtrOffset(node.cptr, 1))
				return f, nil
			}

			// skip the unmatched type
			*node = NewNode(node.Next())
			return nil, newUnmatched(node.Position(), rt.Int64Type)
		} else {
			num, ok := node.AsF64(ctx)
			if !ok {
				// skip the unmatched type
				*node = NewNode(node.Next())
				return nil, newUnmatched(node.Position(), rt.Float64Type)
			} else {
				*node = NewNode(PtrOffset(node.cptr, 1))
				return num, nil
			}
		}
	}
}

//go:nosplit
func PtrOffset(ptr uintptr, off int64) uintptr {
	return uintptr(int64(ptr) + off*int64(unsafe.Sizeof(node{})))
}
