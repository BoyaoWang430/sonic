//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/rt"
)

// StringSpecialMask marks quotes, backslashes, and raw controls in an eight-byte
// little-endian word. The lowest marked byte is exact; subtraction borrows may
// also mark later bytes. Bytes >= 0x80 are ordinary bytes, not UTF-8 validation.
func StringSpecialMask(word uint64) uint64 {
	quote := word ^ 0x2222222222222222
	slash := word ^ 0x5c5c5c5c5c5c5c5c
	return ((quote-0x0101010101010101)&^quote |
		(slash-0x0101010101010101)&^slash |
		(word-0x2020202020202020)&^word) & 0x8080808080808080
}

// ParseString decodes the string beginning with the quote at start in place.
// End follows its original closing quote; written counts decoded bytes starting
// at start+1. The original closing quote and trailing input remain unchanged.
// With no escapes, no stores are needed. Raw UTF-8 bytes are not validated.
//
// Status uses neutral KernelStatus values (StatusOK, StatusControlChar,
// StatusInvalidEscape, StatusEOF, StatusInvalidUnicode). ErrorPos is the
// original control byte, offending escape's backslash, or the logical end for
// EOF. On error end equals errorPos; written is the completed output prefix.
// The caller supplies a valid opening-quote offset.
func ParseString(buf []byte, start int, validate, unicodeErrors bool) (end, written int, escaped bool, errorPos int, status KernelStatus) {
	base := start + 1
	tail := buf[base:]
	mode := uint64(4)
	if validate {
		mode |= 8
	}
	if !unicodeErrors {
		mode |= 2
	}
	var sawEscape int
	end, written, sawEscape, errorPos, status = DecodeQuoted(rt.SliceData(tail), len(tail), mode)
	end += base
	if errorPos >= 0 {
		errorPos += base
	}
	return end, written, sawEscape != 0, errorPos, status
}

// DecodeQuoted decodes in place from the bytes after an opening quote. The caller
// supplies size readable and writable bytes at tail, with size >= 0, and
// precomputes mode with bit 2 set,
// bit 1 for Unicode replacement, and bit 3 for rejecting raw controls. Returned
// offsets are relative to tail; a negative errorPos remains a sentinel. escaped
// is nonzero when an escape was decoded. No padding is needed; tail may be nil
// only when size is zero. The closing quote and following input are unchanged.
func DecodeQuoted(tail *byte, size int, mode uint64) (end, written, escaped, errorPos int, status KernelStatus) {
	if size <= 0 {
		return 0, 0, 0, 0, StatusEOF
	}
	var code int
	if cpu.UseScalar {
		end, written, escaped, errorPos, code = stringDecodeScalar(tail, size, tail, mode)
	} else {
		end, written, escaped, errorPos, code = stringDecode(tail, size, tail, mode)
	}
	status = decodeQuotedRawCodeToStatus(code)
	return
}
