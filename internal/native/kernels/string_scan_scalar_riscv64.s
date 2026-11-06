//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// The four entries match the shared Go declarations in scanning_native.go.
// Bounded unaligned words yield exact quote/escape/control masks. Consume every
// event before loading another word. Overlapping loads cover >= 4B tails.
// FP (X8), g (X27), SP, RA, and the assembler scratch X31 are not used.

// func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringRelaxed(SB), NOSPLIT, $0-48
	PCALIGN $64
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV $0, X13                 // relative block offset
	MOV $-1, X14                // first backslash
	MOV X11, X15
	MOV $0x2222222222222222, X16
	MOV $0x5c5c5c5c5c5c5c5c, X17
	MOV $0x8080808080808080, X20

str_scan:
	BEQZ X11, str_eof
	MOV $8, X24
	BLTU X11, X24, str_tail
	MOV 0(X10), X21
str_classify:
	XOR X16, X21, X7
	ORCB X7, X7
	XOR X17, X21, X6
	ORCB X6, X6
	AND X6, X7, X5
	ANDN X5, X20, X22
str_event:
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL X23, X7, X25
	SRL $3, X23
	AND $1, X25
	BEQZ X25, str_quote
str_escape:
	BGEZ X14, str_escape_seen
	ADD X13, X23, X14
str_escape_seen:
	SUB X23, X11, X30
	MOV $2, X26
	BLTU X30, X26, str_eof
	BGEU X26, X24, str_relaxed_byte
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL X23, X7, X25
	SRL $3, X23
	AND $1, X25
	BEQZ X25, str_quote
	SUB X26, X23, X5
	MOV $2, X26
	BLTU X26, X5, str_escape
	SUB X23, X11, X30
	BLTU X30, X26, str_eof
str_relaxed_enter:
	MOV $92, X28
	MOV $34, X29
	ADD X23, X26, X5
	ADD X5, X10, X9
	SUB X5, X11, X30
str_relaxed_next:
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	SUB X30, X11, X5
	MOV X9, X10
	ADD X5, X13
	MOV X30, X11
	JMP str_scan
	PCALIGN $16
str_relaxed_escape:
	BLTU X30, X26, str_eof
	ADD $2, X9
	SUB $2, X30
	JMP str_relaxed_next
str_relaxed_quote:
	SUB X10, X9, X23
	BLTU X23, X24, str_quote
	ADD X23, X13
	MOV $0, X23
	MOV $1, X24
	JMP str_quote

str_relaxed_byte:
	BEQ X30, X26, str_consume
	MOVBU 2(X10), X5
	MOV $92, X6
	BEQ X5, X6, str_relaxed_enter
str_consume:
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	JMP str_event
str_crossing:
	MOV X26, X24
str_advance:
	ADD X24, X10
	ADD X24, X13
	SUB X24, X11
	JMP str_scan

str_byte:
	MOV $1, X24
	MOV $0, X23
	MOVBU 0(X10), X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BEQ X25, X5, str_escape
	JMP str_advance

str_quote:
	ADD X23, X13, X5
	ADD $1, X5
	MOV $-1, X6
	MOV $0, X7
	JMP str_done
str_eof:
	MOV X15, X5
	MOV X15, X6
	MOV $1, X7
str_done:
	MOV X5, end+16(FP)
	MOV X14, escape+24(FP)
	MOV X6, errorPos+32(FP)
	MOV X7, err+40(FP)
	RET

str_tail:
	MOV $1, X5
	BEQ X11, X5, str_byte
	BLTU X15, X24, str_tail_short
	ADD X11, X10, X5
	MOV -8(X5), X21
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X21
str_tail_fill:
	SLL $3, X11, X5
	MOV $-1, X6
	SLL X5, X6
	OR X6, X21
	MOV X11, X24
	JMP str_classify
str_tail_short:
	MOV $4, X5
	BLTU X11, X5, str_byte
	MOVWU 0(X10), X21
	ADD X11, X10, X5
	MOVWU -4(X5), X6
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X6
	SLL $32, X6
	OR X6, X21
	JMP str_tail_fill

// func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringStrict(SB), NOSPLIT, $0-48
	PCALIGN $64
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV $0, X13                 // relative block offset
	MOV $-1, X14                // first backslash
	MOV X11, X15
	MOV $0x2222222222222222, X16
	MOV $0x5c5c5c5c5c5c5c5c, X17
	MOV $0xe0e0e0e0e0e0e0e0, X18
	MOV $0x8080808080808080, X20

str_scan:
	BEQZ X11, str_eof
	MOV $8, X24
	BLTU X11, X24, str_tail
	MOV 0(X10), X21
str_classify:
	XOR X16, X21, X7
	ORCB X7, X7
	XOR X17, X21, X6
	ORCB X6, X6
	AND X6, X7, X5
	AND X18, X21, X6
	ORCB X6, X6
	AND X6, X5
	ANDN X5, X20, X22
str_event:
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL $3, X23
	SLL $3, X23, X5
	SRL X5, X21, X25
	AND $255, X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BNE X25, X5, str_invalid
str_escape:
	BGEZ X14, str_escape_seen
	ADD X13, X23, X14
str_escape_seen:
	SUB X23, X11, X30
	MOV $2, X26
	BLTU X30, X26, str_eof
	ADD X23, X10, X28
	MOVBU 1(X28), X25
	MOV $34, X5
	BEQ X25, X5, str_consume
	MOV $92, X5
	BEQ X25, X5, str_consume
	ADD $-98, X25, X5
	MOV $19, X6
	BGEU X5, X6, str_escape_other
	MOV $0x00051011, X6
	SRL X5, X6
	AND $1, X6
	BNEZ X6, str_consume
	JMP str_invalid
str_escape_other:
	MOV $47, X5
	BEQ X25, X5, str_consume
	MOV $117, X5
	BNE X25, X5, str_invalid
	MOV $6, X26
	BLTU X30, X26, str_eof
	MOVWU 2(X28), X25
	MOV $0x7f7f7f7f, X5
	AND X25, X5, X5
	MOV $0x50505050, X6
	ADD X5, X6, X6
	MOV $0x46464646, X28
	ADD X5, X28, X28
	XOR X28, X6
	MOV $0x20202020, X28
	OR X28, X5
	MOV $0x1f1f1f1f, X28
	ADD X5, X28, X28
	MOV $0x19191919, X29
	ADD X5, X29, X29
	XOR X29, X28
	OR X28, X6
	ANDN X25, X6, X6
	SRL $32, X20, X5
	AND X5, X6
	BNE X6, X5, str_invalid
str_consume:
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL $3, X23
	SLL $3, X23, X5
	SRL X5, X21, X25
	AND $255, X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BNE X25, X5, str_invalid
	SUB X26, X23, X5
	MOV $2, X6
	BLTU X6, X5, str_escape_seen
	MOV $92, X28
	MOV $34, X29
	MOV $32, X12
	MOV $0x00051011, X19
	ADD X23, X10, X9
	SUB X23, X11, X30
str_strict_dense_escape:
	MOV $2, X26
	BLTU X30, X26, str_eof
	MOVBU 1(X9), X25
	BEQ X25, X29, str_strict_dense_step2
	BEQ X25, X28, str_strict_dense_step2
	ADD $-98, X25, X5
	MOV $19, X6
	BGEU X5, X6, str_strict_dense_other
	SRL X5, X19, X6
	AND $1, X6
	BNEZ X6, str_strict_dense_step2
	JMP str_strict_dense_invalid
str_strict_dense_other:
	MOV $47, X5
	BEQ X25, X5, str_strict_dense_step2
	MOV $117, X5
	BNE X25, X5, str_strict_dense_invalid
	MOV $6, X26
	BLTU X30, X26, str_eof
	MOVWU 2(X9), X25
	MOV $0x7f7f7f7f, X5
	AND X25, X5, X5
	MOV $0x50505050, X6
	ADD X5, X6, X6
	MOV $0x46464646, X7
	ADD X5, X7, X7
	XOR X7, X6
	MOV $0x20202020, X7
	OR X7, X5
	MOV $0x1f1f1f1f, X7
	ADD X5, X7, X7
	MOV $0x19191919, X23
	ADD X5, X23, X23
	XOR X23, X7
	OR X7, X6
	ANDN X25, X6, X6
	SRL $32, X20, X5
	AND X5, X6
	BNE X6, X5, str_strict_dense_invalid
	ADD $6, X9
	SUB $6, X30
	JMP str_strict_dense_next
	PCALIGN $16
str_strict_dense_step2:
	ADD $2, X9
	SUB $2, X30
str_strict_dense_next:
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_strict_dense_escape
	BEQ X25, X29, str_strict_dense_quote
	BLTU X25, X12, str_strict_dense_invalid
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_strict_dense_escape
	BEQ X25, X29, str_strict_dense_quote
	BLTU X25, X12, str_strict_dense_invalid
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_strict_dense_escape
	BEQ X25, X29, str_strict_dense_quote
	BLTU X25, X12, str_strict_dense_invalid
	ADD $1, X9
	SUB $1, X30
	SUB X30, X11, X5
	MOV X9, X10
	ADD X5, X13
	MOV X30, X11
	JMP str_scan
str_strict_dense_quote:
	SUB X10, X9, X23
	BLTU X23, X24, str_quote
	ADD X23, X13
	MOV $0, X23
	MOV $1, X24
	JMP str_quote
str_strict_dense_invalid:
	SUB X10, X9, X23
	JMP str_invalid

str_crossing:
	MOV X26, X24
str_advance:
	ADD X24, X10
	ADD X24, X13
	SUB X24, X11
	JMP str_scan

str_byte:
	MOV $1, X24
	MOV $0, X23
	MOVBU 0(X10), X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BEQ X25, X5, str_escape
	MOV $32, X5
	BLTU X25, X5, str_invalid
	JMP str_advance

str_quote:
	ADD X23, X13, X5
	ADD $1, X5
	MOV $-1, X6
	MOV $0, X7
	JMP str_done
str_invalid:
	ADD X23, X13, X5
	MOV X5, X6
	MOV $2, X7
	JMP str_done
str_eof:
	MOV X15, X5
	MOV X15, X6
	MOV $1, X7
str_done:
	MOV X5, end+16(FP)
	MOV X14, escape+24(FP)
	MOV X6, errorPos+32(FP)
	MOV X7, err+40(FP)
	RET

str_tail:
	MOV $1, X5
	BEQ X11, X5, str_byte
	BLTU X15, X24, str_tail_short
	ADD X11, X10, X5
	MOV -8(X5), X21
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X21
str_tail_fill:
	SLL $3, X11, X5
	MOV $-1, X6
	SLL X5, X6
	OR X6, X21
	MOV X11, X24
	JMP str_classify
str_tail_short:
	MOV $4, X5
	BLTU X11, X5, str_byte
	MOVWU 0(X10), X21
	ADD X11, X10, X5
	MOVWU -4(X5), X6
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X6
	SLL $32, X6
	OR X6, X21
	JMP str_tail_fill

// func scanStringRelaxedBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringRelaxedBlock(SB), NOSPLIT, $0-64
	PCALIGN $64
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV $0, X13                 // relative block offset
	MOV $-1, X14                // first backslash
	MOV X11, X15
	MOV $0x2222222222222222, X16
	MOV $0x5c5c5c5c5c5c5c5c, X17
	MOV $0x8080808080808080, X20

str_scan:
	BEQZ X11, str_eof
	MOV $8, X24
	BLTU X11, X24, str_tail
	MOV 0(X10), X21
str_classify:
	XOR X16, X21, X7
	ORCB X7, X7
	XOR X17, X21, X6
	ORCB X6, X6
	AND X6, X7, X5
	ANDN X5, X20, X22
str_event:
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL X23, X7, X25
	SRL $3, X23
	AND $1, X25
	BEQZ X25, str_quote
str_escape:
	BGEZ X14, str_escape_seen
	ADD X13, X23, X14
str_escape_seen:
	SUB X23, X11, X30
	MOV $2, X26
	BLTU X30, X26, str_eof
	BGEU X26, X24, str_relaxed_byte
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL X23, X7, X25
	SRL $3, X23
	AND $1, X25
	BEQZ X25, str_quote
	SUB X26, X23, X5
	MOV $2, X26
	BLTU X26, X5, str_escape
	SUB X23, X11, X30
	BLTU X30, X26, str_eof
str_relaxed_enter:
	MOV $92, X28
	MOV $34, X29
	ADD X23, X26, X5
	ADD X5, X10, X9
	SUB X5, X11, X30
str_relaxed_next:
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	SUB X30, X11, X5
	MOV X9, X10
	ADD X5, X13
	MOV X30, X11
	JMP str_scan
	PCALIGN $16
str_relaxed_escape:
	BLTU X30, X26, str_eof
	ADD $2, X9
	SUB $2, X30
	JMP str_relaxed_next
str_relaxed_quote:
	SUB X10, X9, X23
	BLTU X23, X24, str_quote
	ADD X23, X13
	MOV $0, X23
	MOV $1, X24
	JMP str_quote

str_relaxed_byte:
	BEQ X30, X26, str_consume
	MOVBU 2(X10), X5
	MOV $92, X6
	BEQ X5, X6, str_relaxed_enter
str_consume:
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	JMP str_event
str_crossing:
	MOV X26, X24
str_advance:
	ADD X24, X10
	ADD X24, X13
	SUB X24, X11
	JMP str_scan

str_byte:
	MOV $1, X24
	MOV $0, X23
	MOVBU 0(X10), X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BEQ X25, X5, str_escape
	JMP str_advance

str_quote:
	ADD X23, X13, X5
	ADD $1, X5
	MOV $-1, X6
	MOV $0, X7
	JMP str_done
str_eof:
	MOV X15, X5
	MOV X15, X6
	MOV $1, X7
str_done:
	MOV X5, end+16(FP)
	MOV X14, escape+24(FP)
	MOV X6, errorPos+32(FP)
	MOV X7, err+40(FP)
	MOV $0, X28
	MOV $0, X29
	BNEZ X7, str_block_done
	MOV $1, X9
	BGEU X9, X24, str_block_done
	ADD $1, X23, X9
	SUB X9, X24, X29
	BEQZ X29, str_block_done
	SRL $7, X22, X28
	MOV $0x0102040810204080, X16
	MUL X16, X28
	SRL $56, X28
	SRL X9, X28
str_block_done:
	MOV X28, tailEvents+48(FP)
	MOV X29, tailWidth+56(FP)
	RET

str_tail:
	MOV $1, X5
	BEQ X11, X5, str_byte
	BLTU X15, X24, str_tail_short
	ADD X11, X10, X5
	MOV -8(X5), X21
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X21
str_tail_fill:
	SLL $3, X11, X5
	MOV $-1, X6
	SLL X5, X6
	OR X6, X21
	MOV X11, X24
	JMP str_classify
str_tail_short:
	MOV $4, X5
	BLTU X11, X5, str_byte
	MOVWU 0(X10), X21
	ADD X11, X10, X5
	MOVWU -4(X5), X6
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X6
	SLL $32, X6
	OR X6, X21
	JMP str_tail_fill

// func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringStrictBlock(SB), NOSPLIT, $0-64
	PCALIGN $64
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV $0, X13                 // relative block offset
	MOV $-1, X14                // first backslash
	MOV X11, X15
	MOV $0x2222222222222222, X16
	MOV $0x5c5c5c5c5c5c5c5c, X17
	MOV $0xe0e0e0e0e0e0e0e0, X18
	MOV $0x8080808080808080, X20

str_scan:
	BEQZ X11, str_eof
	MOV $8, X24
	BLTU X11, X24, str_tail
	MOV 0(X10), X21
str_classify:
	XOR X16, X21, X7
	ORCB X7, X7
	XOR X17, X21, X6
	ORCB X6, X6
	AND X6, X7, X5
	AND X18, X21, X6
	ORCB X6, X6
	AND X6, X5
	ANDN X5, X20, X22
str_event:
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL $3, X23
	SLL $3, X23, X5
	SRL X5, X21, X25
	AND $255, X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BNE X25, X5, str_invalid
str_escape:
	BGEZ X14, str_escape_seen
	ADD X13, X23, X14
str_escape_seen:
	SUB X23, X11, X30
	MOV $2, X26
	BLTU X30, X26, str_eof
	ADD X23, X10, X28
	MOVBU 1(X28), X25
	MOV $34, X5
	BEQ X25, X5, str_consume
	MOV $92, X5
	BEQ X25, X5, str_consume
	ADD $-98, X25, X5
	MOV $19, X6
	BGEU X5, X6, str_escape_other
	MOV $0x00051011, X6
	SRL X5, X6
	AND $1, X6
	BNEZ X6, str_consume
	JMP str_invalid
str_escape_other:
	MOV $47, X5
	BEQ X25, X5, str_consume
	MOV $117, X5
	BNE X25, X5, str_invalid
	MOV $6, X26
	BLTU X30, X26, str_eof
	MOVWU 2(X28), X25
	MOV $0x7f7f7f7f, X5
	AND X25, X5, X5
	MOV $0x50505050, X6
	ADD X5, X6, X6
	MOV $0x46464646, X28
	ADD X5, X28, X28
	XOR X28, X6
	MOV $0x20202020, X28
	OR X28, X5
	MOV $0x1f1f1f1f, X28
	ADD X5, X28, X28
	MOV $0x19191919, X29
	ADD X5, X29, X29
	XOR X29, X28
	OR X28, X6
	ANDN X25, X6, X6
	SRL $32, X20, X5
	AND X5, X6
	BNE X6, X5, str_invalid
str_consume:
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL $3, X23
	SLL $3, X23, X5
	SRL X5, X21, X25
	AND $255, X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BNE X25, X5, str_invalid
	SUB X26, X23, X5
	MOV $2, X6
	BLTU X6, X5, str_escape_seen
	MOV $92, X28
	MOV $34, X29
	MOV $32, X12
	MOV $0x00051011, X19
	ADD X23, X10, X9
	SUB X23, X11, X30
str_strict_dense_escape:
	MOV $2, X26
	BLTU X30, X26, str_eof
	MOVBU 1(X9), X25
	BEQ X25, X29, str_strict_dense_step2
	BEQ X25, X28, str_strict_dense_step2
	ADD $-98, X25, X5
	MOV $19, X6
	BGEU X5, X6, str_strict_dense_other
	SRL X5, X19, X6
	AND $1, X6
	BNEZ X6, str_strict_dense_step2
	JMP str_strict_dense_invalid
str_strict_dense_other:
	MOV $47, X5
	BEQ X25, X5, str_strict_dense_step2
	MOV $117, X5
	BNE X25, X5, str_strict_dense_invalid
	MOV $6, X26
	BLTU X30, X26, str_eof
	MOVWU 2(X9), X25
	MOV $0x7f7f7f7f, X5
	AND X25, X5, X5
	MOV $0x50505050, X6
	ADD X5, X6, X6
	MOV $0x46464646, X7
	ADD X5, X7, X7
	XOR X7, X6
	MOV $0x20202020, X7
	OR X7, X5
	MOV $0x1f1f1f1f, X7
	ADD X5, X7, X7
	MOV $0x19191919, X23
	ADD X5, X23, X23
	XOR X23, X7
	OR X7, X6
	ANDN X25, X6, X6
	SRL $32, X20, X5
	AND X5, X6
	BNE X6, X5, str_strict_dense_invalid
	ADD $6, X9
	SUB $6, X30
	JMP str_strict_dense_next
	PCALIGN $16
str_strict_dense_step2:
	ADD $2, X9
	SUB $2, X30
str_strict_dense_next:
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_strict_dense_escape
	BEQ X25, X29, str_strict_dense_quote
	BLTU X25, X12, str_strict_dense_invalid
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_strict_dense_escape
	BEQ X25, X29, str_strict_dense_quote
	BLTU X25, X12, str_strict_dense_invalid
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_strict_dense_escape
	BEQ X25, X29, str_strict_dense_quote
	BLTU X25, X12, str_strict_dense_invalid
	ADD $1, X9
	SUB $1, X30
	SUB X30, X11, X5
	MOV X9, X10
	ADD X5, X13
	MOV X30, X11
	JMP str_scan
str_strict_dense_quote:
	SUB X10, X9, X23
	BLTU X23, X24, str_quote
	ADD X23, X13
	MOV $0, X23
	MOV $1, X24
	JMP str_quote
str_strict_dense_invalid:
	SUB X10, X9, X23
	JMP str_invalid

str_crossing:
	MOV X26, X24
str_advance:
	ADD X24, X10
	ADD X24, X13
	SUB X24, X11
	JMP str_scan

str_byte:
	MOV $1, X24
	MOV $0, X23
	MOVBU 0(X10), X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BEQ X25, X5, str_escape
	MOV $32, X5
	BLTU X25, X5, str_invalid
	JMP str_advance

str_quote:
	ADD X23, X13, X5
	ADD $1, X5
	MOV $-1, X6
	MOV $0, X7
	JMP str_done
str_invalid:
	ADD X23, X13, X5
	MOV X5, X6
	MOV $2, X7
	JMP str_done
str_eof:
	MOV X15, X5
	MOV X15, X6
	MOV $1, X7
str_done:
	MOV X5, end+16(FP)
	MOV X14, escape+24(FP)
	MOV X6, errorPos+32(FP)
	MOV X7, err+40(FP)
	MOV $0, X28
	MOV $0, X29
	BNEZ X7, str_block_done
	MOV $1, X9
	BGEU X9, X24, str_block_done
	ADD $1, X23, X9
	SUB X9, X24, X29
	BEQZ X29, str_block_done
	AND X18, X21, X6
	ORCB X6, X6
	AND X6, X22
	SRL $7, X22, X28
	MOV $0x0102040810204080, X16
	MUL X16, X28
	SRL $56, X28
	SRL X9, X28
str_block_done:
	MOV X28, tailEvents+48(FP)
	MOV X29, tailWidth+56(FP)
	RET

str_tail:
	MOV $1, X5
	BEQ X11, X5, str_byte
	BLTU X15, X24, str_tail_short
	ADD X11, X10, X5
	MOV -8(X5), X21
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X21
str_tail_fill:
	SLL $3, X11, X5
	MOV $-1, X6
	SLL X5, X6
	OR X6, X21
	MOV X11, X24
	JMP str_classify
str_tail_short:
	MOV $4, X5
	BLTU X11, X5, str_byte
	MOVWU 0(X10), X21
	ADD X11, X10, X5
	MOVWU -4(X5), X6
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X6
	SLL $32, X6
	OR X6, X21
	JMP str_tail_fill
