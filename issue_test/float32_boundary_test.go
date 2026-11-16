/**
* Copyright 2026 ByteDance Inc.
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing,software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */
package issue_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

// TestFloat32RangeBoundaryMatchesStd checks float32 targets near the top of
// the float32 range on every decode path (scalar, struct field, flat struct,
// slice, array, map value, ",string" field). encoding/json parses with
// strconv.ParseFloat(s, 32): literals that round to +/-MaxFloat32 are
// accepted, only literals that round to +/-Inf are rejected.
func TestFloat32RangeBoundaryMatchesStd(t *testing.T) {
	type flat struct {
		Pad string     `json:"pad"`
		F   float32    `json:"f"`
		S   []float32  `json:"s"`
		A   [2]float32 `json:"a"`
	}
	type plain struct {
		F float32 `json:"f"`
	}
	type quoted struct {
		F float32 `json:"f,string"`
	}
	pad := strings.Repeat("x", 300)
	shapes := []struct {
		name string
		typ  reflect.Type
		doc  func(v string) string
	}{
		{"scalar", reflect.TypeOf(float32(0)), func(v string) string { return v }},
		{"field", reflect.TypeOf(plain{}), func(v string) string { return `{"f":` + v + `}` }},
		{"flat", reflect.TypeOf(flat{}), func(v string) string {
			return `{"pad":"` + pad + `","f":` + v + `,"s":[` + v + `,1.5],"a":[` + v + `,` + v + `]}`
		}},
		{"slice", reflect.TypeOf([]float32(nil)), func(v string) string { return `[` + v + `,1]` }},
		{"array", reflect.TypeOf([2]float32{}), func(v string) string { return `[1,` + v + `]` }},
		{"map", reflect.TypeOf(map[string]float32(nil)), func(v string) string { return `{"k":` + v + `}` }},
		{"structs", reflect.TypeOf([]plain(nil)), func(v string) string { return `[{"f":` + v + `},{"f":2}]` }},
		{"string-opt", reflect.TypeOf(quoted{}), func(v string) string { return `{"f":"` + v + `"}` }},
	}
	literals := []string{
		"3.40282346638528859811704183484516925440e+38", // exactly MaxFloat32
		"3.402823469e+38", // rounds down to MaxFloat32
		"3.40282347e+38",
		"3.40282348e+38",
		"3.4028235e+38", // shortest form of MaxFloat32
		"3.4028236e+38", // rounds up to +Inf: rejected
		"1e39",
		"1e400",
		"1.5",
	}
	for _, shape := range shapes {
		for _, lit := range literals {
			for _, v := range []string{lit, "-" + lit} {
				doc := shape.doc(v)
				want := reflect.New(shape.typ)
				stdErr := json.Unmarshal([]byte(doc), want.Interface())
				for _, api := range []sonic.API{sonic.ConfigDefault, sonic.ConfigStd} {
					// Decode a few times so adaptive fast paths get exercised too.
					for round := 0; round < 3; round++ {
						got := reflect.New(shape.typ)
						err := api.UnmarshalFromString(doc, got.Interface())
						require.Equal(t, stdErr != nil, err != nil,
							"%s %s: sonic err=%v, std err=%v", shape.name, v, err, stdErr)
						if stdErr == nil {
							require.Equal(t, want.Elem().Interface(), got.Elem().Interface(), "%s %s", shape.name, v)
						}
					}
				}
			}
		}
	}
}
