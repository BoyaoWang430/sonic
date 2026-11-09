package encoder

import (
	"encoding/json"
	"testing"
)

func TestEncodeDirectValuePointer(t *testing.T) {
	type pointerStruct struct{ Value *int }
	n := 42
	for _, value := range []interface{}{
		pointerStruct{}, pointerStruct{&n},
		[1]*int{}, [1]*int{&n},
	} {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Encode(value, 0)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%#v: got %s, want %s", value, got, want)
		}
	}
}
