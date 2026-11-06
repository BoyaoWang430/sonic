//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// Exact byte equality: high bits in dst identify matching bytes in X13.
// ORCB expands nonzero XOR bytes to 0xff; ANDN retains only matching high bits.
// X14 = byte high bits; X20 is temporary. X15 holds the top three bits per byte
// for control classification: a byte is below 0x20 exactly when those are zero.
#define BYTE_EQ(pattern, dst) \
	XOR pattern, X13, X20; \
	ORCB X20, X20; \
	ANDN X20, X14, dst

// X28..X30 hold repeated space/tab/newline bytes; X26 holds 0xfbfbfbfbfbfbfbfb
// so both tab (0x09) and carriage return (0x0d) map to 0x09 in one ORCB.
// X31 remains available to the assembler for pseudo-instruction expansion.
// AND the non-match bytes, then complement once to form exact space flags.
#define SPACE_MASK \
	XOR X28, X13, X17; \
	ORCB X17, X17; \
	XOR X30, X13, X20; \
	ORCB X20, X20; \
	AND X20, X17, X17; \
	AND X26, X13, X20; \
	XOR X29, X20, X20; \
	ORCB X20, X20; \
	AND X20, X17, X17; \
	ANDN X17, X14, X17

// Pack exact byte high bits into eight consecutive bits, then retain source
// position using X7. X16 is the standard SWAR gather multiplication constant.
#define PACK_MASK(src, dst) \
	SRL $7, src, X23; \
	MUL X16, X23, X23; \
	SRL $56, X23, X23; \
	SLL X7, X23, X23; \
	OR X23, dst, dst

// Each mask operation covers the complete input of at most 64 bytes.
// Loads stay within the input. Bounded overlapping last words combine
// repeated classifications at their source bits using exact masks.
// X8/FP, X27/g, SP and RA are preserved, and X31 is reserved for the assembler.

// func nonSpaceMask(src *byte, size int) (mask uint64)
TEXT ·nonSpaceMask(SB), NOSPLIT, $0-24
	MOV src+0(FP), X5
	MOV size+8(FP), X6
	MOV $0, X7
	MOV $0, X11
	BEQZ X6, masks_done
	MOV $0x8080808080808080, X14
	MOV $0x0102040810204080, X16
	MOV $0x2020202020202020, X28
	MOV $0x0909090909090909, X29
	MOV $0x0a0a0a0a0a0a0a0a, X30
	MOV $0xfbfbfbfbfbfbfbfb, X26
	SLTI $8, X6, X20
	BNEZ X20, masks_short
masks_word:
	MOV (X5), X13
masks_classify:
	SPACE_MASK
	XOR X14, X17, X19
	PACK_MASK(X19, X11)
masks_word_advance:
	ADD $8, X5
	ADD $8, X7
	SUB $8, X6
	SLTI $8, X6, X20
	BEQZ X20, masks_word
	BEQZ X6, masks_done
	// Classify the bounded final word at its original bit offset. OR is
	// idempotent for the already classified prefix of this overlapping word.
	ADD $-8, X6, X20
	ADD X20, X5
	ADD X20, X7
	MOV $8, X6
	JMP masks_word

masks_short:
	SLTI $4, X6, X20
	BNEZ X20, masks_byte
	MOVWU (X5), X13
	ADD X6, X5, X20
	MOVWU -4(X20), X20
	MOV $8, X21
	SUB X6, X21, X21
	SLL $3, X21
	SRL X21, X20
	SLL $32, X20
	OR X20, X13
	SLL $3, X6, X21
	SLL X21, X28, X20
	OR X20, X13
	MOV $8, X6
	JMP masks_classify

masks_byte:
	MOVBU (X5), X13
	MOV $1, X20
	SLL X7, X20, X20
	MOV $32, X21
	BEQ X13, X21, masks_byte_advance
	MOV $9, X21
	BEQ X13, X21, masks_byte_advance
	MOV $10, X21
	BEQ X13, X21, masks_byte_advance
	MOV $13, X21
	BEQ X13, X21, masks_byte_advance
	OR X20, X11, X11
masks_byte_advance:
	ADD $1, X5
	ADD $1, X7
	SUB $1, X6
	BNEZ X6, masks_byte
masks_done:
	MOV X11, mask+16(FP)
	RET

// func traversalMasks(src *byte, size int, validate uint64) (events, nonspace uint64)
TEXT ·traversalMasks(SB), NOSPLIT, $0-40
	MOV src+0(FP), X5
	MOV size+8(FP), X6
	MOV validate+16(FP), X9
	MOV $0, X7
	MOV $0, X10
	MOV $0, X11
	BEQZ X6, masks_done
	MOV $0x8080808080808080, X14
	MOV $0xe0e0e0e0e0e0e0e0, X15
	MOV $0x0102040810204080, X16
	MOV $0x2020202020202020, X28
	MOV $0x0909090909090909, X29
	MOV $0x0a0a0a0a0a0a0a0a, X30
	MOV $0xfbfbfbfbfbfbfbfb, X26
	SLTI $8, X6, X20
	BNEZ X20, masks_short
masks_word:
	MOV (X5), X13
masks_classify:
	SPACE_MASK
	XOR X14, X17, X19
	PACK_MASK(X19, X11)
	MOV $0x2222222222222222, X22
	BYTE_EQ(X22, X18)
	MOV $0x5c5c5c5c5c5c5c5c, X22
	BYTE_EQ(X22, X19)
	OR X19, X18, X18
	BEQZ X9, masks_word_events
	AND X15, X13, X20
	ORCB X20, X20
	ANDN X20, X14, X20
	OR X20, X18, X18
masks_word_events:
	PACK_MASK(X18, X10)
masks_word_advance:
	ADD $8, X5
	ADD $8, X7
	SUB $8, X6
	SLTI $8, X6, X20
	BEQZ X20, masks_word
	BEQZ X6, masks_done
	// Classify the bounded final word at its original bit offset. OR is
	// idempotent for the already classified prefix of this overlapping word.
	ADD $-8, X6, X20
	ADD X20, X5
	ADD X20, X7
	MOV $8, X6
	JMP masks_word

masks_short:
	SLTI $4, X6, X20
	BNEZ X20, masks_byte
	MOVWU (X5), X13
	ADD X6, X5, X20
	MOVWU -4(X20), X20
	MOV $8, X21
	SUB X6, X21, X21
	SLL $3, X21
	SRL X21, X20
	SLL $32, X20
	OR X20, X13
	SLL $3, X6, X21
	SLL X21, X28, X20
	OR X20, X13
	MOV $8, X6
	JMP masks_classify

masks_byte:
	MOVBU (X5), X13
	MOV $1, X20
	SLL X7, X20, X20
	MOV $32, X21
	BEQ X13, X21, masks_byte_events
	MOV $9, X21
	BEQ X13, X21, masks_byte_events
	MOV $10, X21
	BEQ X13, X21, masks_byte_events
	MOV $13, X21
	BEQ X13, X21, masks_byte_events
	OR X20, X11, X11
masks_byte_events:
	MOV $34, X21
	BEQ X13, X21, masks_byte_event
	MOV $92, X21
	BEQ X13, X21, masks_byte_event
	BEQZ X9, masks_byte_advance
	SLTI $32, X13, X21
	BEQZ X21, masks_byte_advance
masks_byte_event:
	OR X20, X10, X10
masks_byte_advance:
	ADD $1, X5
	ADD $1, X7
	SUB $1, X6
	BNEZ X6, masks_byte
masks_done:
	MOV X10, events+24(FP)
	MOV X11, nonspace+32(FP)
	RET

// func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64)
TEXT ·pathMasks(SB), NOSPLIT, $0-40
	MOV src+0(FP), X5
	MOV size+8(FP), X6
	MOV $0, X7
	MOV $0, X10
	MOV $0, X11
	MOV $0, X12
	BEQZ X6, masks_done
	MOV $0x8080808080808080, X14
	MOV $0x0102040810204080, X16
	MOV $0x2020202020202020, X28
	MOV $0x0909090909090909, X29
	MOV $0x0a0a0a0a0a0a0a0a, X30
	MOV $0xfbfbfbfbfbfbfbfb, X26
	SLTI $8, X6, X20
	BNEZ X20, masks_short
masks_word:
	MOV (X5), X13
masks_classify:
	SPACE_MASK
	XOR X14, X17, X19
	PACK_MASK(X19, X11)
	MOV $0x2222222222222222, X22
	BYTE_EQ(X22, X18)
	MOV $0x5c5c5c5c5c5c5c5c, X22
	BYTE_EQ(X22, X19)
	OR X19, X18, X18
masks_word_events:
	PACK_MASK(X18, X10)
masks_word_boundary:
	MOV $0x7d7d7d7d7d7d7d7d, X22
	OR X28, X13, X20
	XOR X22, X20, X20
	ORCB X20, X20
	ANDN X20, X14, X18
	OR X18, X17, X17
	MOV $0x2c2c2c2c2c2c2c2c, X22
	BYTE_EQ(X22, X18)
	OR X18, X17, X17
	PACK_MASK(X17, X12)
masks_word_advance:
	ADD $8, X5
	ADD $8, X7
	SUB $8, X6
	BLTZ X6, masks_short_done
	SLTI $8, X6, X20
	BEQZ X20, masks_word
	BEQZ X6, masks_done
	// Classify the bounded final word at its original bit offset. OR is
	// idempotent for the already classified prefix of this overlapping word.
	ADD $-8, X6, X20
	ADD X20, X5
	ADD X20, X7
	MOV $8, X6
	JMP masks_word

masks_short:
	SLTI $4, X6, X20
	BNEZ X20, masks_byte
	MOVWU (X5), X13
	ADD X6, X5, X20
	MOVWU -4(X20), X20
	MOV $8, X21
	SUB X6, X21, X21
	SLL $3, X21
	SRL X21, X20
	SLL $32, X20
	OR X20, X13
	SLL $3, X6, X21
	SLL X21, X28, X20
	OR X20, X13
	JMP masks_classify
masks_short_done:
	ADD $8, X6
	MOV $1, X20
	SLL X6, X20
	ADD $-1, X20
	AND X20, X12
	JMP masks_done

masks_byte:
	MOVBU (X5), X13
	MOV $1, X20
	SLL X7, X20, X20
	MOV $0, X19
	MOV $32, X21
	BEQ X13, X21, masks_byte_space
	MOV $9, X21
	BEQ X13, X21, masks_byte_space
	MOV $10, X21
	BEQ X13, X21, masks_byte_space
	MOV $13, X21
	BEQ X13, X21, masks_byte_space
	OR X20, X11, X11
	JMP masks_byte_events
masks_byte_space:
	MOV $1, X19
masks_byte_events:
	MOV $34, X21
	BEQ X13, X21, masks_byte_event
	MOV $92, X21
	BEQ X13, X21, masks_byte_event
	JMP masks_byte_boundary
masks_byte_event:
	OR X20, X10, X10
masks_byte_boundary:
	BNEZ X19, masks_byte_boundary_set
	MOV $125, X21
	BEQ X13, X21, masks_byte_boundary_set
	MOV $93, X21
	BEQ X13, X21, masks_byte_boundary_set
	MOV $44, X21
	BNE X13, X21, masks_byte_advance
masks_byte_boundary_set:
	OR X20, X12, X12
masks_byte_advance:
	ADD $1, X5
	ADD $1, X7
	SUB $1, X6
	BNEZ X6, masks_byte
masks_done:
	MOV X10, events+16(FP)
	MOV X11, nonspace+24(FP)
	MOV X12, boundaries+32(FP)
	RET

// func space(src *byte, size int) (n int)
// A complete whitespace scan with bounded loads and overlapping tails.
TEXT ·space(SB), NOSPLIT, $0-24
	PCALIGN $64
	MOV src+0(FP), X5
	MOV size+8(FP), X6
	MOV $0, X7
	BEQZ X6, space_done
	MOV $0x8080808080808080, X14
	MOV $0x2020202020202020, X28
	MOV $0x0909090909090909, X29
	MOV $0x0a0a0a0a0a0a0a0a, X30
	MOV $0xfbfbfbfbfbfbfbfb, X26
	SLTI $8, X6, X20
	BNEZ X20, space_short
space_word:
	MOV (X5), X13
space_classify:
	SPACE_MASK
	XOR X14, X17, X17
	BNEZ X17, space_found
	ADD $8, X5
	ADD $8, X7
	SUB $8, X6
	SLTI $8, X6, X20
	BEQZ X20, space_word
	BEQZ X6, space_done
	// Every repeated byte has already been verified as whitespace, so it
	// cannot produce a match before the newly covered suffix.
	ADD $-8, X6, X20
	ADD X20, X5
	ADD X20, X7
	MOV $8, X6
	JMP space_word
space_short:
	SLTI $4, X6, X20
	BNEZ X20, space_byte
	MOVWU (X5), X13
	ADD X6, X5, X20
	MOVWU -4(X20), X20
	MOV $8, X21
	SUB X6, X21, X21
	SLL $3, X21
	SRL X21, X20
	SLL $32, X20
	OR X20, X13
	JMP space_classify
space_byte:
	MOVBU (X5), X13
	MOV $32, X20
	BEQ X13, X20, space_byte_advance
	MOV $9, X20
	BEQ X13, X20, space_byte_advance
	MOV $10, X20
	BEQ X13, X20, space_byte_advance
	MOV $13, X20
	BNE X13, X20, space_done
space_byte_advance:
	ADD $1, X5
	ADD $1, X7
	SUB $1, X6
	BEQZ X6, space_done
	JMP space_byte
space_found:
	CTZ X17, X17
	SRL $3, X17, X17
	ADD X17, X7, X7
space_done:
	MOV X7, n+16(FP)
	RET
