//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·q_ctrl_vec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·q_ctrl_vec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·q_ctrl_vec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·q_ctrl_vec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL ·q_ctrl_vec<>(SB), RODATA|NOPTR, $32

DATA ·q_quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·q_quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·q_quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·q_quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·q_quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·q_slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·q_slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·q_slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·q_slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·q_slash_vec<>(SB), RODATA|NOPTR, $32

// func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)
TEXT ·quote(SB), NOSPLIT, $0-56
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), CX
	MOVQ dst+16(FP), DI
	MOVQ capacity+24(FP), DX
	MOVQ flags+32(FP), R8
	ANDQ $1, R8                   // R8 = double-quote flag (0 or 1)
	MOVQ CX, R9                   // R9 = original size
	MOVQ DX, R10                  // R10 = original capacity
	XORQ R15, R15                 // R15 = 1 if YMM registers used

	CMPQ CX, $32
	JB   q_short_entry
	CMPQ DX, $32
	JB   q_short_entry
	CMPB ·hasAVX2(SB), $0
	JE   q_short_entry

	MOVQ $1, R15
	VMOVDQU ·q_ctrl_vec<>(SB), Y3
	VMOVDQU ·q_quote_vec<>(SB), Y4
	VMOVDQU ·q_slash_vec<>(SB), Y5
	JMP  q_scan

q_short_entry:
	CMPQ CX, $16
	JB   q_scan_word_entry
	CMPQ DX, $16
	JB   q_scan_word_entry
	MOVOU ·q_ctrl_vec<>(SB), X3
	MOVOU ·q_quote_vec<>(SB), X4
	MOVOU ·q_slash_vec<>(SB), X5
	JMP  q_scan_16

q_scan:
	CMPQ CX, $64
	JB   q_scan_32
	CMPQ DX, $64
	JB   q_scan_32

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPMAXUB Y3, Y0, Y2
	VPCMPEQB Y3, Y2, Y2
	VPCMPEQB Y4, Y0, Y6
	VPOR Y6, Y2, Y2
	VPCMPEQB Y5, Y0, Y6
	VPOR Y6, Y2, Y2

	VPMAXUB Y3, Y1, Y7
	VPCMPEQB Y3, Y7, Y7
	VPCMPEQB Y4, Y1, Y6
	VPOR Y6, Y7, Y7
	VPCMPEQB Y5, Y1, Y6
	VPOR Y6, Y7, Y7

	VPOR Y7, Y2, Y6
	VPMOVMSKB Y6, AX
	TESTL AX, AX
	JNZ  q_vec64_event

	VMOVDQU Y0, 0(DI)
	VMOVDQU Y1, 32(DI)
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $64, CX
	SUBQ $64, DX
	JMP  q_scan

q_vec64_event:
	VPMOVMSKB Y2, AX
	TESTL AX, AX
	JNZ  q_vec32_event
	VMOVDQU Y0, 0(DI)
	ADDQ $32, SI
	ADDQ $32, DI
	SUBQ $32, CX
	SUBQ $32, DX
	VMOVDQA Y1, Y0
	VPMOVMSKB Y7, AX
	JMP  q_vec32_event

q_scan_32:
	CMPQ CX, $32
	JB   q_scan_16
	CMPQ DX, $32
	JB   q_scan_16

	VMOVDQU 0(SI), Y0
	VPMAXUB Y3, Y0, Y2
	VPCMPEQB Y3, Y2, Y2
	VPCMPEQB Y4, Y0, Y6
	VPOR Y6, Y2, Y2
	VPCMPEQB Y5, Y0, Y6
	VPOR Y6, Y2, Y2
	VPMOVMSKB Y2, AX
	TESTL AX, AX
	JNZ  q_vec32_event

	VMOVDQU Y0, 0(DI)
	ADDQ $32, SI
	ADDQ $32, DI
	SUBQ $32, CX
	SUBQ $32, DX
	JMP  q_scan_16

q_vec32_event:
	BSFL AX, BX                   // BX = plain prefix length (0..31)
	CMPQ BX, $16
	JB   q_vec16_prefix
	VMOVDQU X0, 0(DI)
	VEXTRACTI128 $1, Y0, X0
	ADDQ $16, SI
	ADDQ $16, DI
	SUBQ $16, CX
	SUBQ $16, DX
	SUBQ $16, BX
	JMP  q_vec16_prefix

q_scan_16:
	CMPQ CX, $16
	JB   q_scan_word_entry
	CMPQ DX, $16
	JB   q_scan_word_entry

	MOVOU (SI), X0
	MOVO X0, X2
	PMAXUB X3, X2
	PCMPEQB X3, X2
	MOVO X0, X1
	PCMPEQB X4, X1
	POR X1, X2
	MOVO X0, X1
	PCMPEQB X5, X1
	POR X1, X2
	PMOVMSKB X2, AX
	TESTL AX, AX
	JNZ  q_vec16_event

	MOVOU X0, (DI)
	ADDQ $16, SI
	ADDQ $16, DI
	SUBQ $16, CX
	SUBQ $16, DX
	JMP  q_scan_16

q_vec16_event:
	BSFL AX, BX
q_vec16_prefix:
	TESTQ BX, BX
	JZ   q_classify
	MOVQ DI, R11
	MOVQ X0, AX
	TESTQ $8, BX
	JZ   q_prefix4
	MOVQ AX, (R11)
	ADDQ $8, R11
	PSRLDQ $8, X0
	MOVQ X0, AX
q_prefix4:
	TESTQ $4, BX
	JZ   q_prefix2
	MOVL AX, (R11)
	SHRQ $32, AX
	ADDQ $4, R11
q_prefix2:
	TESTQ $2, BX
	JZ   q_prefix1
	MOVW AX, (R11)
	SHRQ $16, AX
	ADDQ $2, R11
q_prefix1:
	TESTQ $1, BX
	JZ   q_prefix_done
	MOVB AX, (R11)
q_prefix_done:
	ADDQ BX, SI
	ADDQ BX, DI
	SUBQ BX, CX
	SUBQ BX, DX
	JMP  q_classify

q_scan_word_entry:
	TESTQ CX, CX
	JZ   q_success
	TESTQ DX, DX
	JZ   q_exhausted
	CMPQ CX, $8
	JB   q_scan_byte
	CMPQ DX, $8
	JB   q_scan_byte

q_scan_word:
	CMPQ CX, $8
	JB   q_scan_tail
	CMPQ DX, $8
	JB   q_scan_byte

	MOVQ (SI), R11
	MOVQ $0x2222222222222222, AX
	XORQ R11, AX
	MOVQ $0x0101010101010101, R12
	MOVQ AX, R13
	SUBQ R12, R13
	NOTQ AX
	ANDQ AX, R13

	MOVQ $0x5c5c5c5c5c5c5c5c, AX
	XORQ R11, AX
	MOVQ AX, BX
	SUBQ R12, BX
	NOTQ AX
	ANDQ AX, BX
	ORQ  BX, R13

	MOVQ $0x2020202020202020, AX
	MOVQ R11, BX
	SUBQ AX, BX
	MOVQ R11, AX
	NOTQ AX
	ANDQ AX, BX
	ORQ  BX, R13

	MOVQ $0x8080808080808080, AX
	ANDQ AX, R13
	JNZ  q_word_event

	MOVQ R11, (DI)
	ADDQ $8, SI
	SUBQ $8, CX
	ADDQ $8, DI
	SUBQ $8, DX
	JMP  q_scan_word

q_word_event:
	BSFQ R13, BX
	SHRQ $3, BX
	TESTQ BX, BX
	JZ   q_classify
	MOVQ DI, AX
	TESTQ $4, BX
	JZ   q_word_short2
	MOVL R11, (AX)
	SHRQ $32, R11
	ADDQ $4, AX
q_word_short2:
	TESTQ $2, BX
	JZ   q_word_short1
	MOVW R11, (AX)
	SHRQ $16, R11
	ADDQ $2, AX
q_word_short1:
	TESTQ $1, BX
	JZ   q_word_short_done
	MOVB R11, (AX)
q_word_short_done:
	ADDQ BX, SI
	SUBQ BX, CX
	ADDQ BX, DI
	SUBQ BX, DX
	JMP  q_classify

q_scan_tail:
	TESTQ CX, CX
	JZ   q_success

q_scan_byte:
	TESTQ CX, CX
	JZ   q_success
	TESTQ DX, DX
	JZ   q_exhausted
	MOVBLZX (SI), AX
	CMPB AL, $32
	JB   q_classify
	CMPB AL, $34
	JE   q_classify
	CMPB AL, $92
	JE   q_classify
	MOVB AL, (DI)
	ADDQ $1, SI
	ADDQ $1, DI
	SUBQ $1, CX
	SUBQ $1, DX
	JMP  q_resume_scan

q_classify:
	MOVBLZX (SI), AX
	CMPB AL, $32
	JAE  q_quote_or_backslash
	CMPB AL, $9
	JE   q_tab
	CMPB AL, $10
	JE   q_newline
	CMPB AL, $13
	JE   q_cr

	// Generic control char: \u00hh (6 bytes) or \\u00hh (7 bytes)
	LEAQ 6(R8), R11               // R11 = output length
	LEAQ 1(R8), R13               // R13 = leading backslashes
	CMPQ DX, R11
	JB   q_exhausted
	MOVQ DI, BX
q_ctrl_bs:
	MOVB $92, (BX)
	ADDQ $1, BX
	SUBQ $1, R13
	JNZ  q_ctrl_bs
	MOVB $117, 0(BX)              // 'u'
	MOVB $48, 1(BX)               // '0'
	MOVB $48, 2(BX)               // '0'
	MOVQ AX, R12
	SHRQ $4, R12
	ADDQ $48, R12
	MOVB R12, 3(BX)
	ANDQ $15, AX
	CMPQ AX, $10
	JB   q_hex_digit
	ADDQ $87, AX
	JMP  q_hex_store
q_hex_digit:
	ADDQ $48, AX
q_hex_store:
	MOVB AL, 4(BX)
	JMP  q_escaped

q_quote_or_backslash:
	MOVQ R8, R13
	SHLQ $1, R13
	LEAQ 2(R13), R11              // 2 bytes normally, 4 when doubled
	ADDQ $1, R13                  // 1 backslash normally, 3 when doubled
	MOVQ AX, R12
	JMP  q_emit_short

q_tab:
	MOVQ $116, R12                // 't'
	JMP  q_short_ctrl
q_newline:
	MOVQ $110, R12                // 'n'
	JMP  q_short_ctrl
q_cr:
	MOVQ $114, R12                // 'r'
q_short_ctrl:
	LEAQ 2(R8), R11               // 2 bytes normally, 3 when doubled
	LEAQ 1(R8), R13               // 1 backslash normally, 2 when doubled

q_emit_short:
	CMPQ DX, R11
	JB   q_exhausted
	MOVQ DI, BX
q_short_bs:
	MOVB $92, (BX)
	ADDQ $1, BX
	SUBQ $1, R13
	JNZ  q_short_bs
	MOVB R12, 0(BX)

q_escaped:
	ADDQ $1, SI
	SUBQ $1, CX
	ADDQ R11, DI
	SUBQ R11, DX

q_resume_scan:
	TESTQ R15, R15
	JNZ  q_scan
	CMPQ CX, $16
	JB   q_scan_word_entry
	CMPQ DX, $16
	JB   q_scan_word_entry
	JMP  q_scan_16

q_exhausted:
	MOVQ R9, AX
	SUBQ CX, AX
	NOTQ AX
	JMP  q_done

q_success:
	MOVQ R9, AX

q_done:
	SUBQ DX, R10
	MOVQ AX, consumed+40(FP)
	MOVQ R10, written+48(FP)
	TESTQ R15, R15
	JZ   q_ret
	VZEROUPPER
q_ret:
	RET
