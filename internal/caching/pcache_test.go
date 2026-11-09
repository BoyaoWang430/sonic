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
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic/internal/rt"
)

func TestPcacheSingleflightDedup(t *testing.T) {
	pc := CreateProgramCache()
	type sample struct{ A int }
	vt := rt.UnpackType(reflect.TypeOf(sample{}))

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
			val, err := pc.Compute(vt, func(*rt.GoType, ...interface{}) (interface{}, error) {
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
	vtA := rt.UnpackType(reflect.TypeOf(typeA{}))
	vtB := rt.UnpackType(reflect.TypeOf(typeB{}))

	enteredA := make(chan struct{})
	enteredB := make(chan struct{})
	done := make(chan error, 2)

	go func() {
		_, err := pc.Compute(vtA, func(*rt.GoType, ...interface{}) (interface{}, error) {
			close(enteredA)
			<-enteredB
			return "progA", nil
		})
		done <- err
	}()

	go func() {
		_, err := pc.Compute(vtB, func(*rt.GoType, ...interface{}) (interface{}, error) {
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
	vt := rt.UnpackType(reflect.TypeOf(panicType{}))

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic")
			}
		}()
		_, _ = pc.Compute(vt, func(*rt.GoType, ...interface{}) (interface{}, error) {
			panic("boom")
		})
	}()

	// Subsequent Compute on the same type must succeed without hanging.
	val, err := pc.Compute(vt, func(*rt.GoType, ...interface{}) (interface{}, error) {
		return "recovered", nil
	})
	if err != nil || val != "recovered" {
		t.Fatalf("expected recovered value, got %v, %v", val, err)
	}

	// Reset while an old Compute is in-flight must isolate epochs cleanly.
	type resetType struct{ Y int }
	vtR := rt.UnpackType(reflect.TypeOf(resetType{}))
	inOld := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan interface{}, 1)
	go func() {
		v, _ := pc.Compute(vtR, func(*rt.GoType, ...interface{}) (interface{}, error) {
			close(inOld)
			<-releaseOld
			return "stale", nil
		})
		oldDone <- v
	}()
	<-inOld
	pc.Reset()

	fresh, err := pc.Compute(vtR, func(*rt.GoType, ...interface{}) (interface{}, error) {
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
		<-start
		for i := 0; i < 100; i++ {
			_, _ = pc.Compute(rt.UnpackEface(k).Type, func(*rt.GoType, ...interface{}) (interface{}, error) {
				return map[string]interface{}{}, nil
			})
		}
	}()

	go func() {
		defer wg.Done()
		var k = map[string]interface{}{}
		<-start
		for i := 0; i < 100; i++ {
			pc.Get(rt.UnpackEface(k).Type)
		}
	}()

	start <- struct{}{}
	start <- struct{}{}
	wg.Wait()
}
