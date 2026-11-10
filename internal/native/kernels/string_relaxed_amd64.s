//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·sr_quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·sr_quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·sr_quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·sr_quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·sr_quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·sr_slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sr_slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sr_slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sr_slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·sr_slash_vec<>(SB), RODATA|NOPTR, $32

// func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringRelaxed(SB), NOSPLIT, $0-48
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	XORQ R11, R11         // consumed offset
	MOVQ $-1, R12         // escape pos (-1 if none)
	XORQ R13, R13         // carry (0 or 1)
	TESTQ DX, DX
	JZ   sr_eof_short

	CMPQ DX, $32
	JB   sr_short_entry
	CMPB ·hasAVX2(SB), $0
	JE   sr_sse_init

	VMOVDQU ·sr_quote_vec<>(SB), Y8
	VMOVDQU ·sr_slash_vec<>(SB), Y9
	JMP  sr_block

sr_sse_init:
	MOVOU ·sr_quote_vec<>(SB), X8
	MOVOU ·sr_slash_vec<>(SB), X9

sr_block:
	CMPB ·hasAVX2(SB), $0
	JE   sr_sse_block
	CMPQ DX, $64
	JB   sr_tail_avx

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPCMPEQB Y8, Y0, Y2
	VPMOVMSKB Y2, AX
	VPCMPEQB Y8, Y1, Y2
	VPMOVMSKB Y2, BX
	SHLQ $32, BX
	ORQ  BX, AX

	VPCMPEQB Y9, Y0, Y2
	VPMOVMSKB Y2, R8
	VPCMPEQB Y9, Y1, Y2
	VPMOVMSKB Y2, BX
	SHLQ $32, BX
	ORQ  BX, R8

	MOVQ $64, R9
	ADDQ $64, SI
	JMP  sr_classify_avx

sr_tail_avx:
	XORQ AX, AX
	XORQ R8, R8
	XORQ R9, R9
	CMPQ DX, $32
	JB   sr_tail_16_avx
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y8, Y0, Y2
	VPMOVMSKB Y2, AX
	VPCMPEQB Y9, Y0, Y2
	VPMOVMSKB Y2, R8
	MOVQ $32, R9
	JMP  sr_tail_16_avx

sr_sse_block:
	XORQ AX, AX
	XORQ R8, R8
	XORQ R9, R9

sr_tail_16_avx:
	CMPQ R9, $64
	JAE  sr_tail_done_avx
	MOVQ DX, CX
	SUBQ R9, CX
	CMPQ CX, $16
	JB   sr_tail_scalar_avx
	MOVOU (SI)(R9*1), X0
	MOVO X0, X4
	PCMPEQB X8, X4
	PMOVMSKB X4, BX
	MOVQ R9, CX
	SHLQ CL, BX
	ORQ  BX, AX

	MOVO X0, X4
	PCMPEQB X9, X4
	PMOVMSKB X4, BX
	SHLQ CL, BX
	ORQ  BX, R8

	ADDQ $16, R9
	JMP  sr_tail_16_avx

sr_tail_scalar_avx:
	CMPQ R9, $64
	JAE  sr_tail_done_avx
	CMPQ R9, DX
	JAE  sr_tail_done_avx

sr_tail_loop_avx:
	MOVBLZX (SI)(R9*1), BX
	MOVQ $1, R10
	MOVQ R9, CX
	SHLQ CL, R10
	CMPB BL, $'"'
	JNE  sr_tail_not_q_avx
	ORQ  R10, AX
sr_tail_not_q_avx:
	CMPB BL, $'\\'
	JNE  sr_tail_next_avx
	ORQ  R10, R8
sr_tail_next_avx:
	INCQ R9
	CMPQ R9, DX
	JB   sr_tail_loop_avx

sr_tail_done_avx:
	ADDQ R9, SI

sr_classify_avx:
	MOVQ R8, R10
	ORQ  R13, R10
	JNZ  sr_classify_slash_avx
	MOVQ AX, DI
	TESTQ DI, DI
	JNZ  sr_found_avx
	JMP  sr_next_block_avx

sr_classify_slash_avx:
	MOVQ R13, BX
	NOTQ BX
	ANDQ R8, BX
	MOVQ BX, R10
	SHLQ $1, R10
	ORQ  R13, R10
	MOVQ R10, DI
	NOTQ DI
	ANDQ BX, DI
	MOVQ $0xaaaaaaaaaaaaaaaa, CX
	ANDQ CX, DI
	ADDQ BX, DI
	SHLQ $1, DI
	MOVQ $0x5555555555555555, CX
	XORQ CX, DI
	ANDQ R10, DI
	MOVQ DI, R10
	NOTQ R10
	MOVQ AX, DI
	ANDQ R10, DI

	CMPQ R12, $0
	JGE  sr_quote_avx
	TESTQ R8, R8
	JZ   sr_quote_avx
	BSFQ R8, BX
	TESTQ DI, DI
	JZ   sr_set_escape_avx
	BSFQ DI, CX
	CMPQ BX, CX
	JAE  sr_quote_avx
sr_set_escape_avx:
	LEAQ (R11)(BX*1), R12

sr_quote_avx:
	TESTQ DI, DI
	JNZ  sr_found_avx
	MOVQ R8, R13
	ANDQ R10, R13
	LEAQ -1(R9), CX
	SHRQ CL, R13
	ANDQ $1, R13

sr_next_block_avx:
	ADDQ R9, R11
	SUBQ R9, DX
	JNZ  sr_block

	CMPB ·hasAVX2(SB), $0
	JE   sr_eof_AfterBlock
	VZEROUPPER
sr_eof_AfterBlock:
	MOVQ size+8(FP), AX
	MOVQ AX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ AX, errorPos+32(FP)
	MOVQ $1, err+40(FP)
	RET

sr_found_avx:
	CMPB ·hasAVX2(SB), $0
	JE   sr_found_AfterVzero
	VZEROUPPER
sr_found_AfterVzero:
	BSFQ DI, BX
	ADDQ R11, BX
	INCQ BX
	MOVQ BX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ $-1, errorPos+32(FP)
	MOVQ $0, err+40(FP)
	RET

// Short < 32B path: uses only 128-bit XMM registers (no VZEROUPPER needed).
sr_short_entry:
	XORQ AX, AX
	XORQ R8, R8
	XORQ R9, R9
	CMPQ DX, $16
	JB   sr_short_scalar
	MOVOU ·sr_quote_vec<>(SB), X8
	MOVOU ·sr_slash_vec<>(SB), X9
	MOVOU 0(SI), X0
	MOVO X0, X4
	PCMPEQB X8, X4
	PMOVMSKB X4, AX
	MOVO X0, X4
	PCMPEQB X9, X4
	PMOVMSKB X4, R8
	MOVQ $16, R9
	CMPQ R9, DX
	JAE  sr_short_classify

sr_short_scalar:
	MOVBLZX (SI)(R9*1), BX
	MOVQ $1, R10
	MOVQ R9, CX
	SHLQ CL, R10
	CMPB BL, $'"'
	JNE  sr_short_not_q
	ORQ  R10, AX
sr_short_not_q:
	CMPB BL, $'\\'
	JNE  sr_short_next
	ORQ  R10, R8
sr_short_next:
	INCQ R9
	CMPQ R9, DX
	JB   sr_short_scalar

sr_short_classify:
	TESTQ R8, R8
	JNZ  sr_short_slash
	TESTQ AX, AX
	JNZ  sr_short_found
	JMP  sr_eof_short

sr_short_slash:
	MOVQ R8, BX
	MOVQ BX, R10
	SHLQ $1, R10
	MOVQ R10, DI
	NOTQ DI
	ANDQ BX, DI
	MOVQ $0xaaaaaaaaaaaaaaaa, CX
	ANDQ CX, DI
	ADDQ BX, DI
	SHLQ $1, DI
	MOVQ $0x5555555555555555, CX
	XORQ CX, DI
	ANDQ R10, DI
	NOTQ DI
	ANDQ AX, DI

	BSFQ R8, BX
	TESTQ DI, DI
	JZ   sr_short_set_esc
	BSFQ DI, CX
	CMPQ BX, CX
	JAE  sr_short_found_di
sr_short_set_esc:
	MOVQ BX, R12
	TESTQ DI, DI
	JZ   sr_eof_short
sr_short_found_di:
	MOVQ DI, AX

sr_short_found:
	BSFQ AX, BX
	INCQ BX
	MOVQ BX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ $-1, errorPos+32(FP)
	MOVQ $0, err+40(FP)
	RET

sr_eof_short:
	MOVQ DX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ DX, errorPos+32(FP)
	MOVQ $1, err+40(FP)
	RET


// func scanStringRelaxedBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringRelaxedBlock(SB), NOSPLIT, $0-64
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	XORQ R11, R11         // consumed offset
	MOVQ $-1, R12         // escape pos (-1 if none)
	XORQ R13, R13         // carry (0 or 1)
	TESTQ DX, DX
	JZ   srb_eof_short

	CMPQ DX, $32
	JB   srb_short_entry
	CMPB ·hasAVX2(SB), $0
	JE   srb_sse_init

	VMOVDQU ·sr_quote_vec<>(SB), Y8
	VMOVDQU ·sr_slash_vec<>(SB), Y9
	JMP  srb_block

srb_sse_init:
	MOVOU ·sr_quote_vec<>(SB), X8
	MOVOU ·sr_slash_vec<>(SB), X9

srb_block:
	CMPB ·hasAVX2(SB), $0
	JE   srb_sse_block
	CMPQ DX, $64
	JB   srb_tail_avx

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPCMPEQB Y8, Y0, Y2
	VPMOVMSKB Y2, AX
	VPCMPEQB Y8, Y1, Y2
	VPMOVMSKB Y2, BX
	SHLQ $32, BX
	ORQ  BX, AX

	VPCMPEQB Y9, Y0, Y2
	VPMOVMSKB Y2, R8
	VPCMPEQB Y9, Y1, Y2
	VPMOVMSKB Y2, BX
	SHLQ $32, BX
	ORQ  BX, R8

	MOVQ $64, R9
	ADDQ $64, SI
	JMP  srb_classify_avx

srb_tail_avx:
	XORQ AX, AX
	XORQ R8, R8
	XORQ R9, R9
	CMPQ DX, $32
	JB   srb_tail_16_avx
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y8, Y0, Y2
	VPMOVMSKB Y2, AX
	VPCMPEQB Y9, Y0, Y2
	VPMOVMSKB Y2, R8
	MOVQ $32, R9
	JMP  srb_tail_16_avx

srb_sse_block:
	XORQ AX, AX
	XORQ R8, R8
	XORQ R9, R9

srb_tail_16_avx:
	CMPQ R9, $64
	JAE  srb_tail_done_avx
	MOVQ DX, CX
	SUBQ R9, CX
	CMPQ CX, $16
	JB   srb_tail_scalar_avx
	MOVOU (SI)(R9*1), X0
	MOVO X0, X4
	PCMPEQB X8, X4
	PMOVMSKB X4, BX
	MOVQ R9, CX
	SHLQ CL, BX
	ORQ  BX, AX

	MOVO X0, X4
	PCMPEQB X9, X4
	PMOVMSKB X4, BX
	SHLQ CL, BX
	ORQ  BX, R8

	ADDQ $16, R9
	JMP  srb_tail_16_avx

srb_tail_scalar_avx:
	CMPQ R9, $64
	JAE  srb_tail_done_avx
	CMPQ R9, DX
	JAE  srb_tail_done_avx

srb_tail_loop_avx:
	MOVBLZX (SI)(R9*1), BX
	MOVQ $1, R10
	MOVQ R9, CX
	SHLQ CL, R10
	CMPB BL, $'"'
	JNE  srb_tail_not_q_avx
	ORQ  R10, AX
srb_tail_not_q_avx:
	CMPB BL, $'\\'
	JNE  srb_tail_next_avx
	ORQ  R10, R8
srb_tail_next_avx:
	INCQ R9
	CMPQ R9, DX
	JB   srb_tail_loop_avx

srb_tail_done_avx:
	ADDQ R9, SI

srb_classify_avx:
	MOVQ R8, R10
	ORQ  R13, R10
	JNZ  srb_classify_slash_avx
	MOVQ AX, DI
	TESTQ DI, DI
	JNZ  srb_found_avx
	JMP  srb_next_block_avx

srb_classify_slash_avx:
	MOVQ R13, BX
	NOTQ BX
	ANDQ R8, BX
	MOVQ BX, R10
	SHLQ $1, R10
	ORQ  R13, R10
	MOVQ R10, DI
	NOTQ DI
	ANDQ BX, DI
	MOVQ $0xaaaaaaaaaaaaaaaa, CX
	ANDQ CX, DI
	ADDQ BX, DI
	SHLQ $1, DI
	MOVQ $0x5555555555555555, CX
	XORQ CX, DI
	ANDQ R10, DI
	MOVQ DI, R10
	NOTQ R10
	MOVQ AX, DI
	ANDQ R10, DI

	CMPQ R12, $0
	JGE  srb_quote_avx
	TESTQ R8, R8
	JZ   srb_quote_avx
	BSFQ R8, BX
	TESTQ DI, DI
	JZ   srb_set_escape_avx
	BSFQ DI, CX
	CMPQ BX, CX
	JAE  srb_quote_avx
srb_set_escape_avx:
	LEAQ (R11)(BX*1), R12

srb_quote_avx:
	TESTQ DI, DI
	JNZ  srb_found_avx
	MOVQ R8, R13
	ANDQ R10, R13
	LEAQ -1(R9), CX
	SHRQ CL, R13
	ANDQ $1, R13

srb_next_block_avx:
	ADDQ R9, R11
	SUBQ R9, DX
	JNZ  srb_block

	CMPB ·hasAVX2(SB), $0
	JE   srb_eof_AfterBlock
	VZEROUPPER
srb_eof_AfterBlock:
	MOVQ size+8(FP), AX
	MOVQ $0, tailEvents+48(FP)
	MOVQ $0, tailWidth+56(FP)
	MOVQ AX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ AX, errorPos+32(FP)
	MOVQ $1, err+40(FP)
	RET

srb_found_avx:
	CMPB ·hasAVX2(SB), $0
	JE   srb_found_AfterVzero
	VZEROUPPER
srb_found_AfterVzero:
	BSFQ DI, BX
	ORQ  R8, AX
	MOVQ BX, CX
	SHRQ $1, AX
	SHRQ CL, AX
	LEAQ 1(BX), CX
	SUBQ CX, R9
	MOVQ AX, tailEvents+48(FP)
	MOVQ R9, tailWidth+56(FP)
	ADDQ R11, BX
	INCQ BX
	MOVQ BX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ $-1, errorPos+32(FP)
	MOVQ $0, err+40(FP)
	RET

// Short < 32B path: uses only 128-bit XMM registers (no VZEROUPPER needed).
srb_short_entry:
	XORQ AX, AX
	XORQ R8, R8
	XORQ R9, R9
	CMPQ DX, $16
	JB   srb_short_scalar
	MOVOU ·sr_quote_vec<>(SB), X8
	MOVOU ·sr_slash_vec<>(SB), X9
	MOVOU 0(SI), X0
	MOVO X0, X4
	PCMPEQB X8, X4
	PMOVMSKB X4, AX
	MOVO X0, X4
	PCMPEQB X9, X4
	PMOVMSKB X4, R8
	MOVQ $16, R9
	CMPQ R9, DX
	JAE  srb_short_classify

srb_short_scalar:
	MOVBLZX (SI)(R9*1), BX
	MOVQ $1, R10
	MOVQ R9, CX
	SHLQ CL, R10
	CMPB BL, $'"'
	JNE  srb_short_not_q
	ORQ  R10, AX
srb_short_not_q:
	CMPB BL, $'\\'
	JNE  srb_tail_next_s
	ORQ  R10, R8
srb_tail_next_s:
	INCQ R9
	CMPQ R9, DX
	JB   srb_short_scalar

srb_short_classify:
	MOVQ AX, DI
	TESTQ R8, R8
	JNZ  srb_short_slash
	TESTQ DI, DI
	JNZ  srb_short_found
	JMP  srb_eof_short

srb_short_slash:
	MOVQ R8, BX
	MOVQ BX, R10
	SHLQ $1, R10
	MOVQ R10, DI
	NOTQ DI
	ANDQ BX, DI
	MOVQ $0xaaaaaaaaaaaaaaaa, CX
	ANDQ CX, DI
	ADDQ BX, DI
	SHLQ $1, DI
	MOVQ $0x5555555555555555, CX
	XORQ CX, DI
	ANDQ R10, DI
	NOTQ DI
	ANDQ AX, DI

	BSFQ R8, BX
	TESTQ DI, DI
	JZ   srb_short_set_esc
	BSFQ DI, CX
	CMPQ BX, CX
	JAE  srb_short_found
srb_short_set_esc:
	MOVQ BX, R12
	TESTQ DI, DI
	JZ   srb_eof_short

srb_short_found:
	BSFQ DI, BX
	ORQ  R8, AX
	LEAQ 1(BX), CX
	SUBQ CX, DX
	SHRQ CL, AX
	MOVQ AX, tailEvents+48(FP)
	MOVQ DX, tailWidth+56(FP)
	INCQ BX
	MOVQ BX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ $-1, errorPos+32(FP)
	MOVQ $0, err+40(FP)
	RET

srb_eof_short:
	MOVQ $0, tailEvents+48(FP)
	MOVQ $0, tailWidth+56(FP)
	MOVQ DX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ DX, errorPos+32(FP)
	MOVQ $1, err+40(FP)
	RET
