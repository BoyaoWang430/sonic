//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestFlatDuplicateContainerKeysMatchStd covers repeated keys on container
// fields. encoding/json decodes a repeated array into the result of the
// previous occurrence, so null elements keep earlier values; the flat
// staging path must not replace the earlier value wholesale.
func TestFlatDuplicateContainerKeysMatchStd(t *testing.T) {
	type author struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	type target struct {
		I       []int     `json:"i"`
		Arr     [2]int32  `json:"arr"`
		Authors []author  `json:"authors"`
		S       []string  `json:"s"`
		F       []float64 `json:"f"`
	}
	inputs := []string{
		`{"i":[1,2],"i":[null]}`,
		`{"arr":[5,6],"arr":[null]}`,
		`{"authors":[{"name":"a"}],"authors":[{"age":1}]}`,
		`{"s":["x","y"],"s":[null,"z"]}`,
		`{"f":[1.5],"f":[]}`,
		`{"i":[1,2],"i":null,"i":[null]}`,
		`{"arr":[5,6],"arr":null,"arr":[null]}`,
		`{"authors":[{"name":"a"}],"authors":null,"authors":[{"age":1}]}`,
		`{"i":null,"i":[3]}`,
	}
	for _, in := range inputs {
		var want, got target
		if err := json.Unmarshal([]byte(in), &want); err != nil {
			t.Fatalf("encoding/json rejected %s: %v", in, err)
		}
		// Decode twice: the second run exercises any adaptive state.
		for i := 0; i < 2; i++ {
			got = target{}
			if _, _, err := DecodeOne(in, 0, &got); err != nil {
				t.Fatalf("DecodeOne(%s): %v", in, err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("DecodeOne(%s) = %+v, encoding/json = %+v", in, got, want)
			}
		}
	}
}
