//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool)
// X10: input pointer; X11: remaining bytes; X12: absolute input offset.
// X13: output pointer; X14: remaining output slots; X15: offsets written.
// Bounded unaligned words skip ASCII runs. Invalid bytes are recorded singly;
// output exhaustion leaves the next invalid byte unread. FP/g/SP/RA/X31 are preserved.
TEXT ·utf8Scan(SB), $0-57
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV base+16(FP), X12
	MOV positions+24(FP), X13
	MOV limit+32(FP), X14
	MOV $0, X15
	MOV $0x8080808080808080, X24
	MOV X11, X25               // original size bounds overlapping tail loads
	JMP utf8_loop

	// Keep both hot loops stable when other scalar kernels change size.
	PCALIGN $64
utf8_loop:
	BEQZ X11, utf8_done
	MOV $8, X16
	BLTU X11, X16, utf8_tail
	MOV 0(X10), X17
	AND X24, X17
	BNEZ X17, utf8_nonascii_word
	// Probe one word before entering the bulk loop so mixed text can reach
	// the multibyte decoder without loading and classifying a whole block.
	MOV $32, X23
	BGEU X11, X23, utf8_ascii_start
	ADD $8, X10
	ADD $8, X12
	SUB $8, X11
	JMP utf8_loop
	PCALIGN $64
utf8_nonascii_word:
	// Consume the ASCII prefix exactly once, then decode at its first high
	// byte. The sequence may cross this word, but never the input boundary.
	CTZ X17, X16
	SRL $3, X16
	ADD X16, X10
	ADD X16, X12
	SUB X16, X11
utf8_scalar:
	MOVBU 0(X10), X18
	MOV $0x80, X21
	BGEU X18, X21, utf8_scalar_nonascii
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	JMP utf8_loop
utf8_scalar_nonascii:
	MOV $0xc2, X21
	BLTU X18, X21, utf8_invalid
	MOV $0xe0, X21
	BLTU X18, X21, utf8_two
	MOV $0xf0, X21
	BLTU X18, X21, utf8_three
	MOV $0xf5, X21
	BLTU X18, X21, utf8_four
	JMP utf8_invalid

utf8_two:
	MOV $2, X20
	BLTU X11, X20, utf8_invalid
	MOVBU 1(X10), X19
	ADD $-0x80, X19, X22
	MOV $0x40, X21
	BGEU X22, X21, utf8_invalid
	JMP utf8_advance

utf8_three:
	MOV $3, X20
	BLTU X11, X20, utf8_invalid
	MOVBU 1(X10), X19
	ADD $-0x80, X19, X22
	MOV $0x40, X21
	BGEU X22, X21, utf8_invalid
	// E0 excludes overlong encodings; ED excludes UTF-16 surrogates.
	MOV $0xe0, X21
	BNE X18, X21, utf8_three_surrogate
	MOV $0xa0, X21
	BLTU X19, X21, utf8_invalid
utf8_three_surrogate:
	MOV $0xed, X21
	BNE X18, X21, utf8_three_last
	MOV $0xa0, X21
	BGEU X19, X21, utf8_invalid
utf8_three_last:
	MOVBU 2(X10), X19
	ADD $-0x80, X19, X22
	MOV $0x40, X21
	BGEU X22, X21, utf8_invalid
	JMP utf8_advance

utf8_four:
	MOV $4, X20
	BLTU X11, X20, utf8_invalid
	MOVBU 1(X10), X19
	ADD $-0x80, X19, X22
	MOV $0x40, X21
	BGEU X22, X21, utf8_invalid
	// F0 requires U+10000 or above; F4 is limited to U+10FFFF.
	MOV $0xf0, X21
	BNE X18, X21, utf8_four_max
	MOV $0x90, X21
	BLTU X19, X21, utf8_invalid
utf8_four_max:
	MOV $0xf4, X21
	BNE X18, X21, utf8_four_tail
	MOV $0x90, X21
	BGEU X19, X21, utf8_invalid
utf8_four_tail:
	MOVBU 2(X10), X19
	ADD $-0x80, X19, X22
	MOV $0x40, X21
	BGEU X22, X21, utf8_invalid
	MOVBU 3(X10), X19
	ADD $-0x80, X19, X22
	BGEU X22, X21, utf8_invalid
	JMP utf8_advance

utf8_invalid:
	// A full output can still accept a valid suffix. Report full only when
	// this invalid byte cannot be recorded, retaining its exact input offset.
	BEQZ X14, utf8_full
	MOV X12, 0(X13)
	ADD $8, X13
	SUB $1, X14
	ADD $1, X15
	MOV $1, X20
utf8_advance:
	ADD X20, X10
	ADD X20, X12
	SUB X20, X11
	// Keep adjacent non-ASCII sequences in the decoder. Probe for the word
	// fast path only after an ASCII byte, avoiding repeated loads and CTZ at
	// non-ASCII positions. The byte load stays within the input.
	BEQZ X11, utf8_done
	MOVBU 0(X10), X18
	MOV $0x80, X21
	BGEU X18, X21, utf8_scalar_nonascii
	JMP utf8_loop

utf8_full:
	MOV $1, X16
	MOVB X16, full+56(FP)
	JMP utf8_return
utf8_done:
	MOVB X0, full+56(FP)
utf8_return:
	MOV X12, next+40(FP)
	MOV X15, count+48(FP)
	RET

// The uncommon overlapping tail must not shift the multibyte decoder.
utf8_tail:
	BLTU X25, X16, utf8_scalar
	// Load only within the original input and remove consumed bytes. In
	// particular, a continuation byte from a valid earlier rune must not be
	// decoded again or reported as an invalid byte.
	ADD X11, X10, X17
	MOV -8(X17), X17
	SUB X11, X16
	SLL $3, X16
	SRL X16, X17
	AND X24, X17
	BNEZ X17, utf8_nonascii_word
	ADD X11, X12
	JMP utf8_done

// The first word is already ASCII. Probe the next word before classifying
// the upper half, avoiding wasted work on short ASCII runs between runes.
utf8_ascii_start:
	MOV 8(X10), X18
	AND X24, X18, X17
	// X16 still holds eight from the entry's word-length check.
	BNEZ X17, utf8_nonascii_block_offset
	MOV 16(X10), X19
	MOV 24(X10), X20
	OR X19, X20, X16
	AND X24, X16
	BNEZ X16, utf8_nonascii_upper
	JMP utf8_ascii_advance

// The bulk path has already established its input bound. Only the next
// 32-byte bound needs checking in this hot loop.
	PCALIGN $64
utf8_ascii_blocks:
	MOV 0(X10), X17
	MOV 8(X10), X18
	MOV 16(X10), X19
	MOV 24(X10), X20
	OR X17, X18, X16
	OR X19, X20, X21
	OR X21, X16
	AND X24, X16
	BNEZ X16, utf8_nonascii_block
utf8_ascii_advance:
	ADD $32, X10
	ADD $32, X12
	SUB $32, X11
	BGEU X11, X23, utf8_ascii_blocks
	JMP utf8_loop

utf8_nonascii_block:
	// Retain the loaded words and locate the first non-ASCII word in input
	// order. The common decoder then handles sequences crossing the block.
	AND X24, X17
	BNEZ X17, utf8_nonascii_word
	AND X24, X18, X17
	MOV $8, X16
	BNEZ X17, utf8_nonascii_block_offset
utf8_nonascii_upper:
	AND X24, X19, X17
	MOV $16, X16
	BNEZ X17, utf8_nonascii_block_offset
	AND X24, X20, X17
	MOV $24, X16
utf8_nonascii_block_offset:
	ADD X16, X10
	ADD X16, X12
	SUB X16, X11
	JMP utf8_nonascii_word
