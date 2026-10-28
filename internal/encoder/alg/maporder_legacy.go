//go:build !riscv64 || !go1.26 || go1.28 || !riscv64.rva22u64

package alg

type platformMapOrder struct{}

func (p *MapIterator) sortMapPairs() {
	if p.kv.Len > 1 {
		radixQsort(p.data(), 0, maxDepth(p.kv.Len))
	}
}

func (p *MapIterator) orderedMapPair(i int) *_MapPair { return p.at(i) }
