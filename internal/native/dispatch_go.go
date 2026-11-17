//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package native

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
)

const BufPaddingSize = types.BufPaddingSize

// Architecture build constraints select ARM64 NEON or RISC-V scalar/RVV kernels.
// Assembly entries use the normal Go ABI0 calling convention.
// Kernels use bounded accesses without padding; scalar loads may be unaligned.
func Quote(s unsafe.Pointer, nb int, dp unsafe.Pointer, dn *int, flags uint64) int {
	consumed, written := kernels.QuoteBuffer((*byte)(s), nb, (*byte)(dp), *dn, flags)
	*dn = written
	return consumed
}

func HTMLEscape(s unsafe.Pointer, nb int, dp unsafe.Pointer, dn *int) int {
	consumed, written := kernels.HTMLEscapeBuffer((*byte)(s), nb, (*byte)(dp), *dn)
	*dn = written
	return consumed
}

func Unquote(s unsafe.Pointer, nb int, dp unsafe.Pointer, ep *int, flags uint64) int {
	written, pos, err := kernels.UnquoteBuffer((*byte)(s), nb, (*byte)(dp), flags)
	if err != 0 {
		*ep = pos
		return -int(err)
	}
	return written
}

func ValidateUTF8(s *string, p *int, m *types.StateMachine) int {
	next, count, full := kernels.InvalidUTF8(*s, *p, m.Vt[m.Sp:])
	*p = next
	m.Sp += count
	if full {
		return -1
	}
	return 0
}

func ValidateUTF8Fast(s *string) int {
	next, _, full := kernels.InvalidUTF8(*s, 0, nil)
	if full {
		return -next - 1
	}
	return 0
}
