//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA cParityWsLUT<>+0(SB)/8, $0x0000000000000020
DATA cParityWsLUT<>+8(SB)/8, $0x00000d00000a0900
DATA cParityWsLUT<>+16(SB)/8, $0x0000000000000020
DATA cParityWsLUT<>+24(SB)/8, $0x00000d00000a0900
GLOBL cParityWsLUT<>(SB), RODATA|NOPTR, $32

DATA cParitySlashVec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA cParitySlashVec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA cParitySlashVec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA cParitySlashVec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL cParitySlashVec<>(SB), RODATA|NOPTR, $32

DATA cParityQuoteVec<>+0(SB)/8, $0x2222222222222222
DATA cParityQuoteVec<>+8(SB)/8, $0x2222222222222222
DATA cParityQuoteVec<>+16(SB)/8, $0x2222222222222222
DATA cParityQuoteVec<>+24(SB)/8, $0x2222222222222222
GLOBL cParityQuoteVec<>(SB), RODATA|NOPTR, $32

DATA cParityCtrlVec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA cParityCtrlVec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA cParityCtrlVec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA cParityCtrlVec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL cParityCtrlVec<>(SB), RODATA|NOPTR, $32

// func cParityNonspace64(src unsafe.Pointer) (mask uint64)
TEXT ·cParityNonspace64(SB), NOSPLIT, $0-16
	MOVQ src+0(FP), SI
	CMPB ·hasAVX2(SB), $0
	JE   nonspace64_sse
	VMOVDQU cParityWsLUT<>(SB), Y8

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPSHUFB Y0, Y8, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, AX

	VPSHUFB Y1, Y8, Y3
	VPCMPEQB Y1, Y3, Y3
	VPMOVMSKB Y3, CX

	SHLQ $32, CX
	ORQ  CX, AX
	NOTQ AX
	MOVQ AX, mask+8(FP)
	VZEROUPPER
	RET

nonspace64_sse:
	MOVOU cParityWsLUT<>(SB), X8
	MOVOU 0(SI), X0
	MOVO X8, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, AX
	MOVOU 16(SI), X0
	MOVO X8, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, DX
	SHLQ $16, DX; ORQ DX, AX
	MOVOU 32(SI), X0
	MOVO X8, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, DX
	SHLQ $32, DX; ORQ DX, AX
	MOVOU 48(SI), X0
	MOVO X8, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, DX
	SHLQ $48, DX; ORQ DX, AX
	NOTQ AX
	MOVQ AX, mask+8(FP)
	RET

// func cParityStringMasks32(src unsafe.Pointer, opts uint64) (bs, quote, control uint32)
TEXT ·cParityStringMasks32(SB), NOSPLIT, $0-28
	MOVQ src+0(FP), SI
	MOVQ opts+8(FP), DX
	CMPB ·hasAVX2(SB), $0
	JE   string_masks32_sse

	VMOVDQU 0(SI), Y0
	VMOVDQU cParitySlashVec<>(SB), Y8
	VMOVDQU cParityQuoteVec<>(SB), Y9

	VPCMPEQB Y8, Y0, Y2
	VPMOVMSKB Y2, AX

	VPCMPEQB Y9, Y0, Y3
	VPMOVMSKB Y3, CX

	XORL R8, R8
	TESTQ $32, DX
	JZ   string_masks_done

	VMOVDQU cParityCtrlVec<>(SB), Y10
	VPMAXUB Y10, Y0, Y2
	VPCMPEQB Y10, Y2, Y2
	VPMOVMSKB Y2, R8

string_masks_done:
	MOVL AX, bs+16(FP)
	MOVL CX, quote+20(FP)
	MOVL R8, control+24(FP)
	VZEROUPPER
	RET

string_masks32_sse:
	MOVOU 0(SI), X0
	MOVOU 16(SI), X1
	MOVOU cParitySlashVec<>(SB), X8
	MOVOU cParityQuoteVec<>(SB), X9

	MOVO X0, X2; PCMPEQB X8, X2; PMOVMSKB X2, AX
	MOVO X1, X2; PCMPEQB X8, X2; PMOVMSKB X2, BX
	SHLL $16, BX; ORL BX, AX

	MOVO X0, X3; PCMPEQB X9, X3; PMOVMSKB X3, CX
	MOVO X1, X3; PCMPEQB X9, X3; PMOVMSKB X3, BX
	SHLL $16, BX; ORL BX, CX

	XORL R8, R8
	TESTQ $32, DX
	JZ   string_masks_sse_done

	MOVOU cParityCtrlVec<>(SB), X10
	MOVO X0, X2; PMAXUB X10, X2; PCMPEQB X10, X2; PMOVMSKB X2, R8
	MOVO X1, X2; PMAXUB X10, X2; PCMPEQB X10, X2; PMOVMSKB X2, BX
	SHLL $16, BX; ORL BX, R8

string_masks_sse_done:
	MOVL AX, bs+16(FP)
	MOVL CX, quote+20(FP)
	MOVL R8, control+24(FP)
	RET

// func cParityMove32(dst, src unsafe.Pointer)
TEXT ·cParityMove32(SB), NOSPLIT, $0-16
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	CMPB ·hasAVX2(SB), $0
	JE   move32_sse
	VMOVDQU 0(SI), Y0
	VMOVDQU Y0, 0(DI)
	VZEROUPPER
	RET
move32_sse:
	MOVOU 0(SI), X0
	MOVOU 16(SI), X1
	MOVOU X0, 0(DI)
	MOVOU X1, 16(DI)
	RET

// func cParityNumberMasks16(src unsafe.Pointer) (decimal, exponent, sign, valid uint32)
// < 32B (16B): uses only X0~X9, no VZEROUPPER needed.
TEXT ·cParityNumberMasks16(SB), NOSPLIT, $0-24
	MOVQ src+0(FP), SI
	MOVOU 0(SI), X0

	// decimal: '.' (0x2e)
	MOVQ $0x2e2e2e2e2e2e2e2e, AX
	MOVQ AX, X8
	PUNPCKLQDQ X8, X8
	MOVO X0, X1
	PCMPEQB X8, X1
	PMOVMSKB X1, AX

	// exponent: 'e' (0x65) or 'E' (0x45) -> (b | 0x20) == 'e'
	MOVQ $0x2020202020202020, BX
	MOVQ BX, X8
	PUNPCKLQDQ X8, X8
	MOVQ $0x6565656565656565, BX
	MOVQ BX, X9
	PUNPCKLQDQ X9, X9
	MOVO X0, X2
	POR  X8, X2
	PCMPEQB X9, X2
	PMOVMSKB X2, BX

	// sign: '+' (0x2b) or '-' (0x2d)
	MOVQ $0x2b2b2b2b2b2b2b2b, CX
	MOVQ CX, X8
	PUNPCKLQDQ X8, X8
	MOVQ $0x2d2d2d2d2d2d2d2d, CX
	MOVQ CX, X9
	PUNPCKLQDQ X9, X9
	MOVO X0, X3
	PCMPEQB X8, X3
	MOVO X0, X4
	PCMPEQB X9, X4
	POR  X4, X3
	PMOVMSKB X3, CX

	// digits: (b - '0') <=u 9
	MOVQ $0x3030303030303030, DX
	MOVQ DX, X8
	PUNPCKLQDQ X8, X8
	MOVQ $0x0909090909090909, DX
	MOVQ DX, X9
	PUNPCKLQDQ X9, X9
	MOVO X0, X4
	PSUBB X8, X4
	MOVO X4, X5
	PMAXUB X9, X5
	PCMPEQB X9, X5
	PMOVMSKB X5, DX

	ORL AX, DX
	ORL BX, DX
	ORL CX, DX

	MOVL AX, decimal+8(FP)
	MOVL BX, exponent+12(FP)
	MOVL CX, sign+16(FP)
	MOVL DX, valid+20(FP)
	RET
