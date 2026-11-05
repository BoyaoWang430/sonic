//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// Accumulate exact non-match bytes. Complement once after all seven boundary
// characters have been compared to locate the first matching byte.
#define NUMBER_NONBOUNDARY(c) \
	XOR c, X23, X5; \
	ORCB X5, X5; \
	AND X5, X14, X14

// Exact non-digit high bits in X6. Tail callers zero-fill the bytes past EOF.
#define NUMBER_DIGIT_FLAGS \
	AND X9, X23, X5; \
	ADD X28, X5, X6; \
	ADD X29, X5, X7; \
	ANDN X7, X6, X6; \
	ANDN X23, X6, X6; \
	AND X14, X6; \
	XOR X14, X6

// func numberScan(src *byte, size int, mode uint64) (end int, err types.ParsingError)
// Modes: prefix grammar (0), strict candidate grammar (1), relaxed boundaries (2).
// Digit runs use bounded unaligned words. FP, g, SP, RA, and X31 are not scratch.
TEXT ·numberScan(SB), $0-40
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV mode+16(FP), X13
	MOV $0, X12
	MOV $2, X5
	BEQ X13, X5, num_skip_init
	BEQZ X11, num_eof
	MOV $48, X16
	MOV $10, X17
	MOV $46, X18
	MOV $101, X19
	MOV $43, X24
	MOV $45, X25
	MOV $32, X26
	MOVBU 0(X10), X6
	BNE X6, X25, num_first_digit
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	BEQZ X11, num_eof
	MOVBU 0(X10), X6
num_first_digit:
	SUB X16, X6, X7
	BGEU X7, X17, num_invalid
	BNEZ X13, num_strict_init
	MOV $0x7f7f7f7f7f7f7f7f, X9
	MOV $0x8080808080808080, X14
	MOV $0x5050505050505050, X28
	MOV $0x4646464646464646, X29
	MOV $0, X20                 // integer, fraction, exponent
	BNE X6, X16, num_digits
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	JMP num_integer_done

	// All entries jump to the aligned word loop after this padding.
	PCALIGN $64
num_digits:
	BEQZ X11, num_digits_eof
	MOV $8, X5
	BLTU X11, X5, num_digits_tail
	MOV 0(X10), X23
	NUMBER_DIGIT_FLAGS
	BNEZ X6, num_word_nondigit
	ADD $8, X10
	ADD $8, X12
	SUB $8, X11
	JMP num_digits
num_word_nondigit:
	CTZ X6, X5
	SRL $3, X5
num_nondigit_at:
	ADD X5, X10
	ADD X5, X12
	SUB X5, X11
	MOVBU 0(X10), X6
	JMP num_nondigit
num_digit_byte:
	MOVBU 0(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_nondigit
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	JMP num_digits
num_digits_eof:
	BNEZ X13, num_strict_finish
	JMP num_success
num_nondigit:
	BNEZ X13, num_strict_marker
	BEQZ X20, num_integer_done
	MOV $1, X5
	BEQ X20, X5, num_fraction_done
	JMP num_success
num_integer_done:
	BEQZ X11, num_success
	MOVBU 0(X10), X6
	BNE X6, X18, num_fraction_done
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	BEQZ X11, num_eof
	MOVBU 0(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_invalid
	MOV $1, X20
	JMP num_digits
num_fraction_done:
	BEQZ X11, num_success
	MOVBU 0(X10), X6
	OR X26, X6, X7
	BNE X7, X19, num_success
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	BEQZ X11, num_eof
	MOVBU 0(X10), X6
	BEQ X6, X24, num_exponent_sign
	BNE X6, X25, num_exponent_digit
num_exponent_sign:
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	BEQZ X11, num_eof
	MOVBU 0(X10), X6
num_exponent_digit:
	SUB X16, X6, X7
	BGEU X7, X17, num_invalid
	MOV $2, X20
	JMP num_digits

num_strict_init:
	// Match the RVV short-integer path: scan at most eight digits before
	// preparing word classification or the complete candidate grammar.
	MOV $1, X5
	BNE X6, X16, num_strict_short_limit
	BEQ X11, X5, num_strict_zero_done
	MOVBU 1(X10), X6
	BEQ X6, X18, num_strict_continue
	OR X26, X6
	BEQ X6, X19, num_strict_continue
num_strict_zero_done:
	ADD $1, X12
	JMP num_success
	// Keep the unrolled short-integer probe on its original cache-line boundary.
	PCALIGN $64
num_strict_short_limit:
	MOV $8, X14
	BGEU X14, X11, num_strict_short_scan
	// With nine readable bytes, test each position without loop bookkeeping.
	// Short tails use the bounded loop below; the ninth digit stays unconsumed.
	MOVBU 1(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at1
	MOVBU 2(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at2
	MOVBU 3(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at3
	MOVBU 4(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at4
	MOVBU 5(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at5
	MOVBU 6(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at6
	MOVBU 7(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at7
	MOVBU 8(X10), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_at8
	MOV $8, X5
	JMP num_strict_continue
num_strict_short_at1:
	MOV $1, X5
	JMP num_strict_short_boundary
num_strict_short_at2:
	MOV $2, X5
	JMP num_strict_short_boundary
num_strict_short_at3:
	MOV $3, X5
	JMP num_strict_short_boundary
num_strict_short_at4:
	MOV $4, X5
	JMP num_strict_short_boundary
num_strict_short_at5:
	MOV $5, X5
	JMP num_strict_short_boundary
num_strict_short_at6:
	MOV $6, X5
	JMP num_strict_short_boundary
num_strict_short_at7:
	MOV $7, X5
	JMP num_strict_short_boundary
num_strict_short_at8:
	MOV $8, X5
	JMP num_strict_short_boundary
num_strict_short_scan:
	BEQ X5, X11, num_strict_short_done
	ADD X5, X10, X15
	MOVBU 0(X15), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_boundary
	BEQ X5, X14, num_strict_continue // ninth digit: use word scanning
	ADD $1, X5
	JMP num_strict_short_scan
	// The preceding short scan also jumps; padding is never on its hot path.
	PCALIGN $64
num_strict_short_boundary:
	// Markers still need complete validation, including duplicate-error
	// priority. Resume at this byte without rescanning the digit prefix.
	BEQ X6, X18, num_strict_continue
	OR X26, X6, X7
	BEQ X7, X19, num_strict_continue
	BEQ X6, X24, num_strict_continue
	BEQ X6, X25, num_strict_continue
num_strict_short_done:
	ADD X5, X12
	MOV X12, end+24(FP)
	MOV X0, err+32(FP)
	RET
num_strict_continue:
	ADD X5, X10
	ADD X5, X12
	SUB X5, X11
	MOV $-1, X20                // decimal point position
	MOV $-1, X21                // exponent position
	MOV $-1, X22                // candidate sign (not an initial minus)
	// Short fractions and exponents do not need word-classification constants.
	// A ninth integer digit still enters the original word scanner directly.
	SUB X16, X6, X7
	BLTU X7, X17, num_strict_prepare_check
	MOV $0, X30                // word classification has not been prepared
	JMP num_strict_marker
num_strict_prepare_digits:
	MOV $1, X30
	MOV $0x7f7f7f7f7f7f7f7f, X9
	MOV $0x8080808080808080, X14
	MOV $0x5050505050505050, X28
	MOV $0x4646464646464646, X29
	JMP num_digits
num_strict_marker:
	BEQ X6, X18, num_strict_dot
	OR X26, X6, X7
	BEQ X7, X19, num_strict_exponent
	BEQ X6, X24, num_strict_sign
	BEQ X6, X25, num_strict_sign
	JMP num_strict_finish
num_strict_dot:
	BGEZ X20, num_invalid
	MOV X12, X20
	JMP num_strict_advance
num_strict_exponent:
	BGEZ X21, num_invalid
	MOV X12, X21
	JMP num_strict_advance
num_strict_sign:
	BGEZ X22, num_invalid
	MOV X12, X22
num_strict_advance:
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	BEQZ X30, num_strict_short_float_digits
	JMP num_digits

num_strict_short_float_digits:
	// Keep short digit runs after a marker in a bounded byte loop. Marker
	// positions remain live, so unusual order and duplicates still use the
	// original strict grammar and its exact error precedence.
	MOV $0, X5
	MOV $4, X14
num_strict_short_float_scan:
	BEQ X5, X11, num_strict_short_float_done
	ADD X5, X10, X15
	MOVBU 0(X15), X6
	SUB X16, X6, X7
	BGEU X7, X17, num_strict_short_float_boundary
	BEQ X5, X14, num_strict_short_float_long
	ADD $1, X5
	JMP num_strict_short_float_scan
num_strict_short_float_boundary:
	ADD X5, X10
	ADD X5, X12
	SUB X5, X11
	JMP num_strict_marker
num_strict_short_float_long:
	// Preserve prior markers and continue at the first unconsumed digit.
	// Later runs reuse the prepared word scanner rather than probing again.
	ADD X5, X10
	ADD X5, X12
	SUB X5, X11
	JMP num_strict_prepare_check
num_strict_short_float_done:
	ADD X5, X12
	JMP num_strict_finish
num_strict_finish:
	// Duplicate markers have already won over positional errors. A final
	// marker is reported at that marker, including a final exponent sign.
	ADD $-1, X12, X6
	BEQ X20, X6, num_strict_trailing
	BEQ X21, X6, num_strict_trailing
	BEQ X22, X6, num_strict_trailing
	BLTZ X22, num_strict_dot_order
	ADD $-1, X22, X7
	BNE X21, X7, num_strict_bad_sign
num_strict_dot_order:
	BLTZ X20, num_success
	BLTZ X21, num_success
	BGE X20, X21, num_strict_bad_dot
	ADD $-1, X21, X6
	BEQ X20, X6, num_strict_bad_exponent
	JMP num_success
num_strict_trailing:
	MOV X6, X12
	JMP num_invalid
num_strict_bad_sign:
	MOV X22, X12
	JMP num_invalid
num_strict_bad_dot:
	MOV X20, X12
	JMP num_invalid
num_strict_bad_exponent:
	MOV X21, X12
	JMP num_invalid

num_skip_init:
	BEQZ X11, num_success
	MOV $0x7d7d7d7d7d7d7d7d, X16
	MOV $0x5d5d5d5d5d5d5d5d, X17
	MOV $0x2c2c2c2c2c2c2c2c, X18
	MOV $0x2020202020202020, X19
	MOV $0x0909090909090909, X20
	MOV $0x0a0a0a0a0a0a0a0a, X21
	MOV $0x0d0d0d0d0d0d0d0d, X22
	MOV $0x8080808080808080, X29
num_skip:
	BEQZ X11, num_success
	MOV $8, X5
	BLTU X11, X5, num_skip_tail
	MOV 0(X10), X23
num_skip_classify:
	XOR X16, X23, X14
	ORCB X14, X14
	NUMBER_NONBOUNDARY(X17)
	NUMBER_NONBOUNDARY(X18)
	NUMBER_NONBOUNDARY(X19)
	NUMBER_NONBOUNDARY(X20)
	NUMBER_NONBOUNDARY(X21)
	NUMBER_NONBOUNDARY(X22)
	ANDN X14, X29, X14
	BNEZ X14, num_skip_found
	ADD $8, X10
	ADD $8, X12
	SUB $8, X11
	JMP num_skip
num_skip_found:
	CTZ X14, X5
	SRL $3, X5
	ADD X5, X12
	JMP num_success
num_skip_byte:
	MOVBU 0(X10), X6
	MOV $125, X5
	BEQ X6, X5, num_success
	MOV $93, X5
	BEQ X6, X5, num_success
	MOV $44, X5
	BEQ X6, X5, num_success
	MOV $32, X5
	BEQ X6, X5, num_success
	MOV $9, X5
	BEQ X6, X5, num_success
	MOV $10, X5
	BEQ X6, X5, num_success
	MOV $13, X5
	BEQ X6, X5, num_success
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	JMP num_skip

num_success:
	MOV X12, end+24(FP)
	MOV X0, err+32(FP)
	RET
num_eof:
	MOV $1, X5
	JMP num_done
num_invalid:
	MOV $2, X5
num_done:
	MOV X12, end+24(FP)
	MOV X5, err+32(FP)
	RET

num_digits_tail:
	MOV $4, X6
	BLTU X11, X6, num_digit_byte
	ADD X12, X11, X6
	BLTU X6, X5, num_digit_byte  // end-8 must remain inside the original input
	ADD X11, X10, X6
	MOV -8(X6), X23
	SUB X11, X5, X5
	SLL $3, X5
	SRL X5, X23, X23           // discard already consumed bytes
	NUMBER_DIGIT_FLAGS
	// Only this cold path can find a synthetic non-digit at logical EOF.
	// Complete words always identify an actual byte and need no EOF check.
	CTZ X6, X5
	SRL $3, X5
	BEQ X5, X11, num_digits_tail_eof
	JMP num_nondigit_at
num_digits_tail_eof:
	ADD X11, X12
	JMP num_digits_eof

num_skip_tail:
	MOV $4, X6
	BLTU X11, X6, num_skip_byte
	ADD X12, X11, X6
	BLTU X6, X5, num_skip_byte
	ADD X11, X10, X6
	MOV -8(X6), X23
	SUB X11, X5, X5
	SLL $3, X5
	SRL X5, X23, X23
	SLL $3, X11, X5
	SLL X5, X19, X6            // a synthetic space marks the logical end
	OR X6, X23
	JMP num_skip_classify

num_strict_prepare_check:
	// Keep the rare transition to word classification outside the short-float
	// path. A short remainder cannot amortize word-classification constants.
	MOV $8, X5
	BGEU X11, X5, num_strict_prepare_digits
	MOV $0, X30
	JMP num_strict_short_float_digits

#undef NUMBER_NONBOUNDARY
#undef NUMBER_DIGIT_FLAGS
