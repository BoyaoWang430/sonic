package alg

// Sort indices to avoid pointer write barriers and keep pair storage fixed.
type platformMapOrder struct{ order []int }

func (p *MapIterator) sortMapPairs() {
	n := len(p.kvs)
	if cap(p.order) < n {
		p.order = make([]int, n)
	} else {
		p.order = p.order[:n]
	}
	for i := range p.order {
		p.order[i] = i
	}
	radixSortMapOrder(p.order, p.data(), 0, maxDepth(n))
}

func (p *MapIterator) orderedMapPair(i int) *_MapPair { return p.at(p.order[i]) }
