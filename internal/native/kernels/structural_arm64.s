//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// Exact byte-to-bit packing, shared by all four structural classes. This is a
// local macro so the complete scan has no per-block ABI or register-save cost.
// The input vector is loaded once and remains available for all four classes.
#define STRUCTURAL_CLASS(c, dst) \
	VCMEQ c.B16, V0.B16, V2.B16; \
	VAND V30.B16, V2.B16, V2.B16; \
	VTBL V31.B16, [V2.B16], V2.B16; \
	VADDV V2.H8, V2; \
	VMOV V2.H[0], R9; \
	LSL R6, R9, R9; \
	ORR R9, dst, dst

// Baseline NEON, matching the Go compiler's scalar popcount lowering. No
// optional crypto/PMULL or newer scalar population-count instruction is used.
#define STRUCTURAL_POPCOUNT(src, dst) \
	FMOVD src, F2; \
	VCNT V2.B8, V2.B8; \
	VUADDLV V2.B8, V2; \
	FMOVD F2, dst

// func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError)
// R0 advances while loading each block; R1 is remaining input, R14 original
// size and R15 the current block's offset. R7 is same-kind depth, R16 carries
// an odd trailing backslash, and R17 is the entry string state (0 or -1).
// R3/R4/R5/R12 are quote/slash/open/close masks. R19/R20 are block width and
// its remaining load count. R2/R13 retain opening/closing bytes. Other named
// scalar/vector registers are scratch or constants. R18, g, FP and LR are
// preserved; there are no calls, stack scratch, allocations or input writes.
TEXT ·skipContainer(SB), NOSPLIT, $0-40
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD R1, R14
	MOVD $0, R15
	MOVD $1, R7
	MOVD $0, R16
	MOVD $0, R17
	CBZ R1, structural_eof
	MOVD open+16(FP), R2
	ADD $2, R2, R13
	VMOV R2, V20.B16
	VMOV R13, V21.B16
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	MOVD $structuralMaskTables<>(SB), R9
	VLD1 (R9), [V30.B16, V31.B16]
	MOVD $0x5555555555555555, R21
	MOVD $0xaaaaaaaaaaaaaaaa, R22

structural_block:
	MOVD $64, R19
	CMP R19, R1
	CSEL LO, R1, R19, R19
	MOVD R19, R20
	MOVD $0, R3
	MOVD $0, R4
	MOVD $0, R5
	MOVD $0, R12
	MOVD $0, R6
structural_load:
	CMP $16, R20
	BLO structural_tail
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VCMEQ V20.B16, V0.B16, V4.B16
	VCMEQ V21.B16, V0.B16, V5.B16
	VAND V30.B16, V2.B16, V2.B16
	VAND V30.B16, V3.B16, V3.B16
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V5.B16, V5.B16
	VADDP V3.B16, V2.B16, V2.B16
	VADDP V5.B16, V4.B16, V4.B16
	VADDP V4.B16, V2.B16, V2.B16
	VADDP V2.B16, V2.B16, V2.B16
	VMOV V2.H[0], R9
	VMOV V2.H[1], R10
	LSL R6, R9, R9
	ORR R9, R3, R3
	LSL R6, R10, R10
	ORR R10, R4, R4
	VMOV V2.H[2], R9
	VMOV V2.H[3], R10
	LSL R6, R9, R9
	ORR R9, R5, R5
	LSL R6, R10, R10
	ORR R10, R12, R12
	ADD $16, R0
	SUB $16, R20
	ADD $16, R6
	CBNZ R20, structural_load
	B structural_classify

// Fewer than sixteen bytes remain: classify exactly those bytes without a
// speculative vector read, copied padding, or a page-boundary assumption.
structural_tail:
	MOVD $1, R10
	LSL R6, R10, R10
	MOVBU (R0), R8
	CMP $34, R8
	BNE structural_tail_slash
	ORR R10, R3, R3
structural_tail_slash:
	CMP $92, R8
	BNE structural_tail_open
	ORR R10, R4, R4
structural_tail_open:
	CMP R2, R8
	BNE structural_tail_close
	ORR R10, R5, R5
structural_tail_close:
	CMP R13, R8
	BNE structural_tail_next
	ORR R10, R12, R12
structural_tail_next:
	ADD $1, R0
	SUB $1, R20
	ADD $1, R6
	CBNZ R20, structural_tail

structural_classify:
	// Carry applies outside strings too. It escapes quotes, never brackets.
	ORR R16, R4, R9
	CBZ R9, structural_quotes
	BIC R16, R4, R11
	LSL $1, R11, R23
	ORR R16, R23, R23
	BIC R23, R11, R24
	AND R22, R24, R24
	ADD R11, R24, R24
	LSL $1, R24, R24
	EOR R21, R24, R24
	AND R23, R24, R24
	BIC R24, R3, R3
	BIC R24, R4, R16
	SUB $1, R19, R9
	LSR R9, R16, R16
	AND $1, R16, R16

structural_quotes:
	CBNZ R3, structural_quote_prefix
	// With no unescaped quote the entire block keeps its entry string state.
	CBNZ R17, structural_advance
	B structural_brackets
structural_quote_prefix:
	EOR R3<<1, R3, R3
	EOR R3<<2, R3, R3
	EOR R3<<4, R3, R3
	EOR R3<<8, R3, R3
	EOR R3<<16, R3, R3
	EOR R3<<32, R3, R3
	EOR R17, R3, R3
	SUB $1, R19, R9
	LSR R9, R3, R17
	AND $1, R17, R17
	NEG R17, R17
	BIC R3, R5, R5
	BIC R3, R12, R12

structural_brackets:
	// A block with fewer closes than entry depth cannot finish the container.
	// Otherwise examine each close in position order, counting only the opens
	// before it. Opens after the first matching close must not hide completion.
	STRUCTURAL_POPCOUNT(R12, R8)
	CMP R7, R8
	BLO structural_count
structural_close:
	SUB $1, R12, R11
	AND R11, R5, R26
	STRUCTURAL_POPCOUNT(R26, R25)
	ADD R25, R7, R7
	BIC R26, R5, R5
	SUB $1, R7
	CBZ R7, structural_success
	AND R11, R12, R12
	CBNZ R12, structural_close
	MOVD $0, R8 // All closes were already accounted for above.
structural_count:
	STRUCTURAL_POPCOUNT(R5, R9)
	ADD R9, R7, R7
	SUB R8, R7, R7
structural_advance:
	ADD R19, R15, R15
	SUB R19, R1, R1
	CBNZ R1, structural_block
structural_eof:
	MOVD R14, end+24(FP)
	MOVD $1, R0 // types.ERR_EOF
	MOVD R0, err+32(FP)
	RET
structural_success:
	RBIT R12, R9
	CLZ R9, R9
	ADD R15, R9, R9
	ADD $1, R9
	MOVD R9, end+24(FP)
	MOVD ZR, err+32(FP)
	RET

DATA structuralMaskTables<>+0(SB)/8, $0x8040201008040201
DATA structuralMaskTables<>+8(SB)/8, $0x8040201008040201
DATA structuralMaskTables<>+16(SB)/8, $0x0b030a0209010800
DATA structuralMaskTables<>+24(SB)/8, $0x0f070e060d050c04
GLOBL structuralMaskTables<>(SB), RODATA|NOPTR, $32

#undef STRUCTURAL_CLASS
#undef STRUCTURAL_POPCOUNT
