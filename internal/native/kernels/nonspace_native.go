//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"github.com/bytedance/sonic/internal/rt"
)

// NonSpaceMask classifies at most 64 bytes from src. Bit i is set
// exactly when src[i] is not JSON whitespace. Bits beyond width are zero.
// The input needs no readable padding.
func NonSpaceMask(src string) (mask uint64, width int) {
	width = len(src)
	if width > 64 {
		width = 64
	}
	return nonSpaceMask(rt.StringData(src), width), width
}

//go:noescape
func nonSpaceMask(src *byte, size int) (mask uint64)
