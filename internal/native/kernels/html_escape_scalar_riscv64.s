//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// Exact candidates in X20: setting bit 1 maps both '<' and '>' to '>'.
// X23..X26 and X28 remain live across escapes; X5 is temporary, X6 the mask.
#define HTML_MASK \
	OR X24, X20, X6; \
	XOR X25, X6; \
	ORCB X6, X6; \
	XOR X26, X20, X5; \
	ORCB X5, X5; \
	AND X5, X6, X6; \
	XOR X28, X20, X5; \
	ORCB X5, X5; \
	AND X5, X6, X6; \
	ANDN X6, X23, X6

// func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)
// Complete HTML escaping in one ABI0 call, with ordinary frame metadata/RET.
// g (X27), SP, and RA are never used as scratch registers.
TEXT ·htmlEscape(SB), $0-48
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	dst+16(FP), X12
	MOV	capacity+24(FP), X13
	MOV	X11, X14
	MOV	X13, X15
	MOV	$60, X16
	MOV	$62, X17
	MOV	$38, X18
	MOV	$226, X19

	// Length and capacity only decrease. Small calls go straight to the
	// byte path; otherwise preserve these masks through every escape.
	BEQZ X11, success
	BEQZ X13, exhausted
	MOV $8, X5
	BLTU X11, X5, scan_byte
	BLTU X13, X5, scan_byte
	MOV $0x8080808080808080, X23
	MOV $0x0202020202020202, X24
	MOV $0x3e3e3e3e3e3e3e3e, X25
	MOV $0x2626262626262626, X26
	MOV $0xe2e2e2e2e2e2e2e2, X28

// Only wholly bounded words are loaded. AND the three non-match byte
// masks, then complement once to find exact escape candidates. The bytes
// before the first candidate are copied exactly once.
scan_word_load:
	MOV (X10), X20
	HTML_MASK
	MOV $8, X7
	BEQZ X6, copy_word
word_candidate:
	CTZ X6, X7
	SRL $3, X7, X7
	BEQZ X7, classify
	JMP copy_short

copy_word:
	// Enter the bulk loop only after a clean word, retaining its result.
	// Both input and output must contain the complete group.
	MOV $32, X5
	BLTU X11, X5, copy_single_word
	BLTU X13, X5, copy_single_word
	MOV X20, (X12)
	JMP scan_bulk_next
copy_single_word:
	MOV X20, (X12)
	JMP copy_advance

copy_short:
	MOV X12, X5
	AND $4, X7, X21
	BEQZ X21, copy_short_half
	MOVW X20, (X5)
	SRL $32, X20
	ADD $4, X5
copy_short_half:
	AND $2, X7, X21
	BEQZ X21, copy_short_byte
	MOVH X20, (X5)
	SRL $16, X20
	ADD $2, X5
copy_short_byte:
	AND $1, X7, X21
	BEQZ X21, copy_advance
	MOVB X20, (X5)

copy_advance:
	ADD X7, X10
	SUB X7, X11
	ADD X7, X12
	SUB X7, X13
	BEQZ X6, scan_word
	JMP classify

// Only reached after copying a complete ordinary word. Reading the last
// eight source bytes therefore stays in bounds, and src/dst never overlap.
// A clean tail can overwrite its identical ordinary prefix with one store.
// An event reuses the loaded suffix and its mask without rescanning bytes.
scan_tail:
	BEQZ X11, success
	BLTU X13, X11, scan
	SUB X11, X5, X29
	SUB X29, X10, X5
	MOV (X5), X20
	HTML_MASK
	BNEZ X6, tail_candidate
	ADD X11, X12
	MOV X20, -8(X12)
	SUB X11, X13
	JMP success

scan_byte:
	MOVBU (X10), X7
	BEQ X7, X16, classify
	BEQ X7, X17, classify
	BEQ X7, X18, classify
	BEQ X7, X19, classify
	MOVB X7, (X12)
	ADD $1, X10
	ADD $1, X12
	SUB $1, X11
	SUB $1, X13
	JMP scan

classify:
	MOVBU	0(X10), X7
classify_loaded:
	MOV	$1, X21                  // input bytes consumed by this escape
	BNE	X7, X19, ascii_escape
	MOV	$3, X5
	BLTU	X11, X5, plain_e2
	MOVBU	1(X10), X6
	MOV	$128, X5
	BNE	X6, X5, plain_e2
	MOVBU	2(X10), X29
	MOV	$168, X5
	BEQ	X29, X5, unicode_escape
	MOV	$169, X5
	BNE	X29, X5, plain_e2
unicode_escape:
	MOV	$3, X21
	MOV	$50, X22                 // \u2028 or \u2029
	MOV	$50, X7
	ADD	$-112, X29
	JMP	emit
ascii_escape:
	MOV	$48, X22                 // \u003c, \u003e, or \u0026
	AND	$15, X7, X29
	SRLI	$4, X7, X7
	ADD	$48, X7
	MOV	$10, X5
	BLTU	X29, X5, hex_digit
	ADD	$87, X29
	JMP	emit
hex_digit:
	ADD	$48, X29
emit:
	MOV	$6, X5
	BLTU	X13, X5, exhausted
	MOV	$92, X6
	MOVB	X6, 0(X12)
	MOV	$117, X6
	MOVB	X6, 1(X12)
	MOVB	X22, 2(X12)
	MOV	$48, X6
	MOVB	X6, 3(X12)
	MOVB	X7, 4(X12)
	MOVB	X29, 5(X12)
	ADD	X21, X10
	SUB	X21, X11
	ADD	$6, X12
	SUB	$6, X13
	JMP scan_after_escape
plain_e2:
	// An incomplete or different UTF-8 sequence is copied byte for byte.
	// The bounded scan guarantees at least one output byte remains.
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	JMP	scan

exhausted:
	MOV	X14, X5
	SUB	X11, X5
	XOR	$-1, X5
	JMP	done
success:
	MOV	X14, X5
done:
	MOV	X15, X6
	SUB	X13, X6
	MOV	X5, consumed+32(FP)
	MOV	X6, written+40(FP)
	RET

scan:
	BEQZ X11, success
	BEQZ X13, exhausted
	MOV $8, X5
	BLTU X11, X5, scan_byte
	BLTU X13, X5, scan_byte
	JMP scan_word_load

scan_word:
	MOV $8, X5
	BLTU X11, X5, scan_tail
	BLTU X13, X5, scan
	JMP scan_word_load

// Adjacent escapes need no word classification or alignment preparation.
scan_after_escape:
	BEQZ X11, success
	BEQZ X13, exhausted
	MOVBU 0(X10), X7
	BEQ X7, X16, classify_loaded
	BEQ X7, X17, classify_loaded
	BEQ X7, X18, classify_loaded
	BEQ X7, X19, classify_loaded
	JMP scan

// At least 32 input/output bytes were checked on entry.
// Copy clean words directly; retain the first candidate word for the exact
// scalar escape path. The preceding stores are committed by bulk_candidate.
	PCALIGN $64
scan_bulk:
	MOV 0(X10), X20
	HTML_MASK
	BNEZ X6, word_candidate
	MOV X20, 0(X12)
scan_bulk_next:
	MOV 8(X10), X20
	HTML_MASK
	BNEZ X6, bulk_candidate_8
	MOV X20, 8(X12)
	MOV 16(X10), X20
	HTML_MASK
	BNEZ X6, bulk_candidate_16
	MOV X20, 16(X12)
	MOV 24(X10), X20
	HTML_MASK
	BNEZ X6, bulk_candidate_24
	MOV X20, 24(X12)
	ADD $32, X10
	SUB $32, X11
	ADD $32, X12
	SUB $32, X13
	MOV $32, X5
	BLTU X11, X5, scan_word
	BGEU X13, X5, scan_bulk
	JMP scan_word

bulk_candidate_8:
	MOV $8, X7
	JMP bulk_candidate
bulk_candidate_16:
	MOV $16, X7
	JMP bulk_candidate
bulk_candidate_24:
	MOV $24, X7
bulk_candidate:
	ADD X7, X10
	SUB X7, X11
	ADD X7, X12
	SUB X7, X13
	JMP word_candidate

// A tail event reuses the bounded load without moving the hot byte/escape path.
tail_candidate:
	SLL $3, X29
	SRL X29, X20
	SRL X29, X6
	CTZ X6, X7
	SRL $3, X7
	BEQZ X7, classify
	JMP copy_short

#undef HTML_MASK
