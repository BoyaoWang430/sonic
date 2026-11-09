//go:build amd64 && go1.16 && !go1.28
// +build amd64,go1.16,!go1.28

/*
 * Copyright 2021 ByteDance Inc.
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
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/encoder/x86"
	"github.com/bytedance/sonic/internal/jit"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/loader"
	"github.com/bytedance/sonic/option"
)

func ForceUseJit() {
	x86.SetCompiler(makeEncoderX86)
	pretouchType = pretouchTypeX86
	encodeValuePointer = x86.EncodeValuePointer
	vars.UseVM = false
}

func init() {
	if vars.UseVM {
		ForceUseVM()
	} else {
		ForceUseJit()
	}
}

var _KeepAlive struct {
	rb    *[]byte
	vp    unsafe.Pointer
	sb    *vars.Stack
	fv    uint64
	err   error
	frame [x86.FP_offs]byte
}

func makeEncoderX86(vt *rt.GoType, ex ...interface{}) (interface{}, error) {
	pp, err := NewCompiler().Compile(vt.Pack(), ex[0].(bool))
	if err != nil {
		return nil, err
	}
	as := x86.NewAssembler(pp)
	as.Name = vt.String()
	return as.Load(), nil
}

func pretouchTypeX86(_vt reflect.Type, opts option.CompileOptions, v uint8) (map[reflect.Type]uint8, error) {
	/* compile function */
	compiler := NewCompiler().apply(opts)
	encoder := func(vt *rt.GoType, ex ...interface{}) (interface{}, error) {
		pp, err := compiler.Compile(vt.Pack(), ex[0].(bool))
		if err != nil {
			return nil, err
		}
		as := x86.NewAssembler(pp)
		as.Name = vt.String()
		return as.Load(), nil
	}

	/* find or compile */
	vt := rt.UnpackType(_vt)
	pv := v == 1
	if val := vars.GetProgram(vt, pv); val != nil {
		return nil, nil
	} else if _, err := vars.ComputeProgram(vt, encoder, pv); err == nil {
		return compiler.rec, nil
	} else {
		return nil, err
	}
}

type x86PretouchKey struct {
	vt *rt.GoType
	pv bool
}

type x86PretouchProgram struct {
	vt   *rt.GoType
	pv   bool
	item loader.LoadOneItem
}

func pretouchRecX86(vtm map[reflect.Type]uint8, opts option.CompileOptions) error {
	pendings := make(map[x86PretouchKey]x86PretouchProgram)

	for opts.RecursiveDepth >= 0 && len(vtm) > 0 {
		next := make(map[reflect.Type]uint8)
		for vt, v := range vtm {
			gvt := rt.UnpackType(vt)
			pv := v == 1
			if vars.GetProgram(gvt, pv) != nil {
				continue
			}
			key := x86PretouchKey{vt: gvt, pv: pv}
			if _, ok := pendings[key]; ok {
				continue
			}

			compiler := NewCompiler().apply(opts)
			pp, err := compiler.Compile(vt, pv)
			if err != nil {
				return err
			}

			as := x86.NewAssembler(pp)
			as.Name = vt.String()
			text, pcdata := as.Export()
			funcName := "encode_" + as.Name
			if pv {
				funcName += "_pv"
			}

			pendings[key] = x86PretouchProgram{
				vt: gvt,
				pv: pv,
				item: loader.LoadOneItem{
					Text:      text,
					FuncName:  funcName,
					ArgSize:   x86.FP_args,
					ArgPtrs:   vars.ArgPtrs,
					LocalPtrs: vars.LocalPtrs,
					Pcdata:    pcdata,
				},
			}

			for svt, pv := range compiler.rec {
				next[svt] = pv
			}
		}

		opts.RecursiveDepth--
		vtm = next
	}

	if len(pendings) == 0 {
		return nil
	}

	entries := make([]x86PretouchProgram, 0, len(pendings))
	items := make([]loader.LoadOneItem, 0, len(pendings))
	for _, p := range pendings {
		entries = append(entries, p)
		items = append(items, p.item)
	}

	loaded := jit.LoadMany(items)
	var batchVal []caching.ProgramBatchItem
	var batchPV []caching.ProgramBatchItem
	for i, p := range entries {
		enc := x86.ToEncoder(loaded[i])
		if p.pv {
			batchPV = append(batchPV, caching.ProgramBatchItem{Vt: p.vt, Fn: enc})
		} else {
			batchVal = append(batchVal, caching.ProgramBatchItem{Vt: p.vt, Fn: enc})
		}
	}
	if len(batchVal) > 0 {
		vars.PutPrograms(batchVal, false)
	}
	if len(batchPV) > 0 {
		vars.PutPrograms(batchPV, true)
	}

	return nil
}
