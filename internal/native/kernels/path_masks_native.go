//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

func PathMasks(src string, start int) (events, nonspace, boundaries uint64, width int) {
	tail := src[start:]
	width = minInt(len(tail), 64)
	if cpu.UseScalar {
		events, nonspace, boundaries = pathMasksScalar(rt.StringData(tail), width)
		return
	}
	events, nonspace, boundaries = pathMasks(rt.StringData(tail), width)
	return
}

func ScanPathStringBlock(tail string) (end, escape int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	if cpu.UseScalar {
		end, escape, _, err, tailEvents, tailWidth = scanStringRelaxedBlockScalar(rt.StringData(tail), len(tail))
		return
	}
	end, escape, _, err, tailEvents, tailWidth = scanStringRelaxedBlock(rt.StringData(tail), len(tail))
	return
}
