//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)
// Normal Go ABI0 frame metadata and RET; no use of g (X27), SP, or RA as scratch.
TEXT ·quote(SB), NOSPLIT, $0-56
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
	MOV	$16, X5
	BLTU	X20, X5, short_bounded
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

short_bounded:
	MOV	$8, X5
	BLTU	X20, X5, short_byte_loop
	MOV	0(X10), X21
	MOV	$0x2222222222222222, X6
	XOR	X21, X6, X6
	ORCB	X6, X6
	MOV	$0x5c5c5c5c5c5c5c5c, X7
	XOR	X21, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0xe0e0e0e0e0e0e0e0, X7
	AND	X21, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0x8080808080808080, X7
	ANDN	X6, X7, X6
	BNEZ	X6, short_word_special
	MOV	X21, 0(X12)
	ADD	$8, X10
	SUB	$8, X11
	ADD	$8, X12
	SUB	$8, X13
	SUB	$8, X20
	BNEZ	X20, short_byte_loop
	BEQZ	X11, success
	JMP	exhausted

short_word_special:
	CTZ	X6, X6
	SRL	$3, X6
	BEQZ	X6, classify
short_word_prefix:
	MOVBU	0(X10), X7
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	SUB	$1, X6
	BNEZ	X6, short_word_prefix
	JMP	classify

short_byte_loop:
	MOVBU	0(X10), X7
	BLTU	X7, X17, classify_loaded
	BEQ	X7, X18, classify_loaded
	BEQ	X7, X19, classify_loaded
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	SUB	$1, X20
	BNEZ	X20, short_byte_loop
	BEQZ	X11, success
	JMP	exhausted

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
classify_loaded:
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

// func quoteHTML(src *byte, size int, dst *byte, capacity int) (consumed, written int)
// Normal Go ABI0 frame metadata and RET; no use of g (X27), SP, or RA as scratch.
TEXT ·quoteHTML(SB), NOSPLIT, $0-48
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	dst+16(FP), X12
	MOV	capacity+24(FP), X13
	MOV	X11, X14
	MOV	X13, X15
	MOV	$60, X16
	MOV	$32, X17
	MOV	$34, X18
	MOV	$92, X19
	MOV	$226, X28

qh_scan:
	BEQZ	X11, qh_success
	BEQZ	X13, qh_exhausted
	MOV	X11, X20
	BLEU	X11, X13, qh_bounded
	MOV	X13, X20
qh_bounded:
	MOV	$16, X5
	BLTU	X20, X5, qh_short_bounded
	VSETVLI	X20, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	VMSLTUVX X17, V8, V2
	VANDVI	$-5, V8, V12
	VMSEQVX	X18, V12, V3
	VMORMM	V2, V3, V2
	VANDVI	$-3, V8, V12
	VMSEQVX	X16, V12, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X19, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X28, V8, V3
	VMORMM	V2, V3, V2
	VFIRSTM	V2, X6
	BGEZ	X6, qh_special
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
	JMP	qh_scan

qh_short_bounded:
	MOV	$8, X5
	BLTU	X20, X5, qh_short_byte_loop
	MOV	0(X10), X21
	MOV	$0x0404040404040404, X6
	OR	X21, X6, X6
	MOV	$0x2626262626262626, X7
	XOR	X7, X6, X6
	ORCB	X6, X6
	MOV	$0x0202020202020202, X7
	OR	X21, X7, X7
	MOV	$0x3e3e3e3e3e3e3e3e, X5
	XOR	X5, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0x5c5c5c5c5c5c5c5c, X7
	XOR	X21, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0xe2e2e2e2e2e2e2e2, X7
	XOR	X21, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0xe0e0e0e0e0e0e0e0, X7
	AND	X21, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0x8080808080808080, X7
	ANDN	X6, X7, X6
	BNEZ	X6, qh_short_word_special
	MOV	X21, 0(X12)
	ADD	$8, X10
	SUB	$8, X11
	ADD	$8, X12
	SUB	$8, X13
	SUB	$8, X20
	BNEZ	X20, qh_short_byte_loop
	BEQZ	X11, qh_success
	JMP	qh_exhausted

qh_short_word_special:
	CTZ	X6, X6
	SRL	$3, X6
	BEQZ	X6, qh_classify
qh_short_word_prefix:
	MOVBU	0(X10), X7
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	SUB	$1, X6
	BNEZ	X6, qh_short_word_prefix
	JMP	qh_classify

qh_short_byte_loop:
	MOVBU	0(X10), X7
	BLTU	X7, X17, qh_classify_loaded
	AND	$0xfb, X7, X6
	BEQ	X6, X18, qh_classify_loaded
	AND	$0xfd, X7, X6
	BEQ	X6, X16, qh_classify_loaded
	BEQ	X7, X19, qh_classify_loaded
	BEQ	X7, X28, qh_classify_loaded
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	SUB	$1, X20
	BNEZ	X20, qh_short_byte_loop
	BEQZ	X11, qh_success
	JMP	qh_exhausted

qh_special:
	BEQZ	X6, qh_classify
	VSETVLI	X6, E8, M4, TA, MA, X5
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13

qh_classify:
	MOVBU	0(X10), X7
qh_classify_loaded:
	BEQ	X7, X28, qh_check_e2
	BEQ	X7, X18, qh_quote_or_bs
	BEQ	X7, X19, qh_quote_or_bs
	MOV	$9, X6
	BEQ	X7, X6, qh_tab
	MOV	$10, X6
	BEQ	X7, X6, qh_newline
	MOV	$13, X6
	BEQ	X7, X6, qh_cr

	MOV	$6, X5
	BLTU	X13, X5, qh_exhausted
	MOVB	X19, 0(X12)
	MOV	$117, X22
	MOVB	X22, 1(X12)
	MOV	$48, X22
	MOVB	X22, 2(X12)
	MOVB	X22, 3(X12)
	SRLI	$4, X7, X22
	ADD	$48, X22
	MOVB	X22, 4(X12)
	AND	$15, X7, X22
	MOV	$10, X23
	BLTU	X22, X23, qh_hex_digit
	ADD	$87, X22
	JMP	qh_hex_store
qh_hex_digit:
	ADD	$48, X22
qh_hex_store:
	MOVB	X22, 5(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$6, X12
	SUB	$6, X13
	JMP	qh_scan

qh_check_e2:
	MOV	$3, X5
	BLTU	X11, X5, qh_plain_e2
	MOVBU	1(X10), X6
	MOV	$128, X5
	BNE	X6, X5, qh_plain_e2
	MOVBU	2(X10), X24
	AND	$0xfe, X24, X5
	MOV	$168, X6
	BNE	X5, X6, qh_plain_e2
	MOV	$6, X5
	BLTU	X13, X5, qh_exhausted
	MOVB	X19, 0(X12)
	MOV	$117, X6
	MOVB	X6, 1(X12)
	MOV	$50, X6
	MOVB	X6, 2(X12)
	MOV	$48, X6
	MOVB	X6, 3(X12)
	MOV	$50, X6
	MOVB	X6, 4(X12)
	ADD	$-112, X24
	MOVB	X24, 5(X12)
	ADD	$3, X10
	SUB	$3, X11
	ADD	$6, X12
	SUB	$6, X13
	JMP	qh_scan

qh_plain_e2:
	BEQZ	X13, qh_exhausted
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	JMP	qh_scan

qh_quote_or_bs:
	MOV	X7, X22
	JMP	qh_emit2
qh_tab:
	MOV	$116, X22
	JMP	qh_emit2
qh_newline:
	MOV	$110, X22
	JMP	qh_emit2
qh_cr:
	MOV	$114, X22
qh_emit2:
	MOV	$2, X5
	BLTU	X13, X5, qh_exhausted
	MOVB	X19, 0(X12)
	MOVB	X22, 1(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$2, X12
	SUB	$2, X13
	JMP	qh_scan

qh_exhausted:
	MOV	X14, X5
	SUB	X11, X5
	XOR	$-1, X5
	JMP	qh_done
qh_success:
	MOV	X14, X5
qh_done:
	MOV	X15, X6
	SUB	X13, X6
	MOV	X5, consumed+32(FP)
	MOV	X6, written+40(FP)
	RET
