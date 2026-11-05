//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

#define QUOTE_MASK \
	XOR X24, X20, X6; \
	ORCB X6, X6; \
	XOR X25, X20, X5; \
	ORCB X5, X5; \
	AND X5, X6, X6; \
	AND X26, X20, X5; \
	ORCB X5, X5; \
	AND X5, X6, X6; \
	ANDN X6, X23, X6

// func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)
// Normal Go ABI0 frame metadata and RET; no use of g (X27), SP, or RA as scratch.
// Short strings and dense escapes use bounded scalar words; longer plain runs use RVV M4.
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

	BEQZ	X11, success
	BEQZ	X13, exhausted
	MOV	$8, X5
	BLTU	X11, X5, scan_byte
	BLTU	X13, X5, scan_byte
	MOV	$0x8080808080808080, X23
	MOV	$0x2222222222222222, X24
	MOV	$0x5c5c5c5c5c5c5c5c, X25
	MOV	$0xe0e0e0e0e0e0e0e0, X26

scan_word_load:
	MOV	(X10), X20
	QUOTE_MASK
	MOV	$8, X7
	BEQZ	X6, copy_word
word_candidate:
	CTZ	X6, X7
	SRL	$3, X7, X7
	BEQZ	X7, classify
	JMP	copy_short

copy_word:
	MOV	$32, X5
	BLTU	X11, X5, copy_single_word
	BLTU	X13, X5, copy_single_word
	MOV	X20, 0(X12)
	MOV	8(X10), X20
	QUOTE_MASK
	BNEZ	X6, bulk_candidate_8
	MOV	X20, 8(X12)
	ADD	$16, X10
	SUB	$16, X11
	ADD	$16, X12
	SUB	$16, X13
rvv_bulk:
	BEQZ	X11, success
	BEQZ	X13, exhausted
	MOV	X11, X20
	BLEU	X11, X13, rvv_bounded
	MOV	X13, X20
rvv_bounded:
	VSETVLI	X20, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	VMSLTUVX X17, V8, V2
	VMSEQVX	X18, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X19, V8, V3
	VMORMM	V2, V3, V2
	VFIRSTM	V2, X6
	BGEZ	X6, rvv_special
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
	JMP	rvv_bulk
rvv_special:
	BEQZ	X6, classify
	VSETVLI	X6, E8, M4, TA, MA, X5
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
	JMP	classify

bulk_candidate_8:
	ADD	$8, X10
	SUB	$8, X11
	ADD	$8, X12
	SUB	$8, X13
	JMP	word_candidate

copy_single_word:
	MOV	X20, (X12)
	JMP	copy_advance

copy_short:
	MOV	X12, X5
	AND	$4, X7, X21
	BEQZ	X21, copy_short_half
	MOVW	X20, (X5)
	SRL	$32, X20
	ADD	$4, X5
copy_short_half:
	AND	$2, X7, X21
	BEQZ	X21, copy_short_byte
	MOVH	X20, (X5)
	SRL	$16, X20
	ADD	$2, X5
copy_short_byte:
	AND	$1, X7, X21
	BEQZ	X21, copy_advance
	MOVB	X20, (X5)

copy_advance:
	ADD	X7, X10
	SUB	X7, X11
	ADD	X7, X12
	SUB	X7, X13
	BEQZ	X6, scan_word
	JMP	classify

scan_tail:
	BEQZ	X11, success
	BLTU	X13, X11, scan
	SUB	X11, X5, X29
	SUB	X29, X10, X5
	MOV	(X5), X20
	QUOTE_MASK
	BNEZ	X6, tail_candidate
	ADD	X11, X12
	MOV	X20, -8(X12)
	SUB	X11, X13
	JMP	success

scan_byte:
	MOVBU	(X10), X7
	BLTU	X7, X17, classify_loaded
	BEQ	X7, X18, classify_loaded
	BEQ	X7, X19, classify_loaded
	MOVB	X7, (X12)
	ADD	$1, X10
	ADD	$1, X12
	SUB	$1, X11
	SUB	$1, X13
	JMP	scan

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

	ADD	$6, X14, X21
	ADD	$1, X14, X28
	MOV	$1, X29
	JMP	emit
quote_or_backslash:
	SLLI	$1, X14, X28
	ADD	$2, X28, X21
	ADD	$1, X28
	MOV	X7, X22
	MOV	$0, X29
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
	ADD	$1, X14, X28
	MOV	$0, X29
emit:
	BLTU	X13, X21, exhausted
	MOV	X12, X20
backslashes:
	MOVB	X19, 0(X20)
	ADD	$1, X20
	SUB	$1, X28
	BNEZ	X28, backslashes
	BEQZ	X29, short_body
	MOV	$117, X22
	MOVB	X22, 0(X20)
	MOV	$48, X22
	MOVB	X22, 1(X20)
	MOVB	X22, 2(X20)
	SRLI	$4, X7, X22
	ADD	$48, X22
	MOVB	X22, 3(X20)
	AND	$15, X7, X22
	MOV	$10, X28
	BLTU	X22, X28, hex_digit
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
	JMP	scan_after_escape

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

scan:
	BEQZ	X11, success
	BEQZ	X13, exhausted
	MOV	$8, X5
	BLTU	X11, X5, scan_byte
	BLTU	X13, X5, scan_byte
	JMP	scan_word_load

scan_word:
	MOV	$8, X5
	BLTU	X11, X5, scan_tail
	BLTU	X13, X5, scan
	JMP	scan_word_load

scan_after_escape:
	BEQZ	X11, success
	BEQZ	X13, exhausted
	MOVBU	0(X10), X7
	BLTU	X7, X17, classify_loaded
	BEQ	X7, X18, classify_loaded
	BEQ	X7, X19, classify_loaded
	JMP	scan

tail_candidate:
	SLL	$3, X29
	SRL	X29, X20
	SRL	X29, X6
	CTZ	X6, X7
	SRL	$3, X7
	BEQZ	X7, classify
	JMP	copy_short

#undef QUOTE_MASK
