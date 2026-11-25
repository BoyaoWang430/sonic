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
	"bytes"
	"reflect"
	"runtime"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/resolver"
)

func TestKeyPlanSeparatedFromDecoderResolverAndSliceVariants(t *testing.T) {
	type decoderOnlyStruct struct {
		Alpha int    `json:"alpha"`
		Beta  string `json:"beta"`
	}
	vt := reflect.TypeOf(decoderOnlyStruct{})
	fms := resolver.ResolveStruct(vt)
	if len(fms) != 2 {
		t.Fatalf("expected 2 resolved fields, got %d", len(fms))
	}

	keyPlanLock.RLock()
	_, populatedByResolver := keyPlanCache[vt]
	keyPlanLock.RUnlock()
	if populatedByResolver {
		t.Fatalf("resolver.ResolveStruct must not populate encoder keyPlanCache")
	}

	type child struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type namedChildren []child
	type namedPtrChildren []*child
	type holder struct {
		NilVal   namedChildren    `json:"nil_val"`
		EmptyVal namedChildren    `json:"empty_val"`
		Items    namedChildren    `json:"items"`
		NilPtr   namedPtrChildren `json:"nil_ptr"`
		EmptyPtr namedPtrChildren `json:"empty_ptr"`
		Ptrs     namedPtrChildren `json:"ptrs"`
		OmitNil  namedChildren    `json:"omit_nil,omitempty"`
		OmitEmp  namedChildren    `json:"omit_emp,omitempty"`
	}

	c1 := child{ID: 1, Name: "one"}
	c2 := child{ID: 2, Name: "two"}
	in := holder{
		NilVal:   nil,
		EmptyVal: namedChildren{},
		Items:    namedChildren{c1, c2},
		NilPtr:   nil,
		EmptyPtr: namedPtrChildren{},
		Ptrs:     namedPtrChildren{&c1, nil, &c2},
		OmitNil:  nil,
		OmitEmp:  namedChildren{},
	}

	out, err := Encode(in, 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	out2, err := Encode(&in, EscapeHTML)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"nil_val":null,"empty_val":[],"items":[{"id":1,"name":"one"},{"id":2,"name":"two"}],"nil_ptr":null,"empty_ptr":[],"ptrs":[{"id":1,"name":"one"},null,{"id":2,"name":"two"}]}`
	if string(out) != want || string(out2) != want {
		t.Fatalf("got:\n  %s\n  %s\nwant:\n  %s", out, out2, want)
	}

	outNoNull, err := Encode(in, NoNullSliceOrMap)
	if err != nil {
		t.Fatal(err)
	}
	const wantNoNull = `{"nil_val":[],"empty_val":[],"items":[{"id":1,"name":"one"},{"id":2,"name":"two"}],"nil_ptr":[],"empty_ptr":[],"ptrs":[{"id":1,"name":"one"},null,{"id":2,"name":"two"}]}`
	if string(outNoNull) != wantNoNull {
		t.Fatalf("NoNullSliceOrMap got:\n  %s\nwant:\n  %s", outNoNull, wantNoNull)
	}
}

func TestKeyPlanContiguousOwnerAndTrailingZeroPadding(t *testing.T) {
	type NestedChild struct {
		ChildA int64  `json:"child_a"`
		ChildB string `json:"child_b"`
	}
	type SampleStruct struct {
		ID      int64       `json:"id"`
		Name    string      `json:"name"`
		EscKey  string      `json:"a<b&c>d"`
		Nested  NestedChild `json:"nested"`
		OmitVal int         `json:"omit_val,omitempty"`
	}

	resolver.ResetFieldCacheForTest()
	efs := resolveEncodedStruct(reflect.TypeOf(SampleStruct{}))
	if len(efs) != 5 {
		t.Fatalf("expected 5 encoded fields, got %d", len(efs))
	}

	blob := efs[0].owner
	firstPtr := uintptr(unsafe.Pointer(unsafe.SliceData(blob)))
	lastField := efs[len(efs)-1]
	lastEnd := firstPtr + uintptr(lastField.keyOffset+lastField.commaKeyLen)
	for i, ef := range efs {
		if unsafe.SliceData(ef.owner) != unsafe.SliceData(blob) {
			t.Fatalf("field %d owner does not share backing slice", i)
		}
		ptr := uintptr(unsafe.Pointer(ef.commaKeyPtr()))
		if ptr < firstPtr || ptr >= lastEnd {
			t.Fatalf("field %d keyOffset pointer out of contiguous owner range", i)
		}
		raw32 := ef.owner[ef.keyOffset : ef.keyOffset+32]
		if !bytes.HasPrefix(raw32, []byte(ef.paddedCommaKey)) {
			t.Fatalf("field %d 32B read prefix mismatch", i)
		}
		if ef.meta.Name == "nested" {
			if !ef.hasStructBegin || string(ef.owner[ef.keyOffset:ef.keyOffset+ef.commaKeyLen+1]) != `,"nested":{` {
				t.Fatalf("unexpected nested struct-begin key span")
			}
		}
	}
	trailingZeros := blob[lastField.keyOffset+lastField.commaKeyLen : lastField.keyOffset+lastField.commaKeyLen+32]
	for i, b := range trailingZeros {
		if b != 0 {
			t.Fatalf("expected trailing zero at byte %d after key owner, got %d", i, b)
		}
	}
}
