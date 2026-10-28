//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import "unsafe"

// TraversalMasks classifies at most 64 bytes once for string boundaries and
// surrounding JSON whitespace. Events mark quotes, backslashes and, when
// validate is enabled, raw controls. Bits beyond width are zero. No padding is
// required; callers may retain these masks while traversing immutable src.
func TraversalMasks(src string, start int, validate bool) (events, nonspace uint64, width int) {
	tail := src[start:]
	width = min(len(tail), 64)
	var strict uint64
	if validate {
		strict = 1
	}
	events, nonspace = traversalMasks(unsafe.StringData(tail), width, strict)
	return
}

//go:noescape
func traversalMasks(src *byte, size int, validate uint64) (events, nonspace uint64)
