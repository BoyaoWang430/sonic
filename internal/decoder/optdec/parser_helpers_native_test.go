//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import "unsafe"

func nativeParserUsed(p *Parser) int {
	return int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
}
