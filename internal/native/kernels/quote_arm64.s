//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"

// func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)
// Normal Go ABI0 frame metadata and RET; no use of g (R28), SP, or RA as scratch.
// Bounded NEON blocks and word tails support every pointer alignment.
TEXT ·quote(SB), $0-56
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD dst+16(FP), R2
	MOVD capacity+24(FP), R3
	MOVD flags+32(FP), R4
	AND $1, R4
	MOVD R1, R5
	MOVD R3, R6
	MOVD $32, R7
	MOVD $34, R8
	MOVD $92, R9

	VMOV R8, V1.B16
	VMOV R9, V2.B16
	VEOR V3.B16, V3.B16, V3.B16

scan:
	// Match the existing ARM classifier: quote, slash, and controls.
	// Every 32-byte and 16-byte load and store is bounded by both logical lengths.
	CMP $32, R1
	BLO scan_16
	CMP $32, R3
	BLO scan_16
	FLDPQ (R0), (F0, F6)
	VCMEQ V1.B16, V0.B16, V4.B16
	VCMEQ V2.B16, V0.B16, V5.B16
	VORR V5.B16, V4.B16, V4.B16
	VUSHR $5, V0.B16, V5.B16
	VCMEQ V3.B16, V5.B16, V5.B16
	VORR V5.B16, V4.B16, V4.B16
	VCMEQ V1.B16, V6.B16, V7.B16
	VCMEQ V2.B16, V6.B16, V5.B16
	VORR V5.B16, V7.B16, V7.B16
	VUSHR $5, V6.B16, V5.B16
	VCMEQ V3.B16, V5.B16, V5.B16
	VORR V5.B16, V7.B16, V7.B16
	VORR V7.B16, V4.B16, V5.B16
	VMOV V5.D[0], R23
	VMOV V5.D[1], R22
	ORR R22, R23, R24
	CBNZ R24, neon32_event
	FSTPQ (F0, F6), (R2)
	ADD $32, R0
	ADD $32, R2
	SUB $32, R1
	SUB $32, R3
	B scan
neon32_event:
	VMOV V4.D[0], R23
	VMOV V4.D[1], R22
	ORR R22, R23, R24
	CBNZ R24, neon_event
	VST1 [V0.B16], (R2)
	ADD $16, R0
	ADD $16, R2
	SUB $16, R1
	SUB $16, R3
	VMOV V6.B16, V0.B16
	VMOV V7.D[0], R23
	VMOV V7.D[1], R22
	B neon_event
scan_16:
	CMP $16, R1
	BLO scan_scalar
	CMP $16, R3
	BLO scan_scalar
	VLD1 (R0), [V0.B16]
	VCMEQ V1.B16, V0.B16, V4.B16
	VCMEQ V2.B16, V0.B16, V5.B16
	VORR V5.B16, V4.B16, V4.B16
	VUSHR $5, V0.B16, V5.B16
	VCMEQ V3.B16, V5.B16, V5.B16
	VORR V5.B16, V4.B16, V4.B16
	VMOV V4.D[0], R23
	VMOV V4.D[1], R22
	ORR R22, R23, R24
	CBNZ R24, neon_event
	VST1 [V0.B16], (R2)
	ADD $16, R0
	ADD $16, R2
	SUB $16, R1
	SUB $16, R3
	B scan
neon_event:
	MOVD $0, R24
	CBNZ R23, neon_first
	MOVD R22, R23
	MOVD $8, R24
neon_first:
	RBIT R23, R23
	CLZ R23, R23
	ADD R23>>3, R24, R24
	CBZ R24, classify
neon_prefix:
	MOVD R2, R22
	VMOV V0.D[0], R10
	TBZ $3, R24, neon_prefix_4
	MOVD R10, (R22)
	ADD $8, R22
	VMOV V0.D[1], R10
neon_prefix_4:
	TBZ $2, R24, neon_prefix_2
	MOVW R10, (R22)
	LSR $32, R10, R10
	ADD $4, R22
neon_prefix_2:
	TBZ $1, R24, neon_prefix_1
	MOVH R10, (R22)
	LSR $16, R10, R10
	ADD $2, R22
neon_prefix_1:
	TBZ $0, R24, neon_prefix_done
	MOVB R10, (R22)
neon_prefix_done:
	ADD R24, R0
	ADD R24, R2
	SUB R24, R1
	SUB R24, R3
	B classify
scan_scalar:
	CBZ R1, success
	CBZ R3, exhausted
	MOVD $8, R22
	CMP R22, R1
	BLO scan_byte
	CMP R22, R3
	BLO scan_byte
	MOVD $0x0101010101010101, R12
	MOVD $0x8080808080808080, R13
	MOVD $0x2222222222222222, R14
	MOVD $0x5c5c5c5c5c5c5c5c, R15
	MOVD $0x2020202020202020, R16

// Each bounded word is loaded once. A subtraction mask may include a later
// borrow bit, but its first set byte is always the first escape candidate.
	// Keep the hot loop aligned without executing its padding.
	B scan_word
	PCALIGN $64
scan_word:
	MOVD $8, R22
	CMP R22, R1
	BLO scan_tail
	CMP R22, R3
	BLO scan
	MOVD (R0), R10
	EOR R14, R10, R22
	SUB R12, R22, R23
	BIC R22, R23, R23
	EOR R15, R10, R22
	SUB R12, R22, R24
	BIC R22, R24, R24
	ORR R24, R23, R23
	SUB R16, R10, R24
	BIC R10, R24, R24
	ORR R24, R23, R23
	AND R13, R23, R23
	MOVD $8, R24
	CBZ R23, copy_word
	RBIT R23, R24
	CLZ R24, R24
	LSR $3, R24, R24
	CBZ R24, classify
	B copy_short

copy_word:
	MOVD R10, (R2)
copy_advance:
	ADD R24, R0
	SUB R24, R1
	ADD R24, R2
	SUB R24, R3
	CBZ R23, scan_word
	B classify

// Only reached after copying a complete ordinary word. Reading the last
// eight source bytes therefore stays in bounds, and src/dst never overlap.
// A clean tail can overwrite its identical ordinary prefix with one store.
// An event reuses the loaded suffix and its mask without rescanning bytes.
scan_tail:
	CBZ R1, success
	CMP R1, R3
	BLO scan
	SUB R1, R22, R19
	SUB R19, R0, R22
	MOVD (R22), R10
	EOR R14, R10, R22
	SUB R12, R22, R23
	BIC R22, R23, R23
	EOR R15, R10, R22
	SUB R12, R22, R11
	BIC R22, R11, R11
	ORR R11, R23, R23
	SUB R16, R10, R11
	BIC R10, R11, R11
	ORR R11, R23, R23
	AND R13, R23, R23
	CBNZ R23, tail_candidate
	ADD R1, R2
	MOVD R10, -8(R2)
	SUB R1, R3
	B success

scan_byte:
	MOVBU (R0), R24
	CMP R7, R24
	BLO classify
	CMP R8, R24
	BEQ classify
	CMP R9, R24
	BEQ classify
	MOVB R24, (R2)
	ADD $1, R0
	ADD $1, R2
	SUB $1, R1
	SUB $1, R3
	B scan

classify:
	MOVBU 0(R0), R24
	CMP R7, R24
	BHS quote_or_backslash
	MOVD $9, R23
	CMP R23, R24
	BEQ tab
	MOVD $10, R23
	CMP R23, R24
	BEQ newline
	MOVD $13, R23
	CMP R23, R24
	BEQ carriage_return

	// Generic controls: \u00hh or \\u00hh, including backspace/form feed.
	ADD $6, R4, R11 // whole output length
	ADD $1, R4, R13 // leading backslash count
	MOVD $1, R14 // Unicode escape body
	B emit
quote_or_backslash:
	LSL $1, R4, R13
	ADD $2, R13, R11 // 2 bytes normally, 4 when doubled
	ADD $1, R13
	MOVD R24, R12
	MOVD $0, R14
	B emit
tab:
	MOVD $116, R12
	B short_control
newline:
	MOVD $110, R12
	B short_control
carriage_return:
	MOVD $114, R12
short_control:
	ADD $2, R4, R11
	ADD $1, R4, R13
	MOVD $0, R14
emit:
	// Reserve the complete escape before writing its first byte.
	CMP R11, R3
	BLO exhausted
	MOVD R2, R10
backslashes:
	MOVB R9, 0(R10)
	ADD $1, R10
	SUB $1, R13
	CBNZ R13, backslashes
	CBZ R14, short_body
	MOVD $117, R12
	MOVB R12, 0(R10)
	MOVD $48, R12
	MOVB R12, 1(R10)
	MOVB R12, 2(R10)
	LSR $4, R24, R12
	ADD $48, R12
	MOVB R12, 3(R10)
	AND $15, R24, R12
	MOVD $10, R13
	CMP R13, R12
	BLO hex_digit
	ADD $87, R12
	B hex_store
hex_digit:
	ADD $48, R12
hex_store:
	MOVB R12, 4(R10)
	B escaped
short_body:
	MOVB R12, 0(R10)
escaped:
	ADD $1, R0
	SUB $1, R1
	ADD R11, R2
	SUB R11, R3
	B scan

exhausted:
	MOVD R5, R22
	SUB R1, R22
	MVN R22, R22
	B done
success:
	MOVD R5, R22
done:
	MOVD R6, R23
	SUB R3, R23
	MOVD R22, consumed+40(FP)
	MOVD R23, written+48(FP)
	RET

// A tail event reuses the bounded load without moving the hot byte/escape path.
tail_candidate:
	LSL $3, R19
	LSR R19, R10
	LSR R19, R23
	RBIT R23, R24
	CLZ R24, R24
	LSR $3, R24
	CBZ R24, classify
	B copy_short

// Keep exact prefix stores off the clean-word loop.
copy_short:
	MOVD R2, R22
	AND $4, R24, R11
	CBZ R11, copy_short_half
	MOVW R10, (R22)
	LSR $32, R10
	ADD $4, R22
copy_short_half:
	AND $2, R24, R11
	CBZ R11, copy_short_byte
	MOVH R10, (R22)
	LSR $16, R10
	ADD $2, R22
copy_short_byte:
	AND $1, R24, R11
	CBZ R11, copy_advance
	MOVB R10, (R22)
	B copy_advance
