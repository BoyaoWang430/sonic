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
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic/internal/jsonscan"
)

// r4ColdRunGen assigns bounded unique dynamic struct identities across repeated
// `-count=N` runs so T3 genuinely exercises cold compilation without unbounded
// type cache growth (§10.1 T3).
var r4ColdRunGen atomic.Uint64

const r4MaxColdGenerations = 64

// Dedicated cold named scalar, bytes, and raw/unmarshaler types never referenced outside TestR4.
type (
	r4ColdScalar01 int
	r4ColdScalar02 int64
	r4ColdScalar03 uint32
	r4ColdScalar04 float64
	r4ColdScalar05 string
	r4ColdScalar06 bool
	r4ColdScalar07 int16
	r4ColdScalar08 uint64

	r4ColdBytes01 []byte
	r4ColdBytes02 []byte
	r4ColdBytes03 []byte
	r4ColdBytes04 []byte

	r4ColdRaw01 struct{ raw []byte }
	r4ColdRaw02 struct{ raw []byte }
	r4ColdRaw03 struct{ raw []byte }
	r4ColdRaw04 struct{ raw []byte }
)

func (r *r4ColdRaw01) UnmarshalJSON(b []byte) error { r.raw = append(r.raw[:0], b...); return nil }
func (r *r4ColdRaw02) UnmarshalJSON(b []byte) error { r.raw = append(r.raw[:0], b...); return nil }
func (r *r4ColdRaw03) UnmarshalJSON(b []byte) error { r.raw = append(r.raw[:0], b...); return nil }
func (r *r4ColdRaw04) UnmarshalJSON(b []byte) error { r.raw = append(r.raw[:0], b...); return nil }

type singletonSnapshot struct {
	full        projPlan
	scalar      projPlan
	typedScalar projPlan
	bytes       projPlan
	raw         projPlan
	jsScal      jsonscan.Plan
	jsTypedScal jsonscan.Plan
	jsByte      jsonscan.Plan
	jsRaw       jsonscan.Plan
}

func captureSingletonSnapshot() singletonSnapshot {
	return singletonSnapshot{
		full:        *projFullPlan,
		scalar:      *projScalarPlan,
		typedScalar: *projTypedScalarPlan,
		bytes:       *projBytesPlan,
		raw:         *projRawPlan,
		jsScal:      *jsScalarPlan,
		jsTypedScal: *jsTypedScalarPlan,
		jsByte:      *jsBytesPlan,
		jsRaw:       *jsRawPlan,
	}
}

func TestR4_SharedSingletonImmutabilityAndConcurrentColdCompile(t *testing.T) {
	before := captureSingletonSnapshot()

	// Warm fast-path *int, *string, *bool readers before concurrent cold compilation.
	var warmInt int
	if _, _, err := DecodeOne("1", 0, &warmInt); err != nil || warmInt != 1 {
		t.Fatalf("warm *int decode failed: %v", err)
	}

	coldTasks := []func(t *testing.T){
		func(t *testing.T) {
			var v r4ColdScalar01
			if _, _, err := DecodeOne("101", 0, &v); err != nil || v != 101 {
				t.Errorf("r4ColdScalar01: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar02
			if _, _, err := DecodeOne("-202", 0, &v); err != nil || v != -202 {
				t.Errorf("r4ColdScalar02: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar03
			if _, _, err := DecodeOne("303", 0, &v); err != nil || v != 303 {
				t.Errorf("r4ColdScalar03: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar04
			if _, _, err := DecodeOne("404.5", 0, &v); err != nil || v != 404.5 {
				t.Errorf("r4ColdScalar04: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar05
			if _, _, err := DecodeOne(`"hello"`, 0, &v); err != nil || v != "hello" {
				t.Errorf("r4ColdScalar05: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar06
			if _, _, err := DecodeOne("true", 0, &v); err != nil || !bool(v) {
				t.Errorf("r4ColdScalar06: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar07
			if _, _, err := DecodeOne("707", 0, &v); err != nil || v != 707 {
				t.Errorf("r4ColdScalar07: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdScalar08
			if _, _, err := DecodeOne("808", 0, &v); err != nil || v != 808 {
				t.Errorf("r4ColdScalar08: err=%v v=%v", err, v)
			}
		},
		func(t *testing.T) {
			var v r4ColdBytes01
			if _, _, err := DecodeOne(`"Ynl0ZXM="`, 0, &v); err != nil || string(v) != "bytes" {
				t.Errorf("r4ColdBytes01: err=%v v=%q", err, string(v))
			}
		},
		func(t *testing.T) {
			var v r4ColdBytes02
			if _, _, err := DecodeOne(`"Ynl0ZXM="`, 0, &v); err != nil || string(v) != "bytes" {
				t.Errorf("r4ColdBytes02: err=%v v=%q", err, string(v))
			}
		},
		func(t *testing.T) {
			var v r4ColdBytes03
			if _, _, err := DecodeOne(`"Ynl0ZXM="`, 0, &v); err != nil || string(v) != "bytes" {
				t.Errorf("r4ColdBytes03: err=%v v=%q", err, string(v))
			}
		},
		func(t *testing.T) {
			var v r4ColdBytes04
			if _, _, err := DecodeOne(`"Ynl0ZXM="`, 0, &v); err != nil || string(v) != "bytes" {
				t.Errorf("r4ColdBytes04: err=%v v=%q", err, string(v))
			}
		},
		func(t *testing.T) {
			var v r4ColdRaw01
			if _, _, err := DecodeOne(`{"k":1}`, 0, &v); err != nil || string(v.raw) != `{"k":1}` {
				t.Errorf("r4ColdRaw01: err=%v raw=%s", err, string(v.raw))
			}
		},
		func(t *testing.T) {
			var v r4ColdRaw02
			if _, _, err := DecodeOne(`[1,2]`, 0, &v); err != nil || string(v.raw) != `[1,2]` {
				t.Errorf("r4ColdRaw02: err=%v raw=%s", err, string(v.raw))
			}
		},
		func(t *testing.T) {
			var v r4ColdRaw03
			if _, _, err := DecodeOne(`"raw"`, 0, &v); err != nil || string(v.raw) != `"raw"` {
				t.Errorf("r4ColdRaw03: err=%v raw=%s", err, string(v.raw))
			}
		},
		func(t *testing.T) {
			var v r4ColdRaw04
			if _, _, err := DecodeOne(`true`, 0, &v); err != nil || string(v.raw) != `true` {
				t.Errorf("r4ColdRaw04: err=%v raw=%s", err, string(v.raw))
			}
		},
	}

	// T3: Build genuinely uncached struct types with a bounded generation counter so
	// repeated `-count=N` invocations stay truly cold (verified via rootPtrCache miss
	// before concurrent goroutines start) without unbounded reflect.Type retention.
	gen := r4ColdRunGen.Add(1)
	uniqueGen := gen
	if uniqueGen > r4MaxColdGenerations {
		uniqueGen = ((uniqueGen - 1) % r4MaxColdGenerations) + 1
	}
	dynamicStructTypes := make([]reflect.Type, 8)
	for i := range dynamicStructTypes {
		dynamicStructTypes[i] = reflect.StructOf([]reflect.StructField{
			{Name: fmt.Sprintf("FieldA_G%d_%d", uniqueGen, i), Type: reflect.TypeOf(int(0)), Tag: `json:"a"`},
			{Name: fmt.Sprintf("FieldB_G%d_%d", uniqueGen, i), Type: reflect.TypeOf(""), Tag: `json:"b"`},
			{Name: fmt.Sprintf("FieldC_G%d_%d", uniqueGen, i), Type: reflect.TypeOf([]byte(nil)), Tag: `json:"c"`},
		})
		if gen <= r4MaxColdGenerations {
			if cached, ok := rootPtrCache.Get(reflect.PtrTo(dynamicStructTypes[i])); ok && cached != nil {
				t.Fatalf("T3 expected genuinely cold uncached dynamic struct type for gen=%d i=%d", gen, i)
			}
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup

	// Readers hammering warmed *int / *string / *float64 / *bool (projScalarPlan) concurrently.
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for iter := 0; iter < 500; iter++ {
				var x int
				if _, _, err := DecodeOne("42", 0, &x); err != nil || x != 42 {
					t.Errorf("reader %d int: err=%v x=%d", id, err, x)
					return
				}
				var s string
				if _, _, err := DecodeOne(`"ok"`, 0, &s); err != nil || s != "ok" {
					t.Errorf("reader %d str: err=%v s=%q", id, err, s)
					return
				}
			}
		}(r)
	}

	// Cold named scalar / bytes / raw compilation goroutines.
	for _, fn := range coldTasks {
		fn := fn
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(t)
		}()
	}

	// Cold dynamic struct compilation goroutines (concurrent racing compile + decode on same & distinct types).
	for _, st := range dynamicStructTypes {
		st := st
		for dup := 0; dup < 2; dup++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ptr := reflect.New(st).Interface()
				if _, _, err := DecodeOne(`{"a":9,"b":"z","c":"Ynl0ZXM="}`, 0, ptr); err != nil {
					t.Errorf("cold dynamic struct %v failed: %v", st, err)
				}
			}()
		}
	}

	close(start)
	wg.Wait()

	after := captureSingletonSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("shared plan singleton mutated:\nbefore=%+v\nafter =%+v", before, after)
	}

	// T2: Directly exercise the shared-singleton clone branch via attachIdxRootPlan on ALL 4
	// package-init singletons (projFullPlan, projScalarPlan, projBytesPlan, projRawPlan),
	// verifying both nil idxPlan (returns original singleton untouched) and non-nil idxPlan
	// (returns private non-singleton shallow copy while leaving singletons immutable).
	synthIdx := &idxRootPlan{kind: idxRootSliceInt}
	for name, singleton := range map[string]*projPlan{
		"projFullPlan":        projFullPlan,
		"projScalarPlan":      projScalarPlan,
		"projTypedScalarPlan": projTypedScalarPlan,
		"projBytesPlan":       projBytesPlan,
		"projRawPlan":         projRawPlan,
	} {
		same := attachIdxRootPlan(singleton, nil)
		if same != singleton {
			t.Fatalf("T2 %s with nil idxPlan should return original singleton", name)
		}
		cloned := attachIdxRootPlan(singleton, synthIdx)
		if cloned == singleton || isSharedProjSingleton(cloned) || cloned.idxPlan != synthIdx || cloned.kind != singleton.kind {
			t.Fatalf("T2 %s with non-nil idxPlan failed clone contract: cloned=%+v singleton=%+v", name, cloned, singleton)
		}
		if singleton.idxPlan != nil {
			t.Fatalf("T2 %s singleton mutated: idxPlan=%+v", name, singleton.idxPlan)
		}
	}
	// Also verify real compileProjPlan(&sliceIntDecoder{}) returns a non-singleton wrapper (fresh projSlice/projFull) without touching singletons.
	p := compileProjPlan(&sliceIntDecoder{})
	if p == nil || p.idxPlan == nil || isSharedProjSingleton(p) {
		t.Fatalf("expected non-singleton root wrapper for sliceIntDecoder, got %+v", p)
	}
	afterWrapper := captureSingletonSnapshot()
	if !reflect.DeepEqual(before, afterWrapper) {
		t.Fatalf("shared plan singleton mutated after T2 singleton-clone and sliceIntDecoder compile")
	}
}
