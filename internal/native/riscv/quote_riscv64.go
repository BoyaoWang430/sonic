//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import "unsafe"

// Quote escapes src into dst without surrounding quotes. Only bit zero of flags
// (types.F_DOUBLE_UNQUOTE) affects the output. The escape spelling matches the
// native quote tables, including Unicode escapes for backspace and form feed.
// It preserves non-ASCII bytes without UTF-8 validation or HTML escaping.
//
// On success consumed equals len(src). If dst is too short, consumed is the
// complement of the input byte count processed. written is the output byte count
// in both cases. Every escape is written atomically. src and dst must not overlap;
// neither requires padding.
func Quote(src string, dst []byte, flags uint64) (consumed, written int) {
	return quote(unsafe.StringData(src), len(src), unsafe.SliceData(dst), len(dst), flags)
}

//go:noescape
func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)
