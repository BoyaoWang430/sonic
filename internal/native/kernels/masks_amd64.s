//go:build amd64 && go1.20 && !go1.28

#include "textflag.h"

// 32-byte broadcast constants for AVX2 (also usable as 16-byte XMM constants).
DATA ·ws_lut<>+0(SB)/8, $0x0000000000000020
DATA ·ws_lut<>+8(SB)/8, $0x00000d00000a0900
DATA ·ws_lut<>+16(SB)/8, $0x0000000000000020
DATA ·ws_lut<>+24(SB)/8, $0x00000d00000a0900
GLOBL ·ws_lut<>(SB), RODATA|NOPTR, $32

DATA ·bound_lut<>+0(SB)/8, $0x0000000000000020
DATA ·bound_lut<>+8(SB)/8, $0x00000d2c000a0900
DATA ·bound_lut<>+16(SB)/8, $0x0000000000000020
DATA ·bound_lut<>+24(SB)/8, $0x00000d2c000a0900
GLOBL ·bound_lut<>(SB), RODATA|NOPTR, $32

DATA ·quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·slash_vec<>(SB), RODATA|NOPTR, $32

DATA ·ctrl_vec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·ctrl_vec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·ctrl_vec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·ctrl_vec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL ·ctrl_vec<>(SB), RODATA|NOPTR, $32

DATA ·rbrack_vec<>+0(SB)/8, $0x5d5d5d5d5d5d5d5d
DATA ·rbrack_vec<>+8(SB)/8, $0x5d5d5d5d5d5d5d5d
DATA ·rbrack_vec<>+16(SB)/8, $0x5d5d5d5d5d5d5d5d
DATA ·rbrack_vec<>+24(SB)/8, $0x5d5d5d5d5d5d5d5d
GLOBL ·rbrack_vec<>(SB), RODATA|NOPTR, $32

DATA ·rbrace_vec<>+0(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·rbrace_vec<>+8(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·rbrace_vec<>+16(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·rbrace_vec<>+24(SB)/8, $0x7d7d7d7d7d7d7d7d
GLOBL ·rbrace_vec<>(SB), RODATA|NOPTR, $32

// func nonSpaceMask(src *byte, size int) (mask uint64)
TEXT ·nonSpaceMask(SB), NOSPLIT, $0-24
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	TESTQ DX, DX
	JLE  ns_zero

	CMPQ DX, $64
	JB   ns_short
	CMPB ·hasAVX2(SB), $0
	JE   ns_short

	VMOVDQU ·ws_lut<>(SB), Y8
	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1
	VPSHUFB Y0, Y8, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, AX
	VPSHUFB Y1, Y8, Y3
	VPCMPEQB Y1, Y3, Y3
	VPMOVMSKB Y3, BX
	SHLQ $32, BX
	ORQ  BX, AX
	NOTQ AX
	VZEROUPPER
	MOVQ AX, mask+16(FP)
	RET

ns_short:
	MOVOU ·ws_lut<>(SB), X8
	XORQ R8, R8           // R8 = nonspace bitmask
	XORQ CX, CX           // CX = bit offset (0, 16, 32, 48)

ns_vec_loop:
	MOVQ DX, R9
	SUBQ CX, R9
	CMPQ R9, $16
	JB   ns_tail
	MOVOU (SI)(CX*1), X0
	MOVO X8, X1
	PSHUFB X0, X1
	PCMPEQB X0, X1
	PMOVMSKB X1, AX
	XORL $0xffff, AX
	SHLQ CL, AX
	ORQ  AX, R8
	ADDQ $16, CX
	JMP  ns_vec_loop

ns_tail:
	CMPQ CX, DX
	JAE  ns_done

ns_tail_loop:
	MOVBLZX (SI)(CX*1), AX
	CMPB AL, $' '
	JE   ns_tail_next
	CMPB AL, $'\t'
	JE   ns_tail_next
	CMPB AL, $'\n'
	JE   ns_tail_next
	CMPB AL, $'\r'
	JE   ns_tail_next
	MOVQ $1, BX
	SHLQ CL, BX
	ORQ  BX, R8
ns_tail_next:
	INCQ CX
	CMPQ CX, DX
	JB   ns_tail_loop

ns_done:
	MOVQ R8, mask+16(FP)
	RET

ns_zero:
	MOVQ $0, mask+16(FP)
	RET


// func traversalMasks(src *byte, size int, validate uint64) (events, nonspace uint64)
TEXT ·traversalMasks(SB), NOSPLIT, $0-40
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	MOVQ validate+16(FP), R10
	TESTQ DX, DX
	JLE  tm_zero

	CMPQ DX, $64
	JB   tm_short
	CMPB ·hasAVX2(SB), $0
	JE   tm_short

	VMOVDQU ·ws_lut<>(SB), Y8
	VMOVDQU ·quote_vec<>(SB), Y9
	VMOVDQU ·slash_vec<>(SB), Y10

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPSHUFB Y0, Y8, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, AX
	VPSHUFB Y1, Y8, Y3
	VPCMPEQB Y1, Y3, Y3
	VPMOVMSKB Y3, BX
	SHLQ $32, BX
	ORQ  BX, AX
	NOTQ AX               // AX = 64-bit nonspace mask

	VPCMPEQB Y9, Y0, Y2
	VPCMPEQB Y10, Y0, Y4
	VPOR Y4, Y2, Y2

	VPCMPEQB Y9, Y1, Y3
	VPCMPEQB Y10, Y1, Y4
	VPOR Y4, Y3, Y3

	TESTQ R10, R10
	JZ   tm_pack64
	VMOVDQU ·ctrl_vec<>(SB), Y11
	VPMAXUB Y11, Y0, Y4
	VPCMPEQB Y11, Y4, Y4
	VPOR Y4, Y2, Y2
	VPMAXUB Y11, Y1, Y4
	VPCMPEQB Y11, Y4, Y4
	VPOR Y4, Y3, Y3

tm_pack64:
	VPMOVMSKB Y2, CX
	VPMOVMSKB Y3, BX
	SHLQ $32, BX
	ORQ  BX, CX
	VZEROUPPER
	MOVQ CX, events+24(FP)
	MOVQ AX, nonspace+32(FP)
	RET

tm_short:
	MOVOU ·ws_lut<>(SB), X8
	MOVOU ·quote_vec<>(SB), X9
	MOVOU ·slash_vec<>(SB), X10
	MOVOU ·ctrl_vec<>(SB), X11

	XORQ R8, R8           // events
	XORQ R9, R9           // nonspace
	XORQ CX, CX

tm_vec_loop:
	MOVQ DX, R11
	SUBQ CX, R11
	CMPQ R11, $16
	JB   tm_tail
	MOVOU (SI)(CX*1), X0

	MOVO X8, X1
	PSHUFB X0, X1
	PCMPEQB X0, X1
	PMOVMSKB X1, AX
	XORL $0xffff, AX
	SHLQ CL, AX
	ORQ  AX, R9

	MOVO X0, X2
	PCMPEQB X9, X2
	MOVO X0, X3
	PCMPEQB X10, X3
	POR  X3, X2
	TESTQ R10, R10
	JZ   tm_no_ctrl_vec
	MOVO X0, X4
	PMAXUB X11, X4
	PCMPEQB X11, X4
	POR  X4, X2
tm_no_ctrl_vec:
	PMOVMSKB X2, AX
	SHLQ CL, AX
	ORQ  AX, R8

	ADDQ $16, CX
	JMP  tm_vec_loop

tm_tail:
	CMPQ CX, DX
	JAE  tm_done

tm_tail_loop:
	MOVBLZX (SI)(CX*1), AX
	MOVQ $1, BX
	SHLQ CL, BX

	CMPB AL, $' '
	JE   tm_check_event
	CMPB AL, $'\t'
	JE   tm_check_event
	CMPB AL, $'\n'
	JE   tm_check_event
	CMPB AL, $'\r'
	JE   tm_check_event
	ORQ  BX, R9

tm_check_event:
	CMPB AL, $'"'
	JE   tm_set_event
	CMPB AL, $'\\'
	JE   tm_set_event
	TESTQ R10, R10
	JZ   tm_tail_next
	CMPB AL, $0x20
	JAE  tm_tail_next
tm_set_event:
	ORQ  BX, R8
tm_tail_next:
	INCQ CX
	CMPQ CX, DX
	JB   tm_tail_loop

tm_done:
	MOVQ R8, events+24(FP)
	MOVQ R9, nonspace+32(FP)
	RET

tm_zero:
	MOVQ $0, events+24(FP)
	MOVQ $0, nonspace+32(FP)
	RET


// func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64)
TEXT ·pathMasks(SB), NOSPLIT, $0-40
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	TESTQ DX, DX
	JLE  pm_zero

	CMPQ DX, $64
	JB   pm_short
	CMPB ·hasAVX2(SB), $0
	JE   pm_short

	VMOVDQU ·ws_lut<>(SB), Y8
	VMOVDQU ·quote_vec<>(SB), Y9
	VMOVDQU ·slash_vec<>(SB), Y10
	VMOVDQU ·bound_lut<>(SB), Y11
	VMOVDQU ·rbrack_vec<>(SB), Y12
	VMOVDQU ·rbrace_vec<>(SB), Y13

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	// events: '"' or '\\'
	VPCMPEQB Y9, Y0, Y2
	VPCMPEQB Y10, Y0, Y4
	VPOR Y4, Y2, Y2
	VPMOVMSKB Y2, R8

	VPCMPEQB Y9, Y1, Y3
	VPCMPEQB Y10, Y1, Y4
	VPOR Y4, Y3, Y3
	VPMOVMSKB Y3, AX
	SHLQ $32, AX
	ORQ  AX, R8

	// nonspace
	VPSHUFB Y0, Y8, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, R9
	VPSHUFB Y1, Y8, Y3
	VPCMPEQB Y1, Y3, Y3
	VPMOVMSKB Y3, AX
	SHLQ $32, AX
	ORQ  AX, R9
	NOTQ R9

	// boundaries: ws | ',' | ']' | '}'
	VPSHUFB Y0, Y11, Y2
	VPCMPEQB Y0, Y2, Y2
	VPCMPEQB Y12, Y0, Y4
	VPCMPEQB Y13, Y0, Y5
	VPOR Y4, Y2, Y2
	VPOR Y5, Y2, Y2
	VPMOVMSKB Y2, R10

	VPSHUFB Y1, Y11, Y3
	VPCMPEQB Y1, Y3, Y3
	VPCMPEQB Y12, Y1, Y4
	VPCMPEQB Y13, Y1, Y5
	VPOR Y4, Y3, Y3
	VPOR Y5, Y3, Y3
	VPMOVMSKB Y3, AX
	SHLQ $32, AX
	ORQ  AX, R10

	VZEROUPPER
	MOVQ R8, events+16(FP)
	MOVQ R9, nonspace+24(FP)
	MOVQ R10, boundaries+32(FP)
	RET

pm_short:
	MOVOU ·ws_lut<>(SB), X8
	MOVOU ·quote_vec<>(SB), X9
	MOVOU ·slash_vec<>(SB), X10
	MOVOU ·bound_lut<>(SB), X11
	MOVOU ·rbrack_vec<>(SB), X12
	MOVOU ·rbrace_vec<>(SB), X13

	XORQ R8, R8           // events
	XORQ R9, R9           // nonspace
	XORQ R10, R10         // boundaries
	XORQ CX, CX

pm_vec_loop:
	MOVQ DX, R11
	SUBQ CX, R11
	CMPQ R11, $16
	JB   pm_tail
	MOVOU (SI)(CX*1), X0

	MOVO X8, X1
	PSHUFB X0, X1
	PCMPEQB X0, X1
	PMOVMSKB X1, AX
	XORL $0xffff, AX
	SHLQ CL, AX
	ORQ  AX, R9

	MOVO X0, X2
	PCMPEQB X9, X2
	MOVO X0, X3
	PCMPEQB X10, X3
	POR  X3, X2
	PMOVMSKB X2, AX
	SHLQ CL, AX
	ORQ  AX, R8

	MOVO X11, X4
	PSHUFB X0, X4
	PCMPEQB X0, X4
	MOVO X0, X5
	PCMPEQB X12, X5
	POR  X5, X4
	MOVO X0, X5
	PCMPEQB X13, X5
	POR  X5, X4
	PMOVMSKB X4, AX
	SHLQ CL, AX
	ORQ  AX, R10

	ADDQ $16, CX
	JMP  pm_vec_loop

pm_tail:
	CMPQ CX, DX
	JAE  pm_done

pm_tail_loop:
	MOVBLZX (SI)(CX*1), AX
	MOVQ $1, BX
	SHLQ CL, BX

	CMPB AL, $' '
	JE   pm_is_ws
	CMPB AL, $'\t'
	JE   pm_is_ws
	CMPB AL, $'\n'
	JE   pm_is_ws
	CMPB AL, $'\r'
	JE   pm_is_ws

	ORQ  BX, R9
	CMPB AL, $'"'
	JE   pm_is_event
	CMPB AL, $'\\'
	JE   pm_is_event
	CMPB AL, $','
	JE   pm_is_bound
	CMPB AL, $']'
	JE   pm_is_bound
	CMPB AL, $'}'
	JE   pm_is_bound
	JMP  pm_tail_next

pm_is_ws:
	ORQ  BX, R10
	JMP  pm_tail_next

pm_is_event:
	ORQ  BX, R8
	JMP  pm_tail_next

pm_is_bound:
	ORQ  BX, R10

pm_tail_next:
	INCQ CX
	CMPQ CX, DX
	JB   pm_tail_loop

pm_done:
	MOVQ R8, events+16(FP)
	MOVQ R9, nonspace+24(FP)
	MOVQ R10, boundaries+32(FP)
	RET

pm_zero:
	MOVQ $0, events+16(FP)
	MOVQ $0, nonspace+24(FP)
	MOVQ $0, boundaries+32(FP)
	RET
