//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package alg

import "slices"

// Sort indices to avoid pointer write barriers and keep pair storage fixed.
type platformMapOrder struct{ order []int }

func (p *MapIterator) sortMapPairs() {
	n := p.kv.Len
	p.order = slices.Grow(p.order[:0], n)[:n]
	for i := range p.order {
		p.order[i] = i
	}
	radixSortMapOrder(p.order, p.data(), 0, maxDepth(n))
}

func (p *MapIterator) orderedMapPair(i int) *_MapPair { return p.at(p.order[i]) }
