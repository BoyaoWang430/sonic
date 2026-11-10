//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math/bits"
	"unsafe"
)

// This experimental scanner follows parse_with_padding.c, including its padded
// reads and malformed-input ordering. Callers provide the same 64-byte suffix.
type cParityNonspaceBlock struct {
	// offPlus64 stores (blockOffset + 64) relative to rawBase so the zero
	// value represents an uninitialized block at rawBase - 64.
	offPlus64 uint64
	bits      uint64
}

func cParityIsSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t'
}

func cParityAtFirst(first, second uint32) bool {
	return (second-1)&first != 0
}

var cParityEscaped = [256]byte{
	'"': '"', '/': '/', '\\': '\\', 'b': '\b',
	'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// cParityHex16Valid and cParityHex16 preserve the C four-byte SWAR operations,
// rather than introducing a byte-at-a-time decoder or different error cursor.
func cParityHex16Valid(p unsafe.Pointer) bool {
	v := *(*uint32)(p)
	less := (v - 0x01010101*'0') &^ v & 0x80808080
	more := ((v + 0x01010101*(127-'f')) | v) & 0x80808080
	between9A := (0x01010101*(127+'A') - (v & 0x7f7f7f7f)) &^ v & ((v & 0x7f7f7f7f) + 0x01010101*(127-'9')) & 0x80808080
	betweenFa := (0x01010101*(127+'a') - (v & 0x7f7f7f7f)) &^ v & ((v & 0x7f7f7f7f) + 0x01010101*(127-'F')) & 0x80808080
	return less|more|between9A|betweenFa == 0
}

func cParityHex16(p unsafe.Pointer) uint32 {
	a := bits.ReverseBytes32(*(*uint32)(p))
	b := 9*((^a&0x10101010)>>4) + (a & 0x0f0f0f0f)
	c := (b >> 4) | b
	return ((c >> 8) & 0xff00) | (c & 0x00ff)
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

func cParityUnicodeReplacement(dst *unsafe.Pointer) {
	p := *dst
	*(*byte)(p) = 0xef
	*(*byte)(unsafe.Add(p, 1)) = 0xbf
	*(*byte)(unsafe.Add(p, 2)) = 0xbd
	*dst = unsafe.Add(p, 3)
}

// This follows handle_unicode, which always replaces unpaired surrogates and
// does not consult F_DIS_URC. A bad second escape keeps the cursor at that escape.
func cParityHandleUnicode(src, dst *unsafe.Pointer) int {
	s, d, code := cParityDecodeUnicode(*src, *dst, false)
	*src, *dst = s, d
	return code
}

// DecodeUnicode keeps cursors local while converting one escape operation or
// an entire adjacent run. Passing cursors by value avoids publishing pointers
// and their write-barrier checks for every code point. The caller owns the
// padded input and publishes both cursors once, including on malformed input.
func cParityDecodeUnicode(src, dst unsafe.Pointer, consecutive bool) (unsafe.Pointer, unsafe.Pointer, int) {
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
