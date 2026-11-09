//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
)

func skipStringToken(src string, p *int, flags uint64) types.ParsingError {
	validate := flags&types.F_VALIDATE_STRING != 0 && flags&noValidateJSON == 0
	i := *p
	var end int
	var err types.ParsingError
	if validate {
		end, err = kernels.SkipStringStrict(src[i:])
	} else {
		end, err = kernels.SkipStringRelaxed(src[i:])
	}
	*p = i + end
	return err
}
