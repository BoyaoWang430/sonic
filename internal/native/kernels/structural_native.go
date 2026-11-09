//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
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
	open := src[start]
	// Empty containers need no classification or quote/depth state.
	if len(tail) != 0 && tail[0] == open+2 {
		return base + 1, 0
	}
	end, err = skipContainer(rt.StringData(tail), len(tail), uint64(open))
	return base + end, err
}
