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
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestBoundedEfaceSliceArenaRetention(t *testing.T) {
	// Build a document with one huge array (20,000 elements) and one tiny 2-element
	// array. Retaining ONLY the 2-element sub-slice must never pin the 20,000-element
	// backing array.
	var sb strings.Builder
	sb.WriteString(`{"huge":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"payload-string-item"`)
	}
	sb.WriteString(`],"small":[1,2]}`)
	doc := sb.String()

	var m map[string]any
	if _, _, err := DecodeOne(doc, 1<<_F_copy_string, &m); err != nil {
		t.Fatalf("DecodeOne failed: %v", err)
	}
	huge := m["huge"].([]any)
	small := m["small"].([]any)
	if len(huge) != 20000 || len(small) != 2 {
		t.Fatalf("unexpected lengths: huge=%d small=%d", len(huge), len(small))
	}
	if cap(small) > maxEfaceSliceArenaChunk {
		t.Fatalf("small slice cap=%d exceeds maxEfaceSliceArenaChunk=%d", cap(small), maxEfaceSliceArenaChunk)
	}
	// Verify small and huge do not share backing storage.
	hugeStart := uintptr(unsafe.Pointer(&huge[0]))
	hugeEnd := hugeStart + uintptr(cap(huge))*unsafe.Sizeof(huge[0])
	smallStart := uintptr(unsafe.Pointer(&small[0]))
	if smallStart >= hugeStart && smallStart < hugeEnd {
		t.Fatalf("small sub-slice aliases huge array backing storage")
	}
}

func TestContainerSlotVsSingleElementContract(t *testing.T) {
	type leaf struct {
		A int `json:"a"`
		B int `json:"b"`
	}

	// 1. Slice of *leaf reuses existing non-nil pointees and zeroes on null.
	s0 := &leaf{A: 10, B: 99}
	s1 := &leaf{A: 20, B: 88}
	sl := []*leaf{s0, s1}
	if _, _, err := DecodeOne(`[{"a":1},null]`, 0, &sl); err != nil {
		t.Fatalf("slice decode failed: %v", err)
	}
	if sl[0] != s0 || sl[0].A != 1 || sl[0].B != 99 || sl[1] != nil {
		t.Fatalf("slice pointee reuse/null contract violated: sl[0]=%+v (same=%v) sl[1]=%+v", sl[0], sl[0] == s0, sl[1])
	}

	// 2. Map of *leaf replaces each entry with a fresh pointee without mutating
	// prior map entries.
	prev := &leaf{A: 10, B: 99}
	mp := map[string]*leaf{"k": prev, "n": {A: 7, B: 7}}
	if _, _, err := DecodeOne(`{"k":{"a":1},"n":null}`, 0, &mp); err != nil {
		t.Fatalf("map decode failed: %v", err)
	}
	if mp["k"] == prev || prev.A != 10 || prev.B != 99 {
		t.Fatalf("map entry mutated previous pointee: prev=%+v mp[k]=%+v", prev, mp["k"])
	}
	if mp["k"].A != 1 || mp["k"].B != 0 || mp["n"] != nil {
		t.Fatalf("unexpected map entries: k=%+v n=%+v", mp["k"], mp["n"])
	}

	// 3. Retaining one *leaf from a decoded []*leaf does not pin sibling structs via ptrStructSlabs.
	var items []*leaf
	if _, _, err := DecodeOne(`[{"a":1},{"a":2},{"a":3},{"a":4},{"a":5}]`, 0, &items); err != nil {
		t.Fatalf("items decode failed: %v", err)
	}
	keep := items[0]
	collected := make(chan struct{}, 1)
	runtime.SetFinalizer(items[4], func(*leaf) {
		select {
		case collected <- struct{}{}:
		default:
		}
	})
	items = nil
	for i := 0; i < 4; i++ {
		runtime.GC()
	}
	runtime.KeepAlive(keep)
	select {
	case <-collected:
	default:
		t.Fatalf("retaining items[0] prevented sibling items[4] from being garbage-collected")
	}
}
