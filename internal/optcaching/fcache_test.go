package caching

import (
	"math/rand"
	"strings"
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
		"Name", "name", "NAME", "ID", "", "K", "Äpfel", "Σ", "İ", "\ufffd",
		strings.Repeat("x", 63), strings.Repeat("y", 64), strings.Repeat("z", 65),
	}
	f := fieldCacheForNames(names)
	queries := append([]string{}, names...)
	queries = append(queries, "nAmE", "id", "unknown", "äPFEL", "σ", "ς", "K", "i", "\xff")
	for _, name := range names {
		queries = append(queries, strings.ToUpper(name), strings.ToLower(name))
	}
	for _, query := range queries {
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

var fieldCacheResult int

func BenchmarkFieldCache(b *testing.B) {
	for _, tc := range []struct{ name, field, query string }{
		{"Exact", "created_at", "created_at"},
		{"FoldASCII", "created_at", "CREATED_AT"},
		{"Fold64", strings.Repeat("a", 64), strings.Repeat("A", 64)},
		{"Fold65", strings.Repeat("a", 65), strings.Repeat("A", 65)},
		{"FoldUnicode", "älpha", "ÄLPHA"},
		{"Miss", "created_at", "UNKNOWN"},
	} {
		f := fieldCacheForNames([]string{tc.field})
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				fieldCacheResult = f.Get(tc.query, false)
			}
		})
	}
}
