//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

func skipStringToken(src string, p *int, flags uint64) types.ParsingError {
	validate := flags&types.F_VALIDATE_STRING != 0 && flags&noValidateJSON == 0
	i := *p
	end, err := riscv.SkipString(src[i:], validate)
	*p = i + end
	return err
}
