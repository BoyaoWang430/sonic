//go:build !sonic_compat && (sonic_scalar || go1.28) && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import "github.com/bytedance/sonic/internal/native/types"

func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int) {
	return htmlEscapeScalar(src, size, dst, capacity)
}

func nonSpaceMask(src *byte, size int) (mask uint64) {
	return nonSpaceMaskScalar(src, size)
}

func numberScan(src *byte, size int, mode uint64) (end int, err types.ParsingError) {
	return numberScanScalar(src, size, mode)
}

func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64) {
	return pathMasksScalar(src, size)
}

func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int) {
	return quoteScalar(src, size, dst, capacity, flags)
}

func quoteHTML(src *byte, size int, dst *byte, capacity int) (consumed, written int) {
	return quoteHTMLScalar(src, size, dst, capacity)
}

func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError) {
	return scanStringRelaxedScalar(src, size)
}

func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError) {
	return scanStringStrictScalar(src, size)
}

func scanStringRelaxedBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	return scanStringRelaxedBlockScalar(src, size)
}

func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	return scanStringStrictBlockScalar(src, size)
}

func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError) {
	return skipContainerScalar(src, size, open)
}

func space(src *byte, size int) (n int) {
	return spaceScalar(src, size)
}

func stringDecode(src *byte, size int, dst *byte, mode uint64) (end, written, escaped, errorPos, code int) {
	return stringDecodeScalar(src, size, dst, mode)
}

func traversalMasks(src *byte, size int, validate uint64) (events, nonspace uint64) {
	return traversalMasksScalar(src, size, validate)
}

func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool) {
	return utf8ScanScalar(src, size, base, positions, limit)
}
