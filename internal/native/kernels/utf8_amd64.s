//go:build amd64 && go1.20 && !go1.28

#include "textflag.h"

DATA ·utf8_tab1<>+0(SB)/8, $0x0202020202020202
DATA ·utf8_tab1<>+8(SB)/8, $0x4915012180808080
DATA ·utf8_tab1<>+16(SB)/8, $0x0202020202020202
DATA ·utf8_tab1<>+24(SB)/8, $0x4915012180808080
GLOBL ·utf8_tab1<>(SB), RODATA|NOPTR, $32

DATA ·utf8_tab2<>+0(SB)/8, $0xcbcbcb8b8383a3e7
DATA ·utf8_tab2<>+8(SB)/8, $0xcbcbdbcbcbcbcbcb
DATA ·utf8_tab2<>+16(SB)/8, $0xcbcbcb8b8383a3e7
DATA ·utf8_tab2<>+24(SB)/8, $0xcbcbdbcbcbcbcbcb
GLOBL ·utf8_tab2<>(SB), RODATA|NOPTR, $32

DATA ·utf8_tab3<>+0(SB)/8, $0x0101010101010101
DATA ·utf8_tab3<>+8(SB)/8, $0x01010101babaaee6
DATA ·utf8_tab3<>+16(SB)/8, $0x0101010101010101
DATA ·utf8_tab3<>+24(SB)/8, $0x01010101babaaee6
GLOBL ·utf8_tab3<>(SB), RODATA|NOPTR, $32

DATA ·utf8_0f<>+0(SB)/8, $0x0f0f0f0f0f0f0f0f
DATA ·utf8_0f<>+8(SB)/8, $0x0f0f0f0f0f0f0f0f
DATA ·utf8_0f<>+16(SB)/8, $0x0f0f0f0f0f0f0f0f
DATA ·utf8_0f<>+24(SB)/8, $0x0f0f0f0f0f0f0f0f
GLOBL ·utf8_0f<>(SB), RODATA|NOPTR, $32

DATA ·utf8_df<>+0(SB)/8, $0xdfdfdfdfdfdfdfdf
DATA ·utf8_df<>+8(SB)/8, $0xdfdfdfdfdfdfdfdf
DATA ·utf8_df<>+16(SB)/8, $0xdfdfdfdfdfdfdfdf
DATA ·utf8_df<>+24(SB)/8, $0xdfdfdfdfdfdfdfdf
GLOBL ·utf8_df<>(SB), RODATA|NOPTR, $32

DATA ·utf8_ef<>+0(SB)/8, $0xefefefefefefefef
DATA ·utf8_ef<>+8(SB)/8, $0xefefefefefefefef
DATA ·utf8_ef<>+16(SB)/8, $0xefefefefefefefef
DATA ·utf8_ef<>+24(SB)/8, $0xefefefefefefefef
GLOBL ·utf8_ef<>(SB), RODATA|NOPTR, $32

DATA ·utf8_80<>+0(SB)/8, $0x8080808080808080
DATA ·utf8_80<>+8(SB)/8, $0x8080808080808080
DATA ·utf8_80<>+16(SB)/8, $0x8080808080808080
DATA ·utf8_80<>+24(SB)/8, $0x8080808080808080
GLOBL ·utf8_80<>(SB), RODATA|NOPTR, $32

// func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool)
// SI: src
// DX: size (remaining bytes)
// R8: base (current absolute offset)
// DI: positions (*int)
// R9: limit (remaining output slots)
// R10: count (offsets written)
TEXT ·utf8Scan(SB), NOSPLIT, $0-57
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	MOVQ base+16(FP), R8
	MOVQ positions+24(FP), DI
	MOVQ limit+32(FP), R9
	XORQ R10, R10

	CMPQ DX, $32
	JB   utf8_vec16
	CMPB ·hasAVX2(SB), $0
	JE   utf8_vec16

	VMOVDQU ·utf8_0f<>(SB), Y6
	VMOVDQU ·utf8_tab1<>(SB), Y7
	VMOVDQU ·utf8_tab2<>(SB), Y8
	VMOVDQU ·utf8_tab3<>(SB), Y9
	VMOVDQU ·utf8_df<>(SB), Y10
	VMOVDQU ·utf8_ef<>(SB), Y11
	VMOVDQU ·utf8_80<>(SB), Y12

utf8_avx_loop:
	CMPQ DX, $64
	JB   utf8_avx_vec32
	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1
	VPOR Y1, Y0, Y2
	VPMOVMSKB Y2, AX
	TESTL AX, AX
	JNZ  utf8_avx_vec32
	ADDQ $64, SI
	ADDQ $64, R8
	SUBQ $64, DX
	JMP  utf8_avx_loop

utf8_avx_vec32:
	CMPQ DX, $32
	JB   utf8_avx_vec16
	VMOVDQU 0(SI), Y0
	VPMOVMSKB Y0, AX
	TESTL AX, AX
	JNZ  utf8_avx_vec32_nonascii
	ADDQ $32, SI
	ADDQ $32, R8
	SUBQ $32, DX
	JMP  utf8_avx_loop

utf8_avx_vec32_nonascii:
	// AVX2 SIMD multi-byte UTF-8 validation on 32-byte block Y0 (SI is at a rune boundary, prev = 0).
	// Y2 = [low128=0, high128=Y0[127:0]]
	VPERM2I128 $0x08, Y0, Y0, Y2
	// Y1 = prev1 = shift right by 1 byte with 0 shifted in
	VPALIGNR $15, Y2, Y0, Y1

	// byte_1_high = VPSHUFB(prev1 >> 4, tab1)
	VPSRLW $4, Y1, Y3
	VPAND Y6, Y3, Y3
	VPSHUFB Y3, Y7, Y4

	// byte_1_low = VPSHUFB(prev1 & 0x0f, tab2)
	VPAND Y6, Y1, Y3
	VPSHUFB Y3, Y8, Y5
	VPAND Y5, Y4, Y4

	// byte_2_high = VPSHUFB(Y0 >> 4, tab3)
	VPSRLW $4, Y0, Y3
	VPAND Y6, Y3, Y3
	VPSHUFB Y3, Y9, Y5
	VPAND Y5, Y4, Y4                // Y4 = sc

	// prev2 and prev3 for 3-byte and 4-byte continuation check
	VPALIGNR $14, Y2, Y0, Y1
	VPALIGNR $13, Y2, Y0, Y3
	VPSUBUSB Y10, Y1, Y1
	VPSUBUSB Y11, Y3, Y3
	VPOR Y3, Y1, Y1
	VPXOR Y5, Y5, Y5
	VPCMPGTB Y5, Y1, Y1
	VPAND Y12, Y1, Y1
	VPXOR Y1, Y4, Y4
	VPTEST Y4, Y4
	JNZ  utf8_avx_vec32_error

	// All complete runes in Y0 are valid UTF-8! Check whether a multi-byte rune straddles byte 31.
	MOVBLZX 31(SI), AX
	CMPB AL, $0x80
	JB   utf8_avx_advance32
	CMPB AL, $0xc0
	JAE  utf8_avx_advance31
	MOVBLZX 30(SI), AX
	CMPB AL, $0xe0
	JAE  utf8_avx_advance30
	CMPB AL, $0xc0
	JAE  utf8_avx_advance32
	MOVBLZX 29(SI), AX
	CMPB AL, $0xf0
	JAE  utf8_avx_advance29

utf8_avx_advance32:
	ADDQ $32, SI
	ADDQ $32, R8
	SUBQ $32, DX
	JMP  utf8_avx_loop

utf8_avx_advance31:
	ADDQ $31, SI
	ADDQ $31, R8
	SUBQ $31, DX
	JMP  utf8_avx_loop

utf8_avx_advance30:
	ADDQ $30, SI
	ADDQ $30, R8
	SUBQ $30, DX
	JMP  utf8_avx_loop

utf8_avx_advance29:
	ADDQ $29, SI
	ADDQ $29, R8
	SUBQ $29, DX
	JMP  utf8_avx_loop

utf8_avx_vec32_error:
	// AX still holds VPMOVMSKB Y0 (non-zero). Skip leading ASCII bytes in Y0.
	BSFL AX, CX
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	JMP  utf8_avx_scalar_nonascii

utf8_avx_vec16:
	CMPQ DX, $16
	JB   utf8_avx_word
	VMOVDQU 0(SI), X0
	VPMOVMSKB X0, AX
	TESTL AX, AX
	JNZ  utf8_avx_vec16_nonascii
	ADDQ $16, SI
	ADDQ $16, R8
	SUBQ $16, DX
	JMP  utf8_avx_vec16

utf8_avx_vec16_nonascii:
	BSFL AX, CX
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	JMP  utf8_avx_scalar_nonascii

utf8_avx_word:
	TESTQ DX, DX
	JZ    utf8_avx_done
	CMPQ  DX, $8
	JB    utf8_avx_tail
	MOVQ  0(SI), AX
	MOVQ  $0x8080808080808080, R11
	ANDQ  R11, AX
	JNZ   utf8_avx_nonascii_word
	ADDQ  $8, SI
	ADDQ  $8, R8
	SUBQ  $8, DX
	JMP   utf8_avx_word

utf8_avx_nonascii_word:
	BSFQ AX, CX
	SHRQ $3, CX
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	JMP  utf8_avx_scalar_nonascii

utf8_avx_scalar:
	TESTQ DX, DX
	JZ    utf8_avx_done
	MOVBLZX 0(SI), AX
	CMPQ  AX, $0x80
	JAE   utf8_avx_scalar_nonascii
	ADDQ  $1, SI
	ADDQ  $1, R8
	SUBQ  $1, DX
	JMP   utf8_avx_loop

utf8_avx_scalar_nonascii:
	CMPQ DX, $4
	JB   utf8_avx_short_nonascii

utf8_avx_multibyte_word:
	MOVL 0(SI), AX
	TESTL $0x80, AX
	JZ   utf8_avx_loop

utf8_avx_multibyte_classify:
	MOVL AX, BX
	ANDL $0x00c0c0f0, BX
	CMPL BX, $0x008080e0
	JNE  utf8_avx_check_two
	MOVL AX, BX
	ANDL $0x200f, BX
	TESTL BX, BX
	JZ   utf8_avx_check_two
	CMPL BX, $0x200d
	JE   utf8_avx_check_two
	JMP  utf8_avx_word_three_valid

utf8_avx_check_two:
	MOVL AX, BX
	ANDL $0xc0e0, BX
	CMPL BX, $0x80c0
	JNE  utf8_avx_check_four
	TESTL $0x1e, AX
	JNZ  utf8_avx_word_two_valid

utf8_avx_check_four:
	MOVL AX, BX
	ANDL $0xc0c0c0f8, BX
	CMPL BX, $0x808080f0
	JNE  utf8_avx_invalid

utf8_avx_word_four_require:
	TESTL $0x3007, AX
	JZ    utf8_avx_invalid
	TESTL $0x04, AX
	JZ    utf8_avx_word_four_valid
	TESTL $0x3003, AX
	JNZ   utf8_avx_invalid

utf8_avx_word_four_valid:
	ADDQ $4, SI
	ADDQ $4, R8
	SUBQ $4, DX
	CMPQ DX, $4
	JB   utf8_avx_multibyte_tail
	MOVL 0(SI), AX
	TESTL $0x80, AX
	JZ   utf8_avx_loop
	MOVL AX, BX
	ANDL $0xc0c0c0f8, BX
	CMPL BX, $0x808080f0
	JE   utf8_avx_word_four_require
	JMP  utf8_avx_multibyte_classify

utf8_avx_word_two_valid:
	MOVQ $2, CX
	JMP  utf8_avx_multibyte_advance

utf8_avx_word_three_valid:
	MOVQ $3, CX

utf8_avx_multibyte_advance:
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	CMPQ DX, $4
	JAE  utf8_avx_multibyte_word

utf8_avx_multibyte_tail:
	TESTQ DX, DX
	JZ    utf8_avx_done
	JMP   utf8_avx_scalar

utf8_avx_short_nonascii:
	MOVBLZX 0(SI), AX
	CMPQ AX, $0xc2
	JB   utf8_avx_invalid
	CMPQ AX, $0xe0
	JB   utf8_avx_two
	CMPQ AX, $0xf0
	JB   utf8_avx_three
	CMPQ AX, $0xf5
	JB   utf8_avx_four
	JMP  utf8_avx_invalid

utf8_avx_two:
	CMPQ DX, $2
	JB   utf8_avx_invalid
	MOVBLZX 1(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_avx_invalid
	MOVQ $2, CX
	JMP  utf8_avx_advance

utf8_avx_three:
	CMPQ DX, $3
	JB   utf8_avx_invalid
	MOVBLZX 1(SI), BX
	MOVQ BX, R11
	SUBQ $0x80, R11
	CMPQ R11, $0x40
	JAE  utf8_avx_invalid
	CMPQ AX, $0xe0
	JNE  utf8_avx_three_surrogate
	CMPQ BX, $0xa0
	JB   utf8_avx_invalid
utf8_avx_three_surrogate:
	CMPQ AX, $0xed
	JNE  utf8_avx_three_last
	CMPQ BX, $0xa0
	JAE  utf8_avx_invalid
utf8_avx_three_last:
	MOVBLZX 2(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_avx_invalid
	MOVQ $3, CX
	JMP  utf8_avx_advance

utf8_avx_four:
	CMPQ DX, $4
	JB   utf8_avx_invalid
	MOVBLZX 1(SI), BX
	MOVQ BX, R11
	SUBQ $0x80, R11
	CMPQ R11, $0x40
	JAE  utf8_avx_invalid
	CMPQ AX, $0xf0
	JNE  utf8_avx_four_max
	CMPQ BX, $0x90
	JB   utf8_avx_invalid
utf8_avx_four_max:
	CMPQ AX, $0xf4
	JNE  utf8_avx_four_tail
	CMPQ BX, $0x90
	JAE  utf8_avx_invalid
utf8_avx_four_tail:
	MOVBLZX 2(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_avx_invalid
	MOVBLZX 3(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_avx_invalid
	MOVQ $4, CX
	JMP  utf8_avx_advance

utf8_avx_invalid:
	TESTQ R9, R9
	JZ    utf8_avx_full
	MOVQ  R8, 0(DI)
	ADDQ  $8, DI
	SUBQ  $1, R9
	ADDQ  $1, R10
	MOVQ  $1, CX

utf8_avx_advance:
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	TESTQ DX, DX
	JZ   utf8_avx_done
	MOVBLZX 0(SI), AX
	CMPQ AX, $0x80
	JAE  utf8_avx_scalar_nonascii
	JMP  utf8_avx_loop

utf8_avx_tail:
	LEAQ (SI)(DX*1), AX
	MOVQ -8(AX), AX
	MOVQ $8, CX
	SUBQ DX, CX
	SHLQ $3, CX
	SHRQ CX, AX
	MOVQ $0x8080808080808080, R11
	ANDQ R11, AX
	JNZ  utf8_avx_nonascii_word
	ADDQ DX, R8
	JMP  utf8_avx_done

utf8_avx_full:
	MOVB $1, full+56(FP)
	JMP  utf8_avx_return

utf8_avx_done:
	MOVB $0, full+56(FP)

utf8_avx_return:
	MOVQ R8, next+40(FP)
	MOVQ R10, count+48(FP)
	VZEROUPPER
	RET

// Short path (< 32B): uses only X0 and scalar registers, no VZEROUPPER needed.
utf8_vec16:
	CMPQ DX, $16
	JB   utf8_word
	MOVOU 0(SI), X0
	PMOVMSKB X0, AX
	TESTL AX, AX
	JNZ  utf8_vec16_nonascii
	ADDQ $16, SI
	ADDQ $16, R8
	SUBQ $16, DX
	JMP  utf8_vec16

utf8_vec16_nonascii:
	BSFL AX, CX
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	JMP  utf8_scalar_nonascii

utf8_word:
	TESTQ DX, DX
	JZ    utf8_done
	CMPQ  DX, $8
	JB    utf8_tail
	MOVQ  0(SI), AX
	MOVQ  $0x8080808080808080, R11
	ANDQ  R11, AX
	JNZ   utf8_nonascii_word
	ADDQ  $8, SI
	ADDQ  $8, R8
	SUBQ  $8, DX
	JMP   utf8_word

utf8_nonascii_word:
	BSFQ AX, CX
	SHRQ $3, CX
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	JMP  utf8_scalar_nonascii

utf8_scalar:
	TESTQ DX, DX
	JZ    utf8_done
	MOVBLZX 0(SI), AX
	CMPQ  AX, $0x80
	JAE   utf8_scalar_nonascii
	ADDQ  $1, SI
	ADDQ  $1, R8
	SUBQ  $1, DX
	JMP   utf8_vec16

utf8_scalar_nonascii:
	CMPQ DX, $4
	JB   utf8_short_nonascii

utf8_multibyte_word:
	MOVL 0(SI), AX
	TESTL $0x80, AX
	JZ   utf8_vec16

utf8_multibyte_classify:
	// 3-byte UTF-8 check: (AX & 0x00c0c0f0) == 0x008080e0
	MOVL AX, BX
	ANDL $0x00c0c0f0, BX
	CMPL BX, $0x008080e0
	JNE  utf8_check_two
	MOVL AX, BX
	ANDL $0x200f, BX
	TESTL BX, BX
	JZ   utf8_check_two
	CMPL BX, $0x200d
	JE   utf8_check_two
	JMP  utf8_word_three_valid

utf8_check_two:
	// 2-byte UTF-8 check: (AX & 0xc0e0) == 0x80c0 && (AX & 0x1e) != 0
	MOVL AX, BX
	ANDL $0xc0e0, BX
	CMPL BX, $0x80c0
	JNE  utf8_check_four
	TESTL $0x1e, AX
	JNZ  utf8_word_two_valid

utf8_check_four:
	// 4-byte UTF-8 check: (AX & 0xc0c0c0f8) == 0x808080f0
	MOVL AX, BX
	ANDL $0xc0c0c0f8, BX
	CMPL BX, $0x808080f0
	JNE  utf8_invalid

utf8_word_four_require:
	TESTL $0x3007, AX
	JZ    utf8_invalid
	TESTL $0x04, AX
	JZ    utf8_word_four_valid
	TESTL $0x3003, AX
	JNZ   utf8_invalid

utf8_word_four_valid:
	ADDQ $4, SI
	ADDQ $4, R8
	SUBQ $4, DX
	CMPQ DX, $4
	JB   utf8_multibyte_tail
	MOVL 0(SI), AX
	TESTL $0x80, AX
	JZ   utf8_vec16
	MOVL AX, BX
	ANDL $0xc0c0c0f8, BX
	CMPL BX, $0x808080f0
	JE   utf8_word_four_require
	JMP  utf8_multibyte_classify

utf8_word_two_valid:
	MOVQ $2, CX
	JMP  utf8_multibyte_advance

utf8_word_three_valid:
	MOVQ $3, CX

utf8_multibyte_advance:
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	CMPQ DX, $4
	JAE  utf8_multibyte_word

utf8_multibyte_tail:
	TESTQ DX, DX
	JZ    utf8_done
	JMP   utf8_scalar

utf8_short_nonascii:
	MOVBLZX 0(SI), AX
	CMPQ AX, $0xc2
	JB   utf8_invalid
	CMPQ AX, $0xe0
	JB   utf8_two
	CMPQ AX, $0xf0
	JB   utf8_three
	CMPQ AX, $0xf5
	JB   utf8_four
	JMP  utf8_invalid

utf8_two:
	CMPQ DX, $2
	JB   utf8_invalid
	MOVBLZX 1(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_invalid
	MOVQ $2, CX
	JMP  utf8_advance

utf8_three:
	CMPQ DX, $3
	JB   utf8_invalid
	MOVBLZX 1(SI), BX
	MOVQ BX, R11
	SUBQ $0x80, R11
	CMPQ R11, $0x40
	JAE  utf8_invalid
	CMPQ AX, $0xe0
	JNE  utf8_three_surrogate
	CMPQ BX, $0xa0
	JB   utf8_invalid
utf8_three_surrogate:
	CMPQ AX, $0xed
	JNE  utf8_three_last
	CMPQ BX, $0xa0
	JAE  utf8_invalid
utf8_three_last:
	MOVBLZX 2(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_invalid
	MOVQ $3, CX
	JMP  utf8_advance

utf8_four:
	CMPQ DX, $4
	JB   utf8_invalid
	MOVBLZX 1(SI), BX
	MOVQ BX, R11
	SUBQ $0x80, R11
	CMPQ R11, $0x40
	JAE  utf8_invalid
	CMPQ AX, $0xf0
	JNE  utf8_four_max
	CMPQ BX, $0x90
	JB   utf8_invalid
utf8_four_max:
	CMPQ AX, $0xf4
	JNE  utf8_four_tail
	CMPQ BX, $0x90
	JAE  utf8_invalid
utf8_four_tail:
	MOVBLZX 2(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_invalid
	MOVBLZX 3(SI), BX
	SUBQ $0x80, BX
	CMPQ BX, $0x40
	JAE  utf8_invalid
	MOVQ $4, CX
	JMP  utf8_advance

utf8_invalid:
	TESTQ R9, R9
	JZ    utf8_full
	MOVQ  R8, 0(DI)
	ADDQ  $8, DI
	SUBQ  $1, R9
	ADDQ  $1, R10
	MOVQ  $1, CX

utf8_advance:
	ADDQ CX, SI
	ADDQ CX, R8
	SUBQ CX, DX
	TESTQ DX, DX
	JZ   utf8_done
	MOVBLZX 0(SI), AX
	CMPQ AX, $0x80
	JAE  utf8_scalar_nonascii
	JMP  utf8_vec16

utf8_tail:
	CMPQ size+8(FP), $8
	JB   utf8_scalar
	LEAQ (SI)(DX*1), AX
	MOVQ -8(AX), AX
	MOVQ $8, CX
	SUBQ DX, CX
	SHLQ $3, CX
	SHRQ CX, AX
	MOVQ $0x8080808080808080, R11
	ANDQ R11, AX
	JNZ  utf8_nonascii_word
	ADDQ DX, R8
	JMP  utf8_done

utf8_full:
	MOVB $1, full+56(FP)
	JMP  utf8_return

utf8_done:
	MOVB $0, full+56(FP)

utf8_return:
	MOVQ R8, next+40(FP)
	MOVQ R10, count+48(FP)
	RET
