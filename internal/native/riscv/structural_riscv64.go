//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// SkipContainer skips the container whose known opening '[' or '{' is at start.
// It returns the position following the matching closing bracket, or len(src)
// and ERR_EOF. It counts only brackets of the opening kind, ignores brackets
// inside strings, and does not validate JSON syntax or impose a depth limit.
// An odd backslash run escapes a following quote even outside a string, matching
// the native relaxed scanner. The source needs no padding and is not modified.
func SkipContainer(src string, start int) (end int, err types.ParsingError) {
	base := start + 1
	tail := src[base:]
	end, err = skipContainer(unsafe.StringData(tail), len(tail), uint64(src[start]))
	return base + end, err
}

//go:noescape
func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError)
