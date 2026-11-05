//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"strconv"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/number"
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

// Number-formatting callers reserve at least 32 output bytes.
func F64toa(out *byte, val float64) int {
	return len(number.AppendFloat(unsafe.Slice(out, 32)[:0], val, 64))
}

func F32toa(out *byte, val float32) int {
	return len(number.AppendFloat(unsafe.Slice(out, 32)[:0], float64(val), 32))
}

func I64toa(out *byte, val int64) int {
	return len(strconv.AppendInt(unsafe.Slice(out, 32)[:0], val, 10))
}

func U64toa(out *byte, val uint64) int {
	return len(strconv.AppendUint(unsafe.Slice(out, 32)[:0], val, 10))
}
