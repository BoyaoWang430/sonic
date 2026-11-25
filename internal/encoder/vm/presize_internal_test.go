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
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type failingFirstCallbackElem struct {
	ID int
}

var callbackCount int

func (e failingFirstCallbackElem) MarshalJSON() ([]byte, error) {
	callbackCount++
	if e.ID == 0 {
		return nil, errors.New("boom on elem 0")
	}
	return []byte(`{"id":1}`), nil
}

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
		grown := growSliceRemainingCap(buf, n, 16)
		if extra := cap(grown) - len(grown); extra > maxSliceMinReserve+64+(cap(buf)>>1) {
			t.Fatalf("n=%d grew %d bytes ahead, want bounded growth", n, extra)
		}
		if string(grown[:len(buf)]) != string(buf) {
			t.Fatalf("n=%d growSliceRemainingCap corrupted existing prefix", n)
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

	// 3. Non-dense struct slice with omitempty + unconditional fields still computes
	// a valid lower bound <= actual wire output even when all optional fields are empty.
	type mixedElem struct {
		OptTag string `json:"opt_tag,omitempty"`
		ID     int64  `json:"id"`
		Active bool   `json:"active"`
		QuoteS string `json:"quote_s,string"`
		PtrVal *int   `json:"ptr_val"`
	}
	plan, err := getOrBuildRootPlan(reflect.TypeOf([]mixedElem{}), new(uint64))
	if err != nil {
		t.Fatal(err)
	}
	if plan.minElemBytes <= 3 {
		t.Fatalf("expected non-trivial lower bound on mixedElem, got %d", plan.minElemBytes)
	}
	mixed := make([]mixedElem, 32)
	mixedOut, ok, err := EncodeRoot(mixed, 0)
	if err != nil || !ok {
		t.Fatalf("EncodeRoot mixedElem failed: ok=%v err=%v", ok, err)
	}
	if len(mixed)*plan.minElemBytes > len(mixedOut)+1 {
		t.Fatalf("minElemBytes=%d * %d = %d exceeded wire len %d",
			plan.minElemBytes, len(mixed), len(mixed)*plan.minElemBytes, len(mixedOut))
	}

	// 4. Element-0 json.Marshaler callback failure on huge slice must NEVER pre-execute
	// later callbacks or pre-allocate megabytes before element 0 fails.
	cbItems := make([]failingFirstCallbackElem, 100000)
	allocBytes := testing.AllocsPerRun(1, func() {
		callbackCount = 0
		_, _, cbErr := EncodeRoot(cbItems, 0)
		if cbErr == nil {
			t.Fatal("expected callback error on element 0")
		}
	})
	if callbackCount != 1 {
		t.Fatalf("expected exactly 1 callback execution before element-0 error, got %d", callbackCount)
	}
	if allocBytes > 16 {
		t.Fatalf("unexpectedly high alloc count (%v) before element-0 callback error", allocBytes)
	}
}
