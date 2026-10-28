//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// Unquote and ParseString share this ABI0 leaf entry. g (X27) is preserved.
// Loads/stores are bounded by the logical length and actual VL.
// X10/X11 input cursor/remaining; X12 output cursor; X13 mode.
// X14/X15 input/output bases; X16 original length; X17 saw-escape.
// X18 current Unicode escape's original offset; X19 first UTF-16/code point.
// X22-X24 hex cursor/count/accumulator; X29 selects first/second UTF-16 unit.
// X8 classified block end; X9 current 64-bit mask chunk's input base.
// X20 event mask; X31 mask chunk index. V2 retains the whole block's mask.
TEXT ·stringDecode(SB), $0-72
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	dst+16(FP), X12
	MOV	mode+24(FP), X13
	MOV	X10, X14
	MOV	X12, X15
	MOV	X11, X16
	MOV	$0, X17

decode_scan:
	BEQZ	X11, decode_input_end
	VSETVLI X11, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	MOV	$92, X21
	VMSEQVX X21, V8, V2
	AND	$4, X13, X26
	BEQZ	X26, decode_find
	MOV	$34, X21
	VMSEQVX X21, V8, V3
	VMORMM	V2, V3, V2
	AND	$8, X13, X26
	BEQZ	X26, decode_find
	MOV	$32, X21
	VMSLTUVX X21, V8, V3
	VMORMM	V2, V3, V2
decode_find:
	VFIRSTM V2, X6
	MOV	$1, X7
	BGEZ	X6, decode_event_block
	MOV	X5, X6
	MOV	$0, X7
decode_copy:
	BEQZ	X6, decode_mask
	// For in-place input, skip identical stores until the first contraction.
	BEQ	X10, X12, decode_advance
	VSETVLI X6, E8, M4, TA, MA, X5
	VSE8V	V8, (X12)
decode_advance:
	ADD	X6, X10
	ADD	X6, X12
	SUB	X6, X11
	BEQZ	X7, decode_scan
decode_mask:
	// Closing quotes and raw controls finish immediately; only an escape
	// needs to retain the rest of this block for subsequent decoding.
	MOVBU	(X10), X21
	MOV	$92, X5
	BNE	X21, X5, decode_non_escape
	// A mask occupies one vector register regardless of the input LMUL.
	// Read its current 64-bit chunk, including when actual VL exceeds 64.
	VSETIVLI $1, E64, M1, TA, MA, X0
	VSLIDEDOWNVX X31, V2, V4
	VMVXS	V4, X20
	JMP	decode_escape
decode_special:
	MOVBU	(X10), X21
	MOV	$92, X5
	BEQ	X21, X5, decode_escape
decode_non_escape:
	MOV	$34, X5
	BEQ	X21, X5, decode_quote
	// Only parser validation puts a raw control into the special mask.
	SUB	X14, X10, X6
	MOV	$1, X7
	JMP	decode_error

decode_event_block:
	ADD	X5, X10, X8
	AND	$-64, X6, X9
	ADD	X10, X9
	SRL	$6, X6, X31
	JMP	decode_copy

decode_escape:
	MOV	$1, X17
	SUB	X14, X10, X18
	MOV	$2, X5
	BLTU	X11, X5, decode_eof
	ADD	$2, X10
	SUB	$2, X11
	AND	$1, X13, X26
	BEQZ	X26, decode_escape_letter
	// Preserve native double-unquote's second escape and EOF rules exactly.
	BEQZ	X11, decode_eof
	MOVBU	-1(X10), X21
	MOV	$92, X5
	BNE	X21, X5, decode_escape_letter
	MOVBU	(X10), X21
	BNE	X21, X5, decode_double_skip
	MOV	$2, X6
	BLTU	X11, X6, decode_eof
	MOVBU	1(X10), X21
	BEQ	X21, X5, decode_double_extra
	MOV	$34, X5
	BEQ	X21, X5, decode_double_extra
	SUB	X14, X10, X6
	ADD	$1, X6
	MOV	$2, X7                  // native ERR_INVALID_CHAR
	JMP	decode_error
decode_double_extra:
	ADD	$1, X10
	SUB	$1, X11
decode_double_skip:
	ADD	$1, X10
	SUB	$1, X11
decode_escape_letter:
	MOVBU	-1(X10), X21
	MOV	$117, X5                // u
	BEQ	X21, X5, decode_unicode
	MOV	$34, X5
	BEQ	X21, X5, decode_emit_byte
	MOV	$92, X5
	BEQ	X21, X5, decode_emit_byte
	MOV	$47, X5
	BEQ	X21, X5, decode_emit_byte
	MOV	$98, X5
	BEQ	X21, X5, decode_backspace
	MOV	$102, X5
	BEQ	X21, X5, decode_formfeed
	MOV	$110, X5
	BEQ	X21, X5, decode_newline
	MOV	$114, X5
	BEQ	X21, X5, decode_return
	MOV	$116, X5
	BEQ	X21, X5, decode_tab
	AND	$4, X13, X26
	BNEZ	X26, decode_parser_escape
	SUB	X14, X10, X6
	SUB	$1, X6
	MOV	$3, X7                  // native ERR_INVALID_ESCAPE
	JMP	decode_error
decode_parser_escape:
	MOV	X18, X6
	MOV	$2, X7
	JMP	decode_error
decode_backspace:
	MOV	$8, X21
	JMP	decode_emit_byte
decode_formfeed:
	MOV	$12, X21
	JMP	decode_emit_byte
decode_newline:
	MOV	$10, X21
	JMP	decode_emit_byte
decode_return:
	MOV	$13, X21
	JMP	decode_emit_byte
decode_tab:
	MOV	$9, X21
decode_emit_byte:
	MOVB	X21, (X12)
	ADD	$1, X12
	JMP	decode_resume

decode_resume:
	// Escape decoding consumes input independently of output contraction.
	// Reuse classifications ahead of the new input cursor; crossing escapes
	// may consume a chunk boundary or the end of the classified block.
	BGEU	X10, X8, decode_scan
	SUB	X9, X10, X6
	MOV	$64, X5
	BLTU	X6, X5, decode_mask_ready
	AND	$-64, X6, X5
	ADD	X5, X9
	SRL	$6, X5
	ADD	X5, X31
	VSETIVLI $1, E64, M1, TA, MA, X0
	VSLIDEDOWNVX X31, V2, V4
	VMVXS	V4, X20
	AND	$63, X6
decode_mask_ready:
	// Bound the mask search by both the chunk and the actual loaded VL.
	// Tail-agnostic bits cannot become an event in the logical input.
	SRL	X6, X20, X7
	CTZ	X7, X6
	ADD	$64, X9, X5
	MINU	X8, X5, X5
	SUB	X10, X5, X5
	// Without another event, resume the ordinary full-width scan rather
	// than splitting clean input into individual mask chunks for copying.
	BGEU	X6, X5, decode_scan
	BEQZ	X6, decode_special
	BEQ	X10, X12, decode_resume_advance
	// Copy only the ordinary span. Its event classification stays in V2.
	VSETVLI X6, E8, M4, TA, MA, X0
	VLE8V	(X10), V8
	VSE8V	V8, (X12)
decode_resume_advance:
	ADD	X6, X10
	ADD	X6, X12
	SUB	X6, X11
	JMP	decode_special

decode_unicode:
	MOV	$4, X5
	BLTU	X11, X5, decode_eof
	MOV	X10, X22
	MOV	$0, X29                 // first UTF-16 unit
decode_hex_start:
	MOV	$4, X23
	MOV	$0, X24
decode_hex:
	MOVBU	(X22), X21
	ADD	$-48, X21, X25
	MOV	$10, X5
	BLTU	X25, X5, decode_hex_digit
	OR	$32, X21
	ADD	$-97, X21, X25
	MOV	$6, X5
	BGEU	X25, X5, decode_bad_hex
	ADD	$10, X25
decode_hex_digit:
	SLL	$4, X24
	OR	X25, X24
	ADD	$1, X22
	SUB	$1, X23
	BNEZ	X23, decode_hex
	BNEZ	X29, decode_second_unit
	MOV	X24, X19
	ADD	$4, X10
	SUB	$4, X11

decode_codepoint:
	MOV	$0x80, X5
	BLTU	X19, X5, decode_ascii
	MOV	$0x800, X5
	BLTU	X19, X5, decode_two
	MOV	$0xd800, X5
	BLTU	X19, X5, decode_three
	MOV	$0xe000, X5
	BGEU	X19, X5, decode_three
	// A surrogate. Double-unquote consumes one extra backslash before
	// inspecting the second escape, including its native replacement cases.
	AND	$1, X13, X26
	BEQZ	X26, decode_surrogate
	BNEZ	X11, decode_double_surrogate
	AND	$2, X13, X26
	BEQZ	X26, decode_eof
	JMP	decode_replace
decode_double_surrogate:
	MOVBU	(X10), X21
	MOV	$92, X5
	BNE	X21, X5, decode_surrogate
	ADD	$1, X10
	SUB	$1, X11
decode_surrogate:
	MOV	$6, X5
	BLTU	X11, X5, decode_unpaired
	MOV	$0xdc00, X5
	BGEU	X19, X5, decode_unpaired
	MOVBU	(X10), X21
	MOV	$92, X5
	BNE	X21, X5, decode_unpaired
	MOVBU	1(X10), X21
	MOV	$117, X5
	BNE	X21, X5, decode_unpaired
	// Keep the second escape's original position for parser error mapping.
	SUB	X14, X10, X18
	ADD	$2, X10, X22
	MOV	$1, X29
	JMP	decode_hex_start

decode_second_unit:
	ADD	$6, X10
	SUB	$6, X11
	MOV	$0xdc00, X5
	BLTU	X24, X5, decode_not_low
	MOV	$0xe000, X5
	BGEU	X24, X5, decode_not_low
	MOV	$0xd800, X5
	SUB	X5, X19
	SLL	$10, X19
	MOV	$0xdc00, X5
	SUB	X5, X24
	MOV	$0x10000, X5
	ADD	X5, X24
	ADD	X24, X19
	JMP	decode_four
decode_not_low:
	AND	$2, X13, X26
	BNEZ	X26, decode_replace_then_retry
	AND	$4, X13, X26
	BNEZ	X26, decode_parser_unicode
	SUB	X14, X10, X6
	SUB	$4, X6
	MOV	$4, X7
	JMP	decode_error
decode_replace_then_retry:
	MOV	$0xef, X21
	MOVB	X21, 0(X12)
	MOV	$0xbf, X21
	MOVB	X21, 1(X12)
	MOV	$0xbd, X21
	MOVB	X21, 2(X12)
	ADD	$3, X12
	MOV	X24, X19
	JMP	decode_codepoint

decode_unpaired:
	AND	$2, X13, X26
	BNEZ	X26, decode_replace
	AND	$4, X13, X26
	BNEZ	X26, decode_parser_unicode
	SUB	X14, X10, X6
	SUB	$4, X6
	AND	$1, X13, X26
	SUB	X26, X6
	MOV	$4, X7
	JMP	decode_error
decode_replace:
	MOV	$0xfffd, X19
	JMP	decode_three
decode_bad_hex:
	AND	$4, X13, X26
	BNEZ	X26, decode_parser_unicode
	SUB	X14, X22, X6
	MOV	$2, X7
	JMP	decode_error
decode_parser_unicode:
	MOV	X18, X6
	MOV	$12, X7
	JMP	decode_error

decode_ascii:
	MOVB	X19, (X12)
	ADD	$1, X12
	JMP	decode_resume
decode_two:
	SRL	$6, X19, X21
	OR	$0xc0, X21
	MOVB	X21, 0(X12)
	AND	$0x3f, X19, X21
	OR	$0x80, X21
	MOVB	X21, 1(X12)
	ADD	$2, X12
	JMP	decode_resume
decode_three:
	SRL	$12, X19, X21
	OR	$0xe0, X21
	MOVB	X21, 0(X12)
	SRL	$6, X19, X21
	AND	$0x3f, X21
	OR	$0x80, X21
	MOVB	X21, 1(X12)
	AND	$0x3f, X19, X21
	OR	$0x80, X21
	MOVB	X21, 2(X12)
	ADD	$3, X12
	JMP	decode_resume
decode_four:
	SRL	$18, X19, X21
	OR	$0xf0, X21
	MOVB	X21, 0(X12)
	SRL	$12, X19, X21
	AND	$0x3f, X21
	OR	$0x80, X21
	MOVB	X21, 1(X12)
	SRL	$6, X19, X21
	AND	$0x3f, X21
	OR	$0x80, X21
	MOVB	X21, 2(X12)
	AND	$0x3f, X19, X21
	OR	$0x80, X21
	MOVB	X21, 3(X12)
	ADD	$4, X12
	JMP	decode_resume

decode_input_end:
	AND	$4, X13, X26
	BNEZ	X26, decode_eof
	MOV	X16, X5
	JMP	decode_success
decode_quote:
	SUB	X14, X10, X5
	ADD	$1, X5
decode_success:
	MOV	$-1, X6
	MOV	$0, X7
	JMP	decode_done
decode_eof:
	MOV	X16, X6
	MOV	$1, X7
	AND	$4, X13, X26
	BEQZ	X26, decode_error
	MOV	$5, X7
decode_error:
	MOV	X6, X5
decode_done:
	SUB	X15, X12, X28
	MOV	X5, end+32(FP)
	MOV	X28, written+40(FP)
	MOV	X17, escaped+48(FP)
	MOV	X6, errorPos+56(FP)
	MOV	X7, code+64(FP)
	RET
