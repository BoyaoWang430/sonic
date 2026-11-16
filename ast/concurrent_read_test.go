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
	"sync"
	"testing"
	"time"
)

// TestConcurrentReadMarshalJSONWhileMaterializing races MarshalJSON on a
// ConcurrentRead raw node against Get calls that materialize it. Run with
// -race: the raw fast path must not read the node without the lock.
func TestConcurrentReadMarshalJSONWhileMaterializing(t *testing.T) {
	const src = `{"a":{"b":[1,2,3],"c":"x"},"d":true}`
	const want = `{"b":[1,2,3],"c":"x"}`
	s := NewSearcher(src)
	s.ConcurrentRead = true
	for round := 0; round < 100; round++ {
		node, err := s.GetByPath("a")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan string, 8)
		for i := 0; i < 4; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if v := node.Get("c"); v == nil || !v.Exists() {
					errs <- "Get(c) failed"
				}
			}()
			go func() {
				defer wg.Done()
				out, err := node.MarshalJSON()
				if err != nil || string(out) != want {
					errs <- "MarshalJSON = " + string(out)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for msg := range errs {
			t.Fatal(msg)
		}
	}
}

// TestConcurrentReadParseErrorDoesNotDeadlock checks that a ConcurrentRead
// node whose raw text fails to parse releases its lock, so goroutines that
// were waiting on it make progress and observe the syntax error.
func TestConcurrentReadParseErrorDoesNotDeadlock(t *testing.T) {
	// Public constructors validate the raw text up front, so build the
	// ConcurrentRead raw node directly to reach the lazy-parse error path.
	const src = `{"b":1,,"c":2}`
	n := newRawNode(src, switchRawType(src[0]), true)
	node := &n
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = node.Get("b")
				_, _ = node.Raw()
				_, _ = node.MarshalJSON()
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent readers deadlocked after a parse error")
	}
	if node.Check() == nil {
		t.Fatal("expected the node to report its syntax error")
	}
}
