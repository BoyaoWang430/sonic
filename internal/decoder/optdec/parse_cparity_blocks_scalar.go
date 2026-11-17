//go:build !sonic_compat && (sonic_scalar || go1.28) && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import "unsafe"

func cParityParseRunsAsm(kern *ScanKernelState, state *cParityRunsState, rawBase *byte, nodesBase *node) (event int) {
	raw := unsafe.Slice(rawBase, kern.end+len(padding))
	nodes := unsafe.Slice(nodesBase, kern.nbuf.ncap)
	return cParityParseRunsScalar(kern, state, raw, nodes)
}

func cParityNonspace64(src unsafe.Pointer) (mask uint64) {
	return cParityNonspace64Scalar(src)
}

func cParityStringMasks32(src unsafe.Pointer, opts uint64) (bs, quote, control uint32) {
	return cParityStringMasks32Scalar(src, opts)
}

func cParityNumberMasks16(src unsafe.Pointer) (decimal, exponent, sign, valid uint32) {
	return cParityNumberMasks16Scalar(src)
}
