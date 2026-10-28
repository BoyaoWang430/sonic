//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)
// Normal Go ABI0 frame metadata and RET; no use of g (X27), SP, or RA as scratch.
TEXT ·quote(SB), $0-56
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	dst+16(FP), X12
	MOV	capacity+24(FP), X13
	MOV	flags+32(FP), X14
	AND	$1, X14
	MOV	X11, X15
	MOV	X13, X16
	MOV	$32, X17
	MOV	$34, X18
	MOV	$92, X19

scan:
	BEQZ	X11, success
	BEQZ	X13, exhausted
	MOV	X11, X20
	BLEU	X11, X13, bounded
	MOV	X13, X20
bounded:
	// Both the load and any plain-byte store fit their logical ranges.
	VSETVLI	X20, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	VMSLTUVX X17, V8, V2
	VMSEQVX	X18, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X19, V8, V3
	VMORMM	V2, V3, V2
	VFIRSTM	V2, X6
	BGEZ	X6, special
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
	JMP	scan

special:
	BEQZ	X6, classify
	// Store only the plain prefix; no output from the escape is committed yet.
	VSETVLI	X6, E8, M4, TA, MA, X5
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
classify:
	MOVBU	0(X10), X7
	BGEU	X7, X17, quote_or_backslash
	MOV	$9, X6
	BEQ	X7, X6, tab
	MOV	$10, X6
	BEQ	X7, X6, newline
	MOV	$13, X6
	BEQ	X7, X6, carriage_return

	// Generic controls: \u00hh or \\u00hh, including backspace/form feed.
	ADD	$6, X14, X21             // whole output length
	ADD	$1, X14, X23             // leading backslash count
	MOV	$1, X24                  // Unicode escape body
	JMP	emit
quote_or_backslash:
	SLLI	$1, X14, X23
	ADD	$2, X23, X21             // 2 bytes normally, 4 when doubled
	ADD	$1, X23
	MOV	X7, X22
	MOV	$0, X24
	JMP	emit
tab:
	MOV	$116, X22
	JMP	short_control
newline:
	MOV	$110, X22
	JMP	short_control
carriage_return:
	MOV	$114, X22
short_control:
	ADD	$2, X14, X21
	ADD	$1, X14, X23
	MOV	$0, X24
emit:
	// Reserve the complete escape before writing its first byte.
	BLTU	X13, X21, exhausted
	MOV	X12, X20
backslashes:
	MOVB	X19, 0(X20)
	ADD	$1, X20
	SUB	$1, X23
	BNEZ	X23, backslashes
	BEQZ	X24, short_body
	MOV	$117, X22
	MOVB	X22, 0(X20)
	MOV	$48, X22
	MOVB	X22, 1(X20)
	MOVB	X22, 2(X20)
	SRLI	$4, X7, X22
	ADD	$48, X22
	MOVB	X22, 3(X20)
	AND	$15, X7, X22
	MOV	$10, X23
	BLTU	X22, X23, hex_digit
	ADD	$87, X22
	JMP	hex_store
hex_digit:
	ADD	$48, X22
hex_store:
	MOVB	X22, 4(X20)
	JMP	escaped
short_body:
	MOVB	X22, 0(X20)
escaped:
	ADD	$1, X10
	SUB	$1, X11
	ADD	X21, X12
	SUB	X21, X13
	JMP	scan

exhausted:
	MOV	X15, X5
	SUB	X11, X5
	XOR	$-1, X5
	JMP	done
success:
	MOV	X15, X5
done:
	MOV	X16, X6
	SUB	X13, X6
	MOV	X5, consumed+40(FP)
	MOV	X6, written+48(FP)
	RET
