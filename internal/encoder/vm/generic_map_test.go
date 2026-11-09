package vm_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"testing"

	"github.com/bytedance/sonic/internal/encoder"
)

type genericMapJSON map[string]any

func (genericMapJSON) MarshalJSON() ([]byte, error) { return []byte(`"custom-map"`), nil }

type genericMapText map[string]any

func (genericMapText) MarshalText() ([]byte, error) { return []byte("<custom-map>"), nil }

type genericMapCallback func() ([]byte, error)

func (f genericMapCallback) MarshalJSON() ([]byte, error) { return f() }

func TestGenericMapValues(t *testing.T) {
	gcValue := genericMapCallback(func() ([]byte, error) {
		runtime.GC()
		return []byte(` { "value": "<gc>" } `), nil
	})
	cases := []any{
		map[string]any(nil), map[string]any{},
		map[string]any{"": nil, "quote\"": "line\n", "<key>": "<value>", "gc": gcValue},
		[]any{map[string]any{"nested": map[string]any{"gc": gcValue}}, map[string]any(nil)},
		struct {
			Object map[string]any `json:"object"`
			Empty  map[string]any `json:"empty,omitempty"`
		}{Object: map[string]any{"value": gcValue}},
		genericMapJSON{"ignored": 1}, genericMapJSON(nil),
		genericMapText{"ignored": 1},
		map[string]any{"json": genericMapJSON{}, "text": genericMapText{}, "typed": map[string]int{"a": 1}},
	}
	for i, input := range cases {
		for _, sorted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/sort=%v", i, sorted), func(t *testing.T) {
				opts := encoder.EscapeHTML | encoder.CompactMarshaler
				if sorted {
					opts |= encoder.SortMapKeys
				}
				got, err := encoder.Encode(input, opts)
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

func TestGenericMapOptionsAndErrors(t *testing.T) {
	for _, sorted := range []encoder.Options{0, encoder.SortMapKeys} {
		for _, tc := range []struct {
			input map[string]any
			opts  encoder.Options
			want  string
		}{
			{nil, encoder.NoNullSliceOrMap, `{}`},
			{map[string]any{"<key>": "<value>"}, 0, `{"<key>":"<value>"}`},
			{map[string]any{"<key>": "<value>"}, encoder.EscapeHTML, `{"\u003ckey\u003e":"\u003cvalue\u003e"}`},
			{map[string]any{"nan": math.NaN()}, encoder.EncodeNullForInfOrNan, `{"nan":null}`},
		} {
			dst := []byte("prefix:")
			if err := encoder.EncodeInto(&dst, tc.input, tc.opts|sorted); err != nil || string(dst) != "prefix:"+tc.want {
				t.Fatalf("encode options %v: %s, %v", tc.opts|sorted, dst, err)
			}
		}
		bad := genericMapCallback(func() ([]byte, error) { return nil, errors.New("map value failure") })
		for i := 0; i < 3; i++ {
			dst := []byte("prefix:")
			if err := encoder.EncodeInto(&dst, map[string]any{"nested": map[string]any{"bad": bad}}, sorted); err == nil {
				t.Fatal("map value error was lost")
			}
			got, err := encoder.Encode(map[string]any{"nested": map[string]any{"ok": true}}, sorted)
			if err != nil || string(got) != `{"nested":{"ok":true}}` {
				t.Fatalf("encode after error: %s, %v", got, err)
			}
		}
	}
}

func TestGenericMapMutation(t *testing.T) {
	for _, remove := range []bool{false, true} {
		makeInput := func() map[string]any {
			m := make(map[string]any)
			value := genericMapCallback(func() ([]byte, error) {
				for k := range m {
					if remove {
						delete(m, k)
					} else {
						m[k] = 7
					}
				}
				runtime.GC()
				return []byte("1"), nil
			})
			for i := 0; i < 16; i++ {
				m[fmt.Sprint(i)] = value
			}
			return m
		}
		t.Run(fmt.Sprintf("remove=%v", remove), func(t *testing.T) {
			got, err := encoder.Encode(makeInput(), 0)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]int
			if err := json.Unmarshal(got, &values); err != nil {
				t.Fatal(err)
			}
			wantCount := 16
			if remove {
				wantCount = 1
			}
			original := 0
			for _, value := range values {
				if value == 1 {
					original++
				} else if value != 7 {
					t.Fatalf("unexpected value %d in %s", value, got)
				}
			}
			if len(values) != wantCount || original != 1 {
				t.Fatalf("map mutation: got %s, want %d keys and one original value", got, wantCount)
			}
			got, err = encoder.Encode(makeInput(), encoder.SortMapKeys)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(makeInput())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("sorted map mutation: got %s, want %s, err=%v", got, want, err)
			}
		})
	}
}

func TestGenericMapDepth(t *testing.T) {
	var nested any = 1
	for i := 0; i < 32; i++ {
		nested = map[string]any{"next": nested}
	}
	cycle := map[string]any{}
	cycle["next"] = cycle
	for _, opts := range []encoder.Options{0, encoder.SortMapKeys} {
		got, err := encoder.Encode(nested, opts)
		want, stdErr := json.Marshal(nested)
		if err != nil || stdErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("nested maps: got %s, want %s, err=%v", got, want, err)
		}
		if _, err = encoder.Encode(cycle, opts); err == nil {
			t.Fatal("cyclic map was accepted")
		} else if _, ok := err.(*json.UnsupportedValueError); !ok {
			t.Fatalf("cyclic map error: %T, %v", err, err)
		}
		got, err = encoder.Encode(map[string]any{"ok": true}, opts)
		if err != nil || string(got) != `{"ok":true}` {
			t.Fatalf("encode after cycle: %s, %v", got, err)
		}
	}
}

//go:noinline
func growGenericMapStack(depth int) int {
	var frame [4096]byte
	for i := range frame {
		frame[i] = byte(depth + i)
	}
	sum := 0
	if depth == 0 {
		runtime.GC()
	} else {
		sum = growGenericMapStack(depth - 1)
	}
	sum += int(frame[depth])
	runtime.KeepAlive(&frame)
	return sum
}

func TestGenericMapStackGrowth(t *testing.T) {
	value := genericMapCallback(func() ([]byte, error) {
		if sum := growGenericMapStack(64); sum != 4160 {
			return nil, fmt.Errorf("stack frame sum: %d", sum)
		}
		return []byte("true"), nil
	})
	input := map[string]any{"nested": map[string]any{"leaf": value}}
	for _, opts := range []encoder.Options{0, encoder.SortMapKeys} {
		t.Run(fmt.Sprint(opts), func(t *testing.T) {
			got, err := encoder.Encode(input, opts)
			if err != nil || string(got) != `{"nested":{"leaf":true}}` {
				t.Fatalf("encode after stack growth: %s, %v", got, err)
			}
		})
	}
}
