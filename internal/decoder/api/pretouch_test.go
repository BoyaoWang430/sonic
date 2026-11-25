//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package api

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic/option"
	"github.com/stretchr/testify/assert"
)

type testAPIPretouchManyA struct {
	Name string                `json:"name"`
	B    *testAPIPretouchManyB `json:"b,omitempty"`
}

type testAPIPretouchManyB struct {
	ID int                   `json:"id"`
	A  *testAPIPretouchManyA `json:"a,omitempty"`
}

func TestAPIPretouchMany(t *testing.T) {
	err := PretouchMany(
		[]reflect.Type{reflect.TypeOf(testAPIPretouchManyA{}), reflect.TypeOf(testAPIPretouchManyB{})},
		option.WithCompileMaxInlineDepth(1),
		option.WithCompileRecursiveDepth(8),
	)
	assert.NoError(t, err)

	dec := NewDecoder(`{"name":"api","b":{"id":3}}`)
	var out testAPIPretouchManyA
	err = dec.Decode(&out)
	assert.NoError(t, err)
	assert.Equal(t, "api", out.Name)
	if assert.NotNil(t, out.B) {
		assert.Equal(t, 3, out.B.ID)
	}
}
