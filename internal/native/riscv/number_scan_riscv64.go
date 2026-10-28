//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// ScanNumber scans one JSON number prefix without skipping space or converting
// its value. The caller validates the following delimiter: "01" ends after "0"
// and "1+2" ends before '+'. Once a fraction or exponent begins, its digits are
// mandatory. On error, end is the offending byte, or len(src) for ERR_EOF.
// An out-of-range start returns ERR_INVALID_CHAR and the unchanged start.
// No readable padding is needed.
func ScanNumber(src string, start int) (end int, err types.ParsingError) {
	if start < 0 || start > len(src) {
		return start, types.ERR_INVALID_CHAR
	}
	tail := src[start:]
	end, err = numberScan(unsafe.StringData(tail), len(tail), 0)
	return start + end, err
}

// ScanNumberStrict scans the native skip-number candidate grammar. It rejects
// repeated decimal points, exponents, and signs, and misplaced markers within
// the candidate. A leading zero still ends the prefix unless followed by '.'
// or an exponent. Unlike ScanNumber, "1e+" reports ERR_INVALID_CHAR at '+'.
// Error offsets, start bounds, and readable input requirements match ScanNumber.
func ScanNumberStrict(src string, start int) (end int, err types.ParsingError) {
	if start < 0 || start > len(src) {
		return start, types.ERR_INVALID_CHAR
	}
	tail := src[start:]
	end, err = numberScan(unsafe.StringData(tail), len(tail), 1)
	return start + end, err
}

// SkipNumber returns the first '}', ']', ',', or JSON whitespace byte at or
// after start, or len(src). It deliberately does not validate number syntax.
// start must be in [0, len(src)]. No readable padding is needed.
func SkipNumber(src string, start int) int {
	tail := src[start:]
	end, _ := numberScan(unsafe.StringData(tail), len(tail), 2)
	return start + end
}

// numberScan performs a complete prefix scan (mode 0), strict candidate scan
// (mode 1), or unvalidated boundary scan (mode 2).
// Positions are relative to src.
// It reads only src[:size], including no reads when size is zero.
//
//go:noescape
func numberScan(src *byte, size int, mode uint64) (end int, err types.ParsingError)
