//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package alg

import "github.com/bytedance/sonic/internal/native/number"

// The Go formatter already appends to a Go slice. Keep that slice through
// the caller instead of reserving a native output region and rebuilding it.
// Preserve the existing encoder policy that represents either zero as "0".
func F64toa(buf []byte, v float64) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	return number.AppendFloat64(buf, v)
}

func F32toa(buf []byte, v float32) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	return number.AppendFloat32(buf, v)
}
