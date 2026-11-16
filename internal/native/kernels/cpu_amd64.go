//go:build amd64 && go1.20 && !go1.28

package kernels

import "github.com/bytedance/sonic/internal/cpu"

var (
	hasAVX2  = cpu.HasAVX2 && cpu.HasPOPCNT && cpu.HasCLMUL
	hasSSSE3 = cpu.HasSSSE3
)
