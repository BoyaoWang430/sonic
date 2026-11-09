//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·runs_ws_lut<>+0(SB)/8, $0x0000000000000020
DATA ·runs_ws_lut<>+8(SB)/8, $0x00000d00000a0900
DATA ·runs_ws_lut<>+16(SB)/8, $0x0000000000000020
DATA ·runs_ws_lut<>+24(SB)/8, $0x00000d00000a0900
GLOBL ·runs_ws_lut<>(SB), RODATA|NOPTR, $32

// func space(src *byte, size int) (n int)
TEXT ·space(SB), NOSPLIT, $0-24
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), CX
	MOVQ SI, DI
	TESTQ CX, CX
	JLE  sp_zero

	MOVBLZX (SI), AX
	CMPB AL, $0x20
	JA   sp_zero
	JE   sp_start_vec
	CMPB AL, $0x09
	JE   sp_start_vec
	CMPB AL, $0x0a
	JE   sp_start_vec
	CMPB AL, $0x0d
	JNE  sp_zero

sp_start_vec:
	CMPQ CX, $16
	JB   sp_scalar
	CMPQ CX, $32
	JB   sp_sse_loop
	CMPB ·hasAVX2(SB), $0
	JNE  sp_avx2_init

sp_sse_loop:
	MOVOU ·runs_ws_lut<>(SB), X4
	MOVOU (SI), X0
	PSHUFB X0, X4
	PCMPEQB X0, X4
	PMOVMSKB X4, AX
	XORL $0xffff, AX
	JNZ  sp_found16
	ADDQ $16, SI
	SUBQ $16, CX
	JZ   sp_done_no_avx
	CMPQ CX, $16
	JAE  sp_sse_loop
	// Overlapping final 16B load within the original >=16B buffer
	LEAQ -16(SI)(CX*1), SI
	MOVOU ·runs_ws_lut<>(SB), X4
	MOVOU (SI), X0
	PSHUFB X0, X4
	PCMPEQB X0, X4
	PMOVMSKB X4, AX
	XORL $0xffff, AX
	JNZ  sp_found16
	ADDQ $16, SI
	JMP  sp_done_no_avx

sp_avx2_init:
	VMOVDQU ·runs_ws_lut<>(SB), Y4

sp_loop64:
	CMPQ CX, $64
	JB   sp_loop32
	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1
	VPSHUFB Y0, Y4, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, AX
	XORL $0xffffffff, AX
	JNZ  sp_found32_avx
	VPSHUFB Y1, Y4, Y3
	VPCMPEQB Y1, Y3, Y3
	VPMOVMSKB Y3, AX
	XORL $0xffffffff, AX
	JNZ  sp_found64_hi_avx
	ADDQ $64, SI
	SUBQ $64, CX
	JMP  sp_loop64

sp_loop32:
	CMPQ CX, $32
	JB   sp_tail_avx
	VMOVDQU 0(SI), Y0
	VPSHUFB Y0, Y4, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, AX
	XORL $0xffffffff, AX
	JNZ  sp_found32_avx
	ADDQ $32, SI
	SUBQ $32, CX

sp_tail_avx:
	TESTQ CX, CX
	JZ   sp_done_avx
	// Overlapping final 32B load within original >=32B buffer
	LEAQ -32(SI)(CX*1), SI
	VMOVDQU 0(SI), Y0
	VPSHUFB Y0, Y4, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, AX
	XORL $0xffffffff, AX
	JNZ  sp_found32_avx
	ADDQ $32, SI
	JMP  sp_done_avx

sp_found64_hi_avx:
	ADDQ $32, SI
sp_found32_avx:
	BSFL AX, AX
	ADDQ AX, SI
sp_done_avx:
	VZEROUPPER
	SUBQ DI, SI
	MOVQ SI, n+16(FP)
	RET

sp_found16:
	BSFL AX, AX
	ADDQ AX, SI
	SUBQ DI, SI
	MOVQ SI, n+16(FP)
	RET

sp_scalar:
	MOVBLZX (SI), AX
	CMPB AL, $0x20
	JA   sp_done_no_avx
	JE   sp_next
	CMPB AL, $0x09
	JE   sp_next
	CMPB AL, $0x0a
	JE   sp_next
	CMPB AL, $0x0d
	JNE  sp_done_no_avx
sp_next:
	INCQ SI
	DECQ CX
	JNZ  sp_scalar

sp_done_no_avx:
	SUBQ DI, SI
	MOVQ SI, n+16(FP)
	RET

sp_zero:
	MOVQ $0, n+16(FP)
	RET
