//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"
// Shared NEON whitespace classifier and exact bit packing. These macros only
// use ordinary Go assembler instructions; the input remains unmodified.
// V29 holds the whitespace lookup, V30 bit weights, V31 the packing shuffle.
#define INIT_WHITESPACE \
	MOVD $·whitespaceTables(SB), R9; \
	VLD1 (R9), [V29.B16, V30.B16, V31.B16]; \
	VMOVI $143, V25.B16

// Keep bit 7 when forming the table index. Bytes >= 128 therefore index past
// the table and yield zero, which cannot equal their original byte. Of the
// remaining bytes, only space, tab, LF, and CR match their table entry.
#define CLASSIFY_WHITESPACE(input, output) \
	VAND V25.B16, input, output; \
	VTBL output, [V29.B16], output; \
	VCMEQ input, output, output

#define PACK_WHITESPACE(reg, output) \
	VAND V30.B16, reg.B16, reg.B16; \
	VTBL V31.B16, [reg.B16], reg.B16; \
	VADDV reg.H8, reg; \
	VMOV reg.H[0], output

// Requires 64 readable bytes at R0. Returns one bit per nonspace byte in R9.
// R0/R1/R3 are preserved. Scratch: R9-R12, V0-V7; constants V25/V29-V31.
#define NONSPACE64 \
	FLDPQ (R0), (F0, F1); \
	FLDPQ 32(R0), (F2, F3); \
	CLASSIFY_WHITESPACE(V0.B16, V4.B16); \
	CLASSIFY_WHITESPACE(V1.B16, V5.B16); \
	CLASSIFY_WHITESPACE(V2.B16, V6.B16); \
	CLASSIFY_WHITESPACE(V3.B16, V7.B16); \
	PACK_WHITESPACE(V4, R9); \
	PACK_WHITESPACE(V5, R10); \
	PACK_WHITESPACE(V6, R11); \
	PACK_WHITESPACE(V7, R12); \
	ORR R10<<16, R9, R9; \
	ORR R11<<32, R9, R9; \
	ORR R12<<48, R9, R9; \
	MVN R9, R9

// space scans a complete run in one ABI0 call, with bounded tails.
TEXT ·space(SB), NOSPLIT, $0-24
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD $0, R3
	CBZ R1, done
	CMP $16, R1
	BLO tail
	INIT_WHITESPACE
block:
	VLD1 (R0), [V0.B16]
	CLASSIFY_WHITESPACE(V0.B16, V2.B16)
	VAND V30.B16, V2.B16, V2.B16
	VTBL V31.B16, [V2.B16], V2.B16
	VADDV V2.H8, V2
	VMOV V2.H[0], R9
	EOR $65535, R9, R9
	CBNZ R9, found
	ADD $16, R0
	SUB $16, R1
	ADD $16, R3
	CBZ R1, done
	CMP $16, R1
	BHS block
	B tail
found:
	RBIT R9, R9
	CLZ R9, R9
	ADD R9, R3, R3
	B done
tail:
	MOVBU (R0), R8
	CMP $32, R8
	BEQ advance
	CMP $9, R8
	BEQ advance
	CMP $10, R8
	BEQ advance
	CMP $13, R8
	BNE done
advance:
	ADD $1, R0
	SUB $1, R1
	ADD $1, R3
	CBNZ R1, tail
done:
	MOVD R3, n+16(FP)
	RET
