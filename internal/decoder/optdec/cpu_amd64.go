//go:build amd64

package optdec

import "github.com/bytedance/sonic/internal/cpu"

var hasAVX2 = cpu.HasAVX2
