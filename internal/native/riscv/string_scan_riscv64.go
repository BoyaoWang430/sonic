//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// ScanString scans the JSON string whose opening quote is at start. On success,
// end follows the closing quote, escape is the first backslash offset or -1,
// and errorPos is -1. On error, end equals errorPos. EOF is reported at len(src);
// invalid escapes are reported at their backslash, and raw controls at the byte.
//
// With validate disabled, escaped bytes are skipped without syntax checks.
// With validate enabled, JSON controls and escape syntax are checked uniformly
// at every input length. Lone UTF-16 surrogates are allowed, and UTF-8 is not
// validated. The input needs no padding and is never modified. The caller must
// provide an opening quote at start.
func ScanString(src string, start int, validate bool) (end, escape, errorPos int, err types.ParsingError) {
	base := start + 1
	tail := src[base:]
	var strict uint64
	if validate {
		strict = 1
	}
	end, escape, errorPos, err = scanString(unsafe.StringData(tail), len(tail), strict)
	end += base
	if escape >= 0 {
		escape += base
	}
	if errorPos >= 0 {
		errorPos += base
	}
	return
}

// scanString starts after the opening quote and returns offsets relative to src.
// It reads only src[:size], including when processing an escape at the tail.
//
//go:noescape
func scanString(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError)
