//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·he_lt_vec<>+0(SB)/8, $0x3c3c3c3c3c3c3c3c
DATA ·he_lt_vec<>+8(SB)/8, $0x3c3c3c3c3c3c3c3c
DATA ·he_lt_vec<>+16(SB)/8, $0x3c3c3c3c3c3c3c3c
DATA ·he_lt_vec<>+24(SB)/8, $0x3c3c3c3c3c3c3c3c
GLOBL ·he_lt_vec<>(SB), RODATA|NOPTR, $32

DATA ·he_gt_vec<>+0(SB)/8, $0x3e3e3e3e3e3e3e3e
DATA ·he_gt_vec<>+8(SB)/8, $0x3e3e3e3e3e3e3e3e
DATA ·he_gt_vec<>+16(SB)/8, $0x3e3e3e3e3e3e3e3e
DATA ·he_gt_vec<>+24(SB)/8, $0x3e3e3e3e3e3e3e3e
GLOBL ·he_gt_vec<>(SB), RODATA|NOPTR, $32

DATA ·he_amp_vec<>+0(SB)/8, $0x2626262626262626
DATA ·he_amp_vec<>+8(SB)/8, $0x2626262626262626
DATA ·he_amp_vec<>+16(SB)/8, $0x2626262626262626
DATA ·he_amp_vec<>+24(SB)/8, $0x2626262626262626
GLOBL ·he_amp_vec<>(SB), RODATA|NOPTR, $32

DATA ·he_e2_vec<>+0(SB)/8, $0xe2e2e2e2e2e2e2e2
DATA ·he_e2_vec<>+8(SB)/8, $0xe2e2e2e2e2e2e2e2
DATA ·he_e2_vec<>+16(SB)/8, $0xe2e2e2e2e2e2e2e2
DATA ·he_e2_vec<>+24(SB)/8, $0xe2e2e2e2e2e2e2e2
GLOBL ·he_e2_vec<>(SB), RODATA|NOPTR, $32

// func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)
TEXT ·htmlEscape(SB), NOSPLIT, $0-48
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), CX
	MOVQ dst+16(FP), DI
	MOVQ capacity+24(FP), DX
	MOVQ CX, R8                   // R8 = original size
	MOVQ DX, R9                   // R9 = original capacity

	CMPQ CX, $32
	JB   he_short_entry
	CMPQ DX, $32
	JB   he_short_entry
	CMPB ·hasAVX2(SB), $0
	JE   he_short_entry

	VMOVDQU ·he_lt_vec<>(SB), Y4
	VMOVDQU ·he_gt_vec<>(SB), Y5
	VMOVDQU ·he_amp_vec<>(SB), Y6
	VMOVDQU ·he_e2_vec<>(SB), Y7

he_avx_scan:
	CMPQ CX, $64
	JB   he_avx_scan32
	CMPQ DX, $64
	JB   he_avx_scan32

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPCMPEQB Y4, Y0, Y2
	VPCMPEQB Y5, Y0, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y6, Y0, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y7, Y0, Y3; VPOR Y3, Y2, Y2
	VPMOVMSKB Y2, AX

	VPCMPEQB Y4, Y1, Y2
	VPCMPEQB Y5, Y1, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y6, Y1, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y7, Y1, Y3; VPOR Y3, Y2, Y2
	VPMOVMSKB Y2, BX

	MOVL AX, R10
	ORL  BX, R10
	JNZ  he_avx_vec64_event

	VMOVDQU Y0, 0(DI)
	VMOVDQU Y1, 32(DI)
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $64, CX
	SUBQ $64, DX
	JMP  he_avx_scan

he_avx_vec64_event:
	TESTL AX, AX
	JNZ   he_avx_vec32_event
	VMOVDQU Y0, 0(DI)
	ADDQ  $32, SI
	ADDQ  $32, DI
	SUBQ  $32, CX
	SUBQ  $32, DX
	VMOVDQA Y1, Y0
	MOVL  BX, AX
	JMP   he_avx_vec32_event

he_avx_scan32:
	CMPQ CX, $32
	JB   he_avx_scan16
	CMPQ DX, $32
	JB   he_avx_scan16

	VMOVDQU 0(SI), Y0
	VPCMPEQB Y4, Y0, Y2
	VPCMPEQB Y5, Y0, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y6, Y0, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y7, Y0, Y3; VPOR Y3, Y2, Y2
	VPMOVMSKB Y2, AX
	TESTL AX, AX
	JNZ   he_avx_vec32_event

	VMOVDQU Y0, 0(DI)
	ADDQ  $32, SI
	ADDQ  $32, DI
	SUBQ  $32, CX
	SUBQ  $32, DX
	JMP   he_avx_scan

he_avx_vec32_event:
	BSFL AX, BX                   // BX = plain prefix length (0..31)
	CMPQ BX, $16
	JB   he_avx_vec16_prefix
	VMOVDQU X0, 0(DI)
	VEXTRACTI128 $1, Y0, X0
	ADDQ $16, SI
	ADDQ $16, DI
	SUBQ $16, CX
	SUBQ $16, DX
	SUBQ $16, BX
	JMP  he_avx_vec16_prefix

he_avx_scan16:
	CMPQ CX, $16
	JB   he_avx_scan_scalar
	CMPQ DX, $16
	JB   he_avx_scan_scalar

	VMOVDQU (SI), X0
	VPCMPEQB X4, X0, X2
	VPCMPEQB X5, X0, X3; VPOR X3, X2, X2
	VPCMPEQB X6, X0, X3; VPOR X3, X2, X2
	VPCMPEQB X7, X0, X3; VPOR X3, X2, X2
	VPMOVMSKB X2, AX
	TESTL AX, AX
	JNZ   he_avx_vec16_event

	VMOVDQU X0, (DI)
	ADDQ  $16, SI
	ADDQ  $16, DI
	SUBQ  $16, CX
	SUBQ  $16, DX
	JMP   he_avx_scan

he_avx_vec16_event:
	BSFL AX, BX                   // BX = plain prefix length (0..15)
he_avx_vec16_prefix:
	TESTQ BX, BX
	JZ   he_avx_classify
	MOVQ DI, R11
	VMOVQ X0, AX
	TESTQ $8, BX
	JZ   he_avx_prefix4
	MOVQ AX, (R11)
	ADDQ $8, R11
	VPEXTRQ $1, X0, AX
he_avx_prefix4:
	TESTQ $4, BX
	JZ   he_avx_prefix2
	MOVL AX, (R11)
	SHRQ $32, AX
	ADDQ $4, R11
he_avx_prefix2:
	TESTQ $2, BX
	JZ   he_avx_prefix1
	MOVW AX, (R11)
	SHRQ $16, AX
	ADDQ $2, R11
he_avx_prefix1:
	TESTQ $1, BX
	JZ   he_avx_prefix_done
	MOVB AX, (R11)
he_avx_prefix_done:
	ADDQ BX, SI
	ADDQ BX, DI
	SUBQ BX, CX
	SUBQ BX, DX
	JMP  he_avx_classify

he_avx_scan_scalar:
	TESTQ CX, CX
	JZ   he_avx_success
	TESTQ DX, DX
	JZ   he_avx_exhausted
	MOVBLZX (SI), AX
	CMPB AL, $60                  // '<'
	JE   he_avx_classify
	CMPB AL, $62                  // '>'
	JE   he_avx_classify
	CMPB AL, $38                  // '&'
	JE   he_avx_classify
	CMPB AL, $226                 // 0xe2
	JE   he_avx_classify
	MOVB AL, (DI)
	ADDQ $1, SI
	SUBQ $1, CX
	ADDQ $1, DI
	SUBQ $1, DX
	JMP  he_avx_scan

he_avx_classify:
	MOVBLZX (SI), AX
	MOVQ $1, R11                  // R11 = input bytes consumed by escape
	CMPB AL, $226
	JNE  he_avx_ascii_escape
	CMPQ CX, $3
	JB   he_avx_plain_e2
	MOVBLZX 1(SI), BX
	CMPB BL, $128
	JNE  he_avx_plain_e2
	MOVBLZX 2(SI), R15
	CMPB R15, $168
	JE   he_avx_unicode_escape
	CMPB R15, $169
	JNE  he_avx_plain_e2

he_avx_unicode_escape:
	MOVQ $3, R11
	MOVQ $50, R12                 // '2' (\u2028 or \u2029)
	MOVQ $50, R13                 // '2'
	SUBQ $112, R15                // 168 - 112 = 56 ('8'), 169 - 112 = 57 ('9')
	JMP  he_avx_emit

he_avx_ascii_escape:
	MOVQ $48, R12                 // '0' (\u003c, \u003e, or \u0026)
	MOVQ AX, R13
	SHRQ $4, R13
	ADDQ $48, R13
	MOVQ AX, R15
	ANDQ $15, R15
	CMPQ R15, $10
	JB   he_avx_hex_digit
	ADDQ $87, R15
	JMP  he_avx_emit
he_avx_hex_digit:
	ADDQ $48, R15

he_avx_emit:
	CMPQ DX, $6
	JB   he_avx_exhausted
	MOVB $92, 0(DI)               // '\\'
	MOVB $117, 1(DI)              // 'u'
	MOVB R12, 2(DI)
	MOVB $48, 3(DI)               // '0'
	MOVB R13, 4(DI)
	MOVB R15, 5(DI)
	ADDQ R11, SI
	SUBQ R11, CX
	ADDQ $6, DI
	SUBQ $6, DX
	JMP  he_avx_scan

he_avx_plain_e2:
	MOVB AL, (DI)
	ADDQ $1, SI
	SUBQ $1, CX
	ADDQ $1, DI
	SUBQ $1, DX
	JMP  he_avx_scan

he_avx_exhausted:
	MOVQ R8, AX
	SUBQ CX, AX
	NOTQ AX
	JMP  he_avx_done

he_avx_success:
	MOVQ R8, AX

he_avx_done:
	SUBQ DX, R9
	MOVQ AX, consumed+32(FP)
	MOVQ R9, written+40(FP)
	VZEROUPPER
	RET

// Short path (< 32B): uses only X0~X7 and scalar registers, no VZEROUPPER needed.
he_short_entry:
	CMPQ CX, $16
	JB   he_scan_scalar
	CMPQ DX, $16
	JB   he_scan_scalar

	MOVOU ·he_lt_vec<>(SB), X4
	MOVOU ·he_gt_vec<>(SB), X5
	MOVOU ·he_amp_vec<>(SB), X6
	MOVOU ·he_e2_vec<>(SB), X7

he_scan16:
	CMPQ CX, $16
	JB   he_scan_scalar
	CMPQ DX, $16
	JB   he_scan_scalar

	MOVOU (SI), X0
	MOVO  X0, X2; PCMPEQB X4, X2
	MOVO  X0, X3; PCMPEQB X5, X3; POR X3, X2
	MOVO  X0, X3; PCMPEQB X6, X3; POR X3, X2
	MOVO  X0, X3; PCMPEQB X7, X3; POR X3, X2
	PMOVMSKB X2, AX
	TESTL AX, AX
	JNZ   he_vec_event

	MOVOU X0, (DI)
	ADDQ  $16, SI
	ADDQ  $16, DI
	SUBQ  $16, CX
	SUBQ  $16, DX
	JMP   he_scan16

he_vec_event:
	BSFL AX, BX                   // BX = plain prefix length (0..15)
	TESTQ BX, BX
	JZ   he_classify
	MOVQ DI, R11
	MOVQ X0, AX
	TESTQ $8, BX
	JZ   he_prefix4
	MOVQ AX, (R11)
	ADDQ $8, R11
	PSRLDQ $8, X0
	MOVQ X0, AX
he_prefix4:
	TESTQ $4, BX
	JZ   he_prefix2
	MOVL AX, (R11)
	SHRQ $32, AX
	ADDQ $4, R11
he_prefix2:
	TESTQ $2, BX
	JZ   he_prefix1
	MOVW AX, (R11)
	SHRQ $16, AX
	ADDQ $2, R11
he_prefix1:
	TESTQ $1, BX
	JZ   he_prefix_done
	MOVB AX, (R11)
he_prefix_done:
	ADDQ BX, SI
	ADDQ BX, DI
	SUBQ BX, CX
	SUBQ BX, DX
	JMP  he_classify

he_scan_scalar:
	TESTQ CX, CX
	JZ   he_success
	TESTQ DX, DX
	JZ   he_exhausted
	MOVBLZX (SI), AX
	CMPB AL, $60                  // '<'
	JE   he_classify
	CMPB AL, $62                  // '>'
	JE   he_classify
	CMPB AL, $38                  // '&'
	JE   he_classify
	CMPB AL, $226                 // 0xe2
	JE   he_classify
	MOVB AL, (DI)
	ADDQ $1, SI
	SUBQ $1, CX
	ADDQ $1, DI
	SUBQ $1, DX
	JMP  he_scan16

he_classify:
	MOVBLZX (SI), AX
	MOVQ $1, R11                  // R11 = input bytes consumed by escape
	CMPB AL, $226
	JNE  he_ascii_escape
	CMPQ CX, $3
	JB   he_plain_e2
	MOVBLZX 1(SI), BX
	CMPB BL, $128
	JNE  he_plain_e2
	MOVBLZX 2(SI), R15
	CMPB R15, $168
	JE   he_unicode_escape
	CMPB R15, $169
	JNE  he_plain_e2

he_unicode_escape:
	MOVQ $3, R11
	MOVQ $50, R12                 // '2' (\u2028 or \u2029)
	MOVQ $50, R13                 // '2'
	SUBQ $112, R15                // 168 - 112 = 56 ('8'), 169 - 112 = 57 ('9')
	JMP  he_emit

he_ascii_escape:
	MOVQ $48, R12                 // '0' (\u003c, \u003e, or \u0026)
	MOVQ AX, R13
	SHRQ $4, R13
	ADDQ $48, R13
	MOVQ AX, R15
	ANDQ $15, R15
	CMPQ R15, $10
	JB   he_hex_digit
	ADDQ $87, R15
	JMP  he_emit
he_hex_digit:
	ADDQ $48, R15

he_emit:
	CMPQ DX, $6
	JB   he_exhausted
	MOVB $92, 0(DI)               // '\\'
	MOVB $117, 1(DI)              // 'u'
	MOVB R12, 2(DI)
	MOVB $48, 3(DI)               // '0'
	MOVB R13, 4(DI)
	MOVB R15, 5(DI)
	ADDQ R11, SI
	SUBQ R11, CX
	ADDQ $6, DI
	SUBQ $6, DX
	JMP  he_scan16

he_plain_e2:
	MOVB AL, (DI)
	ADDQ $1, SI
	SUBQ $1, CX
	ADDQ $1, DI
	SUBQ $1, DX
	JMP  he_scan16

he_exhausted:
	MOVQ R8, AX
	SUBQ CX, AX
	NOTQ AX
	JMP  he_done

he_success:
	MOVQ R8, AX

he_done:
	SUBQ DX, R9
	MOVQ AX, consumed+32(FP)
	MOVQ R9, written+40(FP)
	RET
