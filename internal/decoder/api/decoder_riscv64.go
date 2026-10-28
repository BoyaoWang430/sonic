//go:build go1.26 && !go1.28 && riscv64.rva22u64

package api

import (
	"github.com/bytedance/sonic/internal/decoder/optdec"
	"github.com/bytedance/sonic/internal/envs"
)

var (
	pretouchImpl     = optdec.Pretouch
	pretouchManyImpl = optdec.PretouchMany
	decodeImpl       = optdec.Decode
)

func init() {
	envs.EnableOptDec()
	envs.EnableFastMap()
}
