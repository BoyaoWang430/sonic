package encoder

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

type plainFloat float64
type jsonFloat float64

func (jsonFloat) MarshalJSON() ([]byte, error) { return []byte(`"json"`), nil }

type pointerJSONFloat float64

func (*pointerJSONFloat) MarshalJSON() ([]byte, error) { return []byte(`"pointer-json"`), nil }

type textFloat float64

func (textFloat) MarshalText() ([]byte, error) { return []byte("text"), nil }

type pointerTextFloat float64

func (*pointerTextFloat) MarshalText() ([]byte, error) { return []byte("pointer-text"), nil }

type jsonFloatSlice []float64

func (jsonFloatSlice) MarshalJSON() ([]byte, error) { return []byte(`"slice"`), nil }

func TestFloatSliceCompilation(t *testing.T) {
	previous := vars.UseVM
	defer func() { vars.UseVM = previous }()
	cases := []struct {
		value any
		bulk  bool
	}{
		{[]float64{}, true}, {[]float32{}, true}, {[]plainFloat{}, true},
		{[]jsonFloat{}, false}, {[]pointerJSONFloat{}, false},
		{[]textFloat{}, false}, {[]pointerTextFloat{}, false},
		{jsonFloatSlice{}, false}, {[]*float64{}, false}, {[2]float64{}, false},
	}
	for _, backend := range []struct {
		name string
		vm   bool
	}{{"VM", true}, {"JIT", false}} {
		t.Run(backend.name, func(t *testing.T) {
			vars.UseVM = backend.vm
			for _, tc := range cases {
				typ := reflect.TypeOf(tc.value)
				p, err := NewCompiler().Compile(typ, false)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, ins := range p {
					found = found || ins.Op() == ir.OP_f32_slice || ins.Op() == ir.OP_f64_slice
				}
				if want := backend.vm && tc.bulk; found != want {
					t.Fatalf("%v: bulk=%v, want %v", typ, found, want)
				}
			}
		})
	}
}
