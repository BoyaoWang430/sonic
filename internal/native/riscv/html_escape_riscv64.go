//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import "unsafe"

// HTMLEscape copies src into dst, escaping <, >, &, U+2028, and U+2029 using
// lowercase Unicode escapes. Other bytes, including invalid UTF-8, are preserved.
// It does not add quotes or perform JSON string escaping.
//
// On success consumed equals len(src). If dst is too short, consumed is the
// complement of the input byte count processed. written is the output byte count
// in both cases. Every escape is written atomically, including the three-byte
// U+2028/U+2029 input. src and dst must not overlap; neither requires padding.
func HTMLEscape(src string, dst []byte) (consumed, written int) {
	return htmlEscape(unsafe.StringData(src), len(src), unsafe.SliceData(dst), len(dst))
}

//go:noescape
func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)
