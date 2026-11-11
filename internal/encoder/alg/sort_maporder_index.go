package alg

import "github.com/bytedance/sonic/internal/encoder/vars"

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

// SortMapKVOrder sorts index slice order in ascending key order of entries without moving MapKVEntry elements.
func SortMapKVOrder(order []int, entries []vars.MapKVEntry) {
	n := len(order)
	if n <= 1 {
		return
	}
	if n == 2 {
		if entries[order[1]].K < entries[order[0]].K {
			order[0], order[1] = order[1], order[0]
		}
		return
	}
	if n <= 11 {
		insertMapKVOrder(order, entries, 0)
		return
	}
	radixSortMapKVOrder(order, entries, 0, maxDepth(n))
}

func radixSortMapKVOrder(kvs []int, pairs []vars.MapKVEntry, d, maxDepth int) {
	for len(kvs) > 11 {
		if maxDepth == 0 {
			heapMapKVOrder(kvs, pairs, 0, len(kvs))
			return
		}
		maxDepth--

		p := pivotMapKVOrder(kvs, pairs, d)
		lt, i, gt := 0, 0, len(kvs)
		for i < gt {
			c := byteAt(pairs[kvs[i]].K, d)
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

		if p == -1 {
			if lt > len(kvs)-gt {
				radixSortMapKVOrder(kvs[gt:], pairs, d, maxDepth)
				kvs = kvs[:lt]
			} else {
				radixSortMapKVOrder(kvs[:lt], pairs, d, maxDepth)
				kvs = kvs[gt:]
			}
		} else {
			ml := maxThree(lt, gt-lt, len(kvs)-gt)
			if ml == lt {
				radixSortMapKVOrder(kvs[lt:gt], pairs, d+1, maxDepth)
				radixSortMapKVOrder(kvs[gt:], pairs, d, maxDepth)
				kvs = kvs[:lt]
			} else if ml == gt-lt {
				radixSortMapKVOrder(kvs[:lt], pairs, d, maxDepth)
				radixSortMapKVOrder(kvs[gt:], pairs, d, maxDepth)
				kvs = kvs[lt:gt]
				d++
			} else {
				radixSortMapKVOrder(kvs[:lt], pairs, d, maxDepth)
				radixSortMapKVOrder(kvs[lt:gt], pairs, d+1, maxDepth)
				kvs = kvs[gt:]
			}
		}
	}
	insertMapKVOrder(kvs, pairs, d)
}

func insertMapKVOrder(kvs []int, pairs []vars.MapKVEntry, d int) {
	for i := 1; i < len(kvs); i++ {
		for j := i; j > 0 && lessFrom(pairs[kvs[j]].K, pairs[kvs[j-1]].K, d); j-- {
			swapMapOrder(kvs, j, j-1)
		}
	}
}

func pivotMapKVOrder(kvs []int, pairs []vars.MapKVEntry, d int) int {
	m := len(kvs) >> 1
	if len(kvs) > 40 {
		t := len(kvs) / 8
		return medianThree(
			medianThree(byteAt(pairs[kvs[0]].K, d), byteAt(pairs[kvs[t]].K, d), byteAt(pairs[kvs[2*t]].K, d)),
			medianThree(byteAt(pairs[kvs[m]].K, d), byteAt(pairs[kvs[m-t]].K, d), byteAt(pairs[kvs[m+t]].K, d)),
			medianThree(byteAt(pairs[kvs[len(kvs)-1]].K, d),
				byteAt(pairs[kvs[len(kvs)-1-t]].K, d),
				byteAt(pairs[kvs[len(kvs)-1-2*t]].K, d)))
	}
	return medianThree(byteAt(pairs[kvs[0]].K, d), byteAt(pairs[kvs[m]].K, d), byteAt(pairs[kvs[len(kvs)-1]].K, d))
}

func siftMapKVOrder(kvs []int, pairs []vars.MapKVEntry, lo, hi, first int) {
	root := lo
	for {
		child := 2*root + 1
		if child >= hi {
			break
		}
		if child+1 < hi && pairs[kvs[first+child]].K < pairs[kvs[first+child+1]].K {
			child++
		}
		if pairs[kvs[first+root]].K >= pairs[kvs[first+child]].K {
			return
		}
		swapMapOrder(kvs, first+root, first+child)
		root = child
	}
}

func heapMapKVOrder(kvs []int, pairs []vars.MapKVEntry, a, b int) {
	first := a
	lo := 0
	hi := b - a
	for i := (hi - 1) / 2; i >= 0; i-- {
		siftMapKVOrder(kvs, pairs, i, hi, first)
	}
	for i := hi - 1; i >= 0; i-- {
		swapMapOrder(kvs, first, first+i)
		siftMapKVOrder(kvs, pairs, lo, i, first)
	}
}
