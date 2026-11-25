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

import "testing"

// TestErrorSentinelIdentity checks that error accessors hand back the exact
// ErrNotExist / ErrUnsupportType values, so `err == ast.ErrNotExist` works.
func TestErrorSentinelIdentity(t *testing.T) {
	var missing *Node
	if err := missing.Get("a").Check(); err != ErrNotExist {
		t.Fatalf("nil.Get().Check() = %v, want ErrNotExist", err)
	}
	if _, err := missing.Get("a").Int64(); err != ErrNotExist {
		t.Fatalf("nil.Get().Int64() err = %v, want ErrNotExist", err)
	}
	if _, err := missing.Get("a").Raw(); err != ErrNotExist {
		t.Fatalf("nil.Get().Raw() err = %v, want ErrNotExist", err)
	}

	arr := NewArray([]Node{NewNumber("1")})
	if err := arr.Get("k").Check(); err != ErrUnsupportType {
		t.Fatalf("array.Get().Check() = %v, want ErrUnsupportType", err)
	}

	root := NewRaw(`{"a":{"b":1},"c":2}`)
	if err := root.Get("missing").Get("b").Check(); err != ErrNotExist {
		t.Fatalf("root.Get(missing).Get().Check() = %v, want ErrNotExist", err)
	}
	obj := root.Get("a")
	if _, err := obj.SetByIndex(5, NewNumber("3")); err != ErrNotExist {
		t.Fatalf("object.SetByIndex(out of range) err = %v, want ErrNotExist", err)
	}
}
