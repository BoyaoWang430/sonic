//go:build amd64

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/klauspost/cpuid/v2"
)

var hasAVX2 = cpu.HasAVX2 && cpuid.CPU.Has(cpuid.POPCNT) && cpuid.CPU.Has(cpuid.CLMUL)
