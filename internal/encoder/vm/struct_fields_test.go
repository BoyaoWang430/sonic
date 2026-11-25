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

package vm_test

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/bytedance/sonic/internal/encoder"
	"github.com/stretchr/testify/require"
)

type denseLeafStruct struct {
	S    string      `json:"s"`
	Q    string      `json:"q,string"`
	B    bool        `json:"b"`
	I64  int64       `json:"i64"`
	I32  int32       `json:"i32"`
	I16  int16       `json:"i16"`
	I8   int8        `json:"i8"`
	U64  uint64      `json:"u64"`
	U32  uint32      `json:"u32"`
	U16  uint16      `json:"u16"`
	U8   uint8       `json:"u8"`
	F64  float64     `json:"f64"`
	F32  float32     `json:"f32"`
	Num  json.Number `json:"num"`
	Bin  []byte      `json:"bin"`
	Any  any         `json:"any"`
	SI64 []int64     `json:"si64"`
	SI32 []int32     `json:"si32"`
	SU64 []uint64    `json:"su64"`
	SU32 []uint32    `json:"su32"`
	SStr []string    `json:"sstr"`
	SB   []bool      `json:"sb"`
	SF64 []float64   `json:"sf64"`
	SF32 []float32   `json:"sf32"`
	SAny []any       `json:"sany"`
}

type condLeafStruct struct {
	PS   *string     `json:"ps,omitempty"`
	S    string      `json:"s,omitempty"`
	Q    string      `json:"q,string,omitempty"`
	B    bool        `json:"b,omitempty"`
	I64  int64       `json:"i64,omitempty"`
	I32  int32       `json:"i32,omitempty"`
	I16  int16       `json:"i16,omitempty"`
	I8   int8        `json:"i8,omitempty"`
	U64  uint64      `json:"u64,omitempty"`
	U32  uint32      `json:"u32,omitempty"`
	U16  uint16      `json:"u16,omitempty"`
	U8   uint8       `json:"u8,omitempty"`
	F64  float64     `json:"f64,omitempty"`
	F32  float32     `json:"f32,omitempty"`
	Num  json.Number `json:"num,omitempty"`
	Bin  []byte      `json:"bin,omitempty"`
	Any  any         `json:"any,omitempty"`
	SI64 []int64     `json:"si64,omitempty"`
	SI32 []int32     `json:"si32,omitempty"`
	SU64 []uint64    `json:"su64,omitempty"`
	SU32 []uint32    `json:"su32,omitempty"`
	SStr []string    `json:"sstr,omitempty"`
	SB   []bool      `json:"sb,omitempty"`
	SF64 []float64   `json:"sf64,omitempty"`
	SF32 []float32   `json:"sf32,omitempty"`
	SAny []any       `json:"sany,omitempty"`
}

type compositeStructEnvelope struct {
	DenseInline   denseLeafStruct    `json:"dense_inline"`
	CondInline    condLeafStruct     `json:"cond_inline"`
	DensePtr      *denseLeafStruct   `json:"dense_ptr"`
	CondPtr       *condLeafStruct    `json:"cond_ptr,omitempty"`
	DenseSlice    []denseLeafStruct  `json:"dense_slice"`
	CondSlice     []condLeafStruct   `json:"cond_slice,omitempty"`
	DensePtrSlice []*denseLeafStruct `json:"dense_ptr_slice"`
	CondPtrSlice  []*condLeafStruct  `json:"cond_ptr_slice,omitempty"`
}

func stdMarshalMode(v any, escapeHTML bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}

func assertRootNestedSliceParity(t *testing.T, denseSample denseLeafStruct, condSample condLeafStruct, checkStdJSON bool) {
	t.Helper()
	env := compositeStructEnvelope{
		DenseInline:   denseSample,
		CondInline:    condSample,
		DensePtr:      &denseSample,
		CondPtr:       &condSample,
		DenseSlice:    []denseLeafStruct{denseSample, {}},
		CondSlice:     []condLeafStruct{condSample, {}},
		DensePtrSlice: []*denseLeafStruct{&denseSample, nil, {}},
		CondPtrSlice:  []*condLeafStruct{&condSample, nil, {}},
	}

	targets := []any{
		denseSample,
		condSample,
		&denseSample,
		&condSample,
		[]denseLeafStruct{denseSample, {}},
		[]condLeafStruct{condSample, {}},
		[]*denseLeafStruct{&denseSample, nil},
		[]*condLeafStruct{&condSample, nil},
		env,
	}

	if checkStdJSON {
		opts := encoder.EscapeHTML | encoder.ValidateString
		for _, target := range targets {
			got, err := encoder.Encode(target, opts)
			want, werr := stdMarshalMode(target, true)
			require.Equal(t, werr != nil, err != nil)
			if werr == nil {
				require.Equal(t, string(want), string(got))
			}
		}
	}

	// Across all four sanitize configurations ( exercising encodeStructFieldsDense,
	// encodeStructFieldsSanitizedDense, encodeStructFieldsDefault, and
	// encodeStructFieldsSanitized ), Root, InlineNested, *Struct, []Struct, and
	// []*Struct must emit identical object payloads from the single opcode template.
	for _, opts := range []encoder.Options{
		0,
		encoder.EscapeHTML,
		encoder.ValidateString,
		encoder.EscapeHTML | encoder.ValidateString,
	} {
		rootDense, err := encoder.Encode(denseSample, opts)
		require.NoError(t, err)
		rootCond, err := encoder.Encode(condSample, opts)
		require.NoError(t, err)
		envOut, err := encoder.Encode(env, opts)
		require.NoError(t, err)
		require.Contains(t, string(envOut), `"dense_inline":`+string(rootDense))
		require.Contains(t, string(envOut), `"dense_ptr":`+string(rootDense))
		require.Contains(t, string(envOut), `"dense_slice":[`+string(rootDense)+`,`)
		require.Contains(t, string(envOut), `"dense_ptr_slice":[`+string(rootDense)+`,null,`)
		require.Contains(t, string(envOut), `"cond_inline":`+string(rootCond))
		require.Contains(t, string(envOut), `"cond_ptr":`+string(rootCond))
		require.Contains(t, string(envOut), `"cond_slice":[`+string(rootCond)+`,`)
		require.Contains(t, string(envOut), `"cond_ptr_slice":[`+string(rootCond)+`,null,`)
	}
}

func TestStructFieldsUnifiedSemantics(t *testing.T) {
	richValid := "a<b>&c\u2028\u2029\"\\\n\t"
	densePopulated := denseLeafStruct{
		S:    richValid,
		Q:    "quoted\"\\\n\t123",
		B:    true,
		I64:  -9876543210,
		I32:  -123456,
		I16:  -3200,
		I8:   -42,
		U64:  9876543210,
		U32:  123456,
		U16:  3200,
		U8:   42,
		F64:  3.1415926535,
		F32:  2.5,
		Num:  "123.45e6",
		Bin:  []byte{1, 2, 3, 4},
		Any:  map[string]string{"k": richValid},
		SI64: []int64{-1, 0, 1},
		SI32: []int32{-2, 2},
		SU64: []uint64{0, 99},
		SU32: []uint32{1, 88},
		SStr: []string{"plain", richValid},
		SB:   []bool{true, false, true},
		SF64: []float64{-0.5, 1.25},
		SF32: []float32{0.25, -1.5},
		SAny: []any{"x", 7, nil, richValid},
	}

	condPopulated := condLeafStruct{
		PS:   &richValid,
		S:    richValid,
		Q:    "quoted\"\\\n\t456",
		B:    true,
		I64:  -99,
		I32:  -88,
		I16:  -77,
		I8:   -66,
		U64:  99,
		U32:  88,
		U16:  77,
		U8:   66,
		F64:  1.5,
		F32:  2.5,
		Num:  "42",
		Bin:  []byte("xyz"),
		Any:  "hello<world>",
		SI64: []int64{10},
		SI32: []int32{20},
		SU64: []uint64{30},
		SU32: []uint32{40},
		SStr: []string{richValid},
		SB:   []bool{false},
		SF64: []float64{1.0},
		SF32: []float32{2.0},
		SAny: []any{true},
	}

	assertRootNestedSliceParity(t, densePopulated, condPopulated, true)

	// HTML/JS-separator and invalid UTF-8 bytes inside OP_quote and OP_str emit
	// identical payloads across Root, InlineNested, *Struct, []Struct, []*Struct,
	// and general VM (**Struct).
	for _, tricky := range []string{richValid, "bad\xff<a>&"} {
		denseTricky := densePopulated
		denseTricky.S = tricky
		denseTricky.Q = tricky
		denseTricky.SStr = []string{tricky}
		condTricky := condPopulated
		condTricky.PS = &tricky
		condTricky.S = tricky
		condTricky.Q = tricky
		condTricky.SStr = []string{tricky}
		assertRootNestedSliceParity(t, denseTricky, condTricky, false)

		pDense := &denseTricky
		ppDense := &pDense
		gotRoot, err := encoder.Encode(denseTricky, encoder.EscapeHTML|encoder.ValidateString)
		require.NoError(t, err)
		gotVM, err := encoder.Encode(ppDense, encoder.EscapeHTML|encoder.ValidateString)
		require.NoError(t, err)
		require.Equal(t, string(gotRoot), string(gotVM))

		sliceVal := []denseLeafStruct{denseTricky}
		gotPtrSlice, err := encoder.Encode(&sliceVal, encoder.EscapeHTML|encoder.ValidateString)
		require.NoError(t, err)
		require.Equal(t, "["+string(gotRoot)+"]", string(gotPtrSlice))

		arrVal := [1]denseLeafStruct{denseTricky}
		gotPtrArr, err := encoder.Encode(&arrVal, encoder.EscapeHTML|encoder.ValidateString)
		require.NoError(t, err)
		require.Equal(t, "["+string(gotRoot)+"]", string(gotPtrArr))

		gotSlicePP, err := encoder.Encode([]**denseLeafStruct{ppDense, nil}, encoder.EscapeHTML|encoder.ValidateString)
		require.NoError(t, err)
		require.Equal(t, "["+string(gotRoot)+",null]", string(gotSlicePP))
	}

	// Signed negative zero (-0.0) must be treated as zero by omitempty, while
	// non-nil empty slices in dense structs emit [] and nil slices emit null.
	negZero64 := math.Copysign(0, -1)
	negZero32 := float32(math.Copysign(0, -1))
	denseEdges := denseLeafStruct{
		F64:  negZero64,
		F32:  negZero32,
		SI64: []int64{},
		SStr: []string{},
		SB:   []bool{},
	}
	condEdges := condLeafStruct{
		F64:  negZero64,
		F32:  negZero32,
		SI64: []int64{},
		SStr: nil,
		Bin:  []byte{},
	}
	assertRootNestedSliceParity(t, denseEdges, condEdges, false)
	gotCondEdges, err := encoder.Encode(condEdges, encoder.EscapeHTML|encoder.ValidateString)
	require.NoError(t, err)
	wantCondEdges, err := stdMarshalMode(condEdges, true)
	require.NoError(t, err)
	require.Equal(t, string(wantCondEdges), string(gotCondEdges))
}
