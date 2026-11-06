//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// Scalar RVA22 implementations of the padded C-parity block helpers declared
// in parse_cparity_blocks_asm.go. Every load stays within the fixed block the
// caller guarantees readable; loads may be unaligned. Zbb ORCB turns each
// nonzero byte into 0xff, so (word ^ pattern) classifies bytes without carries.
// FP (X8), g (X27), SP, RA, and the assembler scratch X31 are not used.

// out = 0x80 in each byte of word equal to the corresponding byte of pat.
// high must hold 0x8080808080808080.
#define CP_EQ80(word, pat, high, out) \
	XOR pat, word, out; \
	ORCB out, out; \
	ANDN out, high, out

// Gather byte high bits (only 0x80 bits set) into the low eight bits of reg.
// spread must hold 0x0102040810204080; the partial products never overlap.
#define CP_PACK8(reg, spread) \
	SRL $7, reg; \
	MUL spread, reg; \
	SRL $56, reg

// One word of the nonspace classification: X6 receives eight mask bits.
#define CP_NONSPACE_WORD(offset) \
	MOV offset(X10), X5; \
	XOR X11, X5, X6; \
	ORCB X6, X6; \
	XOR X13, X5, X7; \
	ORCB X7, X7; \
	AND X7, X6; \
	AND X14, X5, X7; \
	XOR X12, X7, X7; \
	ORCB X7, X7; \
	AND X7, X6; \
	AND X15, X6; \
	CP_PACK8(X6, X16)

// func cParityNonspace64(src unsafe.Pointer) (mask uint64)
// Bit i is set exactly when src[i] is not space, tab, line feed or carriage
// return. C reads a complete 64-byte padded block without a length check.
TEXT ·cParityNonspace64(SB), NOSPLIT, $0-16
	MOV src+0(FP), X10
	MOV $0x2020202020202020, X11
	MOV $0x0909090909090909, X12
	MOV $0x0a0a0a0a0a0a0a0a, X13
	MOV $0xfbfbfbfbfbfbfbfb, X14
	MOV $0x8080808080808080, X15
	MOV $0x0102040810204080, X16
	CP_NONSPACE_WORD(0)
	MOV X6, X17
	CP_NONSPACE_WORD(8)
	SLL $8, X6
	OR X6, X17
	CP_NONSPACE_WORD(16)
	SLL $16, X6
	OR X6, X17
	CP_NONSPACE_WORD(24)
	SLL $24, X6
	OR X6, X17
	CP_NONSPACE_WORD(32)
	SLL $32, X6
	OR X6, X17
	CP_NONSPACE_WORD(40)
	SLL $40, X6
	OR X6, X17
	CP_NONSPACE_WORD(48)
	SLL $48, X6
	OR X6, X17
	CP_NONSPACE_WORD(56)
	SLL $56, X6
	OR X6, X17
	MOV X17, mask+8(FP)
	RET

// One word of string classification at offset, merged at bit position shift:
// X20 backslashes, X21 quotes and, when X19 is all ones, X22 raw controls.
// A raw control byte has its top three bits clear.
#define CP_STRING_WORD(offset, shift) \
	MOV offset(X10), X5; \
	CP_EQ80(X5, X11, X15, X6); \
	CP_PACK8(X6, X16); \
	SLL $shift, X6; \
	OR X6, X20; \
	CP_EQ80(X5, X12, X15, X6); \
	CP_PACK8(X6, X16); \
	SLL $shift, X6; \
	OR X6, X21; \
	AND X13, X5, X6; \
	ORCB X6, X6; \
	ANDN X6, X15, X6; \
	AND X19, X6; \
	CP_PACK8(X6, X16); \
	SLL $shift, X6; \
	OR X6, X22

// func cParityStringMasks32(src unsafe.Pointer, opts uint64) (bs, quote, control uint32)
// C string_block_new: backslashes and quotes are always classified; bytes
// <= 31 only when option bit 5 is set. The caller pads all 32 bytes.
TEXT ·cParityStringMasks32(SB), NOSPLIT, $0-28
	MOV src+0(FP), X10
	MOV opts+8(FP), X19
	SRL $5, X19
	AND $1, X19
	NEG X19
	MOV $0x5c5c5c5c5c5c5c5c, X11
	MOV $0x2222222222222222, X12
	MOV $0xe0e0e0e0e0e0e0e0, X13
	MOV $0x8080808080808080, X15
	MOV $0x0102040810204080, X16
	MOV $0, X20
	MOV $0, X21
	MOV $0, X22
	CP_STRING_WORD(0, 0)
	CP_STRING_WORD(8, 8)
	CP_STRING_WORD(16, 16)
	CP_STRING_WORD(24, 24)
	MOVW X20, bs+16(FP)
	MOVW X21, quote+20(FP)
	MOVW X22, control+24(FP)
	RET

// func cParityMove32(dst, src unsafe.Pointer)
// Load all 32 bytes before storing any, matching C's value semantics when the
// output trails the input by fewer than 32 bytes.
TEXT ·cParityMove32(SB), NOSPLIT, $0-16
	MOV dst+0(FP), X10
	MOV src+8(FP), X11
	MOV 0(X11), X5
	MOV 8(X11), X6
	MOV 16(X11), X7
	MOV 24(X11), X9
	MOV X5, 0(X10)
	MOV X6, 8(X10)
	MOV X7, 16(X10)
	MOV X9, 24(X10)
	RET

// One word of numeric classification merged at bit position shift into
// X20 dots, X21 exponent letters, X22 signs and X23 valid bytes. A digit has
// high nibble 3 and a low nibble whose sum with 6 stays below 16; the nibble
// addition cannot carry between bytes.
#define CP_NUMBER_WORD(offset, shift) \
	MOV offset(X10), X5; \
	CP_EQ80(X5, X11, X15, X6); \
	OR X30, X5, X7; \
	CP_EQ80(X7, X12, X15, X7); \
	CP_EQ80(X5, X13, X15, X9); \
	CP_EQ80(X5, X14, X15, X17); \
	OR X17, X9; \
	AND X18, X5, X17; \
	CP_EQ80(X17, X24, X15, X17); \
	AND X25, X5, X28; \
	ADD X26, X28; \
	ANDN X28, X29, X28; \
	SLL $3, X28; \
	AND X28, X17; \
	OR X6, X17; \
	OR X7, X17; \
	OR X9, X17; \
	CP_PACK8(X6, X16); \
	CP_PACK8(X7, X16); \
	CP_PACK8(X9, X16); \
	CP_PACK8(X17, X16); \
	SLL $shift, X6; \
	SLL $shift, X7; \
	SLL $shift, X9; \
	SLL $shift, X17; \
	OR X6, X20; \
	OR X7, X21; \
	OR X9, X22; \
	OR X17, X23

// func cParityNumberMasks16(src unsafe.Pointer) (decimal, exponent, sign, valid uint32)
// The C raw-number scanner classifies one sixteen-byte block. Valid bytes are
// ASCII digits plus the decimal point, both exponent letters and both signs.
TEXT ·cParityNumberMasks16(SB), NOSPLIT, $0-24
	MOV src+0(FP), X10
	MOV $0x2e2e2e2e2e2e2e2e, X11
	MOV $0x6565656565656565, X12
	MOV $0x2b2b2b2b2b2b2b2b, X13
	MOV $0x2d2d2d2d2d2d2d2d, X14
	MOV $0x8080808080808080, X15
	MOV $0x0102040810204080, X16
	MOV $0xf0f0f0f0f0f0f0f0, X18
	MOV $0x3030303030303030, X24
	MOV $0x0f0f0f0f0f0f0f0f, X25
	MOV $0x0606060606060606, X26
	MOV $0x1010101010101010, X29
	MOV $0x2020202020202020, X30
	MOV $0, X20
	MOV $0, X21
	MOV $0, X22
	MOV $0, X23
	CP_NUMBER_WORD(0, 0)
	CP_NUMBER_WORD(8, 8)
	MOVW X20, decimal+8(FP)
	MOVW X21, exponent+12(FP)
	MOVW X22, sign+16(FP)
	MOVW X23, valid+20(FP)
	RET
