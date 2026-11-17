//go:build !race && !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package kernels

func raceRead(_ *byte, _ int)  {}
func raceWrite(_ *byte, _ int) {}
