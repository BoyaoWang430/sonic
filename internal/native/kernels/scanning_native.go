//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
)

// scanString starts after the opening quote and scans a complete string in one
// kernel invocation. Strict mode checks controls and escape syntax inside that
// same scan. Both modes read only src[:size] and return relative offsets.
func scanString(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError) {
	if cpu.UseScalar {
		return scanStringScalar(src, size, validate != 0)
	}
	if validate == 0 {
		return scanStringRelaxed(src, size)
	}
	return scanStringStrict(src, size)
}

func scanStringBlock(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	if cpu.UseScalar {
		return scanStringBlockScalar(src, size, validate != 0)
	}
	if validate == 0 {
		return scanStringRelaxedBlock(src, size)
	}
	return scanStringStrictBlock(src, size)
}
