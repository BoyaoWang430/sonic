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
	VAND V30.B16, V4.B16, V4.B16; \
	VAND V30.B16, V5.B16, V5.B16; \
	VAND V30.B16, V6.B16, V6.B16; \
	VAND V30.B16, V7.B16, V7.B16; \
	VADDP V5.B16, V4.B16, V4.B16; \
	VADDP V7.B16, V6.B16, V6.B16; \
	VADDP V6.B16, V4.B16, V4.B16; \
	VADDP V4.B16, V4.B16, V4.B16; \
	VMOV V4.D[0], R9; \
	MVN R9, R9

// Exact packed byte masks use the weighting/table/reduction sequence from
// the previous NEON backend. Every 16-byte load fits inside the logical input.

TEXT ·nonSpaceMask(SB), NOSPLIT, $0-24
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD $0, R3
	MOVD $0, R6
	CBZ R1, done
	CMP $16, R1
	BLO tail
	INIT_WHITESPACE
	CMP $64, R1
	BEQ full
loop:
	CMP $16, R1
	BLO tail
	VLD1 (R0), [V0.B16]
	CLASSIFY_WHITESPACE(V0.B16, V2.B16)
	VAND V30.B16, V2.B16, V2.B16
	VTBL V31.B16, [V2.B16], V2.B16
	VADDV V2.H8, V2
	VMOV V2.H[0], R9
	EOR $65535, R9, R9
	LSL R6, R9, R9
	ORR R9, R3, R3
advance:
	ADD $16, R0
	SUB $16, R1
	ADD $16, R6
	CBNZ R1, loop
	B done
tail:
	MOVD $1, R10
	LSL R6, R10, R10
	MOVBU (R0), R8
classify_space:
	CMP $32, R8
	BEQ is_space
	CMP $9, R8
	BEQ is_space
	CMP $10, R8
	BEQ is_space
	CMP $13, R8
	BEQ is_space
	ORR R10, R3, R3
	B tail_advance
is_space:
tail_advance:
	ADD $1, R0
	SUB $1, R1
	ADD $1, R6
	CBNZ R1, tail
done:
	MOVD R3, mask+16(FP)
	RET
full:
	NONSPACE64
	MOVD R9, mask+16(FP)
	RET

TEXT ·traversalMasks(SB), NOSPLIT, $0-40
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD $0, R3
	MOVD $0, R4
	MOVD $0, R6
	MOVD validate+16(FP), R2
	CBZ R1, done
	CMP $16, R1
	BLO tail
	INIT_WHITESPACE
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	VMOVI $0, V28.B16
loop:
	CMP $16, R1
	BLO tail
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VORR V3.B16, V2.B16, V2.B16
	CBZ R2, event_pack
	VUSHR $5, V0.B16, V3.B16
	VCMEQ V28.B16, V3.B16, V3.B16
	VORR V3.B16, V2.B16, V2.B16
event_pack:
	CLASSIFY_WHITESPACE(V0.B16, V4.B16)
	VAND V30.B16, V2.B16, V2.B16
	VAND V30.B16, V4.B16, V4.B16
	VADDP V4.B16, V2.B16, V2.B16
	VADDP V2.B16, V2.B16, V2.B16
	VADDP V2.B16, V2.B16, V2.B16
	VMOV V2.H[0], R9
	VMOV V2.H[1], R10
	LSL R6, R9, R9
	ORR R9, R3, R3
	EOR $65535, R10, R10
	LSL R6, R10, R10
	ORR R10, R4, R4
advance:
	ADD $16, R0
	SUB $16, R1
	ADD $16, R6
	CBNZ R1, loop
	B done
tail:
	MOVD $1, R10
	LSL R6, R10, R10
	MOVBU (R0), R8
	CMP $34, R8
	BEQ set_event
	CMP $92, R8
	BEQ set_event
	CBZ R2, classify_space
	CMP $32, R8
	BLO set_event
	B classify_space
set_event:
	ORR R10, R3, R3
classify_space:
	CMP $32, R8
	BEQ is_space
	CMP $9, R8
	BEQ is_space
	CMP $10, R8
	BEQ is_space
	CMP $13, R8
	BEQ is_space
	ORR R10, R4, R4
	B tail_advance
is_space:
tail_advance:
	ADD $1, R0
	SUB $1, R1
	ADD $1, R6
	CBNZ R1, tail
done:
	MOVD R3, events+24(FP)
	MOVD R4, nonspace+32(FP)
	RET

TEXT ·pathMasks(SB), NOSPLIT, $0-40
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD $0, R3
	MOVD $0, R4
	MOVD $0, R5
	MOVD $0, R6
	CBZ R1, done
	CMP $16, R1
	BLO tail
	INIT_WHITESPACE
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	VMOVI $125, V24.B16
	VMOVI $93, V26.B16
	VMOVI $44, V27.B16
loop:
	CMP $16, R1
	BLO tail
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VORR V3.B16, V2.B16, V2.B16
	CLASSIFY_WHITESPACE(V0.B16, V4.B16)
	VCMEQ V24.B16, V0.B16, V3.B16
	VCMEQ V26.B16, V0.B16, V5.B16
	VCMEQ V27.B16, V0.B16, V6.B16
	VORR V5.B16, V3.B16, V3.B16
	VORR V6.B16, V3.B16, V3.B16
	VAND V30.B16, V2.B16, V2.B16
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V3.B16, V3.B16
	VADDP V4.B16, V2.B16, V2.B16
	VADDP V3.B16, V3.B16, V3.B16
	VADDP V3.B16, V2.B16, V2.B16
	VADDP V2.B16, V2.B16, V2.B16
	VMOV V2.H[0], R9
	VMOV V2.H[1], R10
	VMOV V2.H[2], R11
	ORR R10, R11, R11
	EOR $65535, R10, R10
	LSL R6, R9, R9
	ORR R9, R3, R3
	LSL R6, R10, R10
	ORR R10, R4, R4
	LSL R6, R11, R11
	ORR R11, R5, R5
advance:
	ADD $16, R0
	SUB $16, R1
	ADD $16, R6
	CBNZ R1, loop
	B done
tail:
	MOVD $1, R10
	LSL R6, R10, R10
	MOVBU (R0), R8
	CMP $34, R8
	BEQ set_event
	CMP $92, R8
	BEQ set_event
	B classify_space
set_event:
	ORR R10, R3, R3
classify_space:
	CMP $32, R8
	BEQ is_space
	CMP $9, R8
	BEQ is_space
	CMP $10, R8
	BEQ is_space
	CMP $13, R8
	BEQ is_space
	ORR R10, R4, R4
	CMP $125, R8
	BEQ is_space
	CMP $93, R8
	BEQ is_space
	CMP $44, R8
	BEQ is_space
	B tail_advance
is_space:
	ORR R10, R5, R5
tail_advance:
	ADD $1, R0
	SUB $1, R1
	ADD $1, R6
	CBNZ R1, tail
done:
	MOVD R3, events+16(FP)
	MOVD R4, nonspace+24(FP)
	MOVD R5, boundaries+32(FP)
	RET

// Lookup and packing tables are loaded together instead of synthesizing three
// 64-bit constants with MOV/MOVK on every mask call.
DATA ·whitespaceTables+0(SB)/8, $0x0000000000000020
DATA ·whitespaceTables+8(SB)/8, $0x00000d00000a0900
DATA ·whitespaceTables+16(SB)/8, $0x8040201008040201
DATA ·whitespaceTables+24(SB)/8, $0x8040201008040201
DATA ·whitespaceTables+32(SB)/8, $0x0b030a0209010800
DATA ·whitespaceTables+40(SB)/8, $0x0f070e060d050c04
GLOBL ·whitespaceTables(SB), RODATA|NOPTR, $48
