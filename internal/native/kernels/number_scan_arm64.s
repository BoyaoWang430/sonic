//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"
// Shared NEON whitespace classifier and exact bit packing. These macros only
// use ordinary Go assembler instructions; the input remains unmodified.
// V29 holds the whitespace lookup, V30 bit weights, V31 the packing shuffle.
#define INIT_WHITESPACE \
	MOVD $·whitespaceTables(SB), R9; \
	VLD1 (R9), [V29.B16, V30.B16, V31.B16]; \
	VMOVI $143, V25.B16

// Keep bit 7 when forming the table index. Bytes >= 128 therefore index past
// the table and yield zero, which cannot equal their original byte. Of the
// remaining bytes, only space, tab, LF, and CR match their table entry.
#define CLASSIFY_WHITESPACE(input, output) \
	VAND V25.B16, input, output; \
	VTBL output, [V29.B16], output; \
	VCMEQ input, output, output

#define PACK_WHITESPACE(reg, output) \
	VAND V30.B16, reg.B16, reg.B16; \
	VTBL V31.B16, [reg.B16], reg.B16; \
	VADDV reg.H8, reg; \
	VMOV reg.H[0], output

// Requires 64 readable bytes at R0. Returns one bit per nonspace byte in R9.
// R0/R1/R3 are preserved. Scratch: R9-R12, V0-V7; constants V25/V29-V31.
#define NONSPACE64 \
	FLDPQ (R0), (F0, F1); \
	FLDPQ 32(R0), (F2, F3); \
	CLASSIFY_WHITESPACE(V0.B16, V4.B16); \
	CLASSIFY_WHITESPACE(V1.B16, V5.B16); \
	CLASSIFY_WHITESPACE(V2.B16, V6.B16); \
	CLASSIFY_WHITESPACE(V3.B16, V7.B16); \
	PACK_WHITESPACE(V4, R9); \
	PACK_WHITESPACE(V5, R10); \
	PACK_WHITESPACE(V6, R11); \
	PACK_WHITESPACE(V7, R12); \
	ORR R10<<16, R9, R9; \
	ORR R11<<32, R9, R9; \
	ORR R12<<48, R9, R9; \
	MVN R9, R9

// numberScan completes prefix, strict-candidate, or relaxed scanning in one call.
// R0/R1 are the cursor/remaining bytes; R3 is the relative offset. R4 selects
// integer/fraction/exponent (0/1/2), strict integer (3), or strict candidate
// after a marker (4). R12/R13/R14 hold strict dot/exponent/sign positions.
// R15/R16 are word digit
// constants; R17 records initialized word/vector constants in bits 0/1.
// The word probe handles short numbers; NEON handles long digit runs. All
// loads are bounded by the logical input, with scalar tails. There are no
// calls, stack scratch, conversions, or input writes. R18, g, FP/LR are kept.
TEXT ·numberScan(SB), NOSPLIT, $0-40
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD mode+16(FP), R2
	MOVD $0, R3
	CMP $2, R2
	BEQ skip_init
	CBZ R1, eof
	MOVBU (R0), R5
	CMP $45, R5
	BNE first_digit
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBZ R1, eof
	MOVBU (R0), R5
first_digit:
	SUB $48, R5, R6
	CMP $9, R6
	BHI invalid
	MOVD $0, R17
	CBNZ R2, strict_init
	MOVD $0, R4
	CMP $48, R5
	BNE digits
	// A leading zero ends the integer portion before any following digit.
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	B prefix_integer_done

strict_init:
	CMP $48, R5
	BNE strict_markers
	CMP $1, R1
	BEQ strict_zero_done
	MOVBU 1(R0), R5
	CMP $46, R5
	BEQ strict_markers
	ORR $32, R5, R6
	CMP $101, R6
	BEQ strict_markers
strict_zero_done:
	ADD $1, R3
	B success
strict_markers:
	MOVD $3, R4
	MOVD $-1, R12
	MOVD $-1, R13
	MOVD $-1, R14
digits:
	CBZ R1, digits_done
	CMP $8, R1
	BLO digit_tail
	TBNZ $0, R17, digit_word
	MOVD $0x3030303030303030, R15
	MOVD $0x4646464646464646, R16
	ORR $1, R17
digit_word:
	MOVD (R0), R5
	SUB R15, R5, R6
	ADD R16, R5, R7
	ORR R5, R6, R6
	ORR R7, R6, R6
	ANDS $0x8080808080808080, R6
	BNE word_event
	ADD $8, R0
	SUB $8, R1
	ADD $8, R3
	CMP $16, R1
	BLO digits
	TBNZ $1, R17, vector_digits
	MOVD $·whitespaceTables+16(SB), R9
	VLD1 (R9), [V30.B16, V31.B16]
	VMOVI $48, V22.B16
	VMOVI $9, V23.B16
	ORR $2, R17
vector_digits:
	VLD1 (R0), [V0.B16]
	VSUB V22.B16, V0.B16, V2.B16
	VUMIN V23.B16, V2.B16, V3.B16
	VCMEQ V3.B16, V2.B16, V2.B16
	PACK_WHITESPACE(V2, R9)
	EOR $65535, R9, R9
	CBNZ R9, vector_event
	ADD $16, R0
	SUB $16, R1
	ADD $16, R3
	CMP $16, R1
	BHS vector_digits
	B digits
word_event:
	// Earlier lanes are digits, so no carry/borrow can corrupt the first hit.
	RBIT R6, R6
	CLZ R6, R6
	LSR $3, R6, R6
	B digit_event_at
vector_event:
	RBIT R9, R6
	CLZ R6, R6
digit_event_at:
	ADD R6, R0
	SUB R6, R1
	ADD R6, R3
	MOVBU (R0), R5
	B digit_event
digit_tail:
	MOVBU (R0), R5
	SUB $48, R5, R6
	CMP $9, R6
	BHI digit_event
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBNZ R1, digit_tail
digits_done:
	CMP $4, R4
	BEQ strict_finish
	B success
digit_event:
	CMP $3, R4
	BHS strict_event
	// R5 already holds the first non-digit; keep it across grammar phases.
	CBZ R4, prefix_integer_event
	CMP $1, R4
	BEQ prefix_fraction_event
	B success

prefix_integer_done:
	CBZ R1, success
	MOVBU (R0), R5
prefix_integer_event:
	CMP $46, R5
	BNE prefix_fraction_event
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBZ R1, eof
	MOVBU (R0), R5
	SUB $48, R5, R6
	CMP $9, R6
	BHI invalid
	MOVD $1, R4
	B digits
prefix_fraction_event:
	ORR $32, R5, R6
	CMP $101, R6
	BNE success
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBZ R1, eof
	MOVBU (R0), R5
	CMP $43, R5
	BEQ prefix_exponent_sign
	CMP $45, R5
	BNE prefix_exponent_digit
prefix_exponent_sign:
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBZ R1, eof
	MOVBU (R0), R5
prefix_exponent_digit:
	SUB $48, R5, R6
	CMP $9, R6
	BHI invalid
	MOVD $2, R4
	B digits

strict_event:
	CMP $46, R5
	BEQ strict_dot
	ORR $32, R5, R6
	CMP $101, R6
	BEQ strict_exponent
	CMP $43, R5
	BEQ strict_sign
	CMP $45, R5
	BNE strict_finish
strict_sign:
	CMP $0, R14
	BGE invalid
	MOVD R3, R14
	B strict_advance
strict_dot:
	CMP $0, R12
	BGE invalid
	MOVD R3, R12
	B strict_advance
strict_exponent:
	CMP $0, R13
	BGE invalid
	MOVD R3, R13
	MOVD $4, R4
	// An adjacent exponent sign has no digit run before it. Consume it here
	// without another word/vector classification, preserving duplicate order.
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBZ R1, strict_finish
	MOVBU (R0), R5
	CMP $43, R5
	BEQ strict_sign
	CMP $45, R5
	BEQ strict_sign
	B digits
strict_advance:
	MOVD $4, R4
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	B digits
strict_finish:
	// With no marker, the checked digit prefix needs no ordering validation.
	CMP $3, R4
	BEQ success
	// Duplicate markers are rejected during scanning, before the final
	// trailing-marker/sign/order checks. Keep that error-position priority.
	SUB $1, R3, R6
	CMP R6, R12
	BEQ strict_trailing
	CMP R6, R13
	BEQ strict_trailing
	CMP R6, R14
	BEQ strict_trailing
	CMP $0, R14
	BLT strict_dot_order
	SUB $1, R14, R7
	CMP R7, R13
	BNE strict_bad_sign
strict_dot_order:
	CMP $0, R12
	BLT success
	CMP $0, R13
	BLT success
	CMP R13, R12
	BGE strict_bad_dot
	SUB $1, R13, R6
	CMP R6, R12
	BEQ strict_bad_exponent
	B success
strict_trailing:
	MOVD R6, R3
	B invalid
strict_bad_sign:
	MOVD R14, R3
	B invalid
strict_bad_dot:
	MOVD R12, R3
	B invalid
strict_bad_exponent:
	MOVD R13, R3
	B invalid

success:
	MOVD $0, R5
	B done
eof:
	MOVD $1, R5                 // types.ERR_EOF
	B done
invalid:
	MOVD $2, R5                 // types.ERR_INVALID_CHAR
done:
	MOVD R3, end+24(FP)
	MOVD R5, err+32(FP)
	RET

// Mode 2 scans only relaxed boundaries; grammar deliberately remains unchecked.
// Reuse the same cursor and return ABI without another Go or assembly call.
skip_init:
	CBZ R1, success
	CMP $16, R1
	BLO skip_tail
	MOVD $·numberBoundaryTables(SB), R9
	VLD1 (R9), [V22.B16, V23.B16]
	MOVD $·whitespaceTables+16(SB), R9
	VLD1 (R9), [V30.B16, V31.B16]
	VMOVI $15, V25.B16
skip_block:
	VLD1 (R0), [V0.B16]
	VAND V25.B16, V0.B16, V2.B16
	VUSHR $4, V0.B16, V3.B16
	VTBL V2.B16, [V22.B16], V2.B16
	VTBL V3.B16, [V23.B16], V3.B16
	VAND V3.B16, V2.B16, V2.B16
	VADDV V2.B16, V6
	VMOV V6.B[0], R9
	CBNZ R9, skip_found
	ADD $16, R0
	SUB $16, R1
	ADD $16, R3
	CMP $16, R1
	BHS skip_block
	CBZ R1, success
skip_tail:
	MOVBU (R0), R8
	CMP $44, R8
	BEQ success
	CMP $93, R8
	BEQ success
	CMP $125, R8
	BEQ success
	CMP $32, R8
	BEQ success
	CMP $9, R8
	BEQ success
	CMP $10, R8
	BEQ success
	CMP $13, R8
	BEQ success
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBNZ R1, skip_tail
	B success
skip_found:
	// Matches contain 1, 2, 4 or 8. Their sum cannot wrap in a 16-byte
	// block; expand them to all-ones lanes only when packing the first hit.
	VCMTST V2.B16, V2.B16, V2.B16
	PACK_WHITESPACE(V2, R9)
	RBIT R9, R9
	CLZ R9, R9
	ADD R9, R3, R3
	B success

// Intersecting the nibble classes recognizes exactly HT, LF, CR, space,
// comma, ']' and '}'. All high nibbles >= 8 map to zero.
// Low nibble: 0 -> 2, 9/a -> 1, c -> 2, d -> 1|4|8.
// High nibble: 0 -> 1, 2 -> 2, 5 -> 4, 7 -> 8.
DATA ·numberBoundaryTables+0(SB)/8, $0x0000000000000002
DATA ·numberBoundaryTables+8(SB)/8, $0x00000d0200010100
DATA ·numberBoundaryTables+16(SB)/8, $0x0800040000020001
DATA ·numberBoundaryTables+24(SB)/8, $0x0000000000000000
GLOBL ·numberBoundaryTables(SB), RODATA|NOPTR, $32
