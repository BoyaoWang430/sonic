//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// PathMasks classifies at most 64 bytes for a relaxed path search. Events mark
// quotes and backslashes; nonspace excludes exactly JSON whitespace; boundaries
// mark the seven bytes that terminate a relaxed numeric candidate. All masks
// refer to the same bounded load and can be retained across successive tokens.
func PathMasks(src string, start int) (events, nonspace, boundaries uint64, width int) {
	tail := src[start:]
	width = min(len(tail), 64)
	events, nonspace, boundaries = pathMasks(unsafe.StringData(tail), width)
	return
}

//go:noescape
func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64)

// ScanPathStringBlock also returns the raw quote/backslash events in the
// unconsumed suffix of the final loaded block. The suffix starts at end and
// spans tailWidth bytes. Errors and byte-at-a-time tails return an empty suffix.
func ScanPathStringBlock(src string, start int) (end, escape int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	tail := src[start:]
	end, escape, _, err, tailEvents, tailWidth = scanStringBlock(unsafe.StringData(tail), len(tail), 0)
	end += start
	if escape >= 0 {
		escape += start
	}
	return
}

//go:noescape
func scanStringBlock(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
