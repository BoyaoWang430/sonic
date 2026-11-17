package encoder

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic/internal/encoder/ir"
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
	cases := []struct {
		value any
		bulk  bool
	}{
		{[]float64{}, true}, {[]float32{}, true}, {[]plainFloat{}, true},
		{[]jsonFloat{}, false}, {[]pointerJSONFloat{}, false},
		{[]textFloat{}, false}, {[]pointerTextFloat{}, false},
		{jsonFloatSlice{}, false}, {[]*float64{}, false}, {[2]float64{}, false},
	}
	for _, mode := range []struct {
		name       string
		batchSpecs bool
	}{{"BatchSpecs", true}, {"GenericIR", false}} {
		t.Run(mode.name, func(t *testing.T) {
			for _, tc := range cases {
				typ := reflect.TypeOf(tc.value)
				p, err := NewCompiler().withBatchSpecs(mode.batchSpecs).Compile(typ, false)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, ins := range p {
					found = found || ins.Op() == ir.OP_f32_slice || ins.Op() == ir.OP_f64_slice
				}
				if want := mode.batchSpecs && tc.bulk; found != want {
					t.Fatalf("%v: bulk=%v, want %v", typ, found, want)
				}
			}
		})
	}
}
