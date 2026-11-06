//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)
// Complete HTML escaping in one ABI0 call, with ordinary frame metadata/RET.
// g (R28), SP, and RA are never used as scratch registers.
TEXT ·htmlEscape(SB), $0-48
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD dst+16(FP), R2
	MOVD capacity+24(FP), R3
	MOVD R1, R4
	MOVD R3, R5
	MOVD $60, R6
	MOVD $38, R8
	MOVD $226, R9

	VMOV R6, V1.B16
	MOVD $62, R22
	VMOV R22, V2.B16
	VMOV R8, V3.B16
	VMOV R9, V4.B16

scan:
	CMP $32, R1
	BLO scan_16
	CMP $32, R3
	BLO scan_16
	FLDPQ (R0), (F0, F7)
	VCMEQ V1.B16, V0.B16, V5.B16
	VCMEQ V2.B16, V0.B16, V6.B16
	VORR V6.B16, V5.B16, V5.B16
	VCMEQ V3.B16, V0.B16, V6.B16
	VORR V6.B16, V5.B16, V5.B16
	VCMEQ V4.B16, V0.B16, V6.B16
	VORR V6.B16, V5.B16, V5.B16
	VCMEQ V1.B16, V7.B16, V8.B16
	VCMEQ V2.B16, V7.B16, V6.B16
	VORR V6.B16, V8.B16, V8.B16
	VCMEQ V3.B16, V7.B16, V6.B16
	VORR V6.B16, V8.B16, V8.B16
	VCMEQ V4.B16, V7.B16, V6.B16
	VORR V6.B16, V8.B16, V8.B16
	VORR V8.B16, V5.B16, V6.B16
	VMOV V6.D[0], R23
	VMOV V6.D[1], R22
	ORR R22, R23, R20
	CBNZ R20, neon32_event
	FSTPQ (F0, F7), (R2)
	ADD $32, R0
	ADD $32, R2
	SUB $32, R1
	SUB $32, R3
	B scan
neon32_event:
	VMOV V5.D[0], R23
	VMOV V5.D[1], R22
	ORR R22, R23, R20
	CBNZ R20, neon_event
	VST1 [V0.B16], (R2)
	ADD $16, R0
	ADD $16, R2
	SUB $16, R1
	SUB $16, R3
	VMOV V7.B16, V0.B16
	VMOV V8.D[0], R23
	VMOV V8.D[1], R22
	B neon_event
scan_16:
	CMP $16, R1
	BLO scan_scalar
	CMP $16, R3
	BLO scan_scalar
	VLD1 (R0), [V0.B16]
	VCMEQ V1.B16, V0.B16, V5.B16
	VCMEQ V2.B16, V0.B16, V6.B16
	VORR V6.B16, V5.B16, V5.B16
	VCMEQ V3.B16, V0.B16, V6.B16
	VORR V6.B16, V5.B16, V5.B16
	VCMEQ V4.B16, V0.B16, V6.B16
	VORR V6.B16, V5.B16, V5.B16
	VMOV V5.D[0], R23
	VMOV V5.D[1], R22
	ORR R22, R23, R20
	CBNZ R20, neon_event
	VST1 [V0.B16], (R2)
	ADD $16, R0
	ADD $16, R2
	SUB $16, R1
	SUB $16, R3
	B scan
neon_event:
	MOVD $0, R20
	CBNZ R23, neon_first
	MOVD R22, R23
	MOVD $8, R20
neon_first:
	RBIT R23, R23
	CLZ R23, R23
	ADD R23>>3, R20, R20
	CBZ R20, classify
neon_prefix:
	MOVD R2, R22
	VMOV V0.D[0], R24
	TBZ $3, R20, neon_prefix_4
	MOVD R24, (R22)
	ADD $8, R22
	VMOV V0.D[1], R24
neon_prefix_4:
	TBZ $2, R20, neon_prefix_2
	MOVW R24, (R22)
	LSR $32, R24, R24
	ADD $4, R22
neon_prefix_2:
	TBZ $1, R20, neon_prefix_1
	MOVH R24, (R22)
	LSR $16, R24, R24
	ADD $2, R22
neon_prefix_1:
	TBZ $0, R20, neon_prefix_done
	MOVB R24, (R22)
neon_prefix_done:
	ADD R20, R0
	ADD R20, R2
	SUB R20, R1
	SUB R20, R3
	B classify
scan_scalar:
	CBZ R1, success
	CBZ R3, exhausted
	MOVBU (R0), R24
	MOVD $60, R22
	CMP R22, R24
	BEQ classify
	MOVD $62, R22
	CMP R22, R24
	BEQ classify
	CMP R8, R24
	BEQ classify
	CMP R9, R24
	BEQ classify
	MOVB R24, (R2)
	ADD $1, R0
	SUB $1, R1
	ADD $1, R2
	SUB $1, R3
	B scan

classify:
	MOVBU 0(R0), R24
	MOVD $1, R11 // input bytes consumed by this escape
	CMP R9, R24
	BNE ascii_escape
	MOVD $3, R22
	CMP R22, R1
	BLO plain_e2
	MOVBU 1(R0), R23
	MOVD $128, R22
	CMP R22, R23
	BNE plain_e2
	MOVBU 2(R0), R14
	MOVD $168, R22
	CMP R22, R14
	BEQ unicode_escape
	MOVD $169, R22
	CMP R22, R14
	BNE plain_e2
unicode_escape:
	MOVD $3, R11
	MOVD $50, R12 // \u2028 or \u2029
	MOVD $50, R13
	ADD $-112, R14
	B emit
ascii_escape:
	MOVD $48, R12 // \u003c, \u003e, or \u0026
	LSR $4, R24, R13
	ADD $48, R13
	AND $15, R24, R14
	MOVD $10, R22
	CMP R22, R14
	BLO hex_digit
	ADD $87, R14
	B emit
hex_digit:
	ADD $48, R14
emit:
	MOVD $6, R22
	CMP R22, R3
	BLO exhausted
	MOVD $92, R23
	MOVB R23, 0(R2)
	MOVD $117, R23
	MOVB R23, 1(R2)
	MOVB R12, 2(R2)
	MOVD $48, R23
	MOVB R23, 3(R2)
	MOVB R13, 4(R2)
	MOVB R14, 5(R2)
	ADD R11, R0
	SUB R11, R1
	ADD $6, R2
	SUB $6, R3
	B scan
plain_e2:
	// An incomplete or different UTF-8 sequence is copied byte for byte.
	// At least one output byte remains because this byte was inside VL.
	MOVB R24, 0(R2)
	ADD $1, R0
	SUB $1, R1
	ADD $1, R2
	SUB $1, R3
	B scan

exhausted:
	MOVD R4, R22
	SUB R1, R22
	MVN R22, R22
	B done
success:
	MOVD R4, R22
done:
	MOVD R5, R23
	SUB R3, R23
	MOVD R22, consumed+32(FP)
	MOVD R23, written+40(FP)
	RET
