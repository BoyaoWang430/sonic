//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool)
// R0: input pointer; R1: remaining bytes; R2: absolute input offset.
// R3: output pointer; R4: remaining output slots; R5: offsets written.
// Bounded unaligned words skip ASCII runs. Invalid bytes are recorded singly;
// output exhaustion leaves the next invalid byte unread. FP, g (R28), SP, and LR are preserved.
TEXT ·utf8Scan(SB), $0-57
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD base+16(FP), R2
	MOVD positions+24(FP), R3
	MOVD limit+32(FP), R4
	MOVD $0, R5
	MOVD $0, R16 // lazily initialized multibyte masks
	MOVD $0x8080808080808080, R14
	MOVD R1, R15 // original size bounds overlapping tail loads
	B utf8_loop

	// Keep both hot loops stable when other scalar kernels change size.
	PCALIGN $64
utf8_loop:
	// NEON handles ASCII runs without scalar lane extraction per byte.
	// Non-ASCII sequences retain the strict bounded scalar decoder below.
	CMP $16, R1
	BLO utf8_word
	VLD1 (R0), [V0.B16]
	VMOV V0.D[0], R7
	VMOV V0.D[1], R8
	ORR R7, R8, R23
	TST R14, R23
	BNE utf8_word
	ADD $16, R0
	ADD $16, R2
	SUB $16, R1
	B utf8_loop
utf8_word:
	CBZ R1, utf8_done
	MOVD $8, R6
	CMP R6, R1
	BLO utf8_tail
	MOVD 0(R0), R7
	AND R14, R7
	CBNZ R7, utf8_nonascii_word
	// Probe one word before entering the bulk loop so mixed text can reach
	// the multibyte decoder without loading and classifying a whole block.
	MOVD $32, R13
	CMP R13, R1
	BHS utf8_ascii_start
	ADD $8, R0
	ADD $8, R2
	SUB $8, R1
	B utf8_loop
	PCALIGN $64
utf8_nonascii_word:
	// Consume the ASCII prefix exactly once, then decode at its first high
	// byte. The sequence may cross this word, but never the input boundary.
	RBIT R7, R6
	CLZ R6, R6
	LSR $3, R6
	ADD R6, R0
	ADD R6, R2
	SUB R6, R1
utf8_scalar:
	MOVBU 0(R0), R8
	MOVD $0x80, R11
	CMP R11, R8
	BHS utf8_scalar_nonascii
	ADD $1, R0
	ADD $1, R2
	SUB $1, R1
	B utf8_loop
utf8_scalar_nonascii:
	CMP $4, R1
	BLO utf8_short_nonascii
	CBZ R16, utf8_init_masks
utf8_multibyte_word:
	// The legacy ARM core classifies an entire UTF-8 sequence with a
	// bounded 32-bit load. Conditional comparisons combine the pattern,
	// overlong-encoding and surrogate checks without per-byte branches.
	MOVWU (R0), R7
	TBZ $7, R7, utf8_loop
utf8_multibyte_classify:
	AND R16, R7, R11
	CMP R17, R11
	AND R19, R7, R11
	CCMP EQ, R11, R20, $4
	CCMP NE, R11, $0, $4
	BNE utf8_word_three_valid
	AND R21, R7, R11
	AND $0x1e, R7, R12
	CMP R24, R11
	CCMP EQ, R12, $0, $4
	BNE utf8_word_two_valid
	AND R25, R7, R11
	CMP R26, R11
	BNE utf8_invalid
utf8_word_four_require:
	AND R22, R7, R11
	CBZ R11, utf8_invalid
	TBZ $2, R7, utf8_word_four_valid
	TST $0x3003, R7
	BNE utf8_invalid
utf8_word_four_valid:
	ADD $4, R0
	ADD $4, R2
	SUB $4, R1
	CMP $4, R1
	BLO utf8_multibyte_tail
	// Adjacent four-byte runes retain their successful classification.
	// A width change rejoins the general classifier with the loaded word.
	MOVWU (R0), R7
	TBZ $7, R7, utf8_loop
	AND R25, R7, R11
	CMP R26, R11
	BEQ utf8_word_four_require
	B utf8_multibyte_classify
utf8_word_two_valid:
	MOVD $2, R10
	B utf8_multibyte_advance
utf8_word_three_valid:
	MOVD $3, R10
utf8_multibyte_advance:
	ADD R10, R0
	ADD R10, R2
	SUB R10, R1
	CMP $4, R1
	BHS utf8_multibyte_word
utf8_multibyte_tail:
	CBZ R1, utf8_done
	B utf8_scalar

utf8_init_masks:
	// Register constants survive ASCII blocks and error recording.
	MOVD $0x00c0c0f0, R16
	MOVD $0x008080e0, R17
	MOVD $0x200f, R19
	MOVD $0x200d, R20
	MOVD $0xc0e0, R21
	MOVD $0x80c0, R24
	MOVD $0xc0c0c0f8, R25
	MOVD $0x808080f0, R26
	MOVD $0x3007, R22
	B utf8_multibyte_word

utf8_short_nonascii:
	MOVD $0xc2, R11
	CMP R11, R8
	BLO utf8_invalid
	MOVD $0xe0, R11
	CMP R11, R8
	BLO utf8_two
	MOVD $0xf0, R11
	CMP R11, R8
	BLO utf8_three
	MOVD $0xf5, R11
	CMP R11, R8
	BLO utf8_four
	B utf8_invalid

utf8_two:
	MOVD $2, R10
	CMP R10, R1
	BLO utf8_invalid
	MOVBU 1(R0), R9
	ADD $-0x80, R9, R12
	MOVD $0x40, R11
	CMP R11, R12
	BHS utf8_invalid
	B utf8_advance

utf8_three:
	MOVD $3, R10
	CMP R10, R1
	BLO utf8_invalid
	MOVBU 1(R0), R9
	ADD $-0x80, R9, R12
	MOVD $0x40, R11
	CMP R11, R12
	BHS utf8_invalid
	// E0 excludes overlong encodings; ED excludes UTF-16 surrogates.
	MOVD $0xe0, R11
	CMP R11, R8
	BNE utf8_three_surrogate
	MOVD $0xa0, R11
	CMP R11, R9
	BLO utf8_invalid
utf8_three_surrogate:
	MOVD $0xed, R11
	CMP R11, R8
	BNE utf8_three_last
	MOVD $0xa0, R11
	CMP R11, R9
	BHS utf8_invalid
utf8_three_last:
	MOVBU 2(R0), R9
	ADD $-0x80, R9, R12
	MOVD $0x40, R11
	CMP R11, R12
	BHS utf8_invalid
	B utf8_advance

utf8_four:
	MOVD $4, R10
	CMP R10, R1
	BLO utf8_invalid
	MOVBU 1(R0), R9
	ADD $-0x80, R9, R12
	MOVD $0x40, R11
	CMP R11, R12
	BHS utf8_invalid
	// F0 requires U+10000 or above; F4 is limited to U+10FFFF.
	MOVD $0xf0, R11
	CMP R11, R8
	BNE utf8_four_max
	MOVD $0x90, R11
	CMP R11, R9
	BLO utf8_invalid
utf8_four_max:
	MOVD $0xf4, R11
	CMP R11, R8
	BNE utf8_four_tail
	MOVD $0x90, R11
	CMP R11, R9
	BHS utf8_invalid
utf8_four_tail:
	MOVBU 2(R0), R9
	ADD $-0x80, R9, R12
	MOVD $0x40, R11
	CMP R11, R12
	BHS utf8_invalid
	MOVBU 3(R0), R9
	ADD $-0x80, R9, R12
	CMP R11, R12
	BHS utf8_invalid
	B utf8_advance

utf8_invalid:
	// A full output can still accept a valid suffix. Report full only when
	// this invalid byte cannot be recorded, retaining its exact input offset.
	CBZ R4, utf8_full
	MOVD R2, 0(R3)
	ADD $8, R3
	SUB $1, R4
	ADD $1, R5
	MOVD $1, R10
utf8_advance:
	ADD R10, R0
	ADD R10, R2
	SUB R10, R1
	// Keep adjacent non-ASCII sequences in the decoder. Probe for the word
	// fast path only after an ASCII byte, avoiding repeated loads and CTZ at
	// non-ASCII positions. The byte load stays within the input.
	CBZ R1, utf8_done
	MOVBU 0(R0), R8
	MOVD $0x80, R11
	CMP R11, R8
	BHS utf8_scalar_nonascii
	B utf8_loop

utf8_full:
	MOVD $1, R6
	MOVB R6, full+56(FP)
	B utf8_return
utf8_done:
	MOVB ZR, full+56(FP)
utf8_return:
	MOVD R2, next+40(FP)
	MOVD R5, count+48(FP)
	RET

// The uncommon overlapping tail must not shift the multibyte decoder.
utf8_tail:
	CMP R6, R15
	BLO utf8_scalar
	// Load only within the original input and remove consumed bytes. In
	// particular, a continuation byte from a valid earlier rune must not be
	// decoded again or reported as an invalid byte.
	ADD R1, R0, R7
	MOVD -8(R7), R7
	SUB R1, R6
	LSL $3, R6
	LSR R6, R7
	AND R14, R7
	CBNZ R7, utf8_nonascii_word
	ADD R1, R2
	B utf8_done

// The first word is already ASCII. Probe the next word before classifying
// the upper half, avoiding wasted work on short ASCII runs between runes.
utf8_ascii_start:
	MOVD 8(R0), R8
	AND R14, R8, R7
	// R6 still holds eight from the entry's word-length check.
	CBNZ R7, utf8_nonascii_block_offset
	MOVD 16(R0), R9
	MOVD 24(R0), R10
	ORR R9, R10, R6
	AND R14, R6
	CBNZ R6, utf8_nonascii_upper
	B utf8_ascii_advance

// The bulk path has already established its input bound. Only the next
// 32-byte bound needs checking in this hot loop.
	PCALIGN $64
utf8_ascii_blocks:
	MOVD 0(R0), R7
	MOVD 8(R0), R8
	MOVD 16(R0), R9
	MOVD 24(R0), R10
	ORR R7, R8, R6
	ORR R9, R10, R11
	ORR R11, R6
	AND R14, R6
	CBNZ R6, utf8_nonascii_block
utf8_ascii_advance:
	ADD $32, R0
	ADD $32, R2
	SUB $32, R1
	CMP R13, R1
	BHS utf8_ascii_blocks
	B utf8_loop

utf8_nonascii_block:
	// Retain the loaded words and locate the first non-ASCII word in input
	// order. The common decoder then handles sequences crossing the block.
	AND R14, R7
	CBNZ R7, utf8_nonascii_word
	AND R14, R8, R7
	MOVD $8, R6
	CBNZ R7, utf8_nonascii_block_offset
utf8_nonascii_upper:
	AND R14, R9, R7
	MOVD $16, R6
	CBNZ R7, utf8_nonascii_block_offset
	AND R14, R10, R7
	MOVD $24, R6
utf8_nonascii_block_offset:
	ADD R6, R0
	ADD R6, R2
	SUB R6, R1
	B utf8_nonascii_word
