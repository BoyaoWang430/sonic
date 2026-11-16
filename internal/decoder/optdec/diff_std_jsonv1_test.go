//go:build !goexperiment.jsonv2 && !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

const diffStdUsesJSONV2 = false
const diffStdJSONV2Pre1273 = false
