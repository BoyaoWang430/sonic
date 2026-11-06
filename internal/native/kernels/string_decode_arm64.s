//go:build arm64 && go1.20 && !go1.28

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

DATA decodeHexDigits<>+48(SB)/8, $0x1716151413121110
DATA decodeHexDigits<>+56(SB)/2, $0x1918
DATA decodeHexDigits<>+65(SB)/8, $0x00001f1e1d1c1b1a
DATA decodeHexDigits<>+97(SB)/8, $0x00001f1e1d1c1b1a
GLOBL decodeHexDigits<>(SB), RODATA|NOPTR, $256

// Unquote and ParseString share this ABI0 leaf entry. g (R28) is preserved.
// Loads/stores are bounded and may be unaligned.
// R0/R1 input cursor/remaining; R2 output cursor; R3 mode.
// R4/R5 input/output bases; R6 original length; R7 saw-escape.
// R8 current Unicode escape's original offset; R9 first UTF-16/code point.
// R12-R14 hex cursor/count/accumulator; R19 selects first/second UTF-16 unit.
TEXT ·stringDecode(SB), $0-72
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD dst+16(FP), R2
	MOVD mode+24(FP), R3
	MOVD R0, R4
	MOVD R2, R5
	MOVD R1, R6
	MOVD $0, R7

	MOVD $92, R22
	VMOV R22, V1.B16
	MOVD $34, R22
	VMOV R22, V2.B16
	VEOR V3.B16, V3.B16, V3.B16

decode_scan:
	CMP $16, R1
	BLO decode_scan_scalar
	VLD1 (R0), [V0.B16]
	VCMEQ V1.B16, V0.B16, V4.B16
	TBZ $2, R3, decode_neon_mask
	VCMEQ V2.B16, V0.B16, V5.B16
	VORR V5.B16, V4.B16, V4.B16
	TBZ $3, R3, decode_neon_mask
	VUSHR $5, V0.B16, V5.B16
	VCMEQ V3.B16, V5.B16, V5.B16
	VORR V5.B16, V4.B16, V4.B16
decode_neon_mask:
	VMOV V4.D[0], R23
	VMOV V4.D[1], R22
	ORR R22, R23, R24
	CBNZ R24, decode_neon_event
	CMP R0, R2
	BEQ decode_neon_advance
	VST1 [V0.B16], (R2)
decode_neon_advance:
	ADD $16, R0
	ADD $16, R2
	SUB $16, R1
	B decode_scan
decode_neon_event:
	MOVD $0, R24
	CBNZ R23, decode_neon_first
	MOVD R22, R23
	MOVD $8, R24
decode_neon_first:
	RBIT R23, R23
	CLZ R23, R23
	ADD R23>>3, R24, R24
	CBZ R24, decode_special
	// In-place unescaped input already contains the classified prefix.
	// Advance over it once; retain exact stores for contracting decode.
	CMP R0, R2
	BNE decode_neon_prefix
	ADD R24, R0
	ADD R24, R2
	SUB R24, R1
	B decode_special
decode_neon_prefix:
	MOVD R2, R22
	VMOV V0.D[0], R13
	TBZ $3, R24, decode_neon_prefix_4
	MOVD R13, (R22)
	ADD $8, R22
	VMOV V0.D[1], R13
decode_neon_prefix_4:
	TBZ $2, R24, decode_neon_prefix_2
	MOVW R13, (R22)
	LSR $32, R13
	ADD $4, R22
decode_neon_prefix_2:
	TBZ $1, R24, decode_neon_prefix_1
	MOVH R13, (R22)
	LSR $16, R13
	ADD $2, R22
decode_neon_prefix_1:
	TBZ $0, R24, decode_neon_prefix_done
	MOVB R13, (R22)
decode_neon_prefix_done:
	ADD R24, R0
	ADD R24, R2
	SUB R24, R1
	B decode_special
decode_scan_scalar:
	CBZ R1, decode_input_end
	MOVD $8, R22
	CMP R22, R1
	BLO decode_short
decode_scan_word:
	MOVD $0x0101010101010101, R8
	MOVD $0x8080808080808080, R9
	MOVD $0x5c5c5c5c5c5c5c5c, R10
	MOVD $0x2222222222222222, R11
	MOVD $0x2020202020202020, R12

// Scan plain runs with bounded word loads, retaining the first escape only:
// decoding changes the input/output correspondence at that escape. Input and
// output may be identical, in which case the unescaped prefix is never stored.
	B decode_word
	PCALIGN $64
decode_word:
	MOVD $8, R22
	CMP R22, R1
	BLO decode_tail
	MOVD (R0), R13
	MOVD $8, R14
decode_word_classify:
	EOR R10, R13, R22
	SUB R8, R22, R23
	BIC R22, R23, R23
	AND $4, R3, R16
	CBZ R16, decode_word_find
	EOR R11, R13, R22
	SUB R8, R22, R24
	BIC R22, R24, R24
	ORR R24, R23, R23
	AND $8, R3, R16
	CBZ R16, decode_word_find
	SUB R12, R13, R24
	BIC R13, R24, R24
	ORR R24, R23, R23
decode_word_find:
	AND R9, R23, R23
	CBZ R23, decode_word_copy
	RBIT R23, R14
	CLZ R14, R14
	LSR $3, R14, R14
	CBZ R14, decode_special
decode_word_copy:
	CMP R2, R0
	BEQ decode_word_advance
	MOVD $8, R22
	CMP R22, R14
	BNE decode_word_short_copy
	MOVD R13, (R2)
decode_word_advance:
	ADD R14, R0
	ADD R14, R2
	SUB R14, R1
	CBZ R23, decode_word

decode_special:
	MOVBU (R0), R11
	MOVD $92, R22
	CMP R22, R11
	BEQ decode_escape
	MOVD $34, R22
	CMP R22, R11
	BEQ decode_quote
	// Only parser validation puts a raw control into the special mask.
	SUB R4, R0, R23
	MOVD $1, R24
	B decode_error

decode_escape:
	MOVD $1, R7
	SUB R4, R0, R8
	MOVD $2, R22
	CMP R22, R1
	BLO decode_eof
	ADD $2, R0
	SUB $2, R1
	AND $1, R3, R16
	CBZ R16, decode_escape_letter
	// Preserve native double-unquote's second escape and EOF rules exactly.
	CBZ R1, decode_eof
	MOVBU -1(R0), R11
	MOVD $92, R22
	CMP R22, R11
	BNE decode_escape_letter
	MOVBU (R0), R11
	CMP R22, R11
	BNE decode_double_skip
	MOVD $2, R23
	CMP R23, R1
	BLO decode_eof
	MOVBU 1(R0), R11
	CMP R22, R11
	BEQ decode_double_extra
	MOVD $34, R22
	CMP R22, R11
	BEQ decode_double_extra
	SUB R4, R0, R23
	ADD $1, R23
	MOVD $2, R24 // native ERR_INVALID_CHAR
	B decode_error
decode_double_extra:
	ADD $1, R0
	SUB $1, R1
decode_double_skip:
	ADD $1, R0
	SUB $1, R1
decode_escape_letter:
	MOVBU -1(R0), R11
	MOVD $117, R22 // u
	CMP R22, R11
	BEQ decode_unicode
	MOVD $128, R22
	CMP R22, R11
	BHS decode_invalid_escape
	MOVD $decodeSimpleEscapes<>(SB), R22
	ADD R11, R22
	MOVBU (R22), R11
	CBNZ R11, decode_emit_byte
decode_invalid_escape:
	AND $4, R3, R16
	CBNZ R16, decode_parser_escape
	SUB R4, R0, R23
	SUB $1, R23
	MOVD $3, R24 // native ERR_INVALID_ESCAPE
	B decode_error
decode_parser_escape:
	MOVD R8, R23
	MOVD $2, R24
	B decode_error
decode_emit_byte:
	MOVB R11, (R2)
	ADD $1, R2

decode_resume:
	// Dense escapes need no word setup. Keep the input cursor unchanged
	// unless the next escape is adjacent or follows one ordinary byte, so
	// longer plain runs retain the word path and its copy strategy.
	CBZ R1, decode_input_end
	MOVBU (R0), R11
	MOVD $92, R22
	CMP R22, R11
	BEQ decode_escape
	MOVD $2, R23
	CMP R23, R1
	BLO decode_tiny_loaded
	MOVBU 1(R0), R23
	CMP R22, R23
	BNE decode_resume_scan
	AND $4, R3, R16
	CBZ R16, decode_resume_plain
	MOVD $34, R22
	CMP R22, R11
	BEQ decode_quote
	AND $8, R3, R16
	CBZ R16, decode_resume_plain
	MOVD $32, R22
	CMP R22, R11
	BLO decode_special
decode_resume_plain:
	CMP R2, R0
	BEQ decode_resume_advance
	MOVB R11, (R2)
decode_resume_advance:
	ADD $1, R0
	ADD $1, R2
	SUB $1, R1
	B decode_escape

	// Align the Unicode entry so its four-digit loop fits in one cache line.
	// All entries branch past the padding, including the second UTF-16 unit.
	PCALIGN $64
decode_unicode:
	MOVD $4, R22
	CMP R22, R1
	BLO decode_eof
	MOVD R0, R12
	MOVD $0, R19 // first UTF-16 unit
decode_hex_start:
	MOVD $decodeHexDigits<>(SB), R22
	MOVBU 0(R12), R11
	MOVBU 1(R12), R13
	MOVBU 2(R12), R15
	MOVBU 3(R12), R23
	MOVBU (R22)(R11), R11
	MOVBU (R22)(R13), R13
	MOVBU (R22)(R15), R15
	MOVBU (R22)(R23), R23
	AND R11, R13, R24
	AND R15, R24, R24
	AND R23, R24, R24
	TBZ $4, R24, decode_hex_slow
	LSL $12, R11, R14
	ADD R13<<8, R14, R14
	ADD R15<<4, R14, R14
	ADD R23, R14, R14
	MOVD $0x11110, R22
	SUB R22, R14, R14
	CBNZ R19, decode_second_unit
	MOVD R14, R9
	ADD $4, R0
	SUB $4, R1

decode_codepoint:
	MOVD $0x80, R22
	CMP R22, R9
	BLO decode_ascii
	MOVD $0x800, R22
	CMP R22, R9
	BLO decode_two
	MOVD $0xd800, R22
	CMP R22, R9
	BLO decode_three
	MOVD $0xe000, R22
	CMP R22, R9
	BHS decode_three
	// A surrogate. Double-unquote consumes one extra backslash before
	// inspecting the second escape, including its native replacement cases.
	AND $1, R3, R16
	CBZ R16, decode_surrogate
	CBNZ R1, decode_double_surrogate
	AND $2, R3, R16
	CBZ R16, decode_eof
	B decode_replace
decode_double_surrogate:
	MOVBU (R0), R11
	MOVD $92, R22
	CMP R22, R11
	BNE decode_surrogate
	ADD $1, R0
	SUB $1, R1
decode_surrogate:
	MOVD $6, R22
	CMP R22, R1
	BLO decode_unpaired
	MOVD $0xdc00, R22
	CMP R22, R9
	BHS decode_unpaired
	MOVBU (R0), R11
	MOVD $92, R22
	CMP R22, R11
	BNE decode_unpaired
	MOVBU 1(R0), R11
	MOVD $117, R22
	CMP R22, R11
	BNE decode_unpaired
	// Keep the second escape's original position for parser error mapping.
	SUB R4, R0, R8
	ADD $2, R0, R12
	MOVD $1, R19
	B decode_hex_start

decode_second_unit:
	ADD $6, R0
	SUB $6, R1
	MOVD $0xdc00, R22
	CMP R22, R14
	BLO decode_not_low
	MOVD $0xe000, R22
	CMP R22, R14
	BHS decode_not_low
	MOVD $0xd800, R22
	SUB R22, R9
	LSL $10, R9
	MOVD $0xdc00, R22
	SUB R22, R14
	MOVD $0x10000, R22
	ADD R22, R14
	ADD R14, R9
	B decode_four
decode_not_low:
	AND $2, R3, R16
	CBNZ R16, decode_replace_then_retry
	AND $4, R3, R16
	CBNZ R16, decode_parser_unicode
	SUB R4, R0, R23
	SUB $4, R23
	MOVD $4, R24
	B decode_error
decode_replace_then_retry:
	MOVD $0xef, R11
	MOVB R11, 0(R2)
	MOVD $0xbf, R11
	MOVB R11, 1(R2)
	MOVD $0xbd, R11
	MOVB R11, 2(R2)
	ADD $3, R2
	MOVD R14, R9
	B decode_codepoint

decode_unpaired:
	AND $2, R3, R16
	CBNZ R16, decode_replace
	AND $4, R3, R16
	CBNZ R16, decode_parser_unicode
	SUB R4, R0, R23
	SUB $4, R23
	AND $1, R3, R16
	SUB R16, R23
	MOVD $4, R24
	B decode_error
decode_replace:
	MOVD $0xfffd, R9
	B decode_three
decode_hex_slow:
	TBZ $4, R11, decode_bad_hex
	ADD $1, R12
	TBZ $4, R13, decode_bad_hex
	ADD $1, R12
	TBZ $4, R15, decode_bad_hex
	ADD $1, R12
decode_bad_hex:
	AND $4, R3, R16
	CBNZ R16, decode_parser_unicode
	SUB R4, R12, R23
	MOVD $2, R24
	B decode_error
decode_parser_unicode:
	MOVD R8, R23
	MOVD $12, R24
	B decode_error

decode_ascii:
	MOVB R9, (R2)
	ADD $1, R2
	B decode_resume
decode_two:
	LSR $6, R9, R11
	ORR $0xc0, R11
	MOVB R11, 0(R2)
	AND $0x3f, R9, R11
	ORR $0x80, R11
	MOVB R11, 1(R2)
	ADD $2, R2
	B decode_resume
decode_three:
	LSR $12, R9, R11
	ORR $0xe0, R11
	MOVB R11, 0(R2)
	LSR $6, R9, R11
	AND $0x3f, R11
	ORR $0x80, R11
	MOVB R11, 1(R2)
	AND $0x3f, R9, R11
	ORR $0x80, R11
	MOVB R11, 2(R2)
	ADD $3, R2
	B decode_resume
decode_four:
	LSR $18, R9, R11
	ORR $0xf0, R11
	MOVB R11, 0(R2)
	LSR $12, R9, R11
	AND $0x3f, R11
	ORR $0x80, R11
	MOVB R11, 1(R2)
	LSR $6, R9, R11
	AND $0x3f, R11
	ORR $0x80, R11
	MOVB R11, 2(R2)
	AND $0x3f, R9, R11
	ORR $0x80, R11
	MOVB R11, 3(R2)
	ADD $4, R2
	B decode_resume

// Once the remaining input is small, keep its bytes in this loop instead of
// repeating the word-length dispatch for every byte. R11 may
// already contain the current non-backslash byte from short/resume dispatch.
decode_tiny:
	MOVBU (R0), R11
	MOVD $92, R22
	CMP R22, R11
	BEQ decode_escape
decode_tiny_loaded:
	AND $4, R3, R16
	CBZ R16, decode_tiny_plain
	MOVD $34, R22
	CMP R22, R11
	BEQ decode_quote
	AND $8, R3, R16
	CBZ R16, decode_tiny_plain
	MOVD $32, R22
	CMP R22, R11
	BLO decode_special
decode_tiny_plain:
	CMP R2, R0
	BEQ decode_tiny_advance
	MOVB R11, (R2)
decode_tiny_advance:
	ADD $1, R0
	ADD $1, R2
	SUB $1, R1
	CBNZ R1, decode_tiny

decode_input_end:
	AND $4, R3, R16
	CBNZ R16, decode_eof
	MOVD R6, R22
	B decode_success
decode_quote:
	SUB R4, R0, R22
	ADD $1, R22
decode_success:
	MOVD $-1, R23
	MOVD $0, R24
	B decode_done
decode_eof:
	MOVD R6, R23
	MOVD $1, R24
	AND $4, R3, R16
	CBZ R16, decode_error
	MOVD $5, R24
decode_error:
	MOVD R23, R22
decode_done:
	SUB R5, R2, R17
	MOVD R22, end+32(FP)
	MOVD R17, written+40(FP)
	MOVD R7, escaped+48(FP)
	MOVD R23, errorPos+56(FP)
	MOVD R24, code+64(FP)
	RET

// The source word is already loaded, so exact-width stores are safe even
// when in-place decoding has brought the output just behind the input.
decode_word_short_copy:
	MOVD R2, R22
	AND $4, R14, R24
	CBZ R24, decode_word_short_half
	MOVW R13, (R22)
	LSR $32, R13
	ADD $4, R22
decode_word_short_half:
	AND $2, R14, R24
	CBZ R24, decode_word_short_byte
	MOVH R13, (R22)
	LSR $16, R13
	ADD $2, R22
decode_word_short_byte:
	AND $1, R14, R24
	CBZ R24, decode_word_advance
	MOVB R13, (R22)
	B decode_word_advance

// Reached after consuming a complete ordinary word. The final eight input
// bytes are in bounds, even when an earlier escape contracted in-place output.
// Discard the consumed prefix BEFORE classification: it may have been changed
// by stores, and masking matches afterwards would not prevent SWAR borrow.
decode_tail:
	CBZ R1, decode_input_end
	// One to three bytes cost less to inspect directly than to classify a word.
	MOVD $4, R22
	CMP R22, R1
	BLO decode_tiny
	ADD R1, R0, R22
	MOVD -8(R22), R13
	MOVD $8, R22
	SUB R1, R22
	LSL $3, R22
	LSR R22, R13
	// Fill unused lanes with ff, which is neither an escape nor a control.
	MOVD $-1, R23
	LSR R22, R23
	MVN R23, R23
	ORR R23, R13
	MOVD R1, R14
	B decode_word_classify

// A short input or post-escape suffix has no guaranteed preceding clean word.
// For 4..7 bytes, two bounded four-byte loads construct its complete value;
// overlapping bytes agree because both loads precede any output stores.
decode_short:
	// An escape at the cursor must not pay for a word that stops immediately.
	MOVBU (R0), R11
	MOVD $92, R22
	CMP R22, R11
	BEQ decode_escape
decode_short_plain:
	MOVD $4, R22
	CMP R22, R1
	BLO decode_tiny_loaded
	MOVWU (R0), R13
	ADD R1, R0, R22
	MOVWU -4(R22), R23
	ADD $-4, R1, R24
	LSL $3, R24
	LSL R24, R23
	ORR R23, R13
	ADD $32, R24
	MOVD $-1, R23
	LSL R24, R23
	ORR R23, R13
	MOVD $0x0101010101010101, R8
	MOVD $0x8080808080808080, R9
	MOVD $0x5c5c5c5c5c5c5c5c, R10
	MOVD $0x2222222222222222, R11
	MOVD $0x2020202020202020, R12
	MOVD R1, R14
	B decode_word_classify

// Resume has already checked the input length, loaded its first byte, and
// excluded a leading or adjacent escape. Retain that work for a short suffix.
decode_resume_scan:
	CMP $16, R1
	BHS decode_scan
	MOVD $8, R22
	CMP R22, R1
	BHS decode_scan_word
	B decode_short_plain
