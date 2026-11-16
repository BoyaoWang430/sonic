//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import "unsafe"

// cParityNonspace64 reads exactly 64 padded bytes without a logical-length check.
//
//go:noescape
func cParityNonspace64(src unsafe.Pointer) (mask uint64)

// cParityStringMasks32 reads exactly 32 padded bytes and returns backslash,
// quote, and (when option bit 5 is set) raw-control bitmasks.
//
//go:noescape
func cParityStringMasks32(src unsafe.Pointer, opts uint64) (bs, quote, control uint32)

// cParityNumberMasks16 classifies 16 padded bytes for the raw-number scanner.
// Its valid mask covers decimal digits plus '.', 'e', 'E', '+', and '-'.
//
//go:noescape
func cParityNumberMasks16(src unsafe.Pointer) (decimal, exponent, sign, valid uint32)
