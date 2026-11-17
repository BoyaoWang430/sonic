//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package kernels

import "github.com/bytedance/sonic/internal/rt"

// HTMLEscape copies src into dst, escaping <, >, &, U+2028, and U+2029 using
// lowercase Unicode escapes. Other bytes, including invalid UTF-8, are preserved.
// It does not add quotes or perform JSON string escaping.
//
// On success consumed equals len(src). If dst is too short, consumed is the
// complement of the input byte count processed. written is the output byte count
// in both cases. Every escape is written atomically, including the three-byte
// U+2028/U+2029 input. src and dst must not overlap; neither requires padding.
func HTMLEscape(src string, dst []byte) (consumed, written int) {
	return HTMLEscapeBuffer(rt.StringData(src), len(src), rt.SliceData(dst), len(dst))
}

// HTMLEscapeBuffer copies size bytes from src into dst capacity bytes while
// escaping HTML-unsafe runes, avoiding string/slice header construction.
func HTMLEscapeBuffer(src *byte, size int, dst *byte, capacity int) (consumed, written int) {
	if size <= 0 {
		return 0, 0
	}
	raceRead(src, size)
	consumed, written = htmlEscape(src, size, dst, capacity)
	raceWrite(dst, written)
	return consumed, written
}

//go:noescape
func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)
