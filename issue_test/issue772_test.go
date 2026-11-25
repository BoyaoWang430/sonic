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

package issue_test

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

func TestIssue772_SkipIfaceType(t *testing.T) {
	for _, tt := range []struct {
		cas   unmTestCase
		check func(*testing.T, *WrapperEface)
	}{
		{
			cas: unmTestCase{
				name: "should skip non-ptr iface type",
				data: []byte(`{"id": {"id": "2"},"name": "name"}`),
				newfn: func() interface{} {
					obj := WrapperEface{}
					obj.Id = fooEface3{}
					return &obj
				},
			},
			check: func(t *testing.T, got *WrapperEface) {
				require.Equal(t, "name", got.Name)
				require.IsType(t, fooEface3{}, got.Id)
				require.Nil(t, got.Id.(fooEface3).Id)
			},
		},
		{
			cas: unmTestCase{
				name: "should skip nil iface type",
				data: []byte(`{"id": {"id": "2"},"name": "name"}`),
				newfn: func() interface{} {
					obj := WrapperEface{}
					obj.Id = (*fooEface)(nil)
					return &obj
				},
			},
			check: func(t *testing.T, got *WrapperEface) {
				require.Equal(t, "name", got.Name)
				require.IsType(t, (*fooEface)(nil), got.Id)
				require.Nil(t, got.Id.(*fooEface))
			},
		},
	} {
		t.Run(tt.cas.name, func(t *testing.T) {
			if stdUsesJSONV2 && sonic.APIKind == sonic.UseSonicJSON {
				wantJSON := `{"name":"name","id":null}`
				if tt.cas.name == "should skip non-ptr iface type" {
					wantJSON = `{"name":"name","id":{"id":null}}`
				}
				value := requireSonicUnmarshalSnapshot(
					t,
					sonic.ConfigDefault,
					tt.cas,
					true,
					false,
					wantJSON,
				)
				tt.check(t, value.(*WrapperEface))
				return
			}
			assertUnmarshal(t, sonic.ConfigDefault, tt.cas, true)
		})
	}
}
