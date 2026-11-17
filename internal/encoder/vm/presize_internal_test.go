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

import (
	"math"
	"strings"
	"testing"
)

func TestReserveSliceMinCapNoFirstElemExtrapolationAndOverflowSafe(t *testing.T) {
	// 1. Overflow and cap safety across extreme counts.
	buf := append(make([]byte, 0, 8), '[')
	for _, n := range []int{-1, 0, 1, 1000000, math.MaxInt / 2, math.MaxInt} {
		got := reserveSliceMinCap(buf, n, 16)
		if extra := cap(got) - len(got); extra > maxSliceMinReserve+16 {
			t.Fatalf("n=%d reserved %d bytes ahead, want at most %d", n, extra, maxSliceMinReserve+16)
		}
		if string(got[:len(buf)]) != string(buf) {
			t.Fatalf("n=%d corrupted existing prefix", n)
		}
	}

	// 2. Skewed large first element must NOT extrapolate 4KB * 999 -> ~4MB reservation.
	type skewedElem struct {
		ID  int    `json:"id"`
		Bio string `json:"bio"`
	}
	elems := make([]skewedElem, 1000)
	elems[0] = skewedElem{ID: 1, Bio: strings.Repeat("x", 4096)}
	for i := 1; i < len(elems); i++ {
		elems[i] = skewedElem{ID: i + 1, Bio: ""}
	}
	out, ok, err := EncodeRoot(elems, 0)
	if err != nil || !ok {
		t.Fatalf("EncodeRoot skewed slice failed: ok=%v err=%v", ok, err)
	}
	if cap(out) > len(out)*2+256 {
		t.Fatalf("skewed first element over-reserved output slice: len=%d cap=%d", len(out), cap(out))
	}
}
