//go:build amd64

package kernels

import "github.com/bytedance/sonic/internal/cpu"

var hasAVX2 = cpu.HasAVX2 && cpu.HasPOPCNT && cpu.HasCLMUL
