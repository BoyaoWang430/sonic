//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import "unsafe"

// ParseString decodes the string beginning with the quote at start in place.
// End follows its original closing quote; written counts decoded bytes starting
// at start+1. The original closing quote and trailing input remain unchanged.
// With no escapes, no stores are needed. Raw UTF-8 bytes are not validated.
//
// Errors use the optdec family: 1 control, 2 escape, 5 EOF, 12 Unicode escape.
// ErrorPos is the original control byte, offending escape's backslash, or the
// logical end for EOF. On error end equals errorPos; written is the completed
// output prefix. The caller supplies a valid opening-quote offset.
func ParseString(buf []byte, start int, validate, unicodeErrors bool) (end, written int, escaped bool, errorPos, code int) {
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
	end, written, sawEscape, errorPos, code = stringDecode(unsafe.SliceData(tail), len(tail), unsafe.SliceData(tail), mode)
	end += base
	if errorPos >= 0 {
		errorPos += base
	}
	return end, written, sawEscape != 0, errorPos, code
}

// stringDecode implements the whole operation in one ABI0 assembly call.
// mode bits: 0 double-unquote, 1 Unicode replacement, 2 quoted parser input,
// 3 reject raw controls. Parser input starts after the opening quote.
// All offsets returned by this private entry are relative to src.
//
//go:noescape
func stringDecode(src *byte, size int, dst *byte, mode uint64) (end, written, escaped, errorPos, code int)
