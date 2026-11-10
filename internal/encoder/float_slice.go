package encoder

import (
	"reflect"

	"github.com/bytedance/sonic/internal/encoder/ir"
)

func (self *Compiler) compileFloatSlice(p *ir.Program, sp int, vt reflect.Type) bool {
	if !self.useVM {
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
	// Slice elements are addressable (pv=true), so check both value and pointer-receiver custom marshalers (including JSONAppender).
	if self.isCustomMarshaler(vt, true) {
		return false
	}
	p.Tag(sp)
	// Preserve slice depth checks and state restoration.
	p.Add(ir.OP_save)
	p.Add(op)
	p.Add(ir.OP_drop)
	return true
}
