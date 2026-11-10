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
	"encoding/json"
	"reflect"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/option"
	"github.com/stretchr/testify/assert"
)

func TestCompiler_Compile(t *testing.T) {
	p, err := NewCompiler().Compile(reflect.TypeOf(_BindingValue), false)
	assert.Nil(t, err)
	p.Disassemble()
}

func TestReflectDirect(t *testing.T) {
	type A struct {
		A int
		B int
	}
	var a A
	var b = &a
	println("b:", unsafe.Pointer(b))
	vv := reflect.ValueOf(a)
	println("v:", vv.Kind().String())
	pvv := reflect.ValueOf(&a)
	println("pv:", pvv.Kind().String(), pvv.UnsafePointer())
}

func TestPretouchTypeVM(t *testing.T) {
	type subA struct{}
	type subB struct{}
	type subC struct{}
	type data struct {
		SubA subA
		SubB subB
		SubC subC
	}

	sub, err := pretouchTypeVM(
		reflect.TypeOf(data{}),
		option.CompileOptions{
			MaxInlineDepth: 1,
			RecursiveDepth: 1000,
		},
		0,
	)
	assert.NoError(t, err)
	assert.Contains(t, sub, reflect.TypeOf(subA{}))
	assert.Contains(t, sub, reflect.TypeOf(subB{}))
	assert.Contains(t, sub, reflect.TypeOf(subC{}))
}

func TestStructFieldsCompilation(t *testing.T) {
	previous := vars.UseVM
	defer func() { vars.UseVM = previous }()

	type embedded struct {
		E string `json:"e"`
	}
	type sample struct {
		A int    `json:"a"`
		B string `json:"b,omitempty"`
		embedded
		P *int   `json:"p,omitempty"`
		C bool   `json:"c"`
		D uint64 `json:"d,omitempty"`
	}

	for _, useVM := range []bool{true, false} {
		vars.UseVM = useVM
		p, err := NewCompiler().Compile(reflect.TypeOf(sample{}), false)
		assert.NoError(t, err)
		var runs []int
		for _, ins := range p {
			if ins.Op() == ir.OP_struct_fields {
				runs = append(runs, len(ins.VFields()))
			}
		}
		if useVM {
			assert.Equal(t, []int{6}, runs)
		} else {
			assert.Empty(t, runs)
		}
	}
}

func TestStructFieldsPtrScalarSemantics(t *testing.T) {
	type ptrSample struct {
		A *int     `json:"a"`
		B *int     `json:"b,omitempty"`
		C *string  `json:"c"`
		D *string  `json:"d,omitempty"`
		E *bool    `json:"e"`
		F *bool    `json:"f,omitempty"`
		G *float64 `json:"g,omitempty"`
		H *int     `json:"h,string,omitempty"`
	}

	zeroInt := 0
	emptyStr := ""
	falseBool := false
	zeroFloat := 0.0
	seven := 7

	cases := []ptrSample{
		{},
		{A: &zeroInt, B: &zeroInt, C: &emptyStr, D: &emptyStr, E: &falseBool, F: &falseBool, G: &zeroFloat, H: &seven},
	}

	for _, tc := range cases {
		want, err := json.Marshal(tc)
		assert.NoError(t, err)

		gotDefault, err := Encode(tc, 0)
		assert.NoError(t, err)
		assert.Equal(t, string(want), string(gotDefault))

		gotOmitNull, err := Encode(tc, SortMapKeys)
		assert.NoError(t, err)
		assert.Equal(t, string(want), string(gotOmitNull))
	}
}

func BenchmarkEncodePtrScalarStruct(b *testing.B) {
	type dto struct {
		ID      *int64   `json:"id"`
		Name    *string  `json:"name,omitempty"`
		Active  *bool    `json:"active,omitempty"`
		Score   *float64 `json:"score,omitempty"`
		Count   *int32   `json:"count"`
		Tag     *string  `json:"tag,omitempty"`
		Deleted *bool    `json:"deleted"`
		Extra   *uint64  `json:"extra,omitempty"`
	}
	id := int64(1001)
	name := "sonic_dto"
	active := true
	score := 98.5
	count := int32(0)
	extra := uint64(42)
	val := dto{
		ID:     &id,
		Name:   &name,
		Active: &active,
		Score:  &score,
		Count:  &count,
		Extra:  &extra,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Encode(val, 0)
		if err != nil {
			b.Fatal(err)
		}
	}
}
