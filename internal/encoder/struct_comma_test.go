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

package encoder

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/bytedance/sonic/option"
	"github.com/stretchr/testify/require"
)

type StructCommaEmbedded struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}

func TestStructCommas(t *testing.T) {
	type optional struct {
		A int `json:"a,omitempty"`
		B int `json:"b,omitempty"`
		C int `json:"c,omitempty"`
		D int `json:"d,omitempty"`
	}
	type mixed struct {
		A int `json:"a,omitempty"`
		B int `json:"b"`
		C int `json:"c,omitempty"`
		D int `json:"d"`
	}
	type embeddedFirst struct {
		*StructCommaEmbedded
		C int `json:"c"`
	}
	type embeddedMiddle struct {
		C int `json:"c"`
		*StructCommaEmbedded
		D int `json:"d"`
	}
	type arrays struct {
		A [0]int `json:"a,omitempty"`
		B [1]int `json:"b,omitempty"`
		C [0]int `json:"c,omitempty"`
		D int    `json:"d"`
	}
	type quoted struct {
		A int     `json:"a,omitempty,string"`
		B *int    `json:"b,string"`
		C string  `json:"c,string"`
		D float64 `json:"d,string"`
	}
	type nested struct {
		A optional    `json:"a,omitempty"`
		B []optional  `json:"b"`
		C mixed       `json:"c"`
		D interface{} `json:"d"`
		E int         `json:"e"`
	}

	check := func(t *testing.T, value interface{}) {
		t.Helper()
		want, err := json.Marshal(value)
		require.NoError(t, err)
		got, err := Encode(value, SortMapKeys|EscapeHTML|CompactMarshaler)
		require.NoError(t, err)
		require.Equal(t, string(want), string(got))
	}

	// Exercise every combination of emitted and omitted fields, including
	// transitions from runtime comma checks to an unconditionally emitted field.
	for mask := 0; mask < 16; mask++ {
		a, b, c, d := mask&1, mask&2, mask&4, mask&8
		t.Run(fmt.Sprintf("optional/%d", mask), func(t *testing.T) {
			check(t, optional{a, b, c, d})
		})
		t.Run(fmt.Sprintf("mixed/%d", mask), func(t *testing.T) {
			check(t, mixed{a, b, c, d})
		})
	}

	n := 7
	cases := []struct {
		name  string
		value interface{}
	}{
		{"empty", struct{}{}},
		{"embedded_first_nil", embeddedFirst{nil, 3}},
		{"embedded_first_zero", embeddedFirst{&StructCommaEmbedded{}, 3}},
		{"embedded_first_nonzero", embeddedFirst{&StructCommaEmbedded{1, "b"}, 3}},
		{"embedded_middle_nil", embeddedMiddle{3, nil, 4}},
		{"embedded_middle_nonzero", embeddedMiddle{3, &StructCommaEmbedded{1, "b"}, 4}},
		{"arrays", arrays{}},
		{"quoted_nil", quoted{}},
		{"quoted_nonzero", quoted{1, &n, "a\"b", 1.5}},
		{"nested_empty", nested{}},
		{"nested_values", nested{
			A: optional{A: 1},
			B: []optional{{}, {B: 2}, {}, {A: 1, D: 4}},
			C: mixed{C: 3},
			D: embeddedFirst{&StructCommaEmbedded{1, "b"}, 3},
			E: 5,
		}},
		{"slice", []mixed{{}, {A: 1}, {C: 3}, {}}},
		{"map", map[string]interface{}{"a": optional{}, "b": mixed{}, "c": optional{B: 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check(t, tc.value)
		})
	}
}

func TestStructCommasOnlyOmitNull(t *testing.T) {
	type value struct {
		A *int   `json:"a,omitempty"`
		B int    `json:"b,omitempty"`
		C *int   `json:"c,omitempty"`
		D string `json:"d,omitempty"`
	}
	// With this compile option, B and D are guaranteed to be emitted even
	// though they have omitempty tags. A and C can still be skipped.
	err := Pretouch(reflect.TypeOf(value{}), option.WithCompileEncOnlyOmitNull(true))
	require.NoError(t, err)
	n := 1
	cases := []struct {
		value value
		want  string
	}{
		{value{}, `{"b":0,"d":""}`},
		{value{A: &n}, `{"a":1,"b":0,"d":""}`},
		{value{C: &n}, `{"b":0,"c":1,"d":""}`},
		{value{A: &n, C: &n}, `{"a":1,"b":0,"c":1,"d":""}`},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := Encode(tc.value, 0)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}
