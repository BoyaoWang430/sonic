//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package encoder

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
)

type gcMapKey struct {
	Name string
	Fail bool
}

func (k gcMapKey) MarshalText() ([]byte, error) {
	runtime.GC()
	if k.Fail {
		return nil, errors.New("map key failure")
	}
	return []byte(k.Name), nil
}

type gcMapValue struct {
	Padding [160]byte
	Pointer *string
}

func (v gcMapValue) MarshalJSON() ([]byte, error) {
	runtime.GC()
	return json.Marshal(v.Pointer)
}

func TestEncoderMapIteratorSemantics(t *testing.T) {
	text := "value<&>\n\"\\\u2028"
	value := gcMapValue{Pointer: &text}
	cases := []any{
		map[string]int(nil), map[string]int{},
		map[string]any{"": nil, "quoted\"": value, "control\n": []any{value, nil}, "<html>": map[string]any{"nested": value}},
		map[int64]any{-9223372036854775808: value, -10: nil, 2: value, 10: value, 9223372036854775807: value},
		map[uint64]any{0: value, 2: nil, 10: value, 18446744073709551615: value},
		map[gcMapKey]gcMapValue{{Name: "a\""}: value, {Name: "b\\"}: value, {Name: "<c>"}: value},
		map[*gcMapKey]*gcMapValue{nil: nil, {Name: "a"}: &value, {Name: "b"}: nil},
		map[encoding.TextMarshaler]any{gcMapKey{Name: "a"}: value, &gcMapKey{Name: "b"}: &value},
	}
	for i, input := range cases {
		for _, sorted := range []bool{false, true} {
			t.Run(fmt.Sprintf("case=%d/sort=%v", i, sorted), func(t *testing.T) {
				options := EscapeHTML | CompactMarshaler
				if sorted {
					options |= SortMapKeys
				}
				got, err := Encode(input, options)
				if err != nil {
					t.Fatal(err)
				}
				want, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				if sorted {
					if !bytes.Equal(got, want) {
						t.Fatalf("got %s, want %s", got, want)
					}
				} else {
					var a, b any
					if err := json.Unmarshal(got, &a); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(want, &b); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("got %s, want %s", got, want)
					}
				}
			})
		}
	}
}

func TestEncoderMapIteratorErrorReuse(t *testing.T) {
	for i := 0; i < 8; i++ {
		bad := map[gcMapKey]string{{Name: "good"}: "good", {Name: "bad", Fail: true}: "bad"}
		if _, err := Encode(bad, SortMapKeys); err == nil {
			t.Fatal("map key error was lost")
		}
		got, err := Encode(map[int]string{10: "ten", 2: "two"}, SortMapKeys)
		if err != nil || string(got) != `{"10":"ten","2":"two"}` {
			t.Fatalf("pooled iterator after error: %s, %v", got, err)
		}
	}
}

// Duplicate serialized keys must retain all associated values.
func TestEncoderMapIteratorDuplicateTextKeys(t *testing.T) {
	const n = 127
	input := make(map[*gcMapKey]int, n)
	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("long/common/prefix/<key>/%d", i%9)
		input[&gcMapKey{Name: names[i]}] = i
	}
	for _, sorted := range []bool{false, true} {
		t.Run(fmt.Sprintf("sort=%v", sorted), func(t *testing.T) {
			options := EscapeHTML | CompactMarshaler
			if sorted {
				options |= SortMapKeys
			}
			got, err := Encode(input, options)
			if err != nil {
				t.Fatal(err)
			}
			d := json.NewDecoder(bytes.NewReader(got))
			tok, err := d.Token()
			if err != nil || tok != json.Delim('{') {
				t.Fatalf("object start: %v, %v", tok, err)
			}
			seen := make([]bool, n)
			count, prev := 0, ""
			for d.More() {
				tok, err = d.Token()
				if err != nil {
					t.Fatal(err)
				}
				key, ok := tok.(string)
				if !ok {
					t.Fatalf("map key %v is not a string", tok)
				}
				var v int
				if err = d.Decode(&v); err != nil {
					t.Fatal(err)
				}
				if v < 0 || v >= n || seen[v] || key != names[v] {
					t.Fatalf("lost pair: key=%q value=%d", key, v)
				}
				if sorted && key < prev {
					t.Fatalf("keys out of order: %q before %q", prev, key)
				}
				seen[v], prev = true, key
				count++
			}
			tok, err = d.Token()
			if err != nil || tok != json.Delim('}') || count != n {
				t.Fatalf("object end: %v, %v, count=%d", tok, err, count)
			}
		})
	}
}

func TestEncoderMapIteratorValueAddressability(t *testing.T) {
	// Map values must remain unaddressable for marshaler selection.
	value := MarshalerImpl{X: 42}
	cases := []struct {
		input any
		want  string
	}{
		{map[string]MarshalerImpl{"a": value}, `{"a":{"X":42}}`},
		{map[string]*MarshalerImpl{"a": &value}, `{"a":42}`},
		{map[int]MarshalerImpl{1: value}, `{"1":{"X":42}}`},
		{map[string]any{"a": value}, `{"a":{"X":42}}`},
		{map[string]any{"a": &value}, `{"a":42}`},
	}
	for _, sorted := range []bool{false, true} {
		t.Run(fmt.Sprintf("sort=%v", sorted), func(t *testing.T) {
			options := CompactMarshaler
			if sorted {
				options |= SortMapKeys
			}
			for round := 0; round < 4; round++ {
				for _, tc := range cases {
					got, err := Encode(tc.input, options)
					if err != nil || string(got) != tc.want {
						t.Fatalf("%T: got %s, %v; want %s", tc.input, got, err, tc.want)
					}
				}
			}
		})
	}
}
