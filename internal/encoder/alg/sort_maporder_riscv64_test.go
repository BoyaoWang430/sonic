//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package alg

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"unsafe"
)

func TestMapOrder(t *testing.T) {
	for _, n := range []int{0, 1, 2, 11, 12, 40, 41, 127, 1024} {
		for _, heap := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/heap=%v", n, heap), func(t *testing.T) {
				rng := rand.New(rand.NewSource(1))
				pairs := make([]_MapPair, n)
				ids := make([]int, n)
				order := make([]int, n)
				want := make([]string, n)
				for i := range pairs {
					ids[i], order[i] = i, i
					key := strings.Repeat("same-prefix/", i%6) + fmt.Sprint(rng.Intn(43))
					if i%7 == 0 {
						key = ""
					} else if i%11 == 0 {
						key = "\xff\x00unicode-你好"
					}
					pairs[i].k, pairs[i].v = key, unsafe.Pointer(&ids[i])
					want[i] = key
				}
				before := append([]_MapPair(nil), pairs...)
				legacy := append([]_MapPair(nil), pairs...)
				sort.Strings(want)
				depth := maxDepth(n)
				if heap {
					depth = 0
				}
				radixQsort(legacy, 0, depth)
				radixSortMapOrder(order, pairs, 0, depth)
				seen := make([]bool, n)
				for i, position := range order {
					if position < 0 || position >= n || seen[position] || pairs[position].k != want[i] {
						t.Fatalf("bad permutation/order at %d: %v", i, order)
					}
					seen[position] = true
					if pairs[position].k != legacy[i].k || pairs[position].v != legacy[i].v {
						t.Fatalf("pair %d differs from original radix sort", i)
					}
					if pairs[i] != before[i] {
						t.Fatalf("pair %d moved or changed while sorting indices", i)
					}
				}
			})
		}
	}
}
