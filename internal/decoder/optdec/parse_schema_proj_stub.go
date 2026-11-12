//go:build !((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import "unsafe"

func parseWithSchemaOrFull(p *Parser, _ *projPlan) ErrorCode {
	return p.parse()
}

func tryDecodeFlatStruct(_ string, _ int, _ uint64, _ unsafe.Pointer, _ *structDecoder, _, _ bool) (int, bool) {
	return 0, false
}
