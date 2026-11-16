//go:build !sonic_compat && amd64 && go1.20 && !go1.28

package optdec

import "github.com/bytedance/sonic/internal/cpu"

var (
	hasAVX2  = cpu.UseAVX2
	hasSSSE3 = cpu.UseSSSE3
)
