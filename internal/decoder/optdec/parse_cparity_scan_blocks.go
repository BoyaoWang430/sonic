//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"math/bits"
	"unsafe"
)

// cParitySkipSpace consumes and returns the first nonspace byte. Scalar probes
// leave the cached block intact, matching skip_space_impl's cursor contract.
func cParitySkipSpace(base unsafe.Pointer, block *cParityNonspaceBlock, cur *unsafe.Pointer) byte {
	p := *cur
	c := *(*byte)(p)
	p = unsafe.Add(p, 1)
	if !cParityIsSpace(c) {
		*cur = p
		return c
	}
	c = *(*byte)(p)
	p = unsafe.Add(p, 1)
	if !cParityIsSpace(c) {
		*cur = p
		return c
	}

	if block.offPlus64 != 0 {
		blockPtr := unsafe.Add(base, int(block.offPlus64)-64)
		offset := uintptr(p) - uintptr(blockPtr)
		if offset < 64 {
			mask := block.bits & (^uint64(0) << offset)
			if mask != 0 {
				at := unsafe.Add(blockPtr, bits.TrailingZeros64(mask))
				*cur = unsafe.Add(at, 1)
				return *(*byte)(at)
			}
			p = unsafe.Add(blockPtr, 64)
		}
	}
	for {
		// The fixed-width SIMD leaf implements the C shuffle table:
		// only space/tab/LF/CR match. NUL and all high bytes are nonspace.
		mask := cParityNonspace64(p)
		if mask != 0 {
			block.bits, block.offPlus64 = mask, uint64(uintptr(p)-uintptr(base)+64)
			at := unsafe.Add(p, bits.TrailingZeros64(mask))
			*cur = unsafe.Add(at, 1)
			return *(*byte)(at)
		}
		p = unsafe.Add(p, 64)
	}
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
	// Both vector loads precede either store, including overlapping in-place
	// contraction. This explicit leaf replaces C's v256 load/store pair.
	cParityMove32(dst, p)
	p = unsafe.Add(p, 32)
	dst = unsafe.Add(dst, 32)
	goto findAndMove
}
