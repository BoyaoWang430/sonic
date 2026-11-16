package encoder

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

type plainGenericMap map[string]any
type namedIntGenericMap map[string]int
type marshalerGenericMap map[string]any

func (marshalerGenericMap) MarshalJSON() ([]byte, error) {
	return []byte(`"custom"`), nil
}

type customKey string

func (customKey) MarshalText() ([]byte, error) {
	return []byte("ck"), nil
}

func TestGenericMapCompilation(t *testing.T) {
	previous := vars.UseVM
	defer func() { vars.UseVM = previous }()
	for _, useVM := range []bool{false, true} {
		vars.UseVM = useVM
		for _, tc := range []struct {
			value any
			fast  bool
		}{
			{map[string]any{}, true},
			{map[string]string{}, true},
			{map[string]int{}, true},
			{map[string]int64{}, true},
			{map[string]uint64{}, true},
			{map[string]float64{}, true},
			{map[string]bool{}, true},
			{map[int]string{}, true},
			{map[int64]string{}, true},
			{plainGenericMap{}, true},
			{namedIntGenericMap{}, true},
			{marshalerGenericMap{}, false},
			{map[customKey]any{}, false},
			{map[string]struct{}{}, false},
		} {
			typ := reflect.TypeOf(tc.value)
			p, err := NewCompiler().Compile(typ, false)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, ins := range p {
				found = found || ins.Op() == ir.OP_map_string_any
			}
			if want := useVM && tc.fast; found != want {
				t.Fatalf("%v, VM=%v: fast=%v, want %v", typ, useVM, found, want)
			}
		}
	}
}

func TestNamedGenericMapEncodeParity(t *testing.T) {
	gotPlain, err := Encode(plainGenericMap{"b": 2, "a": "ok"}, SortMapKeys)
	if err != nil || string(gotPlain) != `{"a":"ok","b":2}` {
		t.Fatalf("plainGenericMap got (%q, %v)", string(gotPlain), err)
	}
	gotNil, err := Encode(plainGenericMap(nil), 0)
	if err != nil || string(gotNil) != `null` {
		t.Fatalf("nil plainGenericMap got (%q, %v)", string(gotNil), err)
	}
	gotNoNull, err := Encode(plainGenericMap(nil), NoNullSliceOrMap)
	if err != nil || string(gotNoNull) != `{}` {
		t.Fatalf("NoNull plainGenericMap got (%q, %v)", string(gotNoNull), err)
	}
	gotInt, err := Encode(namedIntGenericMap{"x": 7, "y": -3}, SortMapKeys)
	if err != nil || string(gotInt) != `{"x":7,"y":-3}` {
		t.Fatalf("namedIntGenericMap got (%q, %v)", string(gotInt), err)
	}
	gotCustom, err := Encode(marshalerGenericMap{"a": 1}, 0)
	if err != nil || string(gotCustom) != `"custom"` {
		t.Fatalf("marshalerGenericMap got (%q, %v)", string(gotCustom), err)
	}
}
