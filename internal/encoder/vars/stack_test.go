/**
 * Copyright 2024 ByteDance Inc.
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

package vars

import (
	"testing"
	"unsafe"
)

// TestStackOverflowChunks checks that states beyond the inline array are kept
// in on-demand chunks, round-trip correctly, and that ResetStack clears the
// pooled chunks while releasing the deeper ones.
func TestStackOverflowChunks(t *testing.T) {
	s := &Stack{}
	marker := unsafe.Pointer(new(int))
	depth := inlineStackCap + 3*extraChunkSize + 5
	for i := 0; i < depth; i++ {
		if !s.Save(i, uint64(i), marker, nil) {
			t.Fatalf("push %d failed", i)
		}
	}
	for ci := 0; ci < 4; ci++ {
		if s.sbExtra[ci] == nil {
			t.Fatalf("overflow chunk %d was not allocated", ci)
		}
	}
	if s.sbExtra[4] != nil {
		t.Fatal("an overflow chunk beyond the used depth was allocated")
	}
	for i := depth - 1; i >= depth-extraChunkSize-2; i-- {
		x, f, p, _ := s.Drop()
		if x != i || f != uint64(i) || p != marker {
			t.Fatalf("state %d round-tripped as (%d, %d, %p)", i, x, f, p)
		}
	}

	ResetStack(s)
	for ci := 0; ci < pooledExtraChunks; ci++ {
		c := s.sbExtra[ci]
		if c == nil {
			t.Fatalf("pooled overflow chunk %d was dropped", ci)
		}
		for i := range c {
			if c[i] != (State{}) {
				t.Fatalf("pooled overflow chunk %d slot %d still holds %+v", ci, i, c[i])
			}
		}
	}
	for ci := pooledExtraChunks; ci < maxExtraChunks; ci++ {
		if s.sbExtra[ci] != nil {
			t.Fatalf("overflow chunk %d was kept after reset", ci)
		}
	}
	if s.extraHigh != 0 {
		t.Fatalf("extraHigh = %d after reset", s.extraHigh)
	}

	for i := 0; i < MaxStack; i++ {
		if !s.Save(i, 0, marker, nil) {
			t.Fatalf("push %d of %d failed", i, MaxStack)
		}
	}
	if s.Save(0, 0, nil, nil) {
		t.Fatal("push beyond MaxStack must fail")
	}
	FreeStack(s)
}
