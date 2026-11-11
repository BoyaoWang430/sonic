/*
 * Copyright 2021 ByteDance Inc.
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

package caching

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPcacheSingleflightDedup(t *testing.T) {
	pc := CreateProgramCache()
	type sample struct{ A int }
	vt := reflect.TypeOf(sample{})

	var calls int32
	enter := make(chan struct{})
	release := make(chan struct{})

	const workers = 16
	var wg sync.WaitGroup
	results := make([]interface{}, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			val, err := pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
				if atomic.AddInt32(&calls, 1) == 1 {
					close(enter)
				}
				<-release
				v := new(int)
				*v = 42
				return v, nil
			})
			if err != nil {
				t.Errorf("unexpected err: %v", err)
			}
			results[idx] = val
		}()
	}

	<-enter
	time.Sleep(5 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 compute call, got %d", got)
	}
	for i := 1; i < workers; i++ {
		if results[i] != results[0] {
			t.Fatalf("worker %d got distinct result pointer", i)
		}
	}
}

func TestPcacheDistinctTypesParallel(t *testing.T) {
	pc := CreateProgramCache()
	type typeA struct{ A int }
	type typeB struct{ B string }
	vtA := reflect.TypeOf(typeA{})
	vtB := reflect.TypeOf(typeB{})

	enteredA := make(chan struct{})
	enteredB := make(chan struct{})
	done := make(chan error, 2)

	go func() {
		_, err := pc.Compute(vtA, func(reflect.Type, ...interface{}) (interface{}, error) {
			close(enteredA)
			<-enteredB
			return "progA", nil
		})
		done <- err
	}()

	go func() {
		_, err := pc.Compute(vtB, func(reflect.Type, ...interface{}) (interface{}, error) {
			close(enteredB)
			<-enteredA
			return "progB", nil
		})
		done <- err
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("distinct types serialized and deadlocked inside ProgramCache.Compute")
		}
	}
	if pc.Get(vtA) != "progA" || pc.Get(vtB) != "progB" {
		t.Fatalf("unexpected cached values: %v, %v", pc.Get(vtA), pc.Get(vtB))
	}
}

func TestPcachePanicAndResetSafety(t *testing.T) {
	pc := CreateProgramCache()
	type panicType struct{ X int }
	vt := reflect.TypeOf(panicType{})

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic")
			}
		}()
		_, _ = pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
			panic("boom")
		})
	}()

	// Subsequent Compute on the same type must succeed without hanging.
	val, err := pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
		return "recovered", nil
	})
	if err != nil || val != "recovered" {
		t.Fatalf("expected recovered value, got %v, %v", val, err)
	}

	// Reset while an old Compute is in-flight must isolate epochs cleanly.
	type resetType struct{ Y int }
	vtR := reflect.TypeOf(resetType{})
	inOld := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan interface{}, 1)
	go func() {
		v, _ := pc.Compute(vtR, func(reflect.Type, ...interface{}) (interface{}, error) {
			close(inOld)
			<-releaseOld
			return "stale", nil
		})
		oldDone <- v
	}()
	<-inOld
	pc.Reset()

	fresh, err := pc.Compute(vtR, func(reflect.Type, ...interface{}) (interface{}, error) {
		return "fresh", nil
	})
	if err != nil || fresh != "fresh" {
		t.Fatalf("expected fresh value, got %v, %v", fresh, err)
	}
	close(releaseOld)
	<-oldDone
	if got := pc.Get(vtR); got != "fresh" {
		t.Fatalf("stale compute overwrote post-Reset cache: got %v", got)
	}
}

func TestPcacheRace(t *testing.T) {
	t.Parallel()

	pc := CreateProgramCache()
	wg := sync.WaitGroup{}
	wg.Add(2)
	start := make(chan struct{}, 2)

	go func() {
		defer wg.Done()
		var k = map[string]interface{}{}
		vt := reflect.TypeOf(k)
		<-start
		for i := 0; i < 100; i++ {
			_, _ = pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
				return map[string]interface{}{}, nil
			})
		}
	}()

	go func() {
		defer wg.Done()
		var k = map[string]interface{}{}
		vt := reflect.TypeOf(k)
		<-start
		for i := 0; i < 100; i++ {
			pc.Get(vt)
		}
	}()

	start <- struct{}{}
	start <- struct{}{}
	wg.Wait()
}

var (
	coldBenchTypesOnce sync.Once
	coldBenchTypes300  []reflect.Type
)

func getColdBenchTypes300() []reflect.Type {
	coldBenchTypesOnce.Do(func() {
		coldBenchTypes300 = make([]reflect.Type, 300)
		for i := range coldBenchTypes300 {
			coldBenchTypes300[i] = reflect.StructOf([]reflect.StructField{
				{
					Name: fmt.Sprintf("F_%d", i),
					Type: reflect.TypeOf(int64(0)),
				},
			})
		}
	})
	return coldBenchTypes300
}

func TestPcacheStagedCompactionAndVisibility(t *testing.T) {
	types := getColdBenchTypes300()
	pc := CreateProgramCache()

	// Sequential cold start across 300 types + immediate Get verification.
	for i, vt := range types {
		want := i + 1
		got, err := pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
			return want, nil
		})
		if err != nil || got != want {
			t.Fatalf("Compute(%d) = (%v, %v), want (%v, nil)", i, got, err, want)
		}
		if cached := pc.Get(vt); cached != want {
			t.Fatalf("immediate Get(%d) = %v, want %v", i, cached, want)
		}
	}

	//Repeated reads on trailing staged entries must promote cleanly without losing any entry.
	for round := 0; round < 200; round++ {
		vt := types[len(types)-1]
		if cached := pc.Get(vt); cached != len(types) {
			t.Fatalf("round %d trailing Get = %v, want %v", round, cached, len(types))
		}
	}
	for i, vt := range types {
		if cached := pc.Get(vt); cached != i+1 {
			t.Fatalf("post-promotion Get(%d) = %v, want %v", i, cached, i+1)
		}
	}

	// Concurrent Compute + concurrent Get + Reset stress test.
	pc.Reset()
	for i, vt := range types {
		if cached := pc.Get(vt); cached != nil {
			t.Fatalf("after Reset Get(%d) = %v, want nil", i, cached)
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for step := 0; step < len(types); step++ {
				idx := (step + worker*37) % len(types)
				vt := types[idx]
				want := idx + 1000
				if step%3 == 0 {
					if v := pc.Get(vt); v != nil && v != want {
						t.Errorf("concurrent Get(%d) = %v, want %v", idx, v, want)
					}
				}
				got, err := pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
					return want, nil
				})
				if err != nil || got != want {
					t.Errorf("concurrent Compute(%d) = (%v, %v), want (%v, nil)", idx, got, err, want)
				}
				if v := pc.Get(vt); v != want {
					t.Errorf("post-Compute concurrent Get(%d) = %v, want %v", idx, v, want)
				}
			}
		}(w)
	}
	wg.Wait()
}

func BenchmarkPcacheColdStart300(b *testing.B) {
	types := getColdBenchTypes300()
	pc := CreateProgramCache()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc.Reset()
		for k, vt := range types {
			val := k + 1
			_, _ = pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
				return val, nil
			})
		}
	}
}

func BenchmarkPcacheWarmGet(b *testing.B) {
	types := getColdBenchTypes300()
	pc := CreateProgramCache()
	for k, vt := range types {
		val := k + 1
		_, _ = pc.Compute(vt, func(reflect.Type, ...interface{}) (interface{}, error) {
			return val, nil
		})
	}
	// Warm up any trailing staged items so steady-state read path is measured.
	for i := 0; i < 256; i++ {
		_ = pc.Get(types[len(types)-1])
	}
	vt := types[123]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if pc.Get(vt) == nil {
			b.Fatal("unexpected nil")
		}
	}
}
