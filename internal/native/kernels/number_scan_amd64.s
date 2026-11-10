//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·num_zero_vec<>+0(SB)/8, $0x3030303030303030
DATA ·num_zero_vec<>+8(SB)/8, $0x3030303030303030
DATA ·num_zero_vec<>+16(SB)/8, $0x3030303030303030
DATA ·num_zero_vec<>+24(SB)/8, $0x3030303030303030
GLOBL ·num_zero_vec<>(SB), RODATA|NOPTR, $32

DATA ·num_nine_vec<>+0(SB)/8, $0x0909090909090909
DATA ·num_nine_vec<>+8(SB)/8, $0x0909090909090909
DATA ·num_nine_vec<>+16(SB)/8, $0x0909090909090909
DATA ·num_nine_vec<>+24(SB)/8, $0x0909090909090909
GLOBL ·num_nine_vec<>(SB), RODATA|NOPTR, $32

// PSHUFB table for [0]=0x20 ( ), [9]=0x09 (t), [10]=0x0a (n), [12]=0x2c (,), [13]=0x0d (r).
DATA ·num_delim_lut<>+0(SB)/8, $0x0000000000000020
DATA ·num_delim_lut<>+8(SB)/8, $0x00000d2c000a0900
DATA ·num_delim_lut<>+16(SB)/8, $0x0000000000000020
DATA ·num_delim_lut<>+24(SB)/8, $0x00000d2c000a0900
GLOBL ·num_delim_lut<>(SB), RODATA|NOPTR, $32

DATA ·num_rbrack_vec<>+0(SB)/8, $0x5d5d5d5d5d5d5d5d
DATA ·num_rbrack_vec<>+8(SB)/8, $0x5d5d5d5d5d5d5d5d
DATA ·num_rbrack_vec<>+16(SB)/8, $0x5d5d5d5d5d5d5d5d
DATA ·num_rbrack_vec<>+24(SB)/8, $0x5d5d5d5d5d5d5d5d
GLOBL ·num_rbrack_vec<>(SB), RODATA|NOPTR, $32

DATA ·num_rbrace_vec<>+0(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·num_rbrace_vec<>+8(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·num_rbrace_vec<>+16(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·num_rbrace_vec<>+24(SB)/8, $0x7d7d7d7d7d7d7d7d
GLOBL ·num_rbrace_vec<>(SB), RODATA|NOPTR, $32

// func numberScan(src uintptr, size int, mode int) (end int, err int)
TEXT ·numberScan(SB), NOSPLIT, $0-40
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), CX
	MOVQ mode+16(FP), DX
	XORQ R8, R8           // R8 = relative offset (end)

	CMPQ DX, $2
	JE   num_skip_init
	TESTQ CX, CX
	JLE  num_eof

	MOVBLZX (SI), AX
	CMPB AL, $45          // -
	JNE  num_first_digit
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	TESTQ CX, CX
	JZ   num_eof
	MOVBLZX (SI), AX

num_first_digit:
	MOVQ AX, BX
	SUBQ $48, BX
	CMPQ BX, $9
	JA   num_invalid
	TESTQ DX, DX
	JNZ  num_strict_init
	XORQ R9, R9           // R9 = phase = 0 (prefix integer)
	CMPB AL, $48          // 0
	JNE  num_digits
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JMP  num_prefix_integer_done

num_strict_init:
	CMPB AL, $48          // 0
	JNE  num_strict_markers
	CMPQ CX, $1
	JE   num_strict_zero_done
	MOVBLZX 1(SI), AX
	CMPB AL, $46          // .
	JE   num_strict_markers
	MOVQ AX, BX
	ORQ  $32, BX
	CMPB BL, $101         // e or E
	JE   num_strict_markers
num_strict_zero_done:
	ADDQ $1, R8
	JMP  num_success

num_strict_markers:
	MOVQ $3, R9           // R9 = phase = 3 (strict integer)
	MOVQ $-1, R12         // dot pos
	MOVQ $-1, R13         // exp pos
	MOVQ $-1, DX          // sign pos (DX instead of R15 so dynamic linking does not clobber it)

num_digits:
	TESTQ CX, CX
	JZ   num_digits_done
	CMPQ CX, $8
	JLT  num_digit_tail

	MOVQ (SI), AX
	MOVQ $0x3030303030303030, R10
	MOVQ $0x4646464646464646, R11
	MOVQ AX, DI
	SUBQ R10, DI
	MOVQ AX, BX
	ADDQ R11, BX
	ORQ  AX, DI
	ORQ  BX, DI
	MOVQ $0x8080808080808080, R10
	ANDQ R10, DI
	JNZ  num_word_event

	ADDQ $8, SI
	SUBQ $8, CX
	ADDQ $8, R8
	CMPQ CX, $16
	JLT  num_digits
	CMPQ CX, $32
	JLT  num_vector16_init
	CMPB ·hasAVX2(SB), $0
	JE   num_vector16_init

	VMOVDQU ·num_zero_vec<>(SB), Y4
	VMOVDQU ·num_nine_vec<>(SB), Y5

num_vector32_digits:
	VMOVDQU (SI), Y0
	VPSUBB Y4, Y0, Y0
	VPMAXUB Y5, Y0, Y1
	VPCMPEQB Y5, Y1, Y1
	VPMOVMSKB Y1, AX
	XORL  $0xffffffff, AX
	JNZ   num_vector32_event
	ADDQ  $32, SI
	SUBQ  $32, CX
	ADDQ  $32, R8
	CMPQ  CX, $32
	JGE   num_vector32_digits
	VZEROUPPER
	CMPQ  CX, $16
	JLT   num_digits

num_vector16_init:
	MOVOU ·num_zero_vec<>(SB), X4
	MOVOU ·num_nine_vec<>(SB), X5

num_vector16_digits:
	MOVOU (SI), X0
	PSUBB X4, X0
	MOVO  X0, X1
	PMAXUB X5, X1
	PCMPEQB X5, X1
	PMOVMSKB X1, AX
	XORL  $0xffff, AX
	JNZ   num_vector16_event
	ADDQ  $16, SI
	SUBQ  $16, CX
	ADDQ  $16, R8
	CMPQ  CX, $16
	JGE   num_vector16_digits
	JMP   num_digits

num_word_event:
	BSFQ DI, DI
	SHRQ $3, DI
	JMP  num_digit_event_at

num_vector32_event:
	VZEROUPPER
	BSFL AX, DI
	JMP  num_digit_event_at

num_vector16_event:
	BSFL AX, DI

num_digit_event_at:
	ADDQ DI, SI
	SUBQ DI, CX
	ADDQ DI, R8
	MOVBLZX (SI), AX
	JMP  num_digit_event

num_digit_tail:
	MOVBLZX (SI), AX
	MOVQ AX, BX
	SUBQ $48, BX
	CMPQ BX, $9
	JA   num_digit_event
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JNZ  num_digit_tail

num_digits_done:
	CMPQ R9, $4
	JE   num_strict_finish
	JMP  num_success

num_digit_event:
	CMPQ R9, $3
	JAE  num_strict_event
	TESTQ R9, R9
	JZ   num_prefix_integer_event
	CMPQ R9, $1
	JE   num_prefix_fraction_event
	JMP  num_success

num_prefix_integer_done:
	TESTQ CX, CX
	JZ   num_success
	MOVBLZX (SI), AX

num_prefix_integer_event:
	CMPB AL, $46          // .
	JNE  num_prefix_fraction_event
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JZ   num_eof
	MOVBLZX (SI), AX
	MOVQ AX, BX
	SUBQ $48, BX
	CMPQ BX, $9
	JA   num_invalid
	MOVQ $1, R9
	JMP  num_digits

num_prefix_fraction_event:
	MOVQ AX, BX
	ORQ  $32, BX
	CMPB BL, $101         // e or E
	JNE  num_success
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JZ   num_eof
	MOVBLZX (SI), AX
	CMPB AL, $43          // +
	JE   num_prefix_exp_sign
	CMPB AL, $45          // -
	JNE  num_prefix_exp_digit
num_prefix_exp_sign:
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JZ   num_eof
	MOVBLZX (SI), AX
num_prefix_exp_digit:
	MOVQ AX, BX
	SUBQ $48, BX
	CMPQ BX, $9
	JA   num_invalid
	MOVQ $2, R9
	JMP  num_digits

num_strict_event:
	CMPB AL, $46          // .
	JE   num_strict_dot
	MOVQ AX, BX
	ORQ  $32, BX
	CMPB BL, $101         // e or E
	JE   num_strict_exp
	CMPB AL, $43          // +
	JE   num_strict_sign
	CMPB AL, $45          // -
	JNE  num_strict_finish

num_strict_sign:
	TESTQ DX, DX
	JGE  num_invalid
	MOVQ R8, DX
	JMP  num_strict_advance

num_strict_dot:
	TESTQ R12, R12
	JGE  num_invalid
	MOVQ R8, R12
	JMP  num_strict_advance

num_strict_exp:
	TESTQ R13, R13
	JGE  num_invalid
	MOVQ R8, R13
	MOVQ $4, R9
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JZ   num_strict_finish
	MOVBLZX (SI), AX
	CMPB AL, $43          // +
	JE   num_strict_sign
	CMPB AL, $45          // -
	JE   num_strict_sign
	JMP  num_digits

num_strict_advance:
	MOVQ $4, R9
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JMP  num_digits

num_strict_finish:
	CMPQ R9, $3
	JE   num_success
	LEAQ -1(R8), BX
	CMPQ R12, BX
	JE   num_strict_trailing
	CMPQ R13, BX
	JE   num_strict_trailing
	CMPQ DX, BX
	JE   num_strict_trailing
	TESTQ DX, DX
	JLT  num_strict_dot_order
	LEAQ -1(DX), DI
	CMPQ R13, DI
	JNE  num_strict_bad_sign
num_strict_dot_order:
	TESTQ R12, R12
	JLT  num_success
	TESTQ R13, R13
	JLT  num_success
	CMPQ R12, R13
	JGE  num_strict_bad_dot
	LEAQ -1(R13), BX
	CMPQ R12, BX
	JE   num_strict_bad_exp
	JMP  num_success

num_strict_trailing:
	MOVQ BX, R8
	JMP  num_invalid
num_strict_bad_sign:
	MOVQ DX, R8
	JMP  num_invalid
num_strict_bad_dot:
	MOVQ R12, R8
	JMP  num_invalid
num_strict_bad_exp:
	MOVQ R13, R8
	JMP  num_invalid

num_success:
	XORQ AX, AX
	JMP  num_done
num_eof:
	MOVQ $1, AX           // types.ERR_EOF
	JMP  num_done
num_invalid:
	MOVQ $2, AX           // types.ERR_INVALID_CHAR
num_done:
	MOVQ R8, end+24(FP)
	MOVQ AX, err+32(FP)
	RET

// Mode 2: scan until JSON number boundary ( , t, n, r, ,, ], })
num_skip_init:
	TESTQ CX, CX
	JLE  num_success
	CMPQ CX, $16
	JLT  num_skip_tail
	CMPQ CX, $32
	JLT  num_skip16_init
	CMPB ·hasAVX2(SB), $0
	JE   num_skip16_init

	VMOVDQU ·num_delim_lut<>(SB), Y7
	VMOVDQU ·num_rbrack_vec<>(SB), Y5
	VMOVDQU ·num_rbrace_vec<>(SB), Y6

num_skip_block32:
	VMOVDQU (SI), Y0
	VPSHUFB Y0, Y7, Y1
	VPCMPEQB Y0, Y1, Y1
	VPCMPEQB Y5, Y0, Y2
	VPCMPEQB Y6, Y0, Y3
	VPOR  Y2, Y1, Y1
	VPOR  Y3, Y1, Y1
	VPMOVMSKB Y1, AX
	TESTL AX, AX
	JNZ   num_skip_found32
	ADDQ  $32, SI
	SUBQ  $32, CX
	ADDQ  $32, R8
	CMPQ  CX, $32
	JGE   num_skip_block32
	VZEROUPPER
	TESTQ CX, CX
	JZ    num_success
	CMPQ  CX, $16
	JLT   num_skip_tail

num_skip16_init:
	MOVOU ·num_delim_lut<>(SB), X7
	MOVOU ·num_rbrack_vec<>(SB), X5
	MOVOU ·num_rbrace_vec<>(SB), X6

num_skip_block16:
	MOVOU (SI), X0
	MOVO  X7, X1
	PSHUFB X0, X1
	PCMPEQB X0, X1
	MOVO  X0, X2
	PCMPEQB X5, X2
	PCMPEQB X6, X0
	POR   X2, X1
	POR   X0, X1
	PMOVMSKB X1, AX
	TESTL AX, AX
	JNZ   num_skip_found16
	ADDQ  $16, SI
	SUBQ  $16, CX
	ADDQ  $16, R8
	CMPQ  CX, $16
	JGE   num_skip_block16
	TESTQ CX, CX
	JZ    num_success

num_skip_tail:
	MOVBLZX (SI), AX
	CMPB AL, $44          // ,
	JE   num_success
	CMPB AL, $93          // ]
	JE   num_success
	CMPB AL, $125         // }
	JE   num_success
	CMPB AL, $32          //  
	JE   num_success
	CMPB AL, $9           // t
	JE   num_success
	CMPB AL, $10          // n
	JE   num_success
	CMPB AL, $13          // r
	JE   num_success
	ADDQ $1, SI
	ADDQ $1, R8
	SUBQ $1, CX
	JNZ  num_skip_tail
	JMP  num_success

num_skip_found32:
	VZEROUPPER
num_skip_found16:
	BSFL AX, AX
	ADDQ AX, R8
	JMP  num_success
