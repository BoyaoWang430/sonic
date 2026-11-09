//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA decodeSimpleEscapes<>+34(SB)/1, $34
DATA decodeSimpleEscapes<>+47(SB)/1, $47
DATA decodeSimpleEscapes<>+92(SB)/1, $92
DATA decodeSimpleEscapes<>+98(SB)/1, $8
DATA decodeSimpleEscapes<>+102(SB)/1, $12
DATA decodeSimpleEscapes<>+110(SB)/1, $10
DATA decodeSimpleEscapes<>+114(SB)/1, $13
DATA decodeSimpleEscapes<>+116(SB)/1, $9
GLOBL decodeSimpleEscapes<>(SB), RODATA|NOPTR, $128

DATA decodeHexDigits<>+48(SB)/8, $0x1716151413121110
DATA decodeHexDigits<>+56(SB)/2, $0x1918
DATA decodeHexDigits<>+65(SB)/8, $0x00001f1e1d1c1b1a
DATA decodeHexDigits<>+97(SB)/8, $0x00001f1e1d1c1b1a
GLOBL decodeHexDigits<>(SB), RODATA|NOPTR, $256

DATA ·sd_slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sd_slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sd_slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sd_slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·sd_slash_vec<>(SB), RODATA|NOPTR, $32

DATA ·sd_quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·sd_quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·sd_quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·sd_quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·sd_quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·sd_ctrl_vec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·sd_ctrl_vec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·sd_ctrl_vec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·sd_ctrl_vec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL ·sd_ctrl_vec<>(SB), RODATA|NOPTR, $32

// func stringDecode(src *byte, size int, dst *byte, mode uint64) (end, written, escaped, errorPos, code int)
TEXT ·stringDecode(SB), NOSPLIT, $32-72
	MOVQ BP, 24(SP)               // 24(SP) = saved BP (use BP instead of R14 so g is untouched)
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), CX
	MOVQ dst+16(FP), DI
	MOVQ mode+24(FP), R8
	MOVQ SI, 0(SP)                // 0(SP) = input base
	MOVQ DI, 8(SP)                // 8(SP) = output base
	MOVQ CX, 16(SP)               // 16(SP) = original length
	XORQ R9, R9                   // R9 = sawEscape = 0

	CMPQ CX, $32
	JB   sd_short_init
	CMPB ·hasAVX2(SB), $0
	JE   sd_short_init

	VMOVDQU ·sd_slash_vec<>(SB), Y5
	VMOVDQU ·sd_quote_vec<>(SB), Y6
	VMOVDQU ·sd_ctrl_vec<>(SB), Y7
	JMP  sd_scan

sd_short_init:
	CMPQ CX, $16
	JB   sd_scan_scalar
	MOVOU ·sd_slash_vec<>(SB), X5
	MOVOU ·sd_quote_vec<>(SB), X6
	MOVOU ·sd_ctrl_vec<>(SB), X7
	JMP  sd_scan16

sd_scan:
	CMPQ CX, $64
	JB   sd_scan32

	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPCMPEQB Y5, Y0, Y2
	VPCMPEQB Y5, Y1, Y4
	TESTQ $4, R8
	JZ   sd_vec64_mask
	VPCMPEQB Y6, Y0, Y3; VPOR Y3, Y2, Y2
	VPCMPEQB Y6, Y1, Y3; VPOR Y3, Y4, Y4
	TESTQ $8, R8
	JZ   sd_vec64_mask
	VPMAXUB Y7, Y0, Y3; VPCMPEQB Y7, Y3, Y3; VPOR Y3, Y2, Y2
	VPMAXUB Y7, Y1, Y3; VPCMPEQB Y7, Y3, Y3; VPOR Y3, Y4, Y4

sd_vec64_mask:
	VPMOVMSKB Y2, AX
	VPMOVMSKB Y4, DX
	MOVL AX, BX
	ORL  DX, BX
	JNZ  sd_vec64_event
	CMPQ SI, DI
	JE   sd_vec64_advance
	VMOVDQU Y0, 0(DI)
	VMOVDQU Y1, 32(DI)
sd_vec64_advance:
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $64, CX
	JMP  sd_scan

sd_vec64_event:
	TESTL AX, AX
	JNZ   sd_vec32_event
	CMPQ  SI, DI
	JE    sd_vec64_second
	VMOVDQU Y0, 0(DI)
sd_vec64_second:
	ADDQ  $32, SI
	ADDQ  $32, DI
	SUBQ  $32, CX
	VMOVDQA Y1, Y0
	MOVL  DX, AX
	JMP   sd_vec32_event

sd_scan32:
	CMPQ CX, $32
	JB   sd_scan16

	VMOVDQU 0(SI), Y0
	VPCMPEQB Y5, Y0, Y2
	TESTQ $4, R8
	JZ   sd_vec32_mask
	VPCMPEQB Y6, Y0, Y3; VPOR Y3, Y2, Y2
	TESTQ $8, R8
	JZ   sd_vec32_mask
	VPMAXUB Y7, Y0, Y3; VPCMPEQB Y7, Y3, Y3; VPOR Y3, Y2, Y2

sd_vec32_mask:
	VPMOVMSKB Y2, AX
	TESTL AX, AX
	JNZ   sd_vec32_event
	CMPQ  SI, DI
	JE    sd_vec32_advance
	VMOVDQU Y0, 0(DI)
sd_vec32_advance:
	ADDQ  $32, SI
	ADDQ  $32, DI
	SUBQ  $32, CX
	JMP   sd_scan

sd_vec32_event:
	BSFL AX, BX                   // BX = plain prefix length (0..31)
	CMPQ BX, $16
	JB   sd_vec16_prefix
	CMPQ SI, DI
	JE   sd_vec32_event_advance16
	VMOVDQU X0, 0(DI)
	VEXTRACTI128 $1, Y0, X0
sd_vec32_event_advance16:
	ADDQ $16, SI
	ADDQ $16, DI
	SUBQ $16, CX
	SUBQ $16, BX
	JMP  sd_vec16_prefix

sd_scan16:
	CMPQ CX, $16
	JB   sd_scan_scalar

	MOVOU (SI), X0
	MOVO  X0, X2
	PCMPEQB X5, X2
	TESTQ $4, R8
	JZ    sd_vec16_mask
	MOVO  X0, X3; PCMPEQB X6, X3; POR X3, X2
	TESTQ $8, R8
	JZ    sd_vec16_mask
	MOVO  X0, X3; PMAXUB X7, X3; PCMPEQB X7, X3; POR X3, X2

sd_vec16_mask:
	PMOVMSKB X2, AX
	TESTL AX, AX
	JNZ   sd_vec_event
	CMPQ  SI, DI
	JE    sd_vec16_advance
	MOVOU X0, (DI)
sd_vec16_advance:
	ADDQ  $16, SI
	ADDQ  $16, DI
	SUBQ  $16, CX
	JMP   sd_scan16

sd_vec_event:
	BSFL AX, BX                   // BX = plain prefix length (0..15)
sd_vec16_prefix:
	TESTQ BX, BX
	JZ   sd_special
	CMPQ SI, DI
	JNE  sd_vec_prefix
	ADDQ BX, SI
	ADDQ BX, DI
	SUBQ BX, CX
	JMP  sd_special

sd_vec_prefix:
	MOVQ DI, R12
	MOVQ X0, AX
	TESTQ $8, BX
	JZ   sd_vec_prefix4
	MOVQ AX, (R12)
	ADDQ $8, R12
	PSRLDQ $8, X0
	MOVQ X0, AX
sd_vec_prefix4:
	TESTQ $4, BX
	JZ   sd_vec_prefix2
	MOVL AX, (R12)
	SHRQ $32, AX
	ADDQ $4, R12
sd_vec_prefix2:
	TESTQ $2, BX
	JZ   sd_vec_prefix1
	MOVW AX, (R12)
	SHRQ $16, AX
	ADDQ $2, R12
sd_vec_prefix1:
	TESTQ $1, BX
	JZ   sd_vec_prefix_done
	MOVB AX, (R12)
sd_vec_prefix_done:
	ADDQ BX, SI
	ADDQ BX, DI
	SUBQ BX, CX
	JMP  sd_special

sd_scan_scalar:
	TESTQ CX, CX
	JZ   sd_input_end
	CMPQ CX, $8
	JB   sd_short

sd_scan_word:
sd_word:
	CMPQ CX, $8
	JB   sd_tail
	MOVQ (SI), R13
	MOVQ $8, BP

sd_word_classify:
	MOVQ $0x5c5c5c5c5c5c5c5c, AX
	XORQ R13, AX
	MOVQ $0x0101010101010101, DX
	MOVQ AX, BX
	SUBQ DX, BX
	NOTQ AX
	ANDQ AX, BX                   // BX = slash SWAR mask

	TESTQ $4, R8
	JZ   sd_word_find
	MOVQ $0x2222222222222222, AX
	XORQ R13, AX
	MOVQ AX, R12
	SUBQ DX, R12
	NOTQ AX
	ANDQ AX, R12
	ORQ  R12, BX

	TESTQ $8, R8
	JZ   sd_word_find
	MOVQ $0x2020202020202020, AX
	MOVQ R13, R12
	SUBQ AX, R12
	MOVQ R13, AX
	NOTQ AX
	ANDQ AX, R12
	ORQ  R12, BX

sd_word_find:
	MOVQ $0x8080808080808080, AX
	ANDQ AX, BX
	JZ   sd_word_copy
	BSFQ BX, BP
	SHRQ $3, BP
	TESTQ BP, BP
	JZ   sd_special

sd_word_copy:
	CMPQ SI, DI
	JE   sd_word_advance
	CMPQ BP, $8
	JNE  sd_word_short_copy
	MOVQ R13, (DI)
sd_word_advance:
	ADDQ BP, SI
	ADDQ BP, DI
	SUBQ BP, CX
	TESTQ BX, BX
	JZ   sd_word

sd_special:
	MOVBLZX (SI), AX
	CMPB AL, $92                  // '\\'
	JE   sd_escape
	CMPB AL, $34                  // '"'
	JE   sd_quote
	// Raw control in parser mode
	MOVQ SI, R12
	SUBQ 0(SP), R12
	MOVQ $1, R13
	JMP  sd_error

sd_escape:
	MOVQ $1, R9                   // sawEscape = 1
	MOVQ SI, R10
	SUBQ 0(SP), R10               // R10 = current escape offset
	CMPQ CX, $2
	JB   sd_eof
	ADDQ $2, SI
	SUBQ $2, CX
	TESTQ $1, R8                  // double-unquote?
	JZ   sd_escape_letter
	TESTQ CX, CX
	JZ   sd_eof
	MOVBLZX -1(SI), AX
	CMPB AL, $92
	JNE  sd_escape_letter
	MOVBLZX (SI), AX
	CMPB AL, $92
	JNE  sd_double_skip
	CMPQ CX, $2
	JB   sd_eof
	MOVBLZX 1(SI), AX
	CMPB AL, $92
	JE   sd_double_extra
	CMPB AL, $34
	JE   sd_double_extra
	MOVQ SI, R12
	SUBQ 0(SP), R12
	ADDQ $1, R12
	MOVQ $2, R13                  // ERR_INVALID_CHAR
	JMP  sd_error

sd_double_extra:
	ADDQ $1, SI
	SUBQ $1, CX
sd_double_skip:
	ADDQ $1, SI
	SUBQ $1, CX

sd_escape_letter:
	MOVBLZX -1(SI), AX
	CMPB AL, $117                 // 'u'
	JE   sd_unicode
	CMPB AL, $128
	JAE  sd_invalid_escape
	LEAQ decodeSimpleEscapes<>(SB), DX
	MOVBLZX (DX)(AX*1), AX
	TESTB AL, AL
	JNZ  sd_emit_byte

sd_invalid_escape:
	TESTQ $4, R8
	JNZ  sd_parser_escape
	MOVQ SI, R12
	SUBQ 0(SP), R12
	SUBQ $1, R12
	MOVQ $3, R13                  // ERR_INVALID_ESCAPE
	JMP  sd_error

sd_parser_escape:
	MOVQ R10, R12
	MOVQ $2, R13
	JMP  sd_error

sd_emit_byte:
	MOVB AL, (DI)
	ADDQ $1, DI

sd_resume:
	TESTQ CX, CX
	JZ   sd_input_end
	MOVBLZX (SI), AX
	CMPB AL, $92
	JE   sd_escape
	CMPQ CX, $2
	JB   sd_tiny_loaded
	MOVBLZX 1(SI), DX
	CMPB DL, $92
	JNE  sd_resume_scan
	TESTQ $4, R8
	JZ   sd_resume_plain
	CMPB AL, $34
	JE   sd_quote
	TESTQ $8, R8
	JZ   sd_resume_plain
	CMPB AL, $32
	JB   sd_special
sd_resume_plain:
	CMPQ SI, DI
	JE   sd_resume_advance
	MOVB AL, (DI)
sd_resume_advance:
	ADDQ $1, SI
	ADDQ $1, DI
	SUBQ $1, CX
	JMP  sd_escape

sd_unicode:
	CMPQ CX, $4
	JB   sd_eof
	MOVQ SI, R12                  // R12 = hex cursor
	XORQ R15, R15                 // R15 = 0 (first UTF-16 unit)

sd_hex_start:
	LEAQ decodeHexDigits<>(SB), BX
	MOVBLZX 0(R12), AX
	MOVBLZX 1(R12), DX
	MOVBLZX 2(R12), R13
	MOVBLZX 3(R12), BP
	MOVBLZX (BX)(AX*1), AX
	MOVBLZX (BX)(DX*1), DX
	MOVBLZX (BX)(R13*1), R13
	MOVBLZX (BX)(BP*1), BP
	MOVQ AX, BX
	ANDQ DX, BX
	ANDQ R13, BX
	ANDQ BP, BX
	TESTQ $16, BX
	JZ   sd_hex_slow
	SHLQ $12, AX
	SHLQ $8, DX
	ADDQ DX, AX
	SHLQ $4, R13
	ADDQ R13, AX
	ADDQ BP, AX
	SUBQ $0x11110, AX             // AX = 16-bit codepoint
	TESTQ R15, R15
	JNZ  sd_second_unit
	MOVQ AX, R11                  // R11 = first codepoint
	ADDQ $4, SI
	SUBQ $4, CX

sd_codepoint:
	CMPQ R11, $0x80
	JB   sd_ascii
	CMPQ R11, $0x800
	JB   sd_two
	CMPQ R11, $0xd800
	JB   sd_three
	CMPQ R11, $0xe000
	JAE  sd_three

	// Surrogate
	TESTQ $1, R8
	JZ   sd_surrogate
	TESTQ CX, CX
	JNZ  sd_double_surrogate
	TESTQ $2, R8
	JZ   sd_eof
	JMP  sd_replace

sd_double_surrogate:
	MOVBLZX (SI), AX
	CMPB AL, $92
	JNE  sd_surrogate
	ADDQ $1, SI
	SUBQ $1, CX

sd_surrogate:
	CMPQ CX, $6
	JB   sd_unpaired
	CMPQ R11, $0xdc00
	JAE  sd_unpaired
	MOVBLZX 0(SI), AX
	CMPB AL, $92
	JNE  sd_unpaired
	MOVBLZX 1(SI), AX
	CMPB AL, $117
	JNE  sd_unpaired
	MOVQ SI, R10
	SUBQ 0(SP), R10
	LEAQ 2(SI), R12
	MOVQ $1, R15
	JMP  sd_hex_start

sd_second_unit:
	ADDQ $6, SI
	SUBQ $6, CX
	CMPQ AX, $0xdc00
	JB   sd_not_low
	CMPQ AX, $0xe000
	JAE  sd_not_low
	SUBQ $0xd800, R11
	SHLQ $10, R11
	SUBQ $0xdc00, AX
	ADDQ $0x10000, AX
	ADDQ AX, R11
	JMP  sd_four

sd_not_low:
	TESTQ $2, R8
	JNZ  sd_replace_then_retry
	TESTQ $4, R8
	JNZ  sd_parser_unicode
	MOVQ SI, R12
	SUBQ 0(SP), R12
	SUBQ $4, R12
	MOVQ $4, R13
	JMP  sd_error

sd_replace_then_retry:
	MOVB $0xef, 0(DI)
	MOVB $0xbf, 1(DI)
	MOVB $0xbd, 2(DI)
	ADDQ $3, DI
	MOVQ AX, R11
	JMP  sd_codepoint

sd_unpaired:
	TESTQ $2, R8
	JNZ  sd_replace
	TESTQ $4, R8
	JNZ  sd_parser_unicode
	MOVQ SI, R12
	SUBQ 0(SP), R12
	SUBQ $4, R12
	MOVQ R8, AX
	ANDQ $1, AX
	SUBQ AX, R12
	MOVQ $4, R13
	JMP  sd_error

sd_replace:
	MOVQ $0xfffd, R11
	JMP  sd_three

sd_hex_slow:
	TESTQ $16, AX
	JZ   sd_bad_hex
	ADDQ $1, R12
	TESTQ $16, DX
	JZ   sd_bad_hex
	ADDQ $1, R12
	TESTQ $16, R13
	JZ   sd_bad_hex
	ADDQ $1, R12
sd_bad_hex:
	TESTQ $4, R8
	JNZ  sd_parser_unicode
	SUBQ 0(SP), R12
	MOVQ $2, R13
	JMP  sd_error

sd_parser_unicode:
	MOVQ R10, R12
	MOVQ $12, R13
	JMP  sd_error

sd_ascii:
	MOVB R11, (DI)
	ADDQ $1, DI
	JMP  sd_resume

sd_two:
	MOVQ R11, AX
	SHRQ $6, AX
	ORQ  $0xc0, AX
	MOVB AX, 0(DI)
	MOVQ R11, AX
	ANDQ $0x3f, AX
	ORQ  $0x80, AX
	MOVB AX, 1(DI)
	ADDQ $2, DI
	JMP  sd_resume

sd_three:
	MOVQ R11, AX
	SHRQ $12, AX
	ORQ  $0xe0, AX
	MOVB AX, 0(DI)
	MOVQ R11, AX
	SHRQ $6, AX
	ANDQ $0x3f, AX
	ORQ  $0x80, AX
	MOVB AX, 1(DI)
	MOVQ R11, AX
	ANDQ $0x3f, AX
	ORQ  $0x80, AX
	MOVB AX, 2(DI)
	ADDQ $3, DI
	JMP  sd_resume

sd_four:
	MOVQ R11, AX
	SHRQ $18, AX
	ORQ  $0xf0, AX
	MOVB AX, 0(DI)
	MOVQ R11, AX
	SHRQ $12, AX
	ANDQ $0x3f, AX
	ORQ  $0x80, AX
	MOVB AX, 1(DI)
	MOVQ R11, AX
	SHRQ $6, AX
	ANDQ $0x3f, AX
	ORQ  $0x80, AX
	MOVB AX, 2(DI)
	MOVQ R11, AX
	ANDQ $0x3f, AX
	ORQ  $0x80, AX
	MOVB AX, 3(DI)
	ADDQ $4, DI
	JMP  sd_resume

sd_tiny:
	MOVBLZX (SI), AX
	CMPB AL, $92
	JE   sd_escape
sd_tiny_loaded:
	TESTQ $4, R8
	JZ   sd_tiny_plain
	CMPB AL, $34
	JE   sd_quote
	TESTQ $8, R8
	JZ   sd_tiny_plain
	CMPB AL, $32
	JB   sd_special
sd_tiny_plain:
	CMPQ SI, DI
	JE   sd_tiny_advance
	MOVB AL, (DI)
sd_tiny_advance:
	ADDQ $1, SI
	ADDQ $1, DI
	SUBQ $1, CX
	JNZ  sd_tiny

sd_input_end:
	TESTQ $4, R8
	JNZ  sd_eof
	MOVQ 16(SP), AX
	JMP  sd_success

sd_quote:
	MOVQ SI, AX
	SUBQ 0(SP), AX
	ADDQ $1, AX
sd_success:
	MOVQ $-1, R12
	XORQ R13, R13
	JMP  sd_done

sd_eof:
	MOVQ 16(SP), R12
	MOVQ $1, R13
	TESTQ $4, R8
	JZ   sd_error
	MOVQ $5, R13
sd_error:
	MOVQ R12, AX
sd_done:
	SUBQ 8(SP), DI
	MOVQ 24(SP), BP
	MOVQ AX, end+32(FP)
	MOVQ DI, written+40(FP)
	MOVQ R9, escaped+48(FP)
	MOVQ R12, errorPos+56(FP)
	MOVQ R13, code+64(FP)
	CMPQ 16(SP), $32
	JB   sd_ret
	CMPB ·hasAVX2(SB), $0
	JE   sd_ret
	VZEROUPPER
sd_ret:
	RET

sd_word_short_copy:
	MOVQ DI, AX
	TESTQ $4, BP
	JZ   sd_wsc2
	MOVL R13, (AX)
	SHRQ $32, R13
	ADDQ $4, AX
sd_wsc2:
	TESTQ $2, BP
	JZ   sd_wsc1
	MOVW R13, (AX)
	SHRQ $16, R13
	ADDQ $2, AX
sd_wsc1:
	TESTQ $1, BP
	JZ   sd_word_advance
	MOVB R13, (AX)
	JMP  sd_word_advance

sd_tail:
	TESTQ CX, CX
	JZ   sd_input_end
	CMPQ CX, $4
	JB   sd_tiny
	LEAQ (SI)(CX*1), AX
	MOVQ -8(AX), R13
	MOVQ CX, BP                   // save CX
	MOVQ $8, AX
	SUBQ CX, AX
	SHLQ $3, AX
	MOVQ AX, CX
	SHRQ CL, R13
	MOVQ $-1, DX
	SHRQ CL, DX
	NOTQ DX
	ORQ  DX, R13
	MOVQ BP, CX                   // restore CX
	JMP  sd_word_classify

sd_short:
	MOVBLZX (SI), AX
	CMPB AL, $92
	JE   sd_escape
sd_short_plain:
	CMPQ CX, $4
	JB   sd_tiny_loaded
	MOVLQZX (SI), R13
	LEAQ (SI)(CX*1), AX
	MOVLQZX -4(AX), DX
	MOVQ CX, BP                   // save CX
	LEAQ -4(BP), AX
	SHLQ $3, AX
	MOVQ AX, CX
	SHLQ CL, DX
	ORQ  DX, R13
	ADDQ $32, CX
	MOVQ $-1, DX
	SHLQ CL, DX
	ORQ  DX, R13
	MOVQ BP, CX                   // restore CX
	JMP  sd_word_classify

sd_resume_scan:
	CMPQ CX, $32
	JB   sd_resume_16
	CMPB ·hasAVX2(SB), $0
	JNE  sd_scan
sd_resume_16:
	CMPQ CX, $16
	JAE  sd_scan16
	CMPQ CX, $8
	JAE  sd_scan_word
	JMP  sd_short_plain
