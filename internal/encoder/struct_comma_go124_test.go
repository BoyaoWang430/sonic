//go:build go1.24
// +build go1.24

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
	"testing"

	"github.com/stretchr/testify/require"
)

type structCommaZero int

func (v structCommaZero) IsZero() bool { return v < 0 }

func TestStructCommasOmitZero(t *testing.T) {
	type value struct {
		A structCommaZero `json:"a,omitzero"`
		B int             `json:"b,omitzero"`
		C int             `json:"c"`
		D int             `json:"d,omitempty,omitzero"`
	}
	cases := []struct {
		name  string
		value value
	}{
		{"omitted", value{-1, 0, 2, 0}},
		{"present", value{0, 1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.value)
			require.NoError(t, err)
			got, err := Encode(tc.value, 0)
			require.NoError(t, err)
			require.Equal(t, string(want), string(got))
		})
	}
}
