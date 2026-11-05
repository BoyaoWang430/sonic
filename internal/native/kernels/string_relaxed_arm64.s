//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// scanStringRelaxed keeps the complete scan in one ABI0 call. It uses the same
// packed-mask parity calculation as the RVV scanner, including odd backslash
// runs crossing a 64-byte block, while retaining NEON's exact bounded loads.
TEXT ·scanStringRelaxed(SB), NOSPLIT, $0-48
// Reduce both weighted comparison vectors together. The low four bytes
// become quote[0:8], quote[8:16], slash[0:8], slash[8:16]. Each group sums
// disjoint powers of two, so all three byte reductions are exact.
#define RELAXED_MASKS \
	VAND V30.B16, V2.B16, V2.B16; \
	VAND V30.B16, V3.B16, V3.B16; \
	VADDP V3.B16, V2.B16, V4.B16; \
	VADDP V4.B16, V4.B16, V4.B16; \
	VADDP V4.B16, V4.B16, V4.B16; \
	VMOV V4.H[0], R9; \
	VMOV V4.H[1], R10

// Shared relaxed scanner body, included once per ABI result shape.
// STRING_SCAN_BLOCK keeps raw quotes in R3 and filtered quotes in R26 so its
// final block can export the unconsumed events. The ordinary entry uses the
// same scan without exporting cached events.
#define RELAXED_QUOTES R3

	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD R1, R14
	MOVD $0, R15
	MOVD $-1, R16
	MOVD $0, R17
	CBZ R1, eof
	MOVD $0x8040201008040201, R9
	VMOV R9, V30.D2
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	MOVD $0x5555555555555555, R21
	MOVD $0xaaaaaaaaaaaaaaaa, R22
block:
	MOVD $64, R19
block_width:
	CMP R19, R1
	CSEL LO, R1, R19, R19
	CMP $64, R19
	BEQ block64
	CMP $32, R19
	BEQ block32
	MOVD R19, R20
	MOVD $0, R3
	MOVD $0, R4
	MOVD $0, R6
loop:
	CMP $16, R20
	BLO tail
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VORR V3.B16, V2.B16, V4.B16
	VADDV V4.B16, V4
	VMOV V4.B[0], R9
	CBZ R9, advance
	RELAXED_MASKS
	LSL R6, R9, R9
	ORR R9, R3, R3
	LSL R6, R10, R10
	ORR R10, R4, R4
advance:
	ADD $16, R0
	SUB $16, R20
	ADD $16, R6
	CBNZ R20, loop
	B classify
tail:
	// Full 32/64-byte blocks are multiples of 16, so this is the final input block.
	// With at least sixteen bytes in the input, its final vector is a
	// bounded overlapping load. Discard the previously classified prefix
	// before inserting its masks; escape carry is applied only once below.
	CMP $16, R14
	BLO scalar_tail
	ADD R20, R0, R11
	SUB $16, R11
	VLD1 (R11), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	RELAXED_MASKS
	MOVD $16, R11
	SUB R20, R11, R11
	LSR R11, R9, R9
	LSR R11, R10, R10
	LSL R6, R9, R9
	ORR R9, R3, R3
	LSL R6, R10, R10
	ORR R10, R4, R4
	B classify
scalar_tail:
	MOVD $1, R10
	LSL R6, R10, R10
	MOVBU (R0), R8
	CMP $34, R8
	BNE not_quote
	ORR R10, R3, R3
not_quote:
	CMP $92, R8
	BNE tail_advance
	ORR R10, R4, R4
tail_advance:
	ADD $1, R0
	SUB $1, R20
	ADD $1, R6
	CBNZ R20, scalar_tail
	B classify

// Complete chunks classify and reduce all lanes together. The same byte
// weights sum independently within each group of eight bytes; pairwise
// additions then pack the groups into consecutive mask bytes. Partial chunks
// above retain the bounded overlapping final load.
block32:
	VLD1 (R0), [V0.B16, V1.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VCMEQ V22.B16, V1.B16, V4.B16
	VCMEQ V23.B16, V1.B16, V5.B16
	VORR V3.B16, V2.B16, V6.B16
	VORR V5.B16, V4.B16, V7.B16
	VORR V7.B16, V6.B16, V6.B16
	VADDV V6.B16, V6
	VMOV V6.B[0], R9
	ADD $32, R0
	CBZ R9, plain_block
	VAND V30.B16, V2.B16, V2.B16
	VAND V30.B16, V3.B16, V3.B16
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V5.B16, V5.B16
	VADDP V4.B16, V2.B16, V6.B16
	VADDP V5.B16, V3.B16, V7.B16
	VADDP V7.B16, V6.B16, V6.B16
	VADDP V6.B16, V6.B16, V6.B16
	VMOV V6.S[0], R3
	VMOV V6.S[1], R4
	B classify
block64:
	VLD1 (R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	VCMEQ V22.B16, V0.B16, V4.B16
	VCMEQ V23.B16, V0.B16, V5.B16
	VCMEQ V22.B16, V1.B16, V6.B16
	VCMEQ V23.B16, V1.B16, V7.B16
	VCMEQ V22.B16, V2.B16, V8.B16
	VCMEQ V23.B16, V2.B16, V9.B16
	VCMEQ V22.B16, V3.B16, V10.B16
	VCMEQ V23.B16, V3.B16, V11.B16
	VORR V5.B16, V4.B16, V12.B16
	VORR V7.B16, V6.B16, V13.B16
	VORR V9.B16, V8.B16, V14.B16
	VORR V11.B16, V10.B16, V15.B16
	VORR V13.B16, V12.B16, V12.B16
	VORR V15.B16, V14.B16, V14.B16
	VORR V14.B16, V12.B16, V12.B16
	VADDV V12.B16, V12
	VMOV V12.B[0], R9
	ADD $64, R0
	CBZ R9, plain_block
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V5.B16, V5.B16
	VAND V30.B16, V6.B16, V6.B16
	VAND V30.B16, V7.B16, V7.B16
	VAND V30.B16, V8.B16, V8.B16
	VAND V30.B16, V9.B16, V9.B16
	VAND V30.B16, V10.B16, V10.B16
	VAND V30.B16, V11.B16, V11.B16
	VADDP V6.B16, V4.B16, V12.B16
	VADDP V10.B16, V8.B16, V13.B16
	VADDP V13.B16, V12.B16, V12.B16
	VADDP V12.B16, V12.B16, V12.B16
	VMOV V12.D[0], R3
	VADDP V7.B16, V5.B16, V12.B16
	VADDP V11.B16, V9.B16, V13.B16
	VADDP V13.B16, V12.B16, V12.B16
	VADDP V12.B16, V12.B16, V12.B16
	VMOV V12.D[0], R4
classify:
	BIC R17, R4, R23
	LSL $1, R23, R24
	ORR R17, R24, R24
	BIC R24, R23, R25
	AND R22, R25, R25
	ADD R23, R25, R25
	LSL $1, R25, R25
	EOR R21, R25, R25
	AND R24, R25, R25
	BIC R25, R3, RELAXED_QUOTES
	CMP $0, R16
	BGE quote
	CBZ R4, quote
	RBIT R4, R23
	CLZ R23, R23
	RBIT RELAXED_QUOTES, R24
	CLZ R24, R24
	CMP R24, R23
	BHS quote
	ADD R15, R23, R16
quote:
	CBNZ RELAXED_QUOTES, found
	BIC R25, R4, R17
	SUB $1, R19, R23
	LSR R23, R17, R17
	AND $1, R17, R17
	B next_block
plain_block:
	// A pending escape consumes this block's first ordinary byte; no new
	// slash remains to carry into the following block.
	MOVD $0, R17
next_block:
	ADD R19, R15, R15
	SUB R19, R1, R1
	CBNZ R1, block
eof:
	MOVD R14, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD R14, errorPos+32(FP)
	MOVD $1, R0
	MOVD R0, err+40(FP)
	RET
found:
	RBIT RELAXED_QUOTES, RELAXED_QUOTES
	CLZ RELAXED_QUOTES, RELAXED_QUOTES
	ADD R15, RELAXED_QUOTES, R3
	ADD $1, R3, R3
	MOVD R3, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD $-1, R0
	MOVD R0, errorPos+32(FP)
	MOVD ZR, err+40(FP)
	RET

#undef RELAXED_QUOTES
#undef RELAXED_MASKS

// Path search also reuses the already loaded suffix after the closing quote.
// Only this result shape exports raw tail events; the scan body stays shared.
TEXT ·scanStringRelaxedBlock(SB), NOSPLIT, $0-64
// Reduce both weighted comparison vectors together. The low four bytes
// become quote[0:8], quote[8:16], slash[0:8], slash[8:16]. Each group sums
// disjoint powers of two, so all three byte reductions are exact.
#define RELAXED_MASKS \
	VAND V30.B16, V2.B16, V2.B16; \
	VAND V30.B16, V3.B16, V3.B16; \
	VADDP V3.B16, V2.B16, V4.B16; \
	VADDP V4.B16, V4.B16, V4.B16; \
	VADDP V4.B16, V4.B16, V4.B16; \
	VMOV V4.H[0], R9; \
	VMOV V4.H[1], R10

// Shared relaxed scanner body, included once per ABI result shape.
// STRING_SCAN_BLOCK keeps raw quotes in R3 and filtered quotes in R26 so its
// final block can export the unconsumed events. The ordinary entry uses the
// same scan without exporting cached events.
#define RELAXED_QUOTES R26

	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD R1, R14
	MOVD $0, R15
	MOVD $-1, R16
	MOVD $0, R17
	CBZ R1, eof
	MOVD $0x8040201008040201, R9
	VMOV R9, V30.D2
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	MOVD $0x5555555555555555, R21
	MOVD $0xaaaaaaaaaaaaaaaa, R22
	// Start path scans with a smaller chunk; long continuations use the
	// common 64-byte loop without another first-chunk check.
	MOVD $32, R19
	B block_width
block:
	MOVD $64, R19
block_width:
	CMP R19, R1
	CSEL LO, R1, R19, R19
	CMP $64, R19
	BEQ block64
	CMP $32, R19
	BEQ block32
	MOVD R19, R20
	MOVD $0, R3
	MOVD $0, R4
	MOVD $0, R6
loop:
	CMP $16, R20
	BLO tail
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VORR V3.B16, V2.B16, V4.B16
	VADDV V4.B16, V4
	VMOV V4.B[0], R9
	CBZ R9, advance
	RELAXED_MASKS
	LSL R6, R9, R9
	ORR R9, R3, R3
	LSL R6, R10, R10
	ORR R10, R4, R4
advance:
	ADD $16, R0
	SUB $16, R20
	ADD $16, R6
	CBNZ R20, loop
	B classify
tail:
	// Full 32/64-byte blocks are multiples of 16, so this is the final input block.
	// With at least sixteen bytes in the input, its final vector is a
	// bounded overlapping load. Discard the previously classified prefix
	// before inserting its masks; escape carry is applied only once below.
	CMP $16, R14
	BLO scalar_tail
	ADD R20, R0, R11
	SUB $16, R11
	VLD1 (R11), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	RELAXED_MASKS
	MOVD $16, R11
	SUB R20, R11, R11
	LSR R11, R9, R9
	LSR R11, R10, R10
	LSL R6, R9, R9
	ORR R9, R3, R3
	LSL R6, R10, R10
	ORR R10, R4, R4
	B classify
scalar_tail:
	MOVD $1, R10
	LSL R6, R10, R10
	MOVBU (R0), R8
	CMP $34, R8
	BNE not_quote
	ORR R10, R3, R3
not_quote:
	CMP $92, R8
	BNE tail_advance
	ORR R10, R4, R4
tail_advance:
	ADD $1, R0
	SUB $1, R20
	ADD $1, R6
	CBNZ R20, scalar_tail
	B classify

// Complete chunks classify and reduce all lanes together. The same byte
// weights sum independently within each group of eight bytes; pairwise
// additions then pack the groups into consecutive mask bytes. Partial chunks
// above retain the bounded overlapping final load.
block32:
	VLD1 (R0), [V0.B16, V1.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VCMEQ V22.B16, V1.B16, V4.B16
	VCMEQ V23.B16, V1.B16, V5.B16
	ADD $32, R0
	VAND V30.B16, V2.B16, V2.B16
	VAND V30.B16, V3.B16, V3.B16
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V5.B16, V5.B16
	VADDP V4.B16, V2.B16, V6.B16
	VADDP V5.B16, V3.B16, V7.B16
	VADDP V7.B16, V6.B16, V6.B16
	VADDP V6.B16, V6.B16, V6.B16
	VMOV V6.S[0], R3
	VMOV V6.S[1], R4
	// Path callers already found an escape or scanned a plain prefix.
	// Packing this first block directly avoids a separate vector reduction.
	ORR R4, R3, R9
	CBZ R9, plain_block
	B classify
block64:
	VLD1 (R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	VCMEQ V22.B16, V0.B16, V4.B16
	VCMEQ V23.B16, V0.B16, V5.B16
	VCMEQ V22.B16, V1.B16, V6.B16
	VCMEQ V23.B16, V1.B16, V7.B16
	VCMEQ V22.B16, V2.B16, V8.B16
	VCMEQ V23.B16, V2.B16, V9.B16
	VCMEQ V22.B16, V3.B16, V10.B16
	VCMEQ V23.B16, V3.B16, V11.B16
	VORR V5.B16, V4.B16, V12.B16
	VORR V7.B16, V6.B16, V13.B16
	VORR V9.B16, V8.B16, V14.B16
	VORR V11.B16, V10.B16, V15.B16
	VORR V13.B16, V12.B16, V12.B16
	VORR V15.B16, V14.B16, V14.B16
	VORR V14.B16, V12.B16, V12.B16
	VADDV V12.B16, V12
	VMOV V12.B[0], R9
	ADD $64, R0
	CBZ R9, plain_block
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V5.B16, V5.B16
	VAND V30.B16, V6.B16, V6.B16
	VAND V30.B16, V7.B16, V7.B16
	VAND V30.B16, V8.B16, V8.B16
	VAND V30.B16, V9.B16, V9.B16
	VAND V30.B16, V10.B16, V10.B16
	VAND V30.B16, V11.B16, V11.B16
	VADDP V6.B16, V4.B16, V12.B16
	VADDP V10.B16, V8.B16, V13.B16
	VADDP V13.B16, V12.B16, V12.B16
	VADDP V12.B16, V12.B16, V12.B16
	VMOV V12.D[0], R3
	VADDP V7.B16, V5.B16, V12.B16
	VADDP V11.B16, V9.B16, V13.B16
	VADDP V13.B16, V12.B16, V12.B16
	VADDP V12.B16, V12.B16, V12.B16
	VMOV V12.D[0], R4
classify:
	BIC R17, R4, R23
	LSL $1, R23, R24
	ORR R17, R24, R24
	BIC R24, R23, R25
	AND R22, R25, R25
	ADD R23, R25, R25
	LSL $1, R25, R25
	EOR R21, R25, R25
	AND R24, R25, R25
	BIC R25, R3, RELAXED_QUOTES
	CMP $0, R16
	BGE quote
	CBZ R4, quote
	RBIT R4, R23
	CLZ R23, R23
	RBIT RELAXED_QUOTES, R24
	CLZ R24, R24
	CMP R24, R23
	BHS quote
	ADD R15, R23, R16
quote:
	CBNZ RELAXED_QUOTES, found
	BIC R25, R4, R17
	SUB $1, R19, R23
	LSR R23, R17, R17
	AND $1, R17, R17
	B next_block
plain_block:
	// A pending escape consumes this block's first ordinary byte; no new
	// slash remains to carry into the following block.
	MOVD $0, R17
next_block:
	ADD R19, R15, R15
	SUB R19, R1, R1
	CBNZ R1, block
eof:
	MOVD ZR, tailEvents+48(FP)
	MOVD ZR, tailWidth+56(FP)
	MOVD R14, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD R14, errorPos+32(FP)
	MOVD $1, R0
	MOVD R0, err+40(FP)
	RET
found:
	RBIT RELAXED_QUOTES, RELAXED_QUOTES
	CLZ RELAXED_QUOTES, RELAXED_QUOTES
	// Keep raw events, including escaped quotes, after this string's quote.
	// Variable ARM shifts wrap at 64; a block-final quote must export zero.
	ADD $1, RELAXED_QUOTES, R23
	SUB R23, R19, R19
	ORR R4, R3, R24
	LSR R23, R24, R24
	CMP $0, R19
	CSEL EQ, ZR, R24, R24
	MOVD R24, tailEvents+48(FP)
	MOVD R19, tailWidth+56(FP)
	ADD R15, RELAXED_QUOTES, R3
	ADD $1, R3, R3
	MOVD R3, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD $-1, R0
	MOVD R0, errorPos+32(FP)
	MOVD ZR, err+40(FP)
	RET

#undef RELAXED_QUOTES
#undef RELAXED_MASKS
