//go:build !goexperiment.jsonv2 && !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

const diffStdUsesJSONV2 = false
const diffStdJSONV2Pre1273 = false
