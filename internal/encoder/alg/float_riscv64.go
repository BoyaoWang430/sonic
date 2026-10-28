//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package alg

import "github.com/bytedance/sonic/internal/native/number"

// The RISC-V formatter already appends to a Go slice. Keep that slice through
// the caller instead of reserving a native output region and rebuilding it.
// Preserve the existing encoder policy that represents either zero as "0".
func F64toa(buf []byte, v float64) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	return number.AppendFloat(buf, v, 64)
}

func F32toa(buf []byte, v float32) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	return number.AppendFloat(buf, float64(v), 32)
}
