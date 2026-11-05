//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// Convert sixteen all-zero/all-one byte lanes to the corresponding low 16 bits.
#define CPARITY_PACK(reg, output) \
	VAND V30.B16, reg.B16, reg.B16; \
	VTBL V31.B16, [reg.B16], reg.B16; \
	VADDV reg.H8, reg; \
	VMOV reg.H[0], output

DATA cParityMaskTables<>+0(SB)/8, $0x8040201008040201
DATA cParityMaskTables<>+8(SB)/8, $0x8040201008040201
DATA cParityMaskTables<>+16(SB)/8, $0x0b030a0209010800
DATA cParityMaskTables<>+24(SB)/8, $0x0f070e060d050c04
GLOBL cParityMaskTables<>(SB), RODATA|NOPTR, $32

DATA cParityWhitespaceTables<>+0(SB)/8, $0x0000000000000020
DATA cParityWhitespaceTables<>+8(SB)/8, $0x00000d00000a0900
DATA cParityWhitespaceTables<>+16(SB)/8, $0x8040201008040201
DATA cParityWhitespaceTables<>+24(SB)/8, $0x8040201008040201
DATA cParityWhitespaceTables<>+32(SB)/8, $0x0b030a0209010800
DATA cParityWhitespaceTables<>+40(SB)/8, $0x0f070e060d050c04
GLOBL cParityWhitespaceTables<>(SB), RODATA|NOPTR, $48

// Preserve index bit 7: high bytes index outside the table and cannot equal the
// resulting zero. NUL indexes the space entry, so it also remains nonspace.
#define CPARITY_SPACE(input, output) \
	VAND V25.B16, input, output; \
	VTBL output, [V29.B16], output; \
	VCMEQ input, output, output

// C get_nonspace_bits always reads a complete 64-byte padded block. There is no
// size parameter, scalar tail, or logical-boundary check in this leaf.
TEXT ·cParityNonspace64(SB), NOSPLIT, $0-16
	MOVD src+0(FP), R0
	MOVD $cParityWhitespaceTables<>(SB), R9
	VLD1 (R9), [V29.B16, V30.B16, V31.B16]
	VMOVI $143, V25.B16
	FLDPQ (R0), (F0, F1)
	FLDPQ 32(R0), (F2, F3)
	CPARITY_SPACE(V0.B16, V4.B16)
	CPARITY_SPACE(V1.B16, V5.B16)
	CPARITY_SPACE(V2.B16, V6.B16)
	CPARITY_SPACE(V3.B16, V7.B16)
	CPARITY_PACK(V4, R9)
	CPARITY_PACK(V5, R10)
	CPARITY_PACK(V6, R11)
	CPARITY_PACK(V7, R12)
	ORR R10<<16, R9, R9
	ORR R11<<32, R9, R9
	ORR R12<<48, R9, R9
	MVN R9, R9
	MOVD R9, mask+8(FP)
	RET

// C string_block_new: always classify backslashes and quotes; optionally add
// raw bytes <= 31. All 32 input bytes are readable because the caller is padded.
TEXT ·cParityStringMasks32(SB), NOSPLIT, $0-28
	MOVD src+0(FP), R0
	MOVD opts+8(FP), R1
	MOVD $cParityMaskTables<>(SB), R9
	VLD1 (R9), [V30.B16, V31.B16]
	FLDPQ (R0), (F0, F1)
	VMOVI $92, V20.B16
	VMOVI $34, V21.B16
	VCMEQ V20.B16, V0.B16, V4.B16
	VCMEQ V20.B16, V1.B16, V5.B16
	VCMEQ V21.B16, V0.B16, V6.B16
	VCMEQ V21.B16, V1.B16, V7.B16
	CPARITY_PACK(V4, R2)
	CPARITY_PACK(V5, R9)
	ORR R9<<16, R2, R2
	CPARITY_PACK(V6, R3)
	CPARITY_PACK(V7, R9)
	ORR R9<<16, R3, R3
	MOVD $0, R4
	TBZ $5, R1, string_masks_done
	VEOR V20.B16, V20.B16, V20.B16
	VUSHR $5, V0.B16, V8.B16
	VUSHR $5, V1.B16, V9.B16
	VCMEQ V20.B16, V8.B16, V8.B16
	VCMEQ V20.B16, V9.B16, V9.B16
	CPARITY_PACK(V8, R4)
	CPARITY_PACK(V9, R9)
	ORR R9<<16, R4, R4
string_masks_done:
	MOVW R2, bs+16(FP)
	MOVW R3, quote+20(FP)
	MOVW R4, control+24(FP)
	RET

// Snapshot both input vectors before writing either output vector. This matches
// C's v256 value semantics even when output trails input by fewer than 32 bytes.
TEXT ·cParityMove32(SB), NOSPLIT, $0-16
	MOVD dst+0(FP), R0
	MOVD src+8(FP), R1
	FLDPQ (R1), (F0, F1)
	FSTPQ (F0, F1), (R0)
	RET

// The raw-number C scanner asks for four masks from one sixteen-byte block.
// Digit subtraction plus unsigned min rejects all bytes outside ASCII 0..9,
// including high bytes (the same outcome as the C signed SSE comparisons).
TEXT ·cParityNumberMasks16(SB), NOSPLIT, $0-24
	MOVD src+0(FP), R0
	MOVD $cParityMaskTables<>(SB), R9
	VLD1 (R9), [V30.B16, V31.B16]
	VLD1 (R0), [V0.B16]
	VMOVI $46, V20.B16
	VCMEQ V20.B16, V0.B16, V1.B16
	VMOVI $101, V20.B16
	VCMEQ V20.B16, V0.B16, V2.B16
	VMOVI $69, V20.B16
	VCMEQ V20.B16, V0.B16, V5.B16
	VORR V5.B16, V2.B16, V2.B16
	VMOVI $43, V20.B16
	VCMEQ V20.B16, V0.B16, V3.B16
	VMOVI $45, V20.B16
	VCMEQ V20.B16, V0.B16, V5.B16
	VORR V5.B16, V3.B16, V3.B16
	VMOVI $48, V20.B16
	VMOVI $9, V21.B16
	VSUB V20.B16, V0.B16, V4.B16
	VUMIN V21.B16, V4.B16, V5.B16
	VCMEQ V5.B16, V4.B16, V4.B16
	VORR V1.B16, V4.B16, V4.B16
	VORR V2.B16, V4.B16, V4.B16
	VORR V3.B16, V4.B16, V4.B16
	CPARITY_PACK(V1, R1)
	CPARITY_PACK(V2, R2)
	CPARITY_PACK(V3, R3)
	CPARITY_PACK(V4, R4)
	MOVW R1, decimal+8(FP)
	MOVW R2, exponent+12(FP)
	MOVW R3, sign+16(FP)
	MOVW R4, valid+20(FP)
	RET
