//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// PathMasks classifies at most 64 bytes for a relaxed path search. Events mark
// quotes and backslashes; nonspace excludes exactly JSON whitespace; boundaries
// mark the seven bytes that terminate a relaxed numeric candidate. All masks
// refer to the same bounded load and can be retained across successive tokens.
func PathMasks(src string, start int) (events, nonspace, boundaries uint64, width int) {
	width = len(src) - start
	if width > 64 {
		width = 64
	}
	events, nonspace, boundaries = pathMasks(rt.StringData(src[start:]), width)
	return
}

//go:noescape
func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64)

// ScanPathStringBlock scans relaxed string content starting at start, after
// its opening quote. It also returns raw quote/backslash events in the already
// classified suffix after the closing quote. The suffix starts at end and spans
// tailWidth bounded bytes; it may be empty. Errors return an empty suffix.
func ScanPathStringBlock(src string, start int) (end, escape int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	tail := src[start:]
	end, escape, _, err, tailEvents, tailWidth = scanStringRelaxedBlock(rt.StringData(tail), len(tail))
	end += start
	if escape >= 0 {
		escape += start
	}
	return
}
