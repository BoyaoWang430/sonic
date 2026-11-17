//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import (
	"math/bits"
	"unsafe"
)

// cParityHex16Valid and cParityHex16 preserve the reference four-byte SWAR
// hex operations used by the unicode escape equivalence tests.
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

func cParityHandleUnicodeOpt(src, dst *unsafe.Pointer, strictURC bool) int {
	s, d, code := cParityDecodeUnicodeOpt(*src, *dst, false, strictURC)
	*src, *dst = s, d
	return code
}

func cParityDecodeUnicode(src, dst unsafe.Pointer, consecutive bool) (unsafe.Pointer, unsafe.Pointer, int) {
	return cParityDecodeUnicodeOpt(src, dst, consecutive, false)
}

// cParityParseString starts after the opening quote. Success consumes its closing
// quote and returns decoded length; errors return the negative C error code.
func cParityParseString(cur *unsafe.Pointer, hasEsc *bool, opts uint64) int {
	return cParityParseStringOpt(cur, hasEsc, opts, false)
}

//go:nocheckptr
func cParityParseStringOpt(cur *unsafe.Pointer, hasEsc *bool, opts uint64, strictURC bool) int {
	start := *cur
	p := start
	var dst unsafe.Pointer
	var bs, quote, control uint32
	var esc byte
	for {
		bs, quote, control = cParityStringMasks32(p, opts)
		if cParityAtFirst(quote, bs|control) {
			p = unsafe.Add(p, bits.TrailingZeros32(quote)+1)
			*cur, *hasEsc = p, false
			return int(uintptr(p)-uintptr(start)) - 1
		}
		if opts&(1<<5) != 0 && cParityAtFirst(control, bs|quote) {
			*cur = unsafe.Add(p, bits.TrailingZeros32(control))
			return -SONIC_CONTROL_CHAR
		}
		if cParityAtFirst(bs, quote) {
			break
		}
		p = unsafe.Add(p, 32)
	}
	*hasEsc = true
	p = unsafe.Add(p, bits.TrailingZeros32(bs))
	dst = p

escape:
	esc = *(*byte)(unsafe.Add(p, 1))
	if esc != 'u' {
		if cParityEscaped[esc] == 0 {
			*cur = p
			return -SONIC_INVALID_ESCAPED
		}
		p = unsafe.Add(p, 2)
		*(*byte)(dst) = cParityEscaped[esc]
		dst = unsafe.Add(dst, 1)
	} else if cParityHandleUnicodeOpt(&p, &dst, strictURC) != 0 {
		*cur = p
		return -SONIC_INVALID_ESCAPED_UTF
	}
	if *(*byte)(p) == '\\' {
		goto escape
	}

findAndMove:
	bs, quote, control = cParityStringMasks32(p, opts)
	if cParityAtFirst(quote, bs|control) {
		for {
			// The C loop spells out eight copies per backedge.
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
			{
				c := *(*byte)(p)
				p = unsafe.Add(p, 1)
				if c == '"' {
					*cur = p
					return int(uintptr(dst) - uintptr(start))
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
			}
		}
	}
	// After an escape C checks raw controls before the next backslash.
	if opts&(1<<5) != 0 && cParityAtFirst(control, quote) {
		*cur = unsafe.Add(p, bits.TrailingZeros32(control))
		return -SONIC_CONTROL_CHAR
	}
	if cParityAtFirst(bs, quote) {
		for {
			// The C loop spells out eight copies per backedge.
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
			{
				c := *(*byte)(p)
				if c == '\\' {
					goto escape
				}
				*(*byte)(dst) = c
				dst = unsafe.Add(dst, 1)
				p = unsafe.Add(p, 1)
			}
		}
	}
	// Snapshot all 32 bytes before writing dst so overlapping in-place contraction
	// preserves exact value semantics.
	block32 := *(*[32]byte)(p)
	*(*[32]byte)(dst) = block32
	p = unsafe.Add(p, 32)
	dst = unsafe.Add(dst, 32)
	goto findAndMove
}
