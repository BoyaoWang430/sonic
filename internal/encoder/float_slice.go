package encoder

import (
	"reflect"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

func compileFloatSlice(p *ir.Program, sp int, vt reflect.Type) bool {
	if !vars.UseVM {
		return false
	}
	var op ir.Op
	switch vt.Kind() {
	case reflect.Float32:
		op = ir.OP_f32_slice
	case reflect.Float64:
		op = ir.OP_f64_slice
	default:
		return false
	}
	// Slice elements may use pointer-receiver marshalers.
	pt := reflect.PtrTo(vt)
	if vt.Implements(vars.JsonMarshalerType) || pt.Implements(vars.JsonMarshalerType) ||
		vt.Implements(vars.EncodingTextMarshalerType) || pt.Implements(vars.EncodingTextMarshalerType) {
		return false
	}
	p.Tag(sp)
	// Preserve slice depth checks and state restoration.
	p.Add(ir.OP_save)
	p.Add(op)
	p.Add(ir.OP_drop)
	return true
}
