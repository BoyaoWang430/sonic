//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import "unsafe"

// NonSpaceMask classifies at most 64 bytes starting at start. Bit i is set
// exactly when src[start+i] is not JSON whitespace. Bits beyond width are zero.
// start must be in [0, len(src)]. The input needs no readable padding.
func NonSpaceMask(src string, start int) (mask uint64, width int) {
	tail := src[start:]
	width = min(len(tail), 64)
	return nonSpaceMask(unsafe.StringData(tail), width), width
}

//go:noescape
func nonSpaceMask(src *byte, size int) (mask uint64)
