//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/resolver"
)

type decFunc interface {
	FromDom(vp unsafe.Pointer, node Node, ctx *context) error
}

type ptrDecoder struct {
	typ   reflect.Type
	deref decFunc
}

// Pointer Value is allocated in the Caller
func (d *ptrDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		*(*unsafe.Pointer)(vp) = nil
		return nil
	}

	if *(*unsafe.Pointer)(vp) == nil {
		if sd, ok := d.deref.(*structDecoder); ok && sd.goType == d.typ {
			*(*unsafe.Pointer)(vp) = sd.allocPtr(ctx.Parser)
		} else {
			*(*unsafe.Pointer)(vp) = reflect.New(d.typ).UnsafePointer()
		}
	}

	return d.deref.FromDom(*(*unsafe.Pointer)(vp), node, ctx)
}

type embeddedFieldPtrDecoder struct {
	field     resolver.FieldMeta
	fieldDec  decFunc
	fieldName string
}

// Pointer Value is allocated in the Caller
func (d *embeddedFieldPtrDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	// seek into the pointer
	vp = unsafe.Add(vp, -int(d.field.Path[0].Size))
	for _, f := range d.field.Path {
		vp = unsafe.Add(vp, f.Size)
		if f.Kind == resolver.F_deref {
			if *(*unsafe.Pointer)(vp) == nil {
				*(*unsafe.Pointer)(vp) = reflect.New(f.Type).UnsafePointer()
			}
			vp = *(*unsafe.Pointer)(vp)
		}
	}
	return d.fieldDec.FromDom(vp, node, ctx)
}

type i8Decoder struct {
	typ reflect.Type
}

func (d *i8Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsI64(ctx)
	if !ok || ret > math.MaxInt8 || ret < math.MinInt8 {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*int8)(vp) = int8(ret)
	return nil
}

type i16Decoder struct {
	typ reflect.Type
}

func (d *i16Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsI64(ctx)
	if !ok || ret > math.MaxInt16 || ret < math.MinInt16 {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*int16)(vp) = int16(ret)
	return nil
}

type i32Decoder struct {
	typ reflect.Type
}

func (d *i32Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsI64(ctx)
	if !ok || ret > math.MaxInt32 || ret < math.MinInt32 {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*int32)(vp) = int32(ret)
	return nil
}

type i64Decoder struct {
	typ reflect.Type
}

func (d *i64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsI64(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*int64)(vp) = int64(ret)
	return nil
}

type u8Decoder struct {
	typ reflect.Type
}

func (d *u8Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsU64(ctx)
	if !ok || ret > math.MaxUint8 {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*uint8)(vp) = uint8(ret)
	return nil
}

type u16Decoder struct {
	typ reflect.Type
}

func (d *u16Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsU64(ctx)
	if !ok || ret > math.MaxUint16 {
		return error_mismatch(node, ctx, d.typ)
	}
	*(*uint16)(vp) = uint16(ret)
	return nil
}

type u32Decoder struct {
	typ reflect.Type
}

func (d *u32Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsU64(ctx)
	if !ok || ret > math.MaxUint32 {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*uint32)(vp) = uint32(ret)
	return nil
}

type u64Decoder struct {
	typ reflect.Type
}

func (d *u64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsU64(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*uint64)(vp) = uint64(ret)
	return nil
}

type f32Decoder struct {
	typ reflect.Type
}

func (d *f32Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsF32(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*float32)(vp) = ret
	return nil
}

type f64Decoder struct {
	typ reflect.Type
}

func (d *f64Decoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsF64(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*float64)(vp) = float64(ret)
	return nil
}

type boolDecoder struct {
	typ reflect.Type
}

func (d *boolDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsBool()
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}

	*(*bool)(vp) = bool(ret)
	return nil
}

type stringDecoder struct {
	typ reflect.Type
}

func (d *stringDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	ret, ok := node.AsStr(ctx)
	if !ok {
		return error_mismatch(node, ctx, d.typ)
	}
	*(*string)(vp) = ret
	return nil
}

type numberDecoder struct {
}

func (d *numberDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}

	num, ok := node.AsNumber(ctx)
	if !ok {
		return error_mismatch(node, ctx, jsonNumberType)
	}
	*(*json.Number)(vp) = num
	return nil
}

type recuriveDecoder struct {
	typ reflect.Type
	dec unsafe.Pointer
}

func (d *recuriveDecoder) bind(dec decFunc) {
	box := new(decFunc)
	*box = dec
	atomic.StorePointer(&d.dec, unsafe.Pointer(box))
}

func (d *recuriveDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if p := atomic.LoadPointer(&d.dec); p != nil {
		return (*(*decFunc)(p)).FromDom(vp, node, ctx)
	}
	dec, err := findOrCompile(d.typ)
	if err != nil {
		return err
	}
	d.bind(dec)
	return dec.FromDom(vp, node, ctx)
}

type unsupportedTypeDecoder struct {
	typ reflect.Type
}

func (d *unsupportedTypeDecoder) FromDom(vp unsafe.Pointer, node Node, ctx *context) error {
	if node.IsNull() {
		return nil
	}
	return error_unsuppoted(d.typ)
}

// float32Overflows reports whether a value parsed as float64 is out of range
// for a float32 target. It mirrors encoding/json, which parses with
// strconv.ParseFloat(s, 32): values that round to +/-MaxFloat32 (for example
// "3.4028235e+38", the shortest form of MaxFloat32) are accepted, and only
// values that round to +/-Inf are rejected.
func float32Overflows(v float64) bool {
	return math.IsInf(float64(float32(v)), 0)
}

// needsExactFloat32 reports whether rounding math.Float64frombits(bits) to
// float32 could suffer decimal->float64->float32 double rounding:
//  1. bits' low 29 mantissa bits lie within +/-2 float64 ULPs of the exact
//     halfway point 0x10000000 between adjacent float32 values (including the
//     MaxFloat32/+Inf threshold), or
//  2. 0 < |v| < 2^-124 (the float32 subnormal and normal/subnormal boundary
//     region where the effective rounding bit shifts left).
func needsExactFloat32(bits uint64) bool {
	if (uint32(bits)&0x1fffffff)-0x0ffffffe <= 4 {
		return true
	}
	absBits := bits & 0x7fffffffffffffff
	return absBits != 0 && (absBits>>52) <= 1023-125
}

//go:noinline
func parseExactFloat32Slice(raw string) (float32, bool) {
	f, err := strconv.ParseFloat(raw, 32)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return float32(f), true
}

//go:noinline
func parseExactFloat32Token(jsonStr string, pos int) (float32, bool) {
	end, ok := SkipNumberFast(jsonStr, pos)
	if !ok {
		return 0, false
	}
	return parseExactFloat32Slice(jsonStr[pos:end])
}

func realBitsToFloat32(bits uint64, jsonStr string, pos int) (float32, bool) {
	if needsExactFloat32(bits) {
		return parseExactFloat32Token(jsonStr, pos)
	}
	f32 := float32(math.Float64frombits(bits))
	if math.IsInf(float64(f32), 0) {
		return 0, false
	}
	return f32, true
}
