//go:build !sonic_compat && (sonic_scalar || go1.28) && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

func quoteVectorBytes() int        { return 8 }
func spaceVectorBytes() int        { return 8 }
func stringDecodeVectorBytes() int { return 8 }
func stringScanVectorBytes() int   { return 8 }
func utf8VectorBytes() int         { return 8 }
