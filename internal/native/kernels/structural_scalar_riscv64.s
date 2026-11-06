//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// Equality flags are exact: unlike subtraction-based existence tests, these
// byte-high bits may all be consumed without checking for propagated borrows.
#define STRUCT_EQ(c, dst) \
	XOR c, X29, X5; \
	ORCB X5, X5; \
	ANDN X5, X21, dst; \
	SRL $7, dst; \
	MUL X23, dst; \
	SRL $56, dst

// func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError)
// Bounded words carry string/escape state and bracket masks.
// Escaped quotes are ignored even outside strings, matching relaxed scanning.
// SP, RA, FP, and g are preserved.
TEXT ·skipContainer(SB), NOSPLIT, $0-40
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV X11, X12
	MOV $1, X13                 // depth
	MOV $0, X14                 // odd backslash run carried from preceding word
	MOV $0, X15                 // inside string: zero or all ones
	MOV $0x0101010101010101, X20
	MOV $0x8080808080808080, X21
	MOV $0x0102040810204080, X23
	MOV open+16(FP), X16
	ADD $2, X16, X17
	MUL X20, X16
	MUL X20, X17
	MOV $0x2222222222222222, X18
	MOV $0x5c5c5c5c5c5c5c5c, X19
	BEQZ X11, struct_eof

	MOV $8, X5
	BLTU X11, X5, struct_tail
	MOV 0(X10), X29
	MOV $32, X5
	BLTU X11, X5, struct_word
	AND $7, X10, X5
	BEQZ X5, struct_word
	// Align X10 down to an 8-byte boundary for the bulk scan while
	// shifting the first unaligned word into the high lanes of X29.
	SUB X5, X10
	ADD X5, X11
	SLL $3, X5, X6
	SLL X6, X29
	JMP struct_word

	PCALIGN $32
struct_scan:
	MOV $8, X5
	BLTU X11, X5, struct_tail
	MOV 0(X10), X29
struct_word:
	XOR X18, X29, X5
	ORCB X5, X5
	XOR X19, X29, X6
	ORCB X6, X6
	NOT X6, X6
	BNEZ X6, struct_slashes
	BNEZ X14, struct_carry
	NOT X5, X24
	BNEZ X24, struct_pack_quotes
	BNEZ X15, struct_advance
	JMP struct_brackets

struct_carry:
	ANDN X5, X21, X24
	SRL $7, X24
	MUL X23, X24
	SRL $56, X24
	ANDN X14, X24, X24
	MOV $0, X14
	JMP struct_quotes

struct_pack_quotes:
	AND X21, X24
	SRL $7, X24
	MUL X23, X24
	SRL $56, X24
	JMP struct_quotes

struct_slashes:
	ANDN X5, X21, X24
	SRL $7, X24
	MUL X23, X24
	SRL $56, X24
	AND X21, X6, X25
	SRL $7, X25
	MUL X23, X25
	SRL $56, X25
	NOT X14, X5
	AND X25, X5, X6
	SLL $1, X6, X7
	OR X14, X7
	NOT X7, X5
	AND X6, X5
	AND $0xaa, X5
	ADD X6, X5
	SLL $1, X5
	XOR $0x55, X5
	AND X7, X5                  // positions escaped by backslash runs
	NOT X5, X6
	AND X6, X24
	AND X25, X6, X14
	BEXTI $7, X14, X14
struct_quotes:
	// With no unescaped quote, the entire word keeps its entry string state.
	BNEZ X24, struct_brackets
	BNEZ X15, struct_advance
struct_brackets:
	XOR X16, X29, X5
	ORCB X5, X5
	XOR X17, X29, X6
	ORCB X6, X6
	AND X5, X6, X7
	NOT X7, X7
	BNEZ X7, struct_has_brackets
	// Without brackets only final quote parity matters, not each transition.
	BEQZ X24, struct_advance
	CPOP X24, X5
	AND $1, X5
	NEG X5, X5
	XOR X5, X15
	JMP struct_advance
struct_has_brackets:
	ANDN X5, X21, X26
	SRL $7, X26
	MUL X23, X26
	SRL $56, X26
	ANDN X6, X21, X28
	SRL $7, X28
	MUL X23, X28
	SRL $56, X28
	BEQZ X24, struct_depth
	SLL $1, X24, X5
	XOR X5, X24
	SLL $2, X24, X5
	XOR X5, X24
	SLL $4, X24, X5
	XOR X5, X24
	XOR X15, X24
	BEXTI $7, X24, X15
	NEG X15, X15
	ANDN X24, X26, X26
	ANDN X24, X28, X28

struct_depth:
	// Most words cannot close the entry depth. Only potentially completing
	// words need ordered close positions; all other depth updates use counts.
	CPOP X28, X5
	BLTU X5, X13, struct_count
struct_close:
	BEQZ X28, struct_count
	ADD $-1, X28, X5
	AND X26, X5, X6
	CPOP X6, X7
	ADD X7, X13
	XOR X6, X26
	SUB $1, X13
	BEQZ X13, struct_success_word
	AND X5, X28
	JMP struct_close
struct_count:
	CPOP X26, X5
	CPOP X28, X6
	ADD X5, X13
	SUB X6, X13
struct_advance:
	ADD $8, X10
	SUB $8, X11
	BGTZ X11, struct_scan
	JMP struct_eof

struct_tail:
	BLTU X12, X5, struct_short
	ADD X11, X10, X6
	MOV -8(X6), X29
	SUB X11, X5, X6
	SLL $3, X6
	SRL X6, X29
	JMP struct_word

struct_short:
	MOV $4, X6
	BLTU X11, X6, struct_byte
	MOVWU 0(X10), X29
	ADD X11, X10, X6
	MOVWU -4(X6), X6
	SUB X11, X5, X5
	SLL $3, X5
	SRL X5, X6
	SLL $32, X6
	OR X6, X29
	JMP struct_word

struct_byte:
	MOVBU 0(X10), X9
	ADD $1, X10
	SUB $1, X11
	MOV $92, X5
	BEQ X9, X5, struct_byte_slash
	MOV $34, X5
	BNE X9, X5, struct_byte_state
	BNEZ X14, struct_byte_state
	NOT X15, X15
struct_byte_state:
	MOV $0, X14
	BNEZ X15, struct_byte_next
	AND $255, X16, X5
	BEQ X9, X5, struct_byte_open
	AND $255, X17, X5
	BNE X9, X5, struct_byte_next
	SUB $1, X13
	BNEZ X13, struct_byte_next
	SUB X11, X12, X5
	JMP struct_success
struct_byte_open:
	ADD $1, X13
	JMP struct_byte_next
struct_byte_slash:
	XOR $1, X14
struct_byte_next:
	BNEZ X11, struct_byte
	JMP struct_eof

struct_success_word:
	CTZ X28, X5
	SUB X11, X12, X6
	ADD X6, X5
	ADD $1, X5
struct_success:
	MOV $0, X6
	JMP struct_done
struct_eof:
	MOV X12, X5
	MOV $1, X6
struct_done:
	MOV X5, end+24(FP)
	MOV X6, err+32(FP)
	RET

#undef STRUCT_EQ
