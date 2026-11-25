//go:build !sonic_compat && !sonic_scalar && amd64 && go1.20 && !go1.28

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

#include "textflag.h"
#include "lex_batch_offsets.h"

DATA ·lb_ws_lut<>+0(SB)/8, $0x0000000000000020
DATA ·lb_ws_lut<>+8(SB)/8, $0x00000d00000a0900
DATA ·lb_ws_lut<>+16(SB)/8, $0x0000000000000020
DATA ·lb_ws_lut<>+24(SB)/8, $0x00000d00000a0900
GLOBL ·lb_ws_lut<>(SB), RODATA|NOPTR, $32

DATA ·lb_struct_lut<>+0(SB)/8, $0x0000000000000000
DATA ·lb_struct_lut<>+8(SB)/8, $0x00007d2c7b3a0000
DATA ·lb_struct_lut<>+16(SB)/8, $0x0000000000000000
DATA ·lb_struct_lut<>+24(SB)/8, $0x00007d2c7b3a0000
GLOBL ·lb_struct_lut<>(SB), RODATA|NOPTR, $32

DATA ·lb_quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·lb_quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·lb_quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·lb_quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·lb_quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·lb_slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·lb_slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·lb_slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·lb_slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·lb_slash_vec<>(SB), RODATA|NOPTR, $32

DATA ·lb_ctrl_vec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·lb_ctrl_vec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·lb_ctrl_vec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·lb_ctrl_vec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL ·lb_ctrl_vec<>(SB), RODATA|NOPTR, $32

DATA ·lb_20_vec<>+0(SB)/8, $0x2020202020202020
DATA ·lb_20_vec<>+8(SB)/8, $0x2020202020202020
DATA ·lb_20_vec<>+16(SB)/8, $0x2020202020202020
DATA ·lb_20_vec<>+24(SB)/8, $0x2020202020202020
GLOBL ·lb_20_vec<>(SB), RODATA|NOPTR, $32

DATA ·lb_09_vec<>+0(SB)/8, $0x0909090909090909
DATA ·lb_09_vec<>+8(SB)/8, $0x0909090909090909
GLOBL ·lb_09_vec<>(SB), RODATA|NOPTR, $16

DATA ·lb_0a_vec<>+0(SB)/8, $0x0a0a0a0a0a0a0a0a
DATA ·lb_0a_vec<>+8(SB)/8, $0x0a0a0a0a0a0a0a0a
GLOBL ·lb_0a_vec<>(SB), RODATA|NOPTR, $16

DATA ·lb_fb_vec<>+0(SB)/8, $0xfbfbfbfbfbfbfbfb
DATA ·lb_fb_vec<>+8(SB)/8, $0xfbfbfbfbfbfbfbfb
GLOBL ·lb_fb_vec<>(SB), RODATA|NOPTR, $16

DATA ·lb_7b_vec<>+0(SB)/8, $0x7b7b7b7b7b7b7b7b
DATA ·lb_7b_vec<>+8(SB)/8, $0x7b7b7b7b7b7b7b7b
GLOBL ·lb_7b_vec<>(SB), RODATA|NOPTR, $16

DATA ·lb_7d_vec<>+0(SB)/8, $0x7d7d7d7d7d7d7d7d
DATA ·lb_7d_vec<>+8(SB)/8, $0x7d7d7d7d7d7d7d7d
GLOBL ·lb_7d_vec<>(SB), RODATA|NOPTR, $16

DATA ·lb_2c_vec<>+0(SB)/8, $0x2c2c2c2c2c2c2c2c
DATA ·lb_2c_vec<>+8(SB)/8, $0x2c2c2c2c2c2c2c2c
GLOBL ·lb_2c_vec<>(SB), RODATA|NOPTR, $16

DATA ·lb_3a_vec<>+0(SB)/8, $0x3a3a3a3a3a3a3a3a
DATA ·lb_3a_vec<>+8(SB)/8, $0x3a3a3a3a3a3a3a3a
GLOBL ·lb_3a_vec<>(SB), RODATA|NOPTR, $16

// Pure VEX AVX2 64B classifier macro over (Y0, Y1) -> 64-bit R9..R13.
// Requires: Y8=ws_lut, Y9=struct_lut, Y10='"', Y11='\\', Y12=0x1f, Y13=0x20.
// Clobbers: Y2..Y6, AX, BX. Preserves CX, DX, SI, DI, R8.
#define AVX2_CLASSIFY_64B \
	VPCMPEQB  Y10, Y0, Y2; \
	VPMOVMSKB Y2, R9; \
	VPCMPEQB  Y10, Y1, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R9; \
	VPCMPEQB  Y11, Y0, Y2; \
	VPMOVMSKB Y2, R10; \
	VPCMPEQB  Y11, Y1, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R10; \
	VPMINUB   Y12, Y0, Y4; \
	VPCMPEQB  Y0, Y4, Y4; \
	VPMOVMSKB Y4, BX; \
	VPMINUB   Y12, Y1, Y5; \
	VPCMPEQB  Y1, Y5, Y5; \
	VPMOVMSKB Y5, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, BX; \
	XORQ      R11, R11; \
	TESTQ     $LEX_FLAG_REJECT_CTRL, flags+40(FP); \
	CMOVQNE   BX, R11; \
	VPSHUFB   Y0, Y8, Y2; \
	VPCMPEQB  Y0, Y2, Y2; \
	VPMOVMSKB Y2, R12; \
	VPSHUFB   Y1, Y8, Y3; \
	VPCMPEQB  Y1, Y3, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R12; \
	VPOR      Y13, Y0, Y2; \
	VPSHUFB   Y2, Y9, Y6; \
	VPCMPEQB  Y2, Y6, Y6; \
	VPMOVMSKB Y6, R13; \
	VPOR      Y13, Y1, Y3; \
	VPSHUFB   Y3, Y9, Y6; \
	VPCMPEQB  Y3, Y6, Y6; \
	VPMOVMSKB Y6, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R13; \
	NOTQ      BX; \
	ANDQ      BX, R13

// Pure VEX AVX2 64B classifier without control-mask extraction (flags & LEX_FLAG_REJECT_CTRL == 0).
// Clamps control bytes (< 0x20) to 0x20 via VPMAXUB Y13 before OR-0x20 struct lookup so
// 0x0c and 0x1a cannot false-match "," or ":" without computing/filtering the control mask.
#define AVX2_CLASSIFY_64B_NOCTRL \
	VPCMPEQB  Y10, Y0, Y2; \
	VPMOVMSKB Y2, R9; \
	VPCMPEQB  Y10, Y1, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R9; \
	VPCMPEQB  Y11, Y0, Y2; \
	VPMOVMSKB Y2, R10; \
	VPCMPEQB  Y11, Y1, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R10; \
	XORQ      R11, R11; \
	VPSHUFB   Y0, Y8, Y2; \
	VPCMPEQB  Y0, Y2, Y2; \
	VPMOVMSKB Y2, R12; \
	VPSHUFB   Y1, Y8, Y3; \
	VPCMPEQB  Y1, Y3, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R12; \
	VPMAXUB   Y13, Y0, Y2; \
	VPOR      Y13, Y2, Y2; \
	VPSHUFB   Y2, Y9, Y6; \
	VPCMPEQB  Y2, Y6, Y6; \
	VPMOVMSKB Y6, R13; \
	VPMAXUB   Y13, Y1, Y3; \
	VPOR      Y13, Y3, Y3; \
	VPSHUFB   Y3, Y9, Y6; \
	VPCMPEQB  Y3, Y6, Y6; \
	VPMOVMSKB Y6, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R13

// Pure VEX AVX2 64B classifier when LEX_FLAG_REJECT_CTRL is known set (avoids redundant TESTQ/CMOVQNE).
#define AVX2_CLASSIFY_64B_CTRL \
	VPCMPEQB  Y10, Y0, Y2; \
	VPMOVMSKB Y2, R9; \
	VPCMPEQB  Y10, Y1, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R9; \
	VPCMPEQB  Y11, Y0, Y2; \
	VPMOVMSKB Y2, R10; \
	VPCMPEQB  Y11, Y1, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R10; \
	VPMINUB   Y12, Y0, Y4; \
	VPCMPEQB  Y0, Y4, Y4; \
	VPMOVMSKB Y4, BX; \
	VPMINUB   Y12, Y1, Y5; \
	VPCMPEQB  Y1, Y5, Y5; \
	VPMOVMSKB Y5, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, BX; \
	MOVQ      BX, R11; \
	VPSHUFB   Y0, Y8, Y2; \
	VPCMPEQB  Y0, Y2, Y2; \
	VPMOVMSKB Y2, R12; \
	VPSHUFB   Y1, Y8, Y3; \
	VPCMPEQB  Y1, Y3, Y3; \
	VPMOVMSKB Y3, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R12; \
	VPOR      Y13, Y0, Y2; \
	VPSHUFB   Y2, Y9, Y6; \
	VPCMPEQB  Y2, Y6, Y6; \
	VPMOVMSKB Y6, R13; \
	VPOR      Y13, Y1, Y3; \
	VPSHUFB   Y3, Y9, Y6; \
	VPCMPEQB  Y3, Y6, Y6; \
	VPMOVMSKB Y6, AX; \
	SHLQ      $32, AX; \
	ORQ       AX, R13; \
	NOTQ      BX; \
	ANDQ      BX, R13

// SSSE3 16-byte classifier macro using register-resident PSHUFB LUTs (zero memory loads).
// Requires: X8=ws_lut, X9=struct_lut, X10='"', X11='\\', X12=0x1f, X13=0x20, CL=shift.
// Clobbers: X1, X2, X4, AX, BX. Preserves CX, DX, SI, DI, R8..R13.
#define SSSE3_CLASSIFY_SHIFT_CL \
	MOVO     X0, X1; \
	PCMPEQB  X10, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R9; \
	MOVO     X0, X1; \
	PCMPEQB  X11, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R10; \
	MOVO     X0, X4; \
	PMINUB   X12, X4; \
	PCMPEQB  X0, X4; \
	PMOVMSKB X4, BX; \
	MOVQ     BX, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R11; \
	MOVO     X8, X1; \
	PSHUFB   X0, X1; \
	PCMPEQB  X0, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R12; \
	MOVO     X0, X2; \
	POR      X13, X2; \
	MOVO     X9, X1; \
	PSHUFB   X2, X1; \
	PCMPEQB  X2, X1; \
	PMOVMSKB X1, AX; \
	NOTL     BX; \
	ANDL     BX, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R13

// Pure SSE2 16-byte classifier macro (no SSSE3 PSHUFB):
// Classifies X0, shifts each 16-bit mask left by CL, ORing into R9..R13.
// Requires: X10='"', X11='\\', X12=0x1f, X13=0x20, X14=0x09, X15=0x0a.
// Clobbers: X1, X2, X3, AX, BX. Preserves CX, DX, SI, DI, R8..R13.
#define SSE2_CLASSIFY_SHIFT_CL \
	MOVO     X0, X1; \
	PCMPEQB  X10, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R9; \
	MOVO     X0, X1; \
	PCMPEQB  X11, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R10; \
	MOVO     X0, X1; \
	PMINUB   X12, X1; \
	PCMPEQB  X0, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R11; \
	MOVOU    ·lb_fb_vec<>(SB), X1; \
	PAND     X0, X1; \
	PCMPEQB  X14, X1; \
	MOVO     X0, X2; \
	PCMPEQB  X15, X2; \
	POR      X2, X1; \
	MOVO     X0, X2; \
	PCMPEQB  X13, X2; \
	POR      X2, X1; \
	PMOVMSKB X1, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R12; \
	MOVO     X0, X1; \
	POR      X13, X1; \
	MOVOU    ·lb_7b_vec<>(SB), X2; \
	PCMPEQB  X1, X2; \
	MOVOU    ·lb_7d_vec<>(SB), X3; \
	PCMPEQB  X1, X3; \
	POR      X3, X2; \
	MOVOU    ·lb_2c_vec<>(SB), X3; \
	PCMPEQB  X0, X3; \
	POR      X3, X2; \
	MOVOU    ·lb_3a_vec<>(SB), X3; \
	PCMPEQB  X0, X3; \
	POR      X3, X2; \
	PMOVMSKB X2, AX; \
	SHLQ     CL, AX; \
	ORQ      AX, R13

// func lexBatch(src *byte, size int, pos int, blocks *types.LexBlockMask, capBlocks int, flags uint64, carry *types.LexBlockCarry, out *types.LexBatchOut)
//
// Register map (never touches SP, BP, R14=g, or R15):
//   SI  = cur (int)
//   DI  = blocks (*LexBlockMask)
//   R8  = nBlk (produced count)
//   R9  = quote mask
//   R10 = slash mask
//   R11 = ctrl mask
//   R12 = space mask
//   R13 = struc mask
TEXT ·lexBatch(SB), NOSPLIT, $0-64
	MOVQ pos+16(FP), SI
	MOVQ size+8(FP), DX
	CMPQ SI, DX
	JAE  lb_empty
	CMPQ capBlocks+32(FP), $0
	JLE  lb_empty

	MOVQ blocks+24(FP), DI
	XORQ R8, R8

	// Sanitize carry bits once at entry.
	MOVQ carry+48(FP), AX
	ANDQ $1, LEX_CARRY_ODD_SLASH(AX)
	ANDQ $1, LEX_CARRY_PREV_SCALAR(AX)

	// Pure VEX AVX2 + VPCLMULQDQ path when UseAVX2 is enabled and total size >= 32.
	// Inputs < 32B take the legacy SSE/scalar path without ever touching YMM registers
	// or paying VZEROUPPER overhead, and >= 32B inputs NEVER execute non-VEX SSE!
	CMPB ·hasAVX2(SB), $0
	JE   lb_legacy_init
	CMPQ DX, $32
	JB   lb_legacy_init

	VMOVDQU  ·lb_ws_lut<>(SB), Y8
	VMOVDQU  ·lb_struct_lut<>(SB), Y9
	VMOVDQU  ·lb_quote_vec<>(SB), Y10
	VMOVDQU  ·lb_slash_vec<>(SB), Y11
	VMOVDQU  ·lb_ctrl_vec<>(SB), Y12
	VMOVDQU  ·lb_20_vec<>(SB), Y13
	VPCMPEQB X15, X15, X15              // X15 = all-ones for VPCLMULQDQ (zeroes upper Y15)

	PCALIGN $32
lb_avx2_loop:
	CMPQ R8, capBlocks+32(FP)
	JAE  lb_avx2_done
	MOVQ size+8(FP), DX
	SUBQ SI, DX                         // DX = remaining bytes (size - cur)
	JZ   lb_avx2_done
	CMPQ DX, $64
	JB   lb_avx2_tail

	MOVQ    src+0(FP), AX
	ADDQ    SI, AX
	VMOVDQU 0(AX), Y0
	VMOVDQU 32(AX), Y1
	TESTQ   $LEX_FLAG_REJECT_CTRL, flags+40(FP)
	JNZ     lb_avx2_full_ctrl
	AVX2_CLASSIFY_64B_NOCTRL
	MOVQ    $64, DX
	JMP     lb_avx2_combine
lb_avx2_full_ctrl:
	AVX2_CLASSIFY_64B_CTRL
	MOVQ    $64, DX
	JMP     lb_avx2_combine

// In-bounds pure-VEX AVX2 tail classifier (1 <= DX < 64, total size >= 32, SI + DX == size).
// Zero out-of-bounds reads (100% PROT_NONE guard-page safe) and zero legacy SSE instructions.
lb_avx2_tail:
	CMPQ size+8(FP), $64
	JB   lb_avx2_tail_32_63

	// Total size >= 64: classify [size-64, size) in one 64B AVX2 pass and shift right by (64 - DX).
	MOVQ    src+0(FP), AX
	ADDQ    size+8(FP), AX
	VMOVDQU -64(AX), Y0
	VMOVDQU -32(AX), Y1
	AVX2_CLASSIFY_64B
	MOVQ    $64, CX
	SUBQ    DX, CX                      // CL = 64 - width (1..63)
	SHRQ    CL, R9
	SHRQ    CL, R10
	SHRQ    CL, R11
	SHRQ    CL, R12
	SHRQ    CL, R13
	JMP     lb_avx2_combine

lb_avx2_tail_32_63:
	CMPQ DX, $32
	JA   lb_avx2_tail_33_63

	// 32 <= size < 64 and 1 <= DX <= 32: classify [size-32, size) in one 32B VEX load.
	MOVQ      src+0(FP), AX
	ADDQ      size+8(FP), AX
	VMOVDQU   -32(AX), Y0
	VPCMPEQB  Y10, Y0, Y2
	VPMOVMSKB Y2, R9
	VPCMPEQB  Y11, Y0, Y2
	VPMOVMSKB Y2, R10
	VPMINUB   Y12, Y0, Y4
	VPCMPEQB  Y0, Y4, Y4
	VPMOVMSKB Y4, BX
	XORQ      R11, R11
	TESTQ     $LEX_FLAG_REJECT_CTRL, flags+40(FP)
	CMOVQNE   BX, R11
	VPSHUFB   Y0, Y8, Y2
	VPCMPEQB  Y0, Y2, Y2
	VPMOVMSKB Y2, R12
	VPOR      Y13, Y0, Y2
	VPSHUFB   Y2, Y9, Y6
	VPCMPEQB  Y2, Y6, Y6
	VPMOVMSKB Y6, R13
	NOTQ      BX
	ANDQ      BX, R13
	MOVQ      $32, CX
	SUBQ      DX, CX                    // CL = 32 - width (0..31)
	SHRQ      CL, R9
	SHRQ      CL, R10
	SHRQ      CL, R11
	SHRQ      CL, R12
	SHRQ      CL, R13
	JMP       lb_avx2_combine

lb_avx2_tail_33_63:
	// 32 < DX < 64: classify overlapping [SI, SI+32) and [size-32, size) via two 32B VEX loads.
	MOVQ      src+0(FP), AX
	VMOVDQU   (AX)(SI*1), Y0
	ADDQ      size+8(FP), AX
	VMOVDQU   -32(AX), Y1
	MOVQ      DX, CX
	SUBQ      $32, CX                   // CL = width - 32 (1..31)

	VPCMPEQB  Y10, Y0, Y2
	VPMOVMSKB Y2, R9
	VPCMPEQB  Y10, Y1, Y3
	VPMOVMSKB Y3, AX
	SHLQ      CL, AX
	ORQ       AX, R9

	VPCMPEQB  Y11, Y0, Y2
	VPMOVMSKB Y2, R10
	VPCMPEQB  Y11, Y1, Y3
	VPMOVMSKB Y3, AX
	SHLQ      CL, AX
	ORQ       AX, R10

	VPMINUB   Y12, Y0, Y4
	VPCMPEQB  Y0, Y4, Y4
	VPMOVMSKB Y4, BX
	VPMINUB   Y12, Y1, Y5
	VPCMPEQB  Y1, Y5, Y5
	VPMOVMSKB Y5, AX
	SHLQ      CL, AX
	ORQ       AX, BX
	XORQ      R11, R11
	TESTQ     $LEX_FLAG_REJECT_CTRL, flags+40(FP)
	CMOVQNE   BX, R11

	VPSHUFB   Y0, Y8, Y2
	VPCMPEQB  Y0, Y2, Y2
	VPMOVMSKB Y2, R12
	VPSHUFB   Y1, Y8, Y3
	VPCMPEQB  Y1, Y3, Y3
	VPMOVMSKB Y3, AX
	SHLQ      CL, AX
	ORQ       AX, R12

	VPOR      Y13, Y0, Y2
	VPSHUFB   Y2, Y9, Y6
	VPCMPEQB  Y2, Y6, Y6
	VPMOVMSKB Y6, R13
	VPOR      Y13, Y1, Y3
	VPSHUFB   Y3, Y9, Y6
	VPCMPEQB  Y3, Y6, Y6
	VPMOVMSKB Y6, AX
	SHLQ      CL, AX
	ORQ       AX, R13
	NOTQ      BX
	ANDQ      BX, R13

// AVX2 + VPCLMULQDQ mask combination (R9..R13 already masked to width DX in 1..64).
lb_avx2_combine:
	MOVQ carry+48(FP), BX
	MOVQ LEX_CARRY_ODD_SLASH(BX), AX    // oddSlash (0 or 1)
	ORQ  R10, R11                       // R11 = slash | ctrl
	XORQ CX, CX                         // CX = escaped = 0
	MOVQ AX, BX
	ORQ  R10, BX
	JZ   lb_avx2_after_slashes

	MOVQ AX, CX
	NOTQ CX
	ANDQ R10, CX                        // CX = s = slash &^ oddSlash
	MOVQ CX, BX
	SHLQ $1, BX
	ORQ  AX, BX                         // BX = follows = (s << 1) | oddSlash
	MOVQ BX, AX
	NOTQ AX
	ANDQ CX, AX                         // AX = oddStarts = s &^ follows
	MOVQ $0xaaaaaaaaaaaaaaaa, R10
	ANDQ R10, AX
	ADDQ CX, AX
	SHLQ $1, AX
	MOVQ $0x5555555555555555, R10
	XORQ R10, AX
	ANDQ BX, AX                         // AX = escaped
	MOVQ AX, BX
	NOTQ BX
	ANDQ BX, CX                         // CX = unescSlashes = s &^ escaped
	MOVQ CX, R10
	MOVQ DX, CX
	DECQ CX
	SHRQ CL, R10
	ANDQ $1, R10
	MOVQ carry+48(FP), BX
	MOVQ R10, LEX_CARRY_ODD_SLASH(BX)
	MOVQ AX, CX

lb_avx2_after_slashes:
	NOTQ       CX
	ANDQ       CX, R9                   // R9 = unescQuotes = quote &^ escaped
	VMOVQ      R9, X2
	VPCLMULQDQ $0, X15, X2, X2          // X2[63:0] = inclusive prefix XOR of unescQuotes
	VMOVQ      X2, AX

	MOVQ carry+48(FP), BX
	XORQ LEX_CARRY_IN_STRING(BX), AX    // AX = stringMask = rawPrefix ^ inString

	CMPQ DX, $64
	JNE  lb_avx2_partial_instring
	MOVQ AX, CX
	SARQ $63, CX
	MOVQ CX, LEX_CARRY_IN_STRING(BX)
	MOVQ $-1, R10
	JMP  lb_avx2_masks_finish

lb_avx2_partial_instring:
	MOVQ DX, CX
	DECQ CX
	MOVQ AX, R10
	SHRQ CL, R10
	ANDQ $1, R10
	NEGQ R10
	MOVQ R10, LEX_CARRY_IN_STRING(BX)

	MOVQ DX, CX
	MOVQ $1, R10
	SHLQ CL, R10
	DECQ R10                            // R10 = validMask = (1 << width) - 1
	ANDQ R10, AX
	ANDQ R10, R9

lb_avx2_masks_finish:
	ANDQ AX, R11                        // R11 = insideSpecial = (slash | ctrl) & stringMask
	MOVQ R11, LEX_BLK_INSIDE_SPEC(DI)

	MOVQ AX, CX
	NOTQ CX
	ANDQ R9, CX                         // CX = closeQuotes = unescQuotes &^ stringMask
	MOVQ CX, LEX_BLK_CLOSE_QUOTE(DI)

	MOVQ R9, R11
	ANDQ AX, R11                        // R11 = openQuotes = unescQuotes & stringMask

	ORQ  R9, AX
	NOTQ AX
	ANDQ R10, AX                        // AX = outsideString = ^(stringMask | unescQuotes) & validMask

	MOVQ R13, R9
	ANDQ AX, R9
	ORQ  R9, R11

	ORQ  R13, R12
	NOTQ R12
	ANDQ R12, AX                        // AX = scalarBytes = outsideString &^ (space | struc)

	MOVQ carry+48(FP), BX
	MOVQ AX, R9
	SHLQ $1, R9
	ORQ  LEX_CARRY_PREV_SCALAR(BX), R9
	NOTQ R9
	ANDQ AX, R9                         // R9 = scalarStarts
	ORQ  R9, R11
	MOVQ R11, LEX_BLK_EVENT_MASK(DI)

	MOVQ DX, CX
	DECQ CX
	SHRQ CL, AX
	ANDQ $1, AX
	MOVQ AX, LEX_CARRY_PREV_SCALAR(BX)

	ADDQ $LEX_BLK_SIZE, DI
	INCQ R8
	ADDQ DX, SI
	JMP  lb_avx2_loop

lb_avx2_done:
	VZEROUPPER
	MOVQ out+56(FP), AX
	MOVQ R8, LEX_OUT_BLOCKS(AX)
	SUBQ pos+16(FP), SI
	MOVQ SI, LEX_OUT_BYTES(AX)
	RET

// Legacy non-VEX path (SONIC_MODE=noavx2, SONIC_MODE=nossse3, non-AVX2 CPUs, or size < 32).
// Never touches YMM registers and never needs VZEROUPPER.
lb_legacy_init:
	MOVOU ·lb_quote_vec<>(SB), X10
	MOVOU ·lb_slash_vec<>(SB), X11
	MOVOU ·lb_ctrl_vec<>(SB), X12
	MOVOU ·lb_20_vec<>(SB), X13
	CMPB  ·hasSSSE3(SB), $0
	JE    lb_legacy_init_sse2
	MOVOU ·lb_ws_lut<>(SB), X8
	MOVOU ·lb_struct_lut<>(SB), X9
	JMP   lb_legacy_loop

lb_legacy_init_sse2:
	MOVOU ·lb_09_vec<>(SB), X14
	MOVOU ·lb_0a_vec<>(SB), X15

lb_legacy_loop:
	CMPQ R8, capBlocks+32(FP)
	JAE  lb_legacy_done
	MOVQ size+8(FP), DX
	SUBQ SI, DX
	JZ   lb_legacy_done
	CMPQ DX, $64
	JB   lb_legacy_block
	MOVQ $64, DX

lb_legacy_block:
	XORQ R9, R9
	XORQ R10, R10
	XORQ R11, R11
	XORQ R12, R12
	XORQ R13, R13

	CMPQ size+8(FP), $16
	JB   lb_small_scalar

	CMPB ·hasSSSE3(SB), $0
	JE   lb_sse2_start

	// SSSE3 16B PSHUFB loop (noavx2 mode)
	XORQ CX, CX
lb_ssse3_chunk_loop:
	MOVQ DX, BX
	SUBQ CX, BX
	CMPQ BX, $16
	JB   lb_ssse3_rem
	MOVQ  src+0(FP), AX
	ADDQ  SI, AX
	MOVOU (AX)(CX*1), X0
	SSSE3_CLASSIFY_SHIFT_CL
	ADDQ  $16, CX
	CMPQ  CX, DX
	JB    lb_ssse3_chunk_loop
	JMP   lb_legacy_ctrl_gate

lb_ssse3_rem:
	TESTQ BX, BX
	JZ    lb_legacy_ctrl_gate
	MOVQ  src+0(FP), AX
	ADDQ  size+8(FP), AX
	MOVOU -16(AX), X0
	CMPQ  DX, $16
	JB    lb_ssse3_lt16_overlap
	MOVQ  DX, CX
	SUBQ  $16, CX
	SSSE3_CLASSIFY_SHIFT_CL
	JMP   lb_legacy_ctrl_gate

lb_ssse3_lt16_overlap:
	MOVQ     $16, CX
	SUBQ     DX, CX
	MOVO     X0, X1
	PCMPEQB  X10, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R9

	MOVO     X0, X1
	PCMPEQB  X11, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R10

	MOVO     X0, X4
	PMINUB   X12, X4
	PCMPEQB  X0, X4
	PMOVMSKB X4, BX
	MOVQ     BX, AX
	SHRQ     CL, AX
	MOVQ     AX, R11

	MOVO     X8, X1
	PSHUFB   X0, X1
	PCMPEQB  X0, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R12

	MOVO     X0, X2
	POR      X13, X2
	MOVO     X9, X1
	PSHUFB   X2, X1
	PCMPEQB  X2, X1
	PMOVMSKB X1, AX
	NOTL     BX
	ANDL     BX, AX
	SHRQ     CL, AX
	MOVQ     AX, R13
	JMP      lb_legacy_ctrl_gate

lb_sse2_start:
	XORQ CX, CX
lb_sse2_chunk_loop:
	MOVQ DX, BX
	SUBQ CX, BX
	CMPQ BX, $16
	JB   lb_sse2_rem

	MOVQ  src+0(FP), AX
	ADDQ  SI, AX
	MOVOU (AX)(CX*1), X0
	SSE2_CLASSIFY_SHIFT_CL
	ADDQ  $16, CX
	CMPQ  CX, DX
	JB    lb_sse2_chunk_loop
	JMP   lb_legacy_ctrl_gate

lb_sse2_rem:
	TESTQ BX, BX
	JZ    lb_legacy_ctrl_gate
	MOVQ  src+0(FP), AX
	ADDQ  size+8(FP), AX
	MOVOU -16(AX), X0
	CMPQ  DX, $16
	JB    lb_sse2_lt16_overlap

	MOVQ DX, CX
	SUBQ $16, CX
	SSE2_CLASSIFY_SHIFT_CL
	JMP  lb_legacy_ctrl_gate

lb_sse2_lt16_overlap:
	MOVQ     $16, CX
	SUBQ     DX, CX
	MOVO     X0, X1
	PCMPEQB  X10, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R9

	MOVO     X0, X1
	PCMPEQB  X11, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R10

	MOVO     X0, X1
	PMINUB   X12, X1
	PCMPEQB  X0, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R11

	MOVOU    ·lb_fb_vec<>(SB), X1
	PAND     X0, X1
	PCMPEQB  X14, X1
	MOVO     X0, X2
	PCMPEQB  X15, X2
	POR      X2, X1
	MOVO     X0, X2
	PCMPEQB  X13, X2
	POR      X2, X1
	PMOVMSKB X1, AX
	SHRQ     CL, AX
	MOVQ     AX, R12

	MOVO     X0, X1
	POR      X13, X1
	MOVOU    ·lb_7b_vec<>(SB), X2
	PCMPEQB  X1, X2
	MOVOU    ·lb_7d_vec<>(SB), X3
	PCMPEQB  X1, X3
	POR      X3, X2
	MOVOU    ·lb_2c_vec<>(SB), X3
	PCMPEQB  X0, X3
	POR      X3, X2
	MOVOU    ·lb_3a_vec<>(SB), X3
	PCMPEQB  X0, X3
	POR      X3, X2
	PMOVMSKB X2, AX
	SHRQ     CL, AX
	MOVQ     AX, R13

lb_legacy_ctrl_gate:
	TESTQ $LEX_FLAG_REJECT_CTRL, flags+40(FP)
	JNZ   lb_combine
	XORQ  R11, R11
	JMP   lb_combine

// Tiny (< 16B total input) exact byte loop: zero out-of-bounds reads.
lb_small_scalar:
	XORQ CX, CX
lb_small_byte:
	MOVQ    src+0(FP), AX
	ADDQ    SI, AX
	MOVBLZX (AX)(CX*1), AX
	MOVQ    $1, BX
	SHLQ    CL, BX
	TESTQ   $LEX_FLAG_REJECT_CTRL, flags+40(FP)
	JZ      lb_sb_after_ctrl
	CMPB    AL, $0x20
	JAE     lb_sb_after_ctrl
	ORQ     BX, R11
lb_sb_after_ctrl:
	CMPB    AL, $'"'
	JE      lb_sb_quote
	CMPB    AL, $'\\'
	JE      lb_sb_slash
	CMPB    AL, $' '
	JE      lb_sb_space
	CMPB    AL, $'\n'
	JE      lb_sb_space
	CMPB    AL, $'\r'
	JE      lb_sb_space
	CMPB    AL, $'\t'
	JE      lb_sb_space
	CMPB    AL, $','
	JE      lb_sb_struct
	CMPB    AL, $':'
	JE      lb_sb_struct
	CMPB    AL, $'{'
	JE      lb_sb_struct
	CMPB    AL, $'}'
	JE      lb_sb_struct
	CMPB    AL, $'['
	JE      lb_sb_struct
	CMPB    AL, $']'
	JE      lb_sb_struct
	JMP     lb_sb_next
lb_sb_quote:
	ORQ     BX, R9
	JMP     lb_sb_next
lb_sb_slash:
	ORQ     BX, R10
	JMP     lb_sb_next
lb_sb_space:
	ORQ     BX, R12
	JMP     lb_sb_next
lb_sb_struct:
	ORQ     BX, R13
lb_sb_next:
	INCQ    CX
	CMPQ    CX, DX
	JB      lb_small_byte

// Combine 64-bit masks for legacy block of width DX (1..64):
lb_combine:
	CMPQ DX, $64
	JE   lb_combine_valid_ok
	MOVQ DX, CX
	MOVQ $1, AX
	SHLQ CL, AX
	DECQ AX
	ANDQ AX, R9
	ANDQ AX, R10
	ANDQ AX, R11
	ANDQ AX, R12
	ANDQ AX, R13
lb_combine_valid_ok:

	MOVQ carry+48(FP), BX
	MOVQ LEX_CARRY_ODD_SLASH(BX), AX
	ORQ  R10, R11
	XORQ CX, CX
	MOVQ AX, BX
	ORQ  R10, BX
	JZ   lb_after_slashes

	MOVQ AX, CX
	NOTQ CX
	ANDQ R10, CX
	MOVQ CX, BX
	SHLQ $1, BX
	ORQ  AX, BX
	MOVQ BX, AX
	NOTQ AX
	ANDQ CX, AX
	MOVQ $0xaaaaaaaaaaaaaaaa, R10
	ANDQ R10, AX
	ADDQ CX, AX
	SHLQ $1, AX
	MOVQ $0x5555555555555555, R10
	XORQ R10, AX
	ANDQ BX, AX
	MOVQ AX, BX
	NOTQ BX
	ANDQ BX, CX
	MOVQ CX, R10
	MOVQ DX, CX
	DECQ CX
	SHRQ CL, R10
	ANDQ $1, R10
	MOVQ carry+48(FP), BX
	MOVQ R10, LEX_CARRY_ODD_SLASH(BX)
	MOVQ AX, CX

lb_after_slashes:
	NOTQ CX
	ANDQ CX, R9

	MOVQ R9, AX
	MOVQ AX, CX
	SHLQ $1, CX
	XORQ CX, AX
	MOVQ AX, CX
	SHLQ $2, CX
	XORQ CX, AX
	MOVQ AX, CX
	SHLQ $4, CX
	XORQ CX, AX
	MOVQ AX, CX
	SHLQ $8, CX
	XORQ CX, AX
	MOVQ AX, CX
	SHLQ $16, CX
	XORQ CX, AX
	MOVQ AX, CX
	SHLQ $32, CX
	XORQ CX, AX

	MOVQ carry+48(FP), BX
	XORQ LEX_CARRY_IN_STRING(BX), AX

	CMPQ DX, $64
	JNE  lb_partial_instring
	MOVQ AX, CX
	SARQ $63, CX
	MOVQ CX, LEX_CARRY_IN_STRING(BX)
	MOVQ $-1, R10
	JMP  lb_masks_finish

lb_partial_instring:
	MOVQ DX, CX
	DECQ CX
	MOVQ AX, R10
	SHRQ CL, R10
	ANDQ $1, R10
	NEGQ R10
	MOVQ R10, LEX_CARRY_IN_STRING(BX)

	MOVQ DX, CX
	MOVQ $1, R10
	SHLQ CL, R10
	DECQ R10
	ANDQ R10, AX
	ANDQ R10, R9

lb_masks_finish:
	ANDQ AX, R11
	MOVQ R11, LEX_BLK_INSIDE_SPEC(DI)

	MOVQ AX, CX
	NOTQ CX
	ANDQ R9, CX
	MOVQ CX, LEX_BLK_CLOSE_QUOTE(DI)

	MOVQ R9, R11
	ANDQ AX, R11

	ORQ  R9, AX
	NOTQ AX
	ANDQ R10, AX

	MOVQ R13, R9
	ANDQ AX, R9
	ORQ  R9, R11

	ORQ  R13, R12
	NOTQ R12
	ANDQ R12, AX

	MOVQ carry+48(FP), BX
	MOVQ AX, R9
	SHLQ $1, R9
	ORQ  LEX_CARRY_PREV_SCALAR(BX), R9
	NOTQ R9
	ANDQ AX, R9
	ORQ  R9, R11
	MOVQ R11, LEX_BLK_EVENT_MASK(DI)

	MOVQ DX, CX
	DECQ CX
	SHRQ CL, AX
	ANDQ $1, AX
	MOVQ AX, LEX_CARRY_PREV_SCALAR(BX)

	ADDQ $LEX_BLK_SIZE, DI
	INCQ R8
	ADDQ DX, SI
	JMP  lb_legacy_loop

lb_legacy_done:
	MOVQ out+56(FP), AX
	MOVQ R8, LEX_OUT_BLOCKS(AX)
	SUBQ pos+16(FP), SI
	MOVQ SI, LEX_OUT_BYTES(AX)
	RET

lb_empty:
	MOVQ out+56(FP), AX
	MOVQ $0, LEX_OUT_BLOCKS(AX)
	MOVQ $0, LEX_OUT_BYTES(AX)
	RET
