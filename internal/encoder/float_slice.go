/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package encoder

import (
	"reflect"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

func (self *Compiler) compileFloatSlice(p *ir.Program, sp int, st reflect.Type) bool {
	if self.disableBatchSpecs {
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
