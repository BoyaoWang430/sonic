//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func PathMasks(src string, start int) (events, nonspace, boundaries uint64, width int) {
	tail := src[start:]
	width = min(len(tail), 64)
	events, nonspace, boundaries = pathMasks(unsafe.StringData(tail), width)
	return
}

//go:noescape
func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64)

func ScanPathStringBlock(tail string) (end, escape int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	end, escape, _, err, tailEvents, tailWidth = scanStringRelaxedBlock(unsafe.StringData(tail), len(tail))
	return
}
