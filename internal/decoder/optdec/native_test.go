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

package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNative(t *testing.T) {
	data := `{"a":1, "b": true,    "":    false, "d":                             null,"\"f":[], "\\": "\\", "hi": {}, "":[[[]], "", [{}]]}`
	p := newParser(data, 0, 0)
	ecode := p.parse()
	defer p.free()
	assert.Equal(t, int(ecode), 0)
	assert.Equal(t, p.Pos(), len(data))
	spew.Dump(p.nbuf.stat)
}

func TestParseNativeRetryLargeJson(t *testing.T) {
	t.Run("Object", func(t *testing.T) {
		data := "{" + strings.Repeat("\"a\":1,", 1<<20) + "\"a\":1}"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, int(p.Pos()), len(data))
		assert.Equal(t, int(p.nbuf.stat.object), 1)
		assert.Equal(t, int(p.nbuf.stat.object_keys), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 1)
		assert.Equal(t, int(p.nbuf.stat.number), 1<<20+1)
	})

	t.Run("ObjectNull", func(t *testing.T) {
		data := "{" + strings.Repeat("\"a\":null,", 1<<20) + "\"a\":null}"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, int(p.Pos()), len(data))
		assert.Equal(t, int(p.nbuf.stat.object), 1)
		assert.Equal(t, int(p.nbuf.stat.object_keys), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 1)
	})

	t.Run("Object2", func(t *testing.T) {
		data := "{\"top\": {" + strings.Repeat("\"a\":1,", 1<<20) + "\"a\":1}, \"final\": true}"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, int(p.Pos()), len(data))
		assert.Equal(t, int(p.nbuf.stat.object), 2)
		assert.Equal(t, int(p.nbuf.stat.object_keys), 1<<20+3)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 2)
		assert.Equal(t, int(p.nbuf.stat.number), 1<<20+1)
	})

	t.Run("Array", func(t *testing.T) {
		data := "[" + strings.Repeat("1,", 1<<20) + "1]"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, p.Pos(), len(data))
		assert.Equal(t, int(p.nbuf.stat.array), 1)
		assert.Equal(t, int(p.nbuf.stat.array_elems), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.number), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 1)
	})
}

func TestSubPlanSharingAndRecursiveBind(t *testing.T) {
	type sharedLeaf struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Tag  string `json:"tag"`
	}
	type parentDTO struct {
		A     sharedLeaf   `json:"a"`
		B     sharedLeaf   `json:"b"`
		C     *sharedLeaf  `json:"c"`
		Slice []sharedLeaf `json:"slice"`
	}

	dec, err := newCompiler().compileType(reflect.TypeOf(parentDTO{}))
	require.NoError(t, err)
	sd, ok := dec.(*structDecoder)
	require.True(t, ok)
	require.Len(t, sd.fields, 4)

	decA := sd.fieldDecs[0].(*structDecoder)
	decB := sd.fieldDecs[1].(*structDecoder)
	decC := sd.fieldDecs[2].(*ptrDecoder).deref.(*structDecoder)
	decSlice := sd.fieldDecs[3].(*sliceDecoder).structDec
	require.Same(t, decA, decB)
	require.Same(t, decA, decC)
	require.Same(t, decA, decSlice)
	require.Same(t, decA.fieldMap, decB.fieldMap)

	type treeNode struct {
		Val   int       `json:"val"`
		Left  *treeNode `json:"left"`
		Right *treeNode `json:"right"`
	}
	tdec, err := newCompiler().compileType(reflect.TypeOf(treeNode{}))
	require.NoError(t, err)
	tsd := tdec.(*structDecoder)
	leftRec := tsd.fieldDecs[1].(*ptrDecoder).deref.(*recuriveDecoder)
	rightRec := tsd.fieldDecs[2].(*ptrDecoder).deref.(*recuriveDecoder)
	require.NotNil(t, atomic.LoadPointer(&leftRec.dec))
	require.NotNil(t, atomic.LoadPointer(&rightRec.dec))
}

func TestContainerAsRawVariants(t *testing.T) {
	type rawEnvelope struct {
		EmptyObj  json.RawMessage `json:"empty_obj"`
		EmptyArr  json.RawMessage `json:"empty_arr"`
		FlatObj   json.RawMessage `json:"flat_obj"`
		FlatArr   json.RawMessage `json:"flat_arr"`
		NestedObj json.RawMessage `json:"nested_obj"`
		EscEnd    json.RawMessage `json:"esc_end"`
	}
	input := `{"empty_obj": {  }, "empty_arr": [ ], "flat_obj": {"x": 1, "y": "ok"}, "flat_arr": [1, true, false, null, "hi"], "nested_obj": {"a": {"b": [1, {"c": { }}]}}, "esc_end": {"k": "a\"b\nc"  }}`
	var got, want rawEnvelope
	pos := 0
	require.NoError(t, Decode(&input, &pos, 0, &got))
	require.NoError(t, json.Unmarshal([]byte(input), &want))
	require.Equal(t, want, got)
}
