//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package alg

// Sort indices to avoid pointer write barriers and keep pair storage fixed.
type platformMapOrder struct{ order []int }

func (p *MapIterator) sortMapPairs() {
	n := p.kv.Len
	// Go 1.20 ARM64 builds cannot use slices.Grow; old indices are overwritten.
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
