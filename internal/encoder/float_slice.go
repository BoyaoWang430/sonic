package encoder

import (
	"reflect"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

func (self *Compiler) compileFloatSlice(p *ir.Program, sp int, st reflect.Type) bool {
	if !self.useVM {
		return false
	}
	elem := st.Elem()
	var op ir.Op
	switch elem.Kind() {
	case reflect.Float32:
		op = ir.OP_f32_slice
	case reflect.Float64:
		op = ir.OP_f64_slice
	default:
		return false
	}
	// Slice elements are addressable (pv=true), so check both value and pointer-receiver custom marshalers (including JSONAppender).
	if self.isCustomMarshaler(elem, true) {
		return false
	}
	p.Tag(sp)
	if st == vars.Float32SliceType || st == vars.Float64SliceType {
		p.Add(op)
	} else {
		p.Rtt(op, st)
	}
	return true
}
