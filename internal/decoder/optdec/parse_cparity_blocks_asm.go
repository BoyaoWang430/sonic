//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import "unsafe"

// cParityNonspace64 reads exactly 64 padded bytes without a logical-length check.
//
//go:noescape
func cParityNonspace64(src unsafe.Pointer) (mask uint64)

// cParityStringMasks32 reads exactly 32 padded bytes and returns C's masks.
// Only option bit 5 enables raw-control classification.
//
//go:noescape
func cParityStringMasks32(src unsafe.Pointer, opts uint64) (bs, quote, control uint32)

//go:noescape
func cParityMove32(dst, src unsafe.Pointer)

// cParityNumberMasks16 supports the C raw-number scanner. Its valid mask is
// decimal digits plus '.', 'e', 'E', '+', and '-'; high bytes are invalid.
//
//go:noescape
func cParityNumberMasks16(src unsafe.Pointer) (decimal, exponent, sign, valid uint32)
