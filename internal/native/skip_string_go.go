//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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
