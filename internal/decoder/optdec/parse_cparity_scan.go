//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"unsafe"
)

// cParityNonspaceBlock caches a 64-byte non-whitespace bitmask for padded scanners.
// Callers provide a readable 64-byte trailing sentinel after the logical input.
type cParityNonspaceBlock struct {
	// offPlus64 stores (blockOffset + 64) relative to rawBase so the zero
	// value represents an uninitialized block at rawBase - 64.
	offPlus64 uint64
	bits      uint64
}

func cParityAtFirst(first, second uint32) bool {
	return (second-1)&first != 0
}

var cParityEscaped = [256]byte{
	'"': '"', '/': '/', '\\': '\\', 'b': '\b',
	'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// Bit 4 distinguishes valid hex digits from the zero-initialized invalid
// entries. Adding the shifted entries then subtracting their marker bits packs
// all four nibbles while checking validity from the same four table loads.
var cParityHexDigits = [256]uint8{
	'0': 0x10, '1': 0x11, '2': 0x12, '3': 0x13, '4': 0x14,
	'5': 0x15, '6': 0x16, '7': 0x17, '8': 0x18, '9': 0x19,
	'a': 0x1a, 'b': 0x1b, 'c': 0x1c, 'd': 0x1d, 'e': 0x1e, 'f': 0x1f,
	'A': 0x1a, 'B': 0x1b, 'C': 0x1c, 'D': 0x1d, 'E': 0x1e, 'F': 0x1f,
}

func cParityReadHex16(p unsafe.Pointer) (uint32, bool) {
	text := (*[4]byte)(p)
	a := uint32(cParityHexDigits[text[0]])
	b := uint32(cParityHexDigits[text[1]])
	c := uint32(cParityHexDigits[text[2]])
	d := uint32(cParityHexDigits[text[3]])
	return (a << 12) + (b << 8) + (c << 4) + d - 0x11110, a&b&c&d&0x10 != 0
}

// cParityDecodeUnicodeOpt keeps cursors local while converting one escape operation
// or an entire adjacent run. Passing cursors by value avoids publishing pointers
// and their write-barrier checks for every code point. The caller owns the
// padded input and publishes both cursors once, including on malformed input.
func cParityDecodeUnicodeOpt(src, dst unsafe.Pointer, consecutive bool, strictURC bool) (unsafe.Pointer, unsafe.Pointer, int) {
	for {
		r0, valid := cParityReadHex16(unsafe.Add(src, 2))
		if !valid {
			return src, dst, SONIC_INVALID_ESCAPED_UTF
		}
		src = unsafe.Add(src, 6)
		for {
			if r0 <= 0x7f {
				*(*byte)(dst) = byte(r0)
				dst = unsafe.Add(dst, 1)
				break
			}
			if r0 <= 0x07ff {
				*(*byte)(dst) = byte(0xc0 | (r0 >> 6))
				*(*byte)(unsafe.Add(dst, 1)) = byte(0x80 | (r0 & 0x3f))
				dst = unsafe.Add(dst, 2)
				break
			}
			if r0 < 0xd800 || r0 > 0xdfff {
				*(*byte)(dst) = byte(0xe0 | (r0 >> 12))
				*(*byte)(unsafe.Add(dst, 1)) = byte(0x80 | ((r0 >> 6) & 0x3f))
				*(*byte)(unsafe.Add(dst, 2)) = byte(0x80 | (r0 & 0x3f))
				dst = unsafe.Add(dst, 3)
				break
			}
			if r0 > 0xdbff || *(*byte)(src) != '\\' || *(*byte)(unsafe.Add(src, 1)) != 'u' {
				if strictURC {
					return unsafe.Add(src, -3), dst, SONIC_INVALID_ESCAPED_UTF
				}
				*(*byte)(dst) = 0xef
				*(*byte)(unsafe.Add(dst, 1)) = 0xbf
				*(*byte)(unsafe.Add(dst, 2)) = 0xbd
				dst = unsafe.Add(dst, 3)
				break
			}
			r1, valid := cParityReadHex16(unsafe.Add(src, 2))
			if !valid {
				return src, dst, SONIC_INVALID_ESCAPED_UTF
			}
			src = unsafe.Add(src, 6)
			if r1 < 0xdc00 || r1 > 0xdfff {
				if strictURC {
					return unsafe.Add(src, -3), dst, SONIC_INVALID_ESCAPED_UTF
				}
				r0 = r1
				*(*byte)(dst) = 0xef
				*(*byte)(unsafe.Add(dst, 1)) = 0xbf
				*(*byte)(unsafe.Add(dst, 2)) = 0xbd
				dst = unsafe.Add(dst, 3)
				continue
			}
			r0 = ((r0 - 0xd800) << 10) + (r1 - 0xdc00) + 0x010000
			*(*byte)(dst) = byte(0xf0 | (r0 >> 18))
			*(*byte)(unsafe.Add(dst, 1)) = byte(0x80 | ((r0 >> 12) & 0x3f))
			*(*byte)(unsafe.Add(dst, 2)) = byte(0x80 | ((r0 >> 6) & 0x3f))
			*(*byte)(unsafe.Add(dst, 3)) = byte(0x80 | (r0 & 0x3f))
			dst = unsafe.Add(dst, 4)
			break
		}
		if !consecutive || *(*byte)(src) != '\\' || *(*byte)(unsafe.Add(src, 1)) != 'u' {
			return src, dst, 0
		}
	}
}
