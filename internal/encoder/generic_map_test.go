package encoder

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic/internal/encoder/ir"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

type plainGenericMap map[string]any

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
			{map[string]int{}, false},
			{plainGenericMap{}, false},
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
