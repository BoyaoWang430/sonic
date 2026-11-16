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

package ast

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// TestRetainedMaterializedTreeKeepsKeyIndex checks that materializing a lazy
// node in place (Interface/Map/Array keep the parsed tree in the node) still
// builds key indexes for wide objects, so later Get calls stay O(1).
func TestRetainedMaterializedTreeKeepsKeyIndex(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"inner":{`)
	n := _Threshold_Index + 8
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":%d`, i, i)
	}
	b.WriteString(`}}`)

	for _, materialize := range []struct {
		name string
		fn   func(*Node) error
	}{
		{"Interface", func(n *Node) error { _, err := n.Interface(); return err }},
		{"Map", func(n *Node) error { _, err := n.Map(); return err }},
	} {
		root, perr := NewParser(b.String()).Parse()
		if perr != 0 {
			t.Fatalf("parse: %v", perr)
		}
		if err := materialize.fn(&root); err != nil {
			t.Fatalf("%s: %v", materialize.name, err)
		}
		inner := root.Get("inner")
		if inner == nil || inner.t != types.V_OBJECT {
			t.Fatalf("%s: inner object not materialized: %+v", materialize.name, inner)
		}
		if (*linkedPairs)(inner.p).index == nil {
			t.Fatalf("%s: retained object with %d keys has no key index", materialize.name, n)
		}
		last := fmt.Sprintf("k%d", n-1)
		if v, err := inner.Get(last).Int64(); err != nil || v != int64(n-1) {
			t.Fatalf("%s: Get(%s) = %d, %v", materialize.name, last, v, err)
		}
	}
}

// TestUnsetDuplicateKeyKeepsOtherReachable checks that removing one of two
// pairs sharing a key from an indexed object leaves the other one reachable.
func TestUnsetDuplicateKeyKeepsOtherReachable(t *testing.T) {
	pairs := make([]Pair, 0, _Threshold_Index+4)
	for i := 0; i < _Threshold_Index+1; i++ {
		pairs = append(pairs, NewPair(fmt.Sprintf("k%d", i), NewNumber(fmt.Sprint(i))))
	}
	pairs = append(pairs, NewPair("dup", NewNumber("1")), NewPair("dup", NewNumber("2")))
	obj := NewObject(pairs)
	if (*linkedPairs)(obj.p).index == nil {
		t.Fatal("expected the wide object to be indexed")
	}
	if ok, err := obj.Unset("dup"); !ok || err != nil {
		t.Fatalf("Unset(dup) = %v, %v", ok, err)
	}
	if v := obj.Get("dup"); v == nil || !v.Exists() {
		t.Fatal("the remaining duplicate became unreachable after Unset")
	}
	if ok, err := obj.Unset("dup"); !ok || err != nil {
		t.Fatalf("second Unset(dup) = %v, %v", ok, err)
	}
	if v := obj.Get("dup"); v != nil && v.Exists() {
		t.Fatal("dup still reachable after both pairs were unset")
	}
	if v, err := obj.Get("k3").Int64(); err != nil || v != 3 {
		t.Fatalf("unrelated key lost: %d, %v", v, err)
	}
}
