//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"github.com/bytedance/sonic/internal/rt"

	"github.com/bytedance/sonic/internal/native/types"
)

// Unquote decodes a string body without surrounding quotes. len(dst) must be
// at least len(src); exact in-place aliasing is supported, other overlap is not.
// Flags select double-unquote and Unicode replacement. Invalid UTF-8 is preserved.
// On error, written counts the decoded prefix and errorPos follows native/unquote.c.
func Unquote(src string, dst []byte, flags uint64) (written, errorPos int, err types.ParsingError) {
	var code int
	_, written, _, errorPos, code = stringDecode(rt.StringData(src), len(src), rt.SliceData(dst), flags&3)
	err = types.ParsingError(code)
	return
}

// UnquoteBuffer decodes size bytes from src into dst without surrounding quotes,
// avoiding string/slice header construction on raw pointers.
func UnquoteBuffer(src *byte, size int, dst *byte, flags uint64) (written, errorPos int, err types.ParsingError) {
	_, written, _, errorPos, code := stringDecode(src, size, dst, flags&3)
	return written, errorPos, types.ParsingError(code)
}
