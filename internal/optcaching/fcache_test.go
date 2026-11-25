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

package caching

import (
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic/internal/resolver"
)

func fieldCacheForNames(names []string) *FieldCache {
	fields := make([]resolver.FieldMeta, len(names))
	for i, name := range names {
		fields[i].Name = name
	}
	return NewFieldCache(fields)
}

func TestFieldCacheCaseMatching(t *testing.T) {
	names := []string{
		"Name", "name", "NAME", "ID", "", "K", "s", "ſ", "Äpfel", "Σ", "İ", "\ufffd",
		"fookbar99", "fooKbar12",
		strings.Repeat("x", 63), strings.Repeat("y", 64), strings.Repeat("z", 65),
	}
	f := fieldCacheForNames(names)
	queries := append([]string{}, names...)
	queries = append(
		queries,
		"nAmE", "id", "unknown", "unknown_long_ascii_key",
		"k", "K", "S", "ſ", "äPFEL", "σ", "ς", "i", "\xff", "\x80bad",
		"FOOKBAR99", "fooKbar99", "fookbar12", "FOOKBAR12",
	)
	for _, name := range names {
		queries = append(queries, strings.ToUpper(name), strings.ToLower(name))
	}
	for _, query := range queries {
		pureASCII := isPureASCII(query)
		for _, sensitive := range []bool{false, true} {
			want := -1
			// Exact spelling has priority over all case-insensitive matches.
			for i, name := range names {
				if name == query {
					want = i
				}
			}
			if want == -1 && !sensitive {
				for i, name := range names {
					if strings.ToLower(name) == strings.ToLower(query) {
						want = i
						break
					}
				}
			}
			if got := f.Get(query, sensitive); got != want {
				t.Errorf("Get(%q, %v) = %d, want %d", query, sensitive, got, want)
			}
			if pureASCII {
				if got := f.GetKnownASCII(query, sensitive, true); got != want {
					t.Errorf("GetKnownASCII(%q, %v, true) = %d, want %d", query, sensitive, got, want)
				}
			}
		}
	}

	// Pure-ASCII struct cache must still match or reject non-ASCII queries identically to strings.ToLower.
	asciiOnly := fieldCacheForNames([]string{"k", "s", "long_ascii_key", "Alpha", "ALPHA"})
	for _, query := range []string{"k", "K", "K", "s", "S", "ſ", "alpha", "ALPHA", "aLpHa", "miss", "long_K_key", "\xff"} {
		pureASCII := isPureASCII(query)
		for _, sensitive := range []bool{false, true} {
			want := -1
			for i, name := range []string{"k", "s", "long_ascii_key", "Alpha", "ALPHA"} {
				if name == query {
					want = i
				}
			}
			if want == -1 && !sensitive {
				for i, name := range []string{"k", "s", "long_ascii_key", "Alpha", "ALPHA"} {
					if strings.ToLower(name) == strings.ToLower(query) {
						want = i
						break
					}
				}
			}
			if got := asciiOnly.Get(query, sensitive); got != want {
				t.Errorf("asciiOnly.Get(%q, %v) = %d, want %d", query, sensitive, got, want)
			}
			if pureASCII {
				if got := asciiOnly.GetKnownASCII(query, sensitive, true); got != want {
					t.Errorf("asciiOnly.GetKnownASCII(%q, %v, true) = %d, want %d", query, sensitive, got, want)
				}
			}
		}
	}
}

func TestFieldCacheFoldedLookupReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []byte("aAzZ09_-\x00\x7f\x80\xc3\xa4\xff")
	for n := 0; n < 2000; n++ {
		name := make([]byte, rng.Intn(96))
		for i := range name {
			name[i] = alphabet[rng.Intn(len(alphabet))]
		}
		query := string(name)
		f := fieldCacheForNames([]string{"other", strings.ToLower(query)})
		if got := f.Get(query, false); got != 1 {
			t.Fatalf("folded lookup %q = %d, want 1", query, got)
		}
	}
}

func TestFieldCacheShortASCIIFoldAllocations(t *testing.T) {
	f := fieldCacheForNames([]string{"created_at", strings.Repeat("a", 64)})
	for _, query := range []string{"CREATED_AT", strings.Repeat("A", 64), "NOT_FOUND"} {
		if n := testing.AllocsPerRun(100, func() { f.Get(query, false) }); n != 0 {
			t.Errorf("Get(%q) allocated %g times", query, n)
		}
	}
}

func TestFieldCacheReadOnlyConcurrentColdAccess(t *testing.T) {
	// Freshly constructed FieldCache has folded indices prebuilt and performs
	// zero writes under concurrent first-use case-insensitive queries (verified under -race).
	f := fieldCacheForNames([]string{"Alpha", "ALPHA", "beta_long_field_name", "Σigma"})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if got := f.Get("ALPHA", false); got != 1 {
					t.Errorf("exact priority failed: got %d", got)
				}
				if got := f.Get("alpha", false); got != 0 {
					t.Errorf("folded conflict order failed: got %d", got)
				}
				if got := f.Get("BETA_LONG_FIELD_NAME", false); got != 2 {
					t.Errorf("long folded match failed: got %d", got)
				}
				if got := f.Get("σIGMA", false); got != 3 {
					t.Errorf("unicode folded match failed: got %d", got)
				}
			}
		}()
	}
	wg.Wait()
}
