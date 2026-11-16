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

package vm

import "testing"

func TestGrowSliceBufAfterFirstCapsReservation(t *testing.T) {
	// A large first element must not be extrapolated over a million elements.
	buf := append(make([]byte, 0, 8), '[')
	start := len(buf)
	buf = append(buf, make([]byte, 4096)...)
	got := growSliceBufAfterFirst(buf, start, 1000000)
	if extra := cap(got) - len(got); extra > maxSlicePresize+64 {
		t.Fatalf("reserved %d bytes ahead, want at most %d", extra, maxSlicePresize+64)
	}
	if string(got[:len(buf)]) != string(buf) {
		t.Fatal("presize must preserve the bytes already written")
	}

	// Ordinary slices are still presized from the first element.
	small := append([]byte{'['}, `{"a":1}`...)
	got = growSliceBufAfterFirst(small, 1, 1000)
	if want := len(small) + 7*999; cap(got) < want {
		t.Fatalf("cap %d after presize, want at least %d", cap(got), want)
	}
}
