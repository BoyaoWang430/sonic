package vm_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"runtime"
	"testing"

	"github.com/bytedance/sonic/internal/encoder"
)

type plainFloat64 float64
type plainFloat32 float32
type plainFloatSlice []plainFloat64

type jsonFloat64 float64

func (jsonFloat64) MarshalJSON() ([]byte, error) { runtime.GC(); return []byte(`"json-value"`), nil }

type jsonPointerFloat64 float64

func (*jsonPointerFloat64) MarshalJSON() ([]byte, error) {
	runtime.GC()
	return []byte(`"json-pointer"`), nil
}

type textFloat64 float64

func (textFloat64) MarshalText() ([]byte, error) { runtime.GC(); return []byte("<text-value>"), nil }

type textPointerFloat64 float64

func (*textPointerFloat64) MarshalText() ([]byte, error) {
	runtime.GC()
	return []byte("<text-pointer>"), nil
}

type customFloatSlice []float64

func (customFloatSlice) MarshalJSON() ([]byte, error) {
	runtime.GC()
	return []byte(`"custom-slice"`), nil
}

type customPointerFloatSlice []float64

func (*customPointerFloatSlice) MarshalJSON() ([]byte, error) {
	runtime.GC()
	return []byte(`"custom-pointer-slice"`), nil
}

type customTextFloatSlice []float32

func (customTextFloatSlice) MarshalText() ([]byte, error) {
	runtime.GC()
	return []byte("<custom-text-slice>"), nil
}

type failingFloat float64

func (failingFloat) MarshalJSON() ([]byte, error) { return nil, errors.New("custom-float-error") }

func TestFloatSliceMethodsAndContainers(t *testing.T) {
	v := 1.25
	pointerSlice := customPointerFloatSlice{1, 2}
	cases := []any{
		[]float64(nil), []float64{}, []float32(nil), []float32{},
		[]float64{1.25, -1e-7, 1e21}, []float32{1.25, -1e-7, 1e21},
		plainFloatSlice{1.25, 2.5}, []plainFloat32{1.25, 2.5},
		[]jsonFloat64{1, 2}, []jsonPointerFloat64{1, 2}, []textFloat64{1, 2}, []textPointerFloat64{1, 2},
		customFloatSlice{1, 2}, customFloatSlice(nil), pointerSlice, &pointerSlice, customTextFloatSlice{1, 2},
		[]any{[]float64{1, 2}, []float32(nil), []jsonPointerFloat64{1, 2}},
		[][]float64{{1, 2}, nil, {}, {3, 4}}, []*float64{&v, nil}, [2]float64{1, 2},
		struct {
			A []float64 `json:"a,string"`
			B []float32 `json:"b,omitempty"`
		}{A: []float64{1, 2}},
		&struct {
			A customPointerFloatSlice `json:"a"`
			B []float64               `json:"b"`
		}{pointerSlice, []float64{1, 2}},
		map[string]any{"a": []float32{1, 2}, "b": []float64{3, 4}},
	}
	for i, input := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := encoder.Encode(input, encoder.SortMapKeys|encoder.EscapeHTML|encoder.CompactMarshaler)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%T: got %s, want %s", input, got, want)
			}
		})
	}
	if _, err := encoder.Encode([]failingFloat{1, 2}, 0); err == nil {
		t.Fatal("element marshaler error was lost")
	}
	got, err := encoder.Encode([]float64{1, 2}, 0)
	if err != nil || string(got) != "[1,2]" {
		t.Fatalf("encode after error: %s, %v", got, err)
	}
}

func TestFloatSliceScalarParity(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	values64 := []float64{0, math.Copysign(0, -1), math.SmallestNonzeroFloat64, math.MaxFloat64, 1e-6, 1e21, math.Nextafter(1e-6, 0), math.Nextafter(1e21, 0)}
	values32 := []float32{0, float32(math.Copysign(0, -1)), math.SmallestNonzeroFloat32, math.MaxFloat32, 1e-6, 1e21}
	for i := 0; i < 1024; i++ {
		v := math.Float64frombits(rng.Uint64())
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			values64 = append(values64, v)
		}
		f := math.Float32frombits(rng.Uint32())
		if !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) {
			values32 = append(values32, f)
		}
	}
	for _, input := range []any{values64, values32} {
		want := []byte{'['}
		var values []any
		switch x := input.(type) {
		case []float64:
			for _, v := range x {
				values = append(values, v)
			}
		case []float32:
			for _, v := range x {
				values = append(values, v)
			}
		}
		for i, v := range values {
			if i != 0 {
				want = append(want, ',')
			}
			part, err := encoder.Encode(v, 0)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, part...)
		}
		want = append(want, ']')
		for _, opts := range []encoder.Options{0, encoder.EscapeHTML | encoder.ValidateString, encoder.EncodeNullForInfOrNan} {
			got, err := encoder.Encode(input, opts)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%T scalar parity: err=%v", input, err)
			}
			dst := make([]byte, 7, 9)
			copy(dst, "prefix:")
			if err := encoder.EncodeInto(&dst, input, opts); err != nil || string(dst) != "prefix:"+string(want) {
				t.Fatalf("%T append parity: err=%v", input, err)
			}
		}
	}
}

func TestFloatSliceOptions(t *testing.T) {
	for _, input := range []any{[]float64(nil), []float32(nil)} {
		got, err := encoder.Encode(input, encoder.NoNullSliceOrMap)
		if err != nil || string(got) != "[]" {
			t.Fatalf("nil slice: %s, %v", got, err)
		}
	}
	for _, input := range []any{[]float64{1, math.NaN(), math.Inf(1), math.Inf(-1), 2}, []float32{1, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), 2}} {
		for _, capacity := range []int{7, 128} {
			dst := make([]byte, 7, capacity)
			copy(dst, "prefix:")
			if err := encoder.EncodeInto(&dst, input, 0); err == nil || string(dst) != "prefix:" {
				t.Fatalf("failed encoder.EncodeInto changed prefix: %q, %v", dst, err)
			}
		}
		if out, err := encoder.Encode(input, 0); err == nil || out != nil {
			t.Fatalf("non-finite accepted: %s, %v", out, err)
		}
		got, err := encoder.Encode(input, encoder.EncodeNullForInfOrNan)
		if err != nil || string(got) != "[1,null,null,null,2]" {
			t.Fatalf("non-finite option: %s, %v", got, err)
		}
	}
}
