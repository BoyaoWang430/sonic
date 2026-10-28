//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// Unquote decodes a string body without surrounding quotes. len(dst) must be
// at least len(src); exact in-place aliasing is supported, other overlap is not.
// Flags select double-unquote and Unicode replacement. Invalid UTF-8 is preserved.
// On error, written counts the decoded prefix and errorPos follows native/unquote.c.
func Unquote(src string, dst []byte, flags uint64) (written, errorPos int, err types.ParsingError) {
	_, written, _, errorPos, code := stringDecode(unsafe.StringData(src), len(src), unsafe.SliceData(dst), flags&3)
	return written, errorPos, types.ParsingError(code)
}
