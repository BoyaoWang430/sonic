//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// Strict string scanning is one complete operation per ABI0 call. NEON finds
// quote, backslash and control events; the same invocation consumes escapes
// and advances across blocks. No input padding or mutable scratch is needed.
TEXT ·scanStringStrict(SB), NOSPLIT, $0-48
// Shared strict scanner, included once per result shape. R0/R1 are the current
// block and remaining input, R14 is the original length, R15 the block offset,
// R16 the first escape, R19 the block width, R3 its unconsumed event mask.
// V2 retains raw quote/backslash lanes for the optional final-block suffix.
// R18, g, FP and LR are preserved; no calls, stack scratch or input writes.
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD R1, R14
	MOVD $0, R15
	MOVD $-1, R16
	CBZ R1, eof
	CMP $16, R1
	BLO tail
	MOVD $0x8040201008040201, R9
	VMOV R9, V30.D2
	MOVD $0x0b030a0209010800, R9
	VMOV R9, V31.D[0]
	MOVD $0x0f070e060d050c04, R9
	VMOV R9, V31.D[1]
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	VMOVI $0, V28.B16
scan:
	MOVD $16, R19
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VORR V3.B16, V2.B16, V2.B16
	VUSHR $5, V0.B16, V3.B16
	VCMEQ V28.B16, V3.B16, V3.B16
	VORR V2.B16, V3.B16, V3.B16
	VAND V30.B16, V3.B16, V3.B16
	VTBL V31.B16, [V3.B16], V3.B16
	VADDV V3.H8, V3
	VMOV V3.H[0], R3
	CBZ R3, advance
event:
	RBIT R3, R6
	CLZ R6, R6
	ADD R6, R0, R8
	MOVBU (R8), R10
	CMP $34, R10
	BEQ found
	CMP $92, R10
	BNE invalid
escape:
	CMP $0, R16
	BGE escape_seen
	ADD R15, R6, R16
escape_seen:
	SUB R6, R1, R12
	CMP $2, R12
	BLO eof
	MOVBU 1(R8), R10
	MOVD $2, R11
	CMP $34, R10
	BEQ consume
	CMP $92, R10
	BEQ consume
	CMP $47, R10
	BEQ consume
	CMP $117, R10
	BEQ unicode
	// The remaining simple escapes are b, f, n, r and t. The bounded
	// bitmap spells that fixed ASCII set without a five-way branch chain.
	SUB $98, R10, R9
	CMP $18, R9
	BHI invalid
	MOVD $0x51011, R10
	LSR R9, R10, R10
	TBZ $0, R10, invalid
	B consume
unicode:
	// A truncated Unicode escape reports EOF before checking available hex.
	// Invalid complete escapes report their backslash, not the hex position.
	CMP $6, R12
	BLO eof
	MOVD $6, R11
	ADD $2, R8
	MOVD $4, R12
hex:
	MOVBU (R8), R9
	SUB $48, R9, R10
	CMP $9, R10
	BLS hex_valid
	ORR $32, R9, R9
	SUB $97, R9, R9
	CMP $5, R9
	BHI invalid
hex_valid:
	ADD $1, R8
	SUB $1, R12
	CBNZ R12, hex
consume:
	ADD R11, R6, R7
	CMP R19, R7
	BHS crossing_escape
	MOVD $-1, R9
	LSL R7, R9, R9
	AND R9, R3, R3
	CBNZ R3, event
	B advance
crossing_escape:
	// Consume a boundary-crossing escape once, then resume at its end.
	MOVD R7, R19
advance:
	ADD R19, R0
	ADD R19, R15
	SUB R19, R1
	CBZ R1, eof
	CMP $16, R1
	BHS scan
tail:
	// A normal short scan needs no suffix mask or vector setup. Once fewer
	// than 16 bytes remain, plain bytes stay in this compact scalar loop.
	MOVBU (R0), R10
	CMP $34, R10
	BEQ scalar_quote
	CMP $92, R10
	BEQ scalar_escape
	CMP $32, R10
	BLO scalar_invalid
	ADD $1, R0
	ADD $1, R15
	SUBS $1, R1, R1
	BNE tail
	B eof
scalar_escape:
	MOVD $1, R19
	MOVD $0, R6
	MOVD R0, R8
	B escape
scalar_invalid:
	MOVD $0, R6
	B invalid
scalar_quote:
	MOVD $0, R6
found:
	ADD R6, R15, R3
	ADD $1, R3
	MOVD R3, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD $-1, R9
	MOVD R9, errorPos+32(FP)
	MOVD ZR, err+40(FP)
	RET
invalid:
	ADD R6, R15, R3
	MOVD $2, R9
	B failure
eof:
	MOVD R14, R3
	MOVD $1, R9
failure:
	MOVD R3, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD R3, errorPos+32(FP)
	MOVD R9, err+40(FP)
	RET

// The block entry additionally exports raw events after the closing quote.
TEXT ·scanStringStrictBlock(SB), NOSPLIT, $0-64
// Shared strict scanner, included once per result shape. R0/R1 are the current
// block and remaining input, R14 is the original length, R15 the block offset,
// R16 the first escape, R19 the block width, R3 its unconsumed event mask.
// V2 retains raw quote/backslash lanes for the optional final-block suffix.
// R18, g, FP and LR are preserved; no calls, stack scratch or input writes.
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD R1, R14
	MOVD $0, R15
	MOVD $-1, R16
	CBZ R1, eof
	CMP $16, R1
	BLO tail
	MOVD $0x8040201008040201, R9
	VMOV R9, V30.D2
	MOVD $0x0b030a0209010800, R9
	VMOV R9, V31.D[0]
	MOVD $0x0f070e060d050c04, R9
	VMOV R9, V31.D[1]
	VMOVI $34, V22.B16
	VMOVI $92, V23.B16
	VMOVI $0, V28.B16
scan:
	MOVD $16, R19
	VLD1 (R0), [V0.B16]
	VCMEQ V22.B16, V0.B16, V2.B16
	VCMEQ V23.B16, V0.B16, V3.B16
	VORR V3.B16, V2.B16, V2.B16
	VUSHR $5, V0.B16, V3.B16
	VCMEQ V28.B16, V3.B16, V3.B16
	VORR V2.B16, V3.B16, V3.B16
	VAND V30.B16, V3.B16, V3.B16
	VTBL V31.B16, [V3.B16], V3.B16
	VADDV V3.H8, V3
	VMOV V3.H[0], R3
	CBZ R3, advance
event:
	RBIT R3, R6
	CLZ R6, R6
	ADD R6, R0, R8
	MOVBU (R8), R10
	CMP $34, R10
	BEQ found
	CMP $92, R10
	BNE invalid
escape:
	CMP $0, R16
	BGE escape_seen
	ADD R15, R6, R16
escape_seen:
	SUB R6, R1, R12
	CMP $2, R12
	BLO eof
	MOVBU 1(R8), R10
	MOVD $2, R11
	CMP $34, R10
	BEQ consume
	CMP $92, R10
	BEQ consume
	CMP $47, R10
	BEQ consume
	CMP $117, R10
	BEQ unicode
	// The remaining simple escapes are b, f, n, r and t. The bounded
	// bitmap spells that fixed ASCII set without a five-way branch chain.
	SUB $98, R10, R9
	CMP $18, R9
	BHI invalid
	MOVD $0x51011, R10
	LSR R9, R10, R10
	TBZ $0, R10, invalid
	B consume
unicode:
	// A truncated Unicode escape reports EOF before checking available hex.
	// Invalid complete escapes report their backslash, not the hex position.
	CMP $6, R12
	BLO eof
	MOVD $6, R11
	ADD $2, R8
	MOVD $4, R12
hex:
	MOVBU (R8), R9
	SUB $48, R9, R10
	CMP $9, R10
	BLS hex_valid
	ORR $32, R9, R9
	SUB $97, R9, R9
	CMP $5, R9
	BHI invalid
hex_valid:
	ADD $1, R8
	SUB $1, R12
	CBNZ R12, hex
consume:
	ADD R11, R6, R7
	CMP R19, R7
	BHS crossing_escape
	MOVD $-1, R9
	LSL R7, R9, R9
	AND R9, R3, R3
	CBNZ R3, event
	B advance
crossing_escape:
	// Consume a boundary-crossing escape once, then resume at its end.
	MOVD R7, R19
advance:
	ADD R19, R0
	ADD R19, R15
	SUB R19, R1
	CBZ R1, eof
	CMP $16, R1
	BHS scan
tail:
	// Classify the complete bounded short tail before consuming events, so
	// callers can still reuse its suffix even when the input is below 16 B.
	MOVD R1, R19
	MOVD $0, R3
	MOVD $0, R4
	MOVD $0, R6
	MOVD $1, R7
scalar_classify:
	MOVBU (R0)(R6), R9
	CMP $34, R9
	BEQ raw_event
	CMP $92, R9
	BEQ raw_event
	CMP $32, R9
	BHS scalar_next
	ORR R7, R3, R3
	B scalar_next
raw_event:
	ORR R7, R4, R4
	ORR R7, R3, R3
scalar_next:
	ADD $1, R6
	LSL $1, R7, R7
	CMP R19, R6
	BLO scalar_classify
	CBNZ R3, event
	B advance
found:
	CMP $16, R19
	BLO suffix
	VAND V30.B16, V2.B16, V2.B16
	VTBL V31.B16, [V2.B16], V2.B16
	VADDV V2.H8, V2
	VMOV V2.H[0], R4
suffix:
	ADD $1, R6, R9
	SUB R9, R19, R19
	LSR R9, R4, R4
	MOVD R4, tailEvents+48(FP)
	MOVD R19, tailWidth+56(FP)
	ADD R6, R15, R3
	ADD $1, R3
	MOVD R3, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD $-1, R9
	MOVD R9, errorPos+32(FP)
	MOVD ZR, err+40(FP)
	RET
invalid:
	ADD R6, R15, R3
	MOVD $2, R9
	B failure
eof:
	MOVD R14, R3
	MOVD $1, R9
failure:
	MOVD ZR, tailEvents+48(FP)
	MOVD ZR, tailWidth+56(FP)
	MOVD R3, end+16(FP)
	MOVD R16, escape+24(FP)
	MOVD R3, errorPos+32(FP)
	MOVD R9, err+40(FP)
	RET
