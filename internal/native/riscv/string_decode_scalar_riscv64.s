//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// JSON's eight simple escapes. Missing entries are zero and therefore invalid;
// Unicode escapes keep their separate decoder. A bounded lookup replaces the
// repeated character-comparison chain for dense escaped input.
DATA decodeSimpleEscapes<>+34(SB)/1, $34
DATA decodeSimpleEscapes<>+47(SB)/1, $47
DATA decodeSimpleEscapes<>+92(SB)/1, $92
DATA decodeSimpleEscapes<>+98(SB)/1, $8
DATA decodeSimpleEscapes<>+102(SB)/1, $12
DATA decodeSimpleEscapes<>+110(SB)/1, $10
DATA decodeSimpleEscapes<>+114(SB)/1, $13
DATA decodeSimpleEscapes<>+116(SB)/1, $9
GLOBL decodeSimpleEscapes<>(SB), RODATA|NOPTR, $128

// Unquote and ParseString share this ABI0 leaf entry. g (X27) is preserved.
// Loads/stores are bounded and may be unaligned.
// X10/X11 input cursor/remaining; X12 output cursor; X13 mode.
// X14/X15 input/output bases; X16 original length; X17 saw-escape.
// X18 current Unicode escape's original offset; X19 first UTF-16/code point.
// X22-X24 hex cursor/count/accumulator; X29 selects first/second UTF-16 unit.
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
	BEQZ X11, decode_input_end
	MOV $8, X5
	BLTU X11, X5, decode_short
decode_scan_word:
	MOV $0x0101010101010101, X18
	MOV $0x8080808080808080, X19
	MOV $0x5c5c5c5c5c5c5c5c, X20
	MOV $0x2222222222222222, X21
	MOV $0x2020202020202020, X22

// Scan plain runs with bounded word loads, retaining the first escape only:
// decoding changes the input/output correspondence at that escape. Input and
// output may be identical, in which case the unescaped prefix is never stored.
	JMP decode_word
	PCALIGN $64
decode_word:
	MOV $8, X5
	BLTU X11, X5, decode_tail
	MOV (X10), X23
	MOV $8, X24
decode_word_classify:
	XOR X20, X23, X5
	SUB X18, X5, X6
	ANDN X5, X6, X6
	AND $4, X13, X26
	BEQZ X26, decode_word_find
	XOR X21, X23, X5
	SUB X18, X5, X7
	ANDN X5, X7, X7
	OR X7, X6, X6
	AND $8, X13, X26
	BEQZ X26, decode_word_find
	SUB X22, X23, X7
	ANDN X23, X7, X7
	OR X7, X6, X6
decode_word_find:
	AND X19, X6, X6
	BEQZ X6, decode_word_copy
	CTZ X6, X24
	SRL $3, X24, X24
	BEQZ X24, decode_special
decode_word_copy:
	BEQ X10, X12, decode_word_advance
	MOV $8, X5
	BNE X24, X5, decode_word_short_copy
	MOV X23, (X12)
decode_word_advance:
	ADD X24, X10
	ADD X24, X12
	SUB X24, X11
	BEQZ X6, decode_word

decode_special:
	MOVBU	(X10), X21
	MOV	$92, X5
	BEQ	X21, X5, decode_escape
	MOV	$34, X5
	BEQ	X21, X5, decode_quote
	// Only parser validation puts a raw control into the special mask.
	SUB	X14, X10, X6
	MOV	$1, X7
	JMP	decode_error

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
	MOV $128, X5
	BGEU X21, X5, decode_invalid_escape
	MOV $decodeSimpleEscapes<>(SB), X5
	ADD X21, X5
	MOVBU (X5), X21
	BNEZ X21, decode_emit_byte
decode_invalid_escape:
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
decode_emit_byte:
	MOVB	X21, (X12)
	ADD	$1, X12

decode_resume:
	// Dense escapes need no word setup. Keep the input cursor unchanged
	// unless the next escape is adjacent or follows one ordinary byte, so
	// longer plain runs retain the word path and its copy strategy.
	BEQZ X11, decode_input_end
	MOVBU (X10), X21
	MOV $92, X5
	BEQ X21, X5, decode_escape
	MOV $2, X6
	BLTU X11, X6, decode_tiny_loaded
	MOVBU 1(X10), X6
	BNE X6, X5, decode_resume_scan
	AND $4, X13, X26
	BEQZ X26, decode_resume_plain
	MOV $34, X5
	BEQ X21, X5, decode_quote
	AND $8, X13, X26
	BEQZ X26, decode_resume_plain
	MOV $32, X5
	BLTU X21, X5, decode_special
decode_resume_plain:
	BEQ X10, X12, decode_resume_advance
	MOVB X21, (X12)
decode_resume_advance:
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	JMP decode_escape

	// Align the Unicode entry so its four-digit loop fits in one cache line.
	// All entries branch past the padding, including the second UTF-16 unit.
	PCALIGN $64
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

// Once the remaining input is small, keep its bytes in this loop instead of
// repeating the word-length dispatch for every byte. X21 may
// already contain the current non-backslash byte from short/resume dispatch.
decode_tiny:
	MOVBU (X10), X21
	MOV $92, X5
	BEQ X21, X5, decode_escape
decode_tiny_loaded:
	AND $4, X13, X26
	BEQZ X26, decode_tiny_plain
	MOV $34, X5
	BEQ X21, X5, decode_quote
	AND $8, X13, X26
	BEQZ X26, decode_tiny_plain
	MOV $32, X5
	BLTU X21, X5, decode_special
decode_tiny_plain:
	BEQ X10, X12, decode_tiny_advance
	MOVB X21, (X12)
decode_tiny_advance:
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	BNEZ X11, decode_tiny

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

// The source word is already loaded, so exact-width stores are safe even
// when in-place decoding has brought the output just behind the input.
decode_word_short_copy:
	MOV X12, X5
	AND $4, X24, X7
	BEQZ X7, decode_word_short_half
	MOVW X23, (X5)
	SRL $32, X23
	ADD $4, X5
decode_word_short_half:
	AND $2, X24, X7
	BEQZ X7, decode_word_short_byte
	MOVH X23, (X5)
	SRL $16, X23
	ADD $2, X5
decode_word_short_byte:
	AND $1, X24, X7
	BEQZ X7, decode_word_advance
	MOVB X23, (X5)
	JMP decode_word_advance

// Reached after consuming a complete ordinary word. The final eight input
// bytes are in bounds, even when an earlier escape contracted in-place output.
// Discard the consumed prefix BEFORE classification: it may have been changed
// by stores, and masking matches afterwards would not prevent SWAR borrow.
decode_tail:
	BEQZ X11, decode_input_end
	// One to three bytes cost less to inspect directly than to classify a word.
	MOV $4, X5
	BLTU X11, X5, decode_tiny
	ADD X11, X10, X5
	MOV -8(X5), X23
	MOV $8, X5
	SUB X11, X5
	SLL $3, X5
	SRL X5, X23
	// Fill unused lanes with ff, which is neither an escape nor a control.
	MOV $-1, X6
	SRL X5, X6
	XOR $-1, X6
	OR X6, X23
	MOV X11, X24
	JMP decode_word_classify

// A short input or post-escape suffix has no guaranteed preceding clean word.
// For 4..7 bytes, two bounded four-byte loads construct its complete value;
// overlapping bytes agree because both loads precede any output stores.
decode_short:
	// An escape at the cursor must not pay for a word that stops immediately.
	MOVBU (X10), X21
	MOV $92, X5
	BEQ X21, X5, decode_escape
decode_short_plain:
	MOV $4, X5
	BLTU X11, X5, decode_tiny_loaded
	MOVWU (X10), X23
	ADD X11, X10, X5
	MOVWU -4(X5), X6
	ADD $-4, X11, X7
	SLL $3, X7
	SLL X7, X6
	OR X6, X23
	ADD $32, X7
	MOV $-1, X6
	SLL X7, X6
	OR X6, X23
	MOV $0x0101010101010101, X18
	MOV $0x8080808080808080, X19
	MOV $0x5c5c5c5c5c5c5c5c, X20
	MOV $0x2222222222222222, X21
	MOV $0x2020202020202020, X22
	MOV X11, X24
	JMP decode_word_classify

// Resume has already checked the input length, loaded its first byte, and
// excluded a leading or adjacent escape. Retain that work for a short suffix.
decode_resume_scan:
	MOV $8, X5
	BGEU X11, X5, decode_scan_word
	JMP decode_short_plain
