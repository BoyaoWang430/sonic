//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

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

package alg

// Apply radixQsort to indices, leaving key/value pairs in place.

func radixSortMapOrder(kvs []int, pairs []_MapPair, d, maxDepth int) {
	for len(kvs) > 11 {
		// Limit worst-case sorting to O(n log n).
		if maxDepth == 0 {
			heapMapOrder(kvs, pairs, 0, len(kvs))
			return
		}
		maxDepth--

		p := pivotMapOrder(kvs, pairs, d)
		lt, i, gt := 0, 0, len(kvs)
		for i < gt {
			c := byteAt(pairs[kvs[i]].k, d)
			if c < p {
				swapMapOrder(kvs, lt, i)
				i++
				lt++
			} else if c > p {
				gt--
				swapMapOrder(kvs, i, gt)
			} else {
				i++
			}
		}

		// Recurse into smaller partitions to bound stack use.
		if p == -1 {
			if lt > len(kvs)-gt {
				radixSortMapOrder(kvs[gt:], pairs, d, maxDepth)
				kvs = kvs[:lt]
			} else {
				radixSortMapOrder(kvs[:lt], pairs, d, maxDepth)
				kvs = kvs[gt:]
			}
		} else {
			ml := maxThree(lt, gt-lt, len(kvs)-gt)
			if ml == lt {
				radixSortMapOrder(kvs[lt:gt], pairs, d+1, maxDepth)
				radixSortMapOrder(kvs[gt:], pairs, d, maxDepth)
				kvs = kvs[:lt]
			} else if ml == gt-lt {
				radixSortMapOrder(kvs[:lt], pairs, d, maxDepth)
				radixSortMapOrder(kvs[gt:], pairs, d, maxDepth)
				kvs = kvs[lt:gt]
				d += 1
			} else {
				radixSortMapOrder(kvs[:lt], pairs, d, maxDepth)
				radixSortMapOrder(kvs[lt:gt], pairs, d+1, maxDepth)
				kvs = kvs[gt:]
			}
		}
	}
	insertMapOrder(kvs, pairs, d)
}

func insertMapOrder(kvs []int, pairs []_MapPair, d int) {
	for i := 1; i < len(kvs); i++ {
		for j := i; j > 0 && lessFrom(pairs[kvs[j]].k, pairs[kvs[j-1]].k, d); j-- {
			swapMapOrder(kvs, j, j-1)
		}
	}
}

func pivotMapOrder(kvs []int, pairs []_MapPair, d int) int {
	m := len(kvs) >> 1
	if len(kvs) > 40 {
		// Tukey's ninther: median of three medians.
		t := len(kvs) / 8
		return medianThree(
			medianThree(byteAt(pairs[kvs[0]].k, d), byteAt(pairs[kvs[t]].k, d), byteAt(pairs[kvs[2*t]].k, d)),
			medianThree(byteAt(pairs[kvs[m]].k, d), byteAt(pairs[kvs[m-t]].k, d), byteAt(pairs[kvs[m+t]].k, d)),
			medianThree(byteAt(pairs[kvs[len(kvs)-1]].k, d),
				byteAt(pairs[kvs[len(kvs)-1-t]].k, d),
				byteAt(pairs[kvs[len(kvs)-1-2*t]].k, d)))
	}
	return medianThree(byteAt(pairs[kvs[0]].k, d), byteAt(pairs[kvs[m]].k, d), byteAt(pairs[kvs[len(kvs)-1]].k, d))
}

func siftMapOrder(kvs []int, pairs []_MapPair, lo, hi, first int) {
	root := lo
	for {
		child := 2*root + 1
		if child >= hi {
			break
		}
		if child+1 < hi && pairs[kvs[first+child]].k < pairs[kvs[first+child+1]].k {
			child++
		}
		if pairs[kvs[first+root]].k >= pairs[kvs[first+child]].k {
			return
		}
		swapMapOrder(kvs, first+root, first+child)
		root = child
	}
}

func heapMapOrder(kvs []int, pairs []_MapPair, a, b int) {
	first := a
	lo := 0
	hi := b - a

	// Build heap with the greatest element at top.
	for i := (hi - 1) / 2; i >= 0; i-- {
		siftMapOrder(kvs, pairs, i, hi, first)
	}

	// Pop elements, the largest first, into end of kvs.
	for i := hi - 1; i >= 0; i-- {
		swapMapOrder(kvs, first, first+i)
		siftMapOrder(kvs, pairs, lo, i, first)
	}
}

func swapMapOrder(kvs []int, a, b int) { kvs[a], kvs[b] = kvs[b], kvs[a] }
