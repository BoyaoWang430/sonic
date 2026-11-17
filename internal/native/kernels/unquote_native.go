//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

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
	return UnquoteBuffer(rt.StringData(src), len(src), rt.SliceData(dst), flags)
}

// UnquoteStatus decodes a string body without surrounding quotes and returns
// a neutral KernelStatus independent of public AST or Decoder error numbering.
func UnquoteStatus(src string, dst []byte, flags uint64) (written, errorPos int, status KernelStatus) {
	if len(src) == 0 {
		return 0, -1, StatusOK
	}
	w, pos, perr := UnquoteBuffer(rt.StringData(src), len(src), rt.SliceData(dst), flags)
	if perr == 0 {
		return w, pos, StatusOK
	}
	// In unquote mode (bit 2 == 0), \u with invalid hex returns ERR_INVALID_CHAR;
	// normalize invalid \uXXXX sequences to StatusInvalidUnicode for neutral callers.
	if perr == types.ERR_INVALID_CHAR {
		return w, pos, StatusInvalidUnicode
	}
	return w, pos, FromParsingError(perr)
}

// UnquoteBuffer decodes size bytes from src into dst without surrounding quotes,
// avoiding string/slice header construction on raw pointers.
func UnquoteBuffer(src *byte, size int, dst *byte, flags uint64) (written, errorPos int, err types.ParsingError) {
	if size <= 0 {
		return 0, -1, 0
	}
	raceRead(src, size)
	_, written, _, errorPos, code := stringDecode(src, size, dst, flags&3)
	raceWrite(dst, written)
	return written, errorPos, types.ParsingError(code)
}
