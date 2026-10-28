//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func numberScan(src *byte, size int, mode uint64) (end int, err types.ParsingError)
// One complete operation per Go ABI0 call. Every vector load is bounded by
// the remaining input and uses the actual VL. No padding or conversion occurs.
// X27 (g), SP, and RA are not used as scratch; Go owns the frame metadata.
TEXT ·numberScan(SB), $0-40
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	mode+16(FP), X13
	MOV	$0, X12                  // current relative offset
	MOV	$2, X5
	BEQ	X13, X5, skip_init
	BEQZ	X11, eof
	MOV	$48, X16                 // digit zero
	MOV	$10, X17                 // digit range
	MOV	$46, X18                 // decimal point
	MOV	$101, X19                // lowercase exponent
	MOV	$43, X24                 // plus
	MOV	$45, X25                 // minus
	MOV	$32, X26                 // ASCII lowercase bit
	MOVBU	0(X10), X6
	BNE	X6, X25, first_digit
	ADD	$1, X10
	ADD	$1, X12
	SUB	$1, X11
	BEQZ	X11, eof
	MOVBU	0(X10), X6
first_digit:
	SUB	X16, X6, X7
	BGEU	X7, X17, invalid
	BNEZ	X13, strict_init
	MOV	$0, X20                  // prefix state: integer/fraction/exponent
	BNE	X6, X16, prefix_digits
	// A leading zero consumes exactly one integer digit, even before a digit.
	ADD	$1, X10
	ADD	$1, X12
	SUB	$1, X11
	JMP	prefix_integer_done

prefix_digits:
	VSETVLI	X11, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	VSUBVX	X16, V8, V16
	VMSLTUVX X17, V16, V2
	VMNOTM	V2, V2
	VFIRSTM	V2, X6
	BGEZ	X6, prefix_nondigit
	ADD	X5, X10
	ADD	X5, X12
	SUB	X5, X11
	BNEZ	X11, prefix_digits
	JMP	success
prefix_nondigit:
	ADD	X6, X10
	ADD	X6, X12
	SUB	X6, X11
	BEQZ	X20, prefix_integer_done
	MOV	$1, X5
	BEQ	X20, X5, prefix_fraction_done
	JMP	success
prefix_integer_done:
	BEQZ	X11, success
	MOVBU	0(X10), X6
	BNE	X6, X18, prefix_fraction_done
	ADD	$1, X10
	ADD	$1, X12
	SUB	$1, X11
	BEQZ	X11, eof
	MOVBU	0(X10), X6
	SUB	X16, X6, X7
	BGEU	X7, X17, invalid
	MOV	$1, X20
	JMP	prefix_digits
prefix_fraction_done:
	BEQZ	X11, success
	MOVBU	0(X10), X6
	OR	X26, X6, X7
	BNE	X7, X19, success
	ADD	$1, X10
	ADD	$1, X12
	SUB	$1, X11
	BEQZ	X11, eof
	MOVBU	0(X10), X6
	BEQ	X6, X24, prefix_exponent_sign
	BNE	X6, X25, prefix_exponent_digit
prefix_exponent_sign:
	ADD	$1, X10
	ADD	$1, X12
	SUB	$1, X11
	BEQZ	X11, eof
	MOVBU	0(X10), X6
prefix_exponent_digit:
	SUB	X16, X6, X7
	BGEU	X7, X17, invalid
	MOV	$2, X20
	JMP	prefix_digits

strict_init:
	// At most eight integer digits stay scalar. The first digit is known;
	// a leading zero still ends immediately unless followed by '.' or 'e'.
	MOV	$1, X5
	BNE	X6, X16, strict_short_limit
	BEQ	X11, X5, strict_zero_done
	MOVBU	1(X10), X6
	BEQ	X6, X18, strict_continue_markers
	OR	X26, X6
	BEQ	X6, X19, strict_continue_markers
strict_zero_done:
	ADD	$1, X12
	JMP	success
strict_short_limit:
	MOV	$8, X14
strict_short_scan:
	BEQ	X5, X11, strict_short_done
	ADD	X5, X10, X8
	MOVBU	0(X8), X6
	SUB	X16, X6, X7
	BGEU	X7, X17, strict_short_boundary
	BEQ	X5, X14, strict_continue_markers // ninth digit: continue with RVV
	ADD	$1, X5
	JMP	strict_short_scan
strict_short_boundary:
	// The preceding digits cannot affect marker ordering or duplicates. Resume
	// the complete candidate scanner at this first unconsumed byte.
	BEQ	X6, X18, strict_continue_markers
	OR	X26, X6, X7
	BEQ	X7, X19, strict_continue_markers
	BEQ	X6, X24, strict_continue_markers
	BEQ	X6, X25, strict_continue_markers
strict_short_done:
	ADD	X5, X12
	JMP	success
strict_continue_markers:
	ADD	X5, X10
	ADD	X5, X12
	SUB	X5, X11
strict_markers:
	MOV	$-1, X20                 // first decimal point
	MOV	$-1, X21                 // first exponent
	MOV	$-1, X22                 // first candidate sign (excludes initial '-')
strict_scan:
	VSETVLI	X11, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	VSUBVX	X16, V8, V16
	VMSGTUVI $9, V16, V2             // every non-digit is an ordered event
	VFIRSTM	V2, X6
	BLTZ	X6, strict_advance       // keep the full-VL digit-only path
	MOV	$64, X7
	BGEU	X6, X7, strict_late_event
	// A scalar mask holds at most 64 events. The input group can be larger;
	// only consume its first 64 bytes and carry marker positions forward.
	MINU	X5, X7, X5
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X23
	SUB	X5, X7, X7
	MOV	$-1, X8
	SRL	X7, X8
	AND	X8, X23                 // discard mask-agnostic tail bits
strict_event:
	CTZ	X23, X6
	ADD	X6, X10, X8
	MOVBU	0(X8), X7
	BEQ	X7, X18, strict_dot_event
	OR	X26, X7, X8
	BEQ	X8, X19, strict_exponent_event
	BEQ	X7, X24, strict_sign_event
	BEQ	X7, X25, strict_sign_event
	// The first non-candidate ends this number. Later mask bits belong to
	// following JSON tokens and must not affect marker validation.
	ADD	X6, X12
	JMP	strict_finish
strict_dot_event:
	BGEZ	X20, strict_duplicate
	ADD	X12, X6, X20
	JMP	strict_next_event
strict_exponent_event:
	BGEZ	X21, strict_duplicate
	ADD	X12, X6, X21
	JMP	strict_next_event
strict_sign_event:
	BGEZ	X22, strict_duplicate
	ADD	X12, X6, X22
strict_next_event:
	ADD	$-1, X23, X7
	AND	X7, X23
	BNEZ	X23, strict_event
strict_advance:
	ADD	X5, X10
	ADD	X5, X12
	SUB	X5, X11
	BNEZ	X11, strict_scan
	JMP	strict_finish
strict_late_event:
	// All preceding bytes are known digits, even when VLEN exceeds 128.
	// Resume at the first event rather than truncating its index to 64 bits.
	MOV	X6, X5
	JMP	strict_advance
strict_finish:
	// The first byte after an optional '-' was checked to be a digit, so
	// none of the marker positions can equal the candidate's first byte.
	ADD	$-1, X12, X6
	BEQ	X20, X6, strict_trailing
	BEQ	X21, X6, strict_trailing
	BEQ	X22, X6, strict_trailing
	BLTZ	X22, strict_dot_order
	ADD	$-1, X22, X7
	BNE	X21, X7, strict_bad_sign
strict_dot_order:
	BLTZ	X20, success
	BLTZ	X21, success
	BGE	X20, X21, strict_bad_dot
	ADD	$-1, X21, X6
	BEQ	X20, X6, strict_bad_exponent
	JMP	success
strict_duplicate:
	ADD	X6, X12
	JMP	invalid
strict_trailing:
	MOV	X6, X12
	JMP	invalid
strict_bad_sign:
	MOV	X22, X12
	JMP	invalid
strict_bad_dot:
	MOV	X20, X12
	JMP	invalid
strict_bad_exponent:
	MOV	X21, X12
	JMP	invalid

skip_init:
	BEQZ	X11, success
	MOV	$125, X16                // }
	MOV	$93, X17                 // ]
	MOV	$44, X18                 // comma
	MOV	$32, X19                 // space
skip_scan:
	VSETVLI	X11, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	VMSEQVX	X16, V8, V2
	VMSEQVX	X17, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X18, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X19, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVI	$9, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVI	$10, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVI	$13, V8, V3
	VMORMM	V2, V3, V2
	VFIRSTM	V2, X6
	BGEZ	X6, skip_found
	ADD	X5, X10
	ADD	X5, X12
	SUB	X5, X11
	BNEZ	X11, skip_scan
	JMP	success
skip_found:
	ADD	X6, X12
success:
	MOV	$0, X5
	JMP	done
eof:
	MOV	$1, X5                   // types.ERR_EOF
	JMP	done
invalid:
	MOV	$2, X5                   // types.ERR_INVALID_CHAR
done:
	MOV	X12, end+24(FP)
	MOV	X5, err+32(FP)
	RET
