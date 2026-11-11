package optdec

import (
	"encoding/json"
	"math"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/envs"
	"github.com/bytedance/sonic/internal/rt"
)

type Context struct {
	Parser     *Parser
	sliceArena []interface{}
	sliceIdx   int
	Utf8Inv    bool
}

func (ctx *Context) Options() uint64 {
	return ctx.Parser.options
}

func (ctx *Context) initSliceArena() {
	if n := int(ctx.Parser.nbuf.stat.array_elems); n > 0 && ctx.sliceArena == nil {
		ctx.sliceArena = make([]interface{}, n)
		ctx.sliceIdx = 0
	}
}

func (ctx *Context) allocEfaceSlice(size int) []interface{} {
	if size == 0 {
		return []interface{}{}
	}
	if rem := len(ctx.sliceArena) - ctx.sliceIdx; rem >= size {
		s := ctx.sliceArena[ctx.sliceIdx : ctx.sliceIdx+size : ctx.sliceIdx+size]
		ctx.sliceIdx += size
		return s
	}
	return make([]interface{}, size)
}

func prepareSlice[T any](vp *[]T, size int) []T {
	if size == 0 {
		return []T{}
	}
	old := *vp
	if cap(old) >= size {
		return old[:size]
	}
	s := make([]T, size)
	if len(old) > 0 {
		copy(s, old)
	}
	return s
}

func canUseFastMap(opts uint64, root reflect.Type) bool {
	return envs.UseFastMap && (opts&(1<<_F_use_int64)) == 0 && (root == anyType || root == mapEfaceType || root == sliceEfaceType)
}

func newContextOnParser(p *Parser, opts uint64, root reflect.Type) (*Context, error) {
	return newContextOnParserWithPlan(p, opts, root, nil)
}

func newContextOnParserWithPlan(p *Parser, opts uint64, root reflect.Type, proj *projPlan) (*Context, error) {
	ctx := &p.ctx
	ctx.Parser = p
	if root == anyType || root == mapEfaceType || root == sliceEfaceType {
		p.isEface = true
	}

	var ecode ErrorCode
	if proj != nil {
		ecode = parseWithSchemaOrFull(p, proj)
	} else {
		ecode = p.parse()
	}
	if ecode != 0 {
		return ctx, p.fixError(ecode)
	}

	if canUseFastMap(opts, root) {
		ctx.initSliceArena()
	}

	return ctx, nil
}

func NewContext(json string, pos int, opts uint64, root reflect.Type) (Context, error) {
	return NewContextWithSession(json, pos, opts, root, nil)
}

func NewContextWithSession(json string, pos int, opts uint64, root reflect.Type, sess *InputSession) (Context, error) {
	p := newParserFromSession(json, pos, opts, sess)
	ctx, err := newContextOnParser(p, opts, root)
	return *ctx, err
}

func (ctx *Context) Delete() {
	if ctx.Parser != nil {
		ctx.Parser.free()
		ctx.Parser = nil
	}
	ctx.sliceArena = nil
	ctx.sliceIdx = 0
}

type Node struct {
	ptr *node
}

func NewNode(ptr *node) Node {
	return Node{ptr: ptr}
}

func (ctx *Context) Root() Node {
	return Node{ptr: unsafe.SliceData(ctx.Parser.nodes)}
}

type Array struct {
	ptr *node
}

type Object struct {
	ptr *node
}

func (obj Object) Len() int {
	return int(uint64(obj.ptr.val) & ConLenMask)
}

func (arr Array) Len() int {
	return int(uint64(arr.ptr.val) & ConLenMask)
}

func (val Node) Type() uint8 {
	return uint8(val.ptr.typ & TypeMask)
}

func (val Node) Next() *node {
	cobj := val.ptr
	if uint8(cobj.typ)-KObject > 1 {
		return PtrOffset(cobj, 1)
	}
	return PtrOffset(cobj, int64(cobj.val>>ConLenBits))
}

func (val *Node) next() {
	*val = NewNode(val.Next())
}

type NodeIter struct {
	next *node
}

func NewNodeIter(n Node) NodeIter {
	return NodeIter{next: n.ptr}
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
	return val.ptr.val
}

func (val Node) I64() int64 {
	return int64(val.ptr.val)
}

func (val Node) IsNull() bool {
	return val.Type() == KNull
}

func (val Node) IsNumber() bool {
	return val.Type()&KNumber != 0
}

func (val Node) F64() float64 {
	return math.Float64frombits(val.ptr.val)
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
		ptr: val.ptr,
	}, true
}

func (val Node) Obj() Object {
	return Object{ptr: val.ptr}
}

func (val Node) Arr() Array {
	return Array{ptr: val.ptr}
}

func (val *Node) AsArr() (Array, bool) {
	var ret Array
	if val.Type() != KArray {
		return ret, false
	}
	return Array{
		ptr: val.ptr,
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
	cnode := val.ptr
	t := uint8(cnode.typ)
	if t == KStringCommon {
		offset := int(cnode.typ >> PosBits)
		return ctx.Parser.strCommon(offset, int(cnode.val), ctx.Parser.options&(1<<_F_copy_string) != 0), true
	}
	if t == KStringEscaped {
		offset := int(cnode.typ >> PosBits)
		return ctx.Parser.strEscaped(offset, cnode.val), true
	}
	return "", false
}

func (val Node) AsStrRef(ctx *Context) (string, bool) {
	cnode := val.ptr
	t := uint8(cnode.typ)
	if t == KStringCommon {
		offset := int(cnode.typ >> PosBits)
		return ctx.Parser.strRefCommon(offset, int(cnode.val)), true
	}
	if t == KStringEscaped {
		offset := int(cnode.typ >> PosBits)
		return ctx.Parser.strRefEscaped(offset, cnode.val), true
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
	n := val.ptr
	length := int(n.val)
	offset := val.Position()
	return ctx.Parser.strCommon(offset, length, ctx.Parser.options&(1<<_F_copy_string) != 0)
}

func (val Node) Position() int {
	return int(val.ptr.typ >> PosBits)
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
	return json.Number(ctx.Parser.strCommon(start, end-start, ctx.Parser.options&(1<<_F_copy_string) != 0)), true
}

func (val Node) AsRaw(ctx *Context) string {
	return val.rawSpan(ctx.Parser)
}

func (val Node) rawSpan(p *Parser) string {
	switch val.Type() {
	case KNull, KTrue:
		pos := val.Position()
		return p.rawSlice(pos, pos+4)
	case KFalse:
		pos := val.Position()
		return p.rawSlice(pos, pos+5)
	case KStringCommon:
		n := val.ptr
		length := int(n.val)
		offset := val.Position()
		return p.rawSlice(offset-1, offset+length+1)
	case KRawNumber, KRaw:
		n := val.ptr
		length := int(n.val)
		offset := val.Position()
		return p.rawSlice(offset, offset+length)
	case KUint, KSint, KReal:
		start := val.Position()
		end, ok := SkipNumberFast(p.Json, start)
		if !ok {
			break
		}
		return p.rawSlice(start, end)
	case KStringEscaped:
		raw, _ := SkipOneFast(p.Json, val.Position()-1)
		return raw
	case KObject, KArray:
		return val.containerAsRaw(p)
	default:
		raw, err := SkipOneFast(p.Json, val.Position())
		if err != nil {
			break
		}
		return raw
	}
	panic("should always be valid json here")
}

func (val Node) containerAsRaw(p *Parser) string {
	s := p.Json
	cnode := val.ptr
	start := int(cnode.typ >> PosBits)
	span := int(cnode.val >> ConLenBits)
	if span <= 0 {
		raw, err := SkipOneFast(s, start)
		if err != nil {
			panic("should always be valid json here")
		}
		return raw
	}
	curOff := 0
	endOff := span
	depth := 0
	for curOff < endOff {
		cn := PtrOffset(cnode, int64(curOff))
		ctyp := uint8(cn.typ)
		if ctyp != KObject && ctyp != KArray {
			break
		}
		cspan := int(cn.val >> ConLenBits)
		csize := int(cn.val & ConLenMask)
		depth++
		if cspan <= 1 {
			break
		}
		if (ctyp == KObject && cspan == 1+2*csize) || (ctyp == KArray && cspan == 1+csize) {
			curOff = endOff - 1
			continue
		}
		childOff := curOff + 1
		for {
			vOff := childOff
			if ctyp == KObject {
				vOff++
			}
			vnode := PtrOffset(cnode, int64(vOff))
			var nextOff int
			if uint8(vnode.typ)-KObject > 1 {
				nextOff = vOff + 1
			} else {
				nextOff = vOff + int(vnode.val>>ConLenBits)
			}
			if nextOff >= endOff {
				curOff = vOff
				break
			}
			childOff = nextOff
		}
	}
	lnode := PtrOffset(cnode, int64(curOff))
	ltyp := uint8(lnode.typ)
	lpos := int(lnode.typ >> PosBits)
	var pos int
	switch ltyp {
	case KObject, KArray:
		pos = lpos + 1
	case KStringCommon:
		pos = lpos + int(lnode.val) + 1
	case KNull, KTrue:
		pos = lpos + 4
	case KFalse:
		pos = lpos + 5
	case KRawNumber, KRaw:
		pos = lpos + int(lnode.val)
	case KUint, KSint, KReal:
		pos, _ = SkipNumberFast(s, lpos)
	case KStringEscaped:
		raw, err := SkipOneFast(s, lpos-1)
		if err != nil {
			panic("should always be valid json here")
		}
		pos = lpos - 1 + len(raw)
	default:
		raw, err := SkipOneFast(s, start)
		if err != nil {
			panic("should always be valid json here")
		}
		return raw
	}
	for depth > 0 && pos < len(s) {
		c := s[pos]
		pos++
		if c == '}' || c == ']' {
			depth--
		}
	}
	return p.rawSlice(start, pos)
}

// reference from the input JSON as possible
func (val Node) StringRef(ctx *Context) string {
	return val.Raw(ctx)
}

func (val Node) StringCopyEsc(ctx *Context) string {
	n := val.ptr
	offset := val.Position()
	return ctx.Parser.strEscaped(offset, n.val)
}

func (val Node) Object() Object {
	return Object{ptr: val.ptr}
}

func (val Node) Array() Array {
	return Array{ptr: val.ptr}
}

func (val *Array) Children() *node {
	return PtrOffset(val.ptr, 1)
}

func (val *Object) Children() *node {
	return PtrOffset(val.ptr, 1)
}

func (val *Node) Equal(ctx *Context, lhs string) bool {
	cstr := val.ptr
	offset := val.Position()
	length := int(cstr.val)
	return lhs == ctx.Parser.strRefCommon(offset, length)
}

func (node *Node) AsMapEface(ctx *Context, vp unsafe.Pointer) error {
	if node.IsNull() {
		return nil
	}

	obj, ok := node.AsObj()
	if !ok {
		return newUnmatched(node.Position(), mapEfaceType)
	}

	var err, gerr error
	size := obj.Len()

	dst := (*map[string]interface{})(vp)
	m := *dst
	if m == nil {
		m = make(map[string]interface{}, size)
	}

	next := obj.Children()
	for i := 0; i < size; i++ {
		knode := NewNode(next)
		key, _ := knode.AsStr(ctx)
		val := NewNode(PtrOffset(next, 1))
		m[key], err = val.AsEface(ctx)
		next = val.ptr
		if gerr == nil && err != nil {
			gerr = err
		}
	}

	*dst = m
	return gerr
}

func (node *Node) AsMapString(ctx *Context, vp unsafe.Pointer) error {
	obj, ok := node.AsObj()
	if !ok {
		return newUnmatched(node.Position(), mapStringType)
	}

	size := obj.Len()

	dst := (*map[string]string)(vp)
	m := *dst
	if m == nil {
		m = make(map[string]string, size)
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
				gerr = newUnmatched(val.Position(), stringType)
			}
			next = val.Next()
		} else {
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = m
	return gerr
}

func (node *Node) AsSliceEface(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceEfaceType)
	}

	size := arr.Len()
	dst := (*[]interface{})(vp)
	var s []interface{}
	if size == 0 {
		*dst = []interface{}{}
		return nil
	}
	if cap(*dst) >= size {
		s = (*dst)[:size]
	} else if *dst == nil && ctx.sliceArena != nil {
		s = ctx.allocEfaceSlice(size)
	} else {
		s = prepareSlice(dst, size)
	}

	*node = NewNode(arr.Children())

	var err, gerr error
	for i := 0; i < size; i++ {
		s[i], err = node.AsEface(ctx)
		if gerr == nil && err != nil {
			gerr = err
		}
	}

	*dst = s
	return nil
}

func (node *Node) AsSliceI32(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceI32Type)
	}

	size := arr.Len()
	dst := (*[]int32)(vp)
	s := prepareSlice(dst, size)
	next := arr.Children()

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsI64(ctx)
		if !ok || ret > math.MaxInt32 || ret < math.MinInt32 {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), int32Type)
			}
			next = val.Next()
		} else {
			s[i] = int32(ret)
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceI64(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceI64Type)
	}

	size := arr.Len()
	dst := (*[]int64)(vp)
	s := prepareSlice(dst, size)
	next := arr.Children()

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)

		ret, ok := val.AsI64(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), int64Type)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceU32(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceU32Type)
	}

	size := arr.Len()
	next := arr.Children()
	dst := (*[]uint32)(vp)
	s := prepareSlice(dst, size)

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsU64(ctx)
		if !ok || ret > math.MaxUint32 {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), uint32Type)
			}
			next = val.Next()
		} else {
			s[i] = uint32(ret)
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceU64(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceU64Type)
	}

	size := arr.Len()
	next := arr.Children()

	dst := (*[]uint64)(vp)
	s := prepareSlice(dst, size)
	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsU64(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), uint64Type)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceString(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceStringType)
	}

	size := arr.Len()
	next := arr.Children()
	dst := (*[]string)(vp)
	s := prepareSlice(dst, size)

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsStr(ctx)
		if !ok {
			if gerr == nil {
				gerr = newUnmatched(val.Position(), stringType)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceF64(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceF64Type)
	}

	size := arr.Len()
	next := arr.Children()
	dst := (*[]float64)(vp)
	s := prepareSlice(dst, size)

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsF64(ctx)
		if !ok {
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = newUnmatched(val.Position(), float64Type)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceF32(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceF32Type)
	}

	size := arr.Len()
	next := arr.Children()
	dst := (*[]float32)(vp)
	s := prepareSlice(dst, size)

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsF64(ctx)
		if !ok || ret > math.MaxFloat32 || ret < -math.MaxFloat32 {
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = newUnmatched(val.Position(), float32Type)
			}
			next = val.Next()
		} else {
			s[i] = float32(ret)
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (node *Node) AsSliceBool(ctx *Context, vp unsafe.Pointer) error {
	arr, ok := node.AsArr()
	if !ok {
		return newUnmatched(node.Position(), sliceBoolType)
	}

	size := arr.Len()
	next := arr.Children()
	dst := (*[]bool)(vp)
	s := prepareSlice(dst, size)

	var gerr error
	for i := 0; i < size; i++ {
		val := NewNode(next)
		ret, ok := val.AsBool()
		if !ok {
			if val.IsNull() {
				next = PtrOffset(val.ptr, 1)
				continue
			}
			if gerr == nil {
				gerr = newUnmatched(val.Position(), boolType)
			}
			next = val.Next()
		} else {
			s[i] = ret
			next = PtrOffset(val.ptr, 1)
		}
	}

	*dst = s
	return gerr
}

func (val *Node) AsSliceBytes(ctx *Context) ([]byte, error) {
	var origin []byte
	switch val.Type() {
	case KStringEscaped:
		n := val.ptr
		offset := val.Position()
		origin = ctx.Parser.bytesEscaped(offset, n.val)
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
				gerr = newUnmatched(val.Position(), bytesType)
			}
			elem = NewNode(PtrOffset(elem.ptr, 1))
		}
		return a, gerr
	default:
		return nil, newUnmatched(val.Position(), bytesType)
	}

	b64, err := rt.DecodeBase64(origin)
	if err != nil {
		return nil, newUnmatched(val.Position(), bytesType)
	}
	return b64, nil
}

const smallUintAnyLimit = 4096

var (
	emptyEfaceSliceAny any = []interface{}{}
	smallFloat64Any    [smallUintAnyLimit]any
	smallInt64Any      [smallUintAnyLimit]any
)

func init() {
	for i := 0; i < smallUintAnyLimit; i++ {
		smallFloat64Any[i] = float64(i)
		smallInt64Any[i] = int64(i)
	}
}

func uintToFloat64Any(v uint64) any {
	if v < smallUintAnyLimit {
		return smallFloat64Any[v]
	}
	return float64(v)
}

func uintToInt64Any(v uint64) any {
	if v < smallUintAnyLimit {
		return smallInt64Any[v]
	}
	return int64(v)
}

// AsEface will always ok, because we have parse in native.
func (node *Node) AsEface(ctx *Context) (interface{}, error) {
	if ctx.Parser != nil && ctx.Parser.isEface && (ctx.Parser.options&(1<<_F_use_int64)) == 0 && node.ptr == ctx.Root().ptr && ctx.sliceArena == nil {
		ctx.initSliceArena()
	}
	return node.AsEfaceFallback(ctx)
}

func (node *Node) AsEfaceFallback(ctx *Context) (interface{}, error) {
	p := ctx.Parser
	opts := p.options
	copyStr := opts&(1<<_F_copy_string) != 0
	numSpecial := opts&((1<<_F_use_number)|(1<<_F_use_int64)) != 0

	switch node.Type() {
	case KObject:
		obj := node.Object()
		size := obj.Len()
		m := make(map[string]interface{}, size)
		cur := obj.Children()
		var gerr, err error
		for i := 0; i < size; i++ {
			knode := cur
			var key string
			if uint8(knode.typ) == KStringCommon {
				key = p.strCommon(int(knode.typ>>PosBits), int(knode.val), copyStr)
			} else {
				key = p.strEscaped(int(knode.typ>>PosBits), knode.val)
			}
			vptr := PtrOffset(cur, 1)
			if !numSpecial {
				vnode := vptr
				switch uint8(vnode.typ) {
				case KNull:
					m[key] = nil
					cur = PtrOffset(vptr, 1)
					continue
				case KTrue:
					m[key] = true
					cur = PtrOffset(vptr, 1)
					continue
				case KFalse:
					m[key] = false
					cur = PtrOffset(vptr, 1)
					continue
				case KStringCommon:
					m[key] = p.strCommon(int(vnode.typ>>PosBits), int(vnode.val), copyStr)
					cur = PtrOffset(vptr, 1)
					continue
				case KStringEscaped:
					m[key] = p.strEscaped(int(vnode.typ>>PosBits), vnode.val)
					cur = PtrOffset(vptr, 1)
					continue
				case KUint:
					m[key] = uintToFloat64Any(vnode.val)
					cur = PtrOffset(vptr, 1)
					continue
				case KSint:
					m[key] = float64(int64(vnode.val))
					cur = PtrOffset(vptr, 1)
					continue
				case KReal:
					m[key] = math.Float64frombits(vnode.val)
					cur = PtrOffset(vptr, 1)
					continue
				case KArray:
					if (uint64(vnode.val) & ConLenMask) == 0 {
						m[key] = emptyEfaceSliceAny
						cur = PtrOffset(vptr, 1)
						continue
					}
				}
			}
			*node = Node{ptr: vptr}
			m[key], err = node.AsEfaceFallback(ctx)
			cur = node.ptr
			if gerr == nil && err != nil {
				gerr = err
			}
		}
		*node = Node{ptr: cur}
		return m, gerr
	case KArray:
		arr := node.Array()
		size := arr.Len()
		if size == 0 {
			*node = Node{ptr: arr.Children()}
			return emptyEfaceSliceAny, nil
		}
		var a []interface{}
		if ctx.sliceArena != nil {
			a = ctx.allocEfaceSlice(size)
		} else {
			a = make([]interface{}, size)
		}
		cur := arr.Children()
		var gerr, err error
		for i := 0; i < size; i++ {
			if !numSpecial {
				vnode := cur
				switch uint8(vnode.typ) {
				case KNull:
					cur = PtrOffset(cur, 1)
					continue
				case KTrue:
					a[i] = true
					cur = PtrOffset(cur, 1)
					continue
				case KFalse:
					a[i] = false
					cur = PtrOffset(cur, 1)
					continue
				case KStringCommon:
					a[i] = p.strCommon(int(vnode.typ>>PosBits), int(vnode.val), copyStr)
					cur = PtrOffset(cur, 1)
					continue
				case KStringEscaped:
					a[i] = p.strEscaped(int(vnode.typ>>PosBits), vnode.val)
					cur = PtrOffset(cur, 1)
					continue
				case KUint:
					a[i] = uintToFloat64Any(vnode.val)
					cur = PtrOffset(cur, 1)
					continue
				case KSint:
					a[i] = float64(int64(vnode.val))
					cur = PtrOffset(cur, 1)
					continue
				case KReal:
					a[i] = math.Float64frombits(vnode.val)
					cur = PtrOffset(cur, 1)
					continue
				case KArray:
					if (uint64(vnode.val) & ConLenMask) == 0 {
						a[i] = emptyEfaceSliceAny
						cur = PtrOffset(cur, 1)
						continue
					}
				}
			}
			*node = Node{ptr: cur}
			a[i], err = node.AsEfaceFallback(ctx)
			cur = node.ptr
			if gerr == nil && err != nil {
				gerr = err
			}
		}
		*node = Node{ptr: cur}
		return a, gerr
	case KStringCommon:
		str, _ := node.AsStr(ctx)
		*node = NewNode(PtrOffset(node.ptr, 1))
		return str, nil
	case KStringEscaped:
		str := node.StringCopyEsc(ctx)
		*node = NewNode(PtrOffset(node.ptr, 1))
		return str, nil
	case KTrue:
		*node = NewNode(PtrOffset(node.ptr, 1))
		return true, nil
	case KFalse:
		*node = NewNode(PtrOffset(node.ptr, 1))
		return false, nil
	case KNull:
		*node = NewNode(PtrOffset(node.ptr, 1))
		return nil, nil
	case KUint:
		if !numSpecial {
			v := node.ptr.val
			*node = NewNode(PtrOffset(node.ptr, 1))
			return uintToFloat64Any(v), nil
		}
		if opts&(1<<_F_use_int64) != 0 && node.ptr.val <= math.MaxInt64 {
			v := node.ptr.val
			*node = NewNode(PtrOffset(node.ptr, 1))
			return uintToInt64Any(v), nil
		}
		fallthrough
	default:
		// use float64
		if ctx.Parser.options&(1<<_F_use_number) != 0 {
			num, ok := node.AsNumber(ctx)
			if !ok {
				// skip the unmatched type
				*node = NewNode(node.Next())
				return nil, newUnmatched(node.Position(), jsonNumberType)
			} else {
				*node = NewNode(PtrOffset(node.ptr, 1))
				return num, nil
			}
		} else if ctx.Parser.options&(1<<_F_use_int64) != 0 {
			// first try int64
			i, ok := node.AsI64(ctx)
			if ok {
				*node = NewNode(PtrOffset(node.ptr, 1))
				return i, nil
			}

			// is not integer, then use float64
			f, ok := node.AsF64(ctx)
			if ok {
				*node = NewNode(PtrOffset(node.ptr, 1))
				return f, nil
			}

			// skip the unmatched type
			*node = NewNode(node.Next())
			return nil, newUnmatched(node.Position(), int64Type)
		} else {
			num, ok := node.AsF64(ctx)
			if !ok {
				// skip the unmatched type
				*node = NewNode(node.Next())
				return nil, newUnmatched(node.Position(), float64Type)
			} else {
				*node = NewNode(PtrOffset(node.ptr, 1))
				return num, nil
			}
		}
	}
}

//go:nosplit
func PtrOffset(ptr *node, off int64) *node {
	return (*node)(unsafe.Add(unsafe.Pointer(ptr), uintptr(off)*unsafe.Sizeof(node{})))
}
