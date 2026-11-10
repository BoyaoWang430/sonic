//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·sc_quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·sc_quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·sc_quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·sc_quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·sc_quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·sc_slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sc_slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sc_slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·sc_slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·sc_slash_vec<>(SB), RODATA|NOPTR, $32

// func skipContainer(src uintptr, size int, open uint64) (end int, err int)
TEXT ·skipContainer(SB), NOSPLIT, $16-40
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), AX
	MOVQ AX, 0(SP)                // 0(SP) = original size
	TESTQ AX, AX
	JLE  sc_eof_no_avx

	MOVQ open+16(FP), DX          // DX = open byte
	LEAQ 2(DX), BX                // BX = close byte (open + 2)

	XORQ R8, R8                   // R8 = block offset = 0
	MOVQ $1, R9                  // R9 = depth = 1
	XORQ R10, R10                 // R10 = odd backslash carry (0 or 1)
	XORQ R11, R11                 // R11 = entry string state (0 or -1)

	CMPB ·hasAVX2(SB), $0
	JE   sc_sse_check_short
	MOVQ 0(SP), R15               // R15 = remaining length
	CMPQ R15, $32
	JB   sc_avx_short

	VMOVDQU ·sc_quote_vec<>(SB), Y4
	VMOVDQU ·sc_slash_vec<>(SB), Y5
	MOVQ 0(SP), R15
	VMOVQ DX, X6
	VPBROADCASTB X6, Y6           // Y6 = broadcast open byte
	VMOVQ BX, X7
	VPBROADCASTB X7, Y7           // Y7 = broadcast close byte
	VPXMM0:
	VPCMPEQB X8, X8, X8           // X8 = all-ones for PCLMULQDQ prefix XOR

sc_block:
	CMPQ R15, $64
	JB   sc_partial_block

	MOVQ $64, R12
	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1
	ADDQ $64, SI

	// qmask in R13, smask in CX
	VPCMPEQB Y4, Y0, Y2
	VPCMPEQB Y4, Y1, Y3
	VPCMPEQB Y5, Y0, Y9
	VPCMPEQB Y5, Y1, Y10
	VPMOVMSKB Y2, R13
	VPMOVMSKB Y3, AX
	VPMOVMSKB Y9, CX
	VPMOVMSKB Y10, DI
	SHLQ $32, AX
	SHLQ $32, DI
	ORQ  AX, R13
	ORQ  DI, CX

	// omask in DX, cmask in BX
	VPCMPEQB Y6, Y0, Y2
	VPCMPEQB Y6, Y1, Y3
	VPCMPEQB Y7, Y0, Y9
	VPCMPEQB Y7, Y1, Y10
	VPMOVMSKB Y2, DX
	VPMOVMSKB Y3, AX
	VPMOVMSKB Y9, BX
	VPMOVMSKB Y10, DI
	SHLQ $32, AX
	SHLQ $32, DI
	ORQ  AX, DX
	ORQ  DI, BX

sc_classify:
	MOVQ CX, AX
	ORQ  R10, AX
	JNZ  sc_slash_slow

sc_quotes:
	MOVQ DX, AX
	ORQ  BX, AX
	JZ   sc_no_brackets

	TESTQ R13, R13
	JNZ  sc_quote_prefix
	TESTQ R11, R11
	JNZ  sc_advance
	JMP  sc_brackets

sc_no_brackets:
	TESTQ R13, R13
	JZ   sc_advance
	POPCNTQ R13, AX
	ANDQ $1, AX
	NEGQ AX
	XORQ AX, R11
	JMP  sc_advance

sc_quote_prefix:
	VMOVQ R13, X2
	PCLMULQDQ $0, X8, X2
	VMOVQ X2, R13
	XORQ R11, R13                 // R13 = inString mask
	MOVQ R13, R11
	SARQ $63, R11                 // new entry string state (0 or -1)
	NOTQ R13                      // ~inString
	ANDQ R13, DX                  // omask &= ~inString
	ANDQ R13, BX                  // cmask &= ~inString

sc_brackets:
	POPCNTQ BX, AX                // AX = popcount(cmask)
	CMPQ AX, R9
	JAE  sc_close_loop

sc_count:
	POPCNTQ DX, CX
	ADDQ CX, R9
	SUBQ AX, R9

sc_advance:
	ADDQ R12, R8
	SUBQ R12, R15
	JNZ  sc_block

	CMPQ 0(SP), $32
	JB   sc_eof_no_avx
	VZEROUPPER
sc_eof_no_avx:
	MOVQ 0(SP), AX
	MOVQ AX, end+24(FP)
	MOVQ $1, err+32(FP)          // types.ERR_EOF
	RET

sc_close_loop:
	LEAQ -1(BX), CX               // CX = cmask - 1
	MOVQ DX, AX
	ANDQ CX, AX                   // AX = opens before lowest close
	POPCNTQ AX, DI
	ADDQ DI, R9                   // depth += popcount(opens_before_close)
	NOTQ AX
	ANDQ AX, DX                   // omask &= ~opens_before_close
	SUBQ $1, R9                  // depth -= 1
	JZ   sc_success
	ANDQ CX, BX                   // cmask &= (cmask - 1)
	JNZ  sc_close_loop
	POPCNTQ DX, CX
	ADDQ CX, R9
	JMP  sc_advance

sc_success:
	CMPQ 0(SP), $32
	JB   sc_success_no_avx
	VZEROUPPER
sc_success_no_avx:
	BSFQ BX, AX
	ADDQ R8, AX
	ADDQ $1, AX
	MOVQ AX, end+24(FP)
	MOVQ $0, err+32(FP)
	RET

sc_slash_slow:
	MOVQ R10, AX
	NOTQ AX
	ANDQ CX, AX                   // AX = sm = smask & ~carry
	MOVQ AX, DI
	SHLQ $1, DI
	ORQ  R10, DI                  // DI = fe = (sm << 1) | carry
	MOVQ DI, R10
	NOTQ R10
	ANDQ AX, R10
	MOVQ $0xaaaaaaaaaaaaaaaa, R12
	ANDQ R12, R10                 // R10 = os = (sm & ~fe) & oddMask
	ADDQ AX, R10                  // R10 = os + sm
	SHLQ $1, R10
	MOVQ $0x5555555555555555, AX
	XORQ AX, R10
	ANDQ DI, R10                  // R10 = escaped mask
	MOVQ R10, AX
	NOTQ AX
	ANDQ AX, R13                  // qmask &= ~escaped
	ANDQ AX, CX                   // unescaped_slashes = smask & ~escaped
	MOVQ $64, R12
	CMPQ R15, R12
	CMOVQLT R15, R12              // R12 = blockWidth
	MOVQ CX, R10
	LEAQ -1(R12), CX
	SHRQ CL, R10
	ANDQ $1, R10
	JMP  sc_quotes

sc_partial_block:
	MOVQ R15, R12                 // R12 = blockWidth (< 64)
	MOVQ R12, DI                  // DI = remaining bytes in block
	XORQ R13, R13                 // R13 = qmask
	MOVQ $0, 8(SP)               // 8(SP) = smask
	XORQ DX, DX                   // DX = omask
	XORQ BX, BX                   // BX = cmask
	XORQ CX, CX                   // CX = bit shift in block

	CMPQ DI, $32
	JB   sc_avx_load16
	VMOVDQU (SI), Y0
	VPCMPEQB Y4, Y0, Y1; VPMOVMSKB Y1, R13
	VPCMPEQB Y5, Y0, Y1; VPMOVMSKB Y1, AX; MOVQ AX, 8(SP)
	VPCMPEQB Y6, Y0, Y1; VPMOVMSKB Y1, DX
	VPCMPEQB Y7, Y0, Y1; VPMOVMSKB Y1, BX
	ADDQ $32, SI
	ADDQ $32, CX
	SUBQ $32, DI
	JZ   sc_partial_done

sc_avx_load16:
	CMPQ DI, $16
	JLT  sc_avx_tail
	VMOVDQU (SI), X0
	VPCMPEQB X4, X0, X1; VPMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, R13
	VPCMPEQB X5, X0, X1; VPMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, 8(SP)
	VPCMPEQB X6, X0, X1; VPMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, DX
	VPCMPEQB X7, X0, X1; VPMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, BX
	ADDQ $16, SI
	ADDQ $16, CX
	SUBQ $16, DI
	JZ   sc_partial_done

sc_avx_tail:
	MOVQ $1, AX
	SHLQ CL, AX
	MOVBLZX (SI), R12
	CMPB R12B, $0x22
	JNE  sc_avx_tail_slash
	ORQ  AX, R13
sc_avx_tail_slash:
	CMPB R12B, $0x5c
	JNE  sc_avx_tail_open
	ORQ  AX, 8(SP)
sc_avx_tail_open:
	CMPB R12B, open+16(FP)
	JNE  sc_avx_tail_close
	ORQ  AX, DX
sc_avx_tail_close:
	SUBB open+16(FP), R12B
	CMPB R12B, $2
	JNE  sc_avx_tail_next
	ORQ  AX, BX
sc_avx_tail_next:
	ADDQ $1, SI
	ADDQ $1, CX
	SUBQ $1, DI
	JNZ  sc_avx_tail
	MOVQ R15, R12

sc_partial_done:
	MOVQ 8(SP), CX                // CX = smask
	JMP  sc_classify

// ============================================================================
// Pure SSE2 + scalar fallback path (< 32B short inputs OR non-AVX2 CPUs)
// Uses zero AVX2, zero SSE4.1, zero PCLMULQDQ, and zero POPCNTQ instructions.
// ============================================================================
sc_avx_short:
	MOVQ R15, R12
	MOVQ R12, DI
	XORQ R13, R13
	MOVQ $0, 8(SP)
	XORQ CX, CX
	CMPQ DI, $16
	JLT  sc_avx_short_tail
	VMOVDQU ·sc_quote_vec<>(SB), X4
	VMOVDQU ·sc_slash_vec<>(SB), X5
	MOVQ 0(SP), R15
	VMOVQ DX, X6
	VPBROADCASTB X6, X6
	VMOVQ BX, X7
	VPBROADCASTB X7, X7
	VPCMPEQB X8, X8, X8
	XORQ DX, DX
	XORQ BX, BX
	JMP  sc_avx_load16
sc_avx_short_tail:
	VPCMPEQB X8, X8, X8
	XORQ DX, DX
	XORQ BX, BX
	JMP  sc_avx_tail

sc_sse_check_short:
	MOVQ 0(SP), R15               // R15 = remaining length
	CMPQ R15, $16
	JLT  sc_sse_short_scalar
sc_sse_entry:
	MOVOU ·sc_quote_vec<>(SB), X4
	MOVOU ·sc_slash_vec<>(SB), X5
	MOVQ 0(SP), R15
	PXOR X1, X1
	MOVQ DX, X6
	PSHUFB X1, X6
	MOVQ BX, X7
	PSHUFB X1, X7

sc_sse_block:
	MOVQ $64, R12
	CMPQ R15, R12
	CMOVQLT R15, R12
	MOVQ R12, DI
	XORQ R13, R13
	MOVQ $0, 8(SP)
	XORQ DX, DX
	XORQ BX, BX
	XORQ CX, CX

sc_sse_load16:
	CMPQ DI, $16
	JLT  sc_sse_tail

	MOVOU (SI), X0
	MOVO X0, X1; PCMPEQB X4, X1; PMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, R13
	MOVO X0, X1; PCMPEQB X5, X1; PMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, 8(SP)
	MOVO X0, X1; PCMPEQB X6, X1; PMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, DX
	MOVO X0, X1; PCMPEQB X7, X1; PMOVMSKB X1, AX; SHLQ CL, AX; ORQ AX, BX

	ADDQ $16, SI
	ADDQ $16, CX
	SUBQ $16, DI
	JZ   sc_sse_partial_done
	JMP  sc_sse_load16

sc_sse_short_scalar:
	MOVQ R15, R12
	MOVQ R12, DI
	XORQ R13, R13
	MOVQ $0, 8(SP)
	XORQ DX, DX
	XORQ BX, BX
	XORQ CX, CX

sc_sse_tail:
	MOVQ $1, AX
	SHLQ CL, AX
	MOVBLZX (SI), R12
	CMPB R12B, $0x22
	JNE  sc_sse_tail_slash
	ORQ  AX, R13
sc_sse_tail_slash:
	CMPB R12B, $0x5c
	JNE  sc_sse_tail_open
	ORQ  AX, 8(SP)
sc_sse_tail_open:
	CMPB R12B, open+16(FP)
	JNE  sc_sse_tail_close
	ORQ  AX, DX
sc_sse_tail_close:
	SUBB open+16(FP), R12B
	CMPB R12B, $2
	JNE  sc_sse_tail_next
	ORQ  AX, BX
sc_sse_tail_next:
	ADDQ $1, SI
	ADDQ $1, CX
	SUBQ $1, DI
	JNZ  sc_sse_tail
	MOVQ $64, R12
	CMPQ R15, R12
	CMOVQLT R15, R12

sc_sse_partial_done:
	MOVQ 8(SP), CX
	MOVQ CX, AX
	ORQ  R10, AX
	JNZ  sc_sse_slash_slow

sc_sse_quotes:
	TESTQ R13, R13
	JNZ  sc_sse_quote_prefix
	TESTQ R11, R11
	JNZ  sc_sse_advance
	MOVQ DX, AX
	ORQ  BX, AX
	JZ   sc_sse_advance
	JMP  sc_sse_brackets

sc_sse_quote_prefix:
	MOVQ R13, AX; SHLQ $1, AX; XORQ AX, R13
	MOVQ R13, AX; SHLQ $2, AX; XORQ AX, R13
	MOVQ R13, AX; SHLQ $4, AX; XORQ AX, R13
	MOVQ R13, AX; SHLQ $8, AX; XORQ AX, R13
	MOVQ R13, AX; SHLQ $16, AX; XORQ AX, R13
	MOVQ R13, AX; SHLQ $32, AX; XORQ AX, R13
	XORQ R11, R13
	MOVQ R13, R11
	SARQ $63, R11
	NOTQ R13
	ANDQ R13, DX
	ANDQ R13, BX
	MOVQ DX, AX
	ORQ  BX, AX
	JZ   sc_sse_advance

sc_sse_brackets:
	TESTQ BX, BX
	JZ   sc_sse_rem_opens
sc_sse_close_loop:
	LEAQ -1(BX), CX
	MOVQ DX, AX
	ANDQ CX, AX
	JZ   sc_sse_after_opens
sc_sse_count_opens_before:
	ADDQ $1, R9
	LEAQ -1(AX), DI
	ANDQ DI, AX
	JNZ  sc_sse_count_opens_before
sc_sse_after_opens:
	MOVQ CX, AX
	NOTQ AX
	ANDQ AX, DX
	SUBQ $1, R9
	JZ   sc_success_no_avx
	ANDQ CX, BX
	JNZ  sc_sse_close_loop
sc_sse_rem_opens:
	TESTQ DX, DX
	JZ   sc_sse_advance
sc_sse_count_rem_opens:
	ADDQ $1, R9
	LEAQ -1(DX), AX
	ANDQ AX, DX
	JNZ  sc_sse_count_rem_opens

sc_sse_advance:
	ADDQ R12, R8
	SUBQ R12, R15
	JNZ  sc_sse_block
	JMP  sc_eof_no_avx

sc_sse_slash_slow:
	MOVQ R10, AX
	NOTQ AX
	ANDQ CX, AX
	MOVQ AX, DI
	SHLQ $1, DI
	ORQ  R10, DI
	MOVQ DI, R10
	NOTQ R10
	ANDQ AX, R10
	MOVQ $0xaaaaaaaaaaaaaaaa, R12
	ANDQ R12, R10
	ADDQ AX, R10
	SHLQ $1, R10
	MOVQ $0x5555555555555555, AX
	XORQ AX, R10
	ANDQ DI, R10
	MOVQ R10, AX
	NOTQ AX
	ANDQ AX, R13
	ANDQ AX, CX
	MOVQ $64, R12
	CMPQ R15, R12
	CMOVQLT R15, R12
	MOVQ CX, R10
	LEAQ -1(R12), CX
	SHRQ CL, R10
	ANDQ $1, R10
	JMP  sc_sse_quotes
