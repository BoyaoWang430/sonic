//go:build !sonic_compat && !sonic_scalar && arm64 && go1.20 && !go1.28

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

#define PACK64(m0, m1, m2, m3, outreg) \
	VAND  V30.B16, m0.B16, m0.B16; \
	VAND  V30.B16, m1.B16, m1.B16; \
	VAND  V30.B16, m2.B16, m2.B16; \
	VAND  V30.B16, m3.B16, m3.B16; \
	VADDP m1.B16, m0.B16, m0.B16; \
	VADDP m3.B16, m2.B16, m2.B16; \
	VADDP m2.B16, m0.B16, m0.B16; \
	VADDP m0.B16, m0.B16, m0.B16; \
	VMOV  m0.D[0], outreg

// func lexBatch(src *byte, size int, pos int, blocks *types.LexBlockMask, capBlocks int, flags uint64, carry *types.LexBlockCarry, out *types.LexBatchOut)
//
// Register map (never touches R18, R27, g=R28, FP=R29, LR=R30, or V8..V15):
//   R0  = src (*byte)
//   R1  = size (int)
//   R2  = cur (int)
//   R3  = blocks (*LexBlockMask)
//   R4  = capBlocks (int)
//   R5  = flags (uint64)
//   R6  = carry (*LexBlockCarry)
//   R7  = out (*LexBatchOut)
//   R8  = nBlk (produced block count)
//   R19 = initial pos
//   R20 = oddSlash (0 or 1)
//   R21 = inString (0 or ^0)
//   R22 = prevScalar (0 or 1)
//   R23 = 0x5555555555555555
//   R24 = 0xaaaaaaaaaaaaaaaa
//   R25 = current block width (1..64)
TEXT ·lexBatch(SB), NOSPLIT, $0-64
	MOVD src+0(FP), R0
	MOVD size+8(FP), R1
	MOVD pos+16(FP), R2
	MOVD blocks+24(FP), R3
	MOVD capBlocks+32(FP), R4
	MOVD flags+40(FP), R5
	MOVD carry+48(FP), R6
	MOVD out+56(FP), R7

	MOVD $0, R8
	MOVD R2, R19

	CMP  R1, R2
	BHS  lb_empty
	CMP  $0, R4
	BLE  lb_empty

	MOVD LEX_CARRY_ODD_SLASH(R6), R20
	AND  $1, R20, R20
	MOVD LEX_CARRY_IN_STRING(R6), R21
	MOVD LEX_CARRY_PREV_SCALAR(R6), R22
	AND  $1, R22, R22

	MOVD $0x5555555555555555, R23
	MOVD $0xaaaaaaaaaaaaaaaa, R24

	VMOVI $0x22, V20.B16 // '"'
	VMOVI $0x5c, V21.B16 // '\\'
	VMOVI $0x00, V22.B16 // 0
	VMOVI $0x0f, V23.B16 // low nibble mask
	VMOVI $0x20, V24.B16 // ASCII clamp floor & case/bracket fold bit
	MOVD  $lexBatchTables<>(SB), R12
	VLD1  (R12), [V28.B16, V29.B16, V30.B16]

lb_loop:
	CMP  R4, R8
	BHS  lb_done
	SUB  R2, R1, R25
	CBZ  R25, lb_done
	CMP  $64, R25
	BLO  lb_tail_block

	MOVD  $64, R25
	ADD   R0, R2, R12
	FLDPQ (R12), (F0, F1)
	FLDPQ 32(R12), (F2, F3)

lb_classify_v0_v3:
	// 1. Quotes ('"') -> R9
	VCMEQ V20.B16, V0.B16, V4.B16
	VCMEQ V20.B16, V1.B16, V5.B16
	VCMEQ V20.B16, V2.B16, V6.B16
	VCMEQ V20.B16, V3.B16, V7.B16
	PACK64(V4, V5, V6, V7, R9)

	// 2. Backslashes ('\\') -> R10
	VCMEQ V21.B16, V0.B16, V4.B16
	VCMEQ V21.B16, V1.B16, V5.B16
	VCMEQ V21.B16, V2.B16, V6.B16
	VCMEQ V21.B16, V3.B16, V7.B16
	PACK64(V4, V5, V6, V7, R10)

	// 3. Raw control (< 0x20) -> R11 (only computed when LEX_FLAG_REJECT_CTRL is set!)
	MOVD $0, R11
	TBZ  $0, R5, lb_no_ctrl
	VUSHR $5, V0.B16, V4.B16
	VUSHR $5, V1.B16, V5.B16
	VUSHR $5, V2.B16, V6.B16
	VUSHR $5, V3.B16, V7.B16
	VCMEQ V22.B16, V4.B16, V4.B16
	VCMEQ V22.B16, V5.B16, V5.B16
	VCMEQ V22.B16, V6.B16, V6.B16
	VCMEQ V22.B16, V7.B16, V7.B16
	PACK64(V4, V5, V6, V7, R11)
lb_no_ctrl:

	// 4. Low-nibble indices in V4..V7
	VAND V23.B16, V0.B16, V4.B16
	VAND V23.B16, V1.B16, V5.B16
	VAND V23.B16, V2.B16, V6.B16
	VAND V23.B16, V3.B16, V7.B16

	// 5. Exact JSON structural ('{', '}', '[', ']', ':', ',') -> R14
	//    (struct_lut[b & 0x0f] == (max_u8(b, 0x20) | 0x20))
	VTBL   V4.B16, [V29.B16], V16.B16
	VTBL   V5.B16, [V29.B16], V17.B16
	VTBL   V6.B16, [V29.B16], V18.B16
	VTBL   V7.B16, [V29.B16], V19.B16
	VUMAX  V24.B16, V0.B16, V26.B16
	VUMAX  V24.B16, V1.B16, V27.B16
	VORR   V24.B16, V26.B16, V26.B16
	VORR   V24.B16, V27.B16, V27.B16
	VCMEQ  V26.B16, V16.B16, V16.B16
	VCMEQ  V27.B16, V17.B16, V17.B16
	VUMAX  V24.B16, V2.B16, V26.B16
	VUMAX  V24.B16, V3.B16, V27.B16
	VORR   V24.B16, V26.B16, V26.B16
	VORR   V24.B16, V27.B16, V27.B16
	VCMEQ  V26.B16, V18.B16, V18.B16
	VCMEQ  V27.B16, V19.B16, V19.B16
	PACK64(V16, V17, V18, V19, R14)

	// 6. Exact JSON whitespace (' ', '\t', '\n', '\r') -> R13
	//    (ws_lut[b & 0x0f] == b)
	VTBL  V4.B16, [V28.B16], V4.B16
	VTBL  V5.B16, [V28.B16], V5.B16
	VTBL  V6.B16, [V28.B16], V6.B16
	VTBL  V7.B16, [V28.B16], V7.B16
	VCMEQ V0.B16, V4.B16, V4.B16
	VCMEQ V1.B16, V5.B16, V5.B16
	VCMEQ V2.B16, V6.B16, V6.B16
	VCMEQ V3.B16, V7.B16, V7.B16
	PACK64(V4, V5, V6, V7, R13)
	B     lb_combine

// Tail block (< 64 bytes, width = R25 in 1..63).
// Whenever size >= 16, loads strictly inside [src, src+size) via 16B NEON chunks
// plus an in-bounds [size-16, size) VTBL-shifted vector, then shares lb_classify_v0_v3!
lb_tail_block:
	CMP  $16, R1
	BLO  lb_small_scalar

	ADD  R0, R2, R12            // ptr = src + cur
	ADD  R0, R1, R16
	SUB  $16, R16, R16          // end16 = src + size - 16 (in-bounds since size >= 16)
	MOVD $lexShiftSeq<>(SB), R13

	CMP  $32, R25
	BHS  lb_tail_ge32
	CMP  $16, R25
	BHS  lb_tail_16_31

	// width in 1..15
	MOVD $16, R17
	SUB  R25, R17, R17
	ADD  R13, R17, R17
	VLD1 (R17), [V6.B16]
	VLD1 (R16), [V7.B16]
	VTBL V6.B16, [V7.B16], V0.B16
	B    lb_classify_v0_v3

lb_tail_16_31:
	VLD1 (R12), [V0.B16]
	SUB  $16, R25, R15
	CBZ  R15, lb_classify_v0_v3
	MOVD $16, R17
	SUB  R15, R17, R17
	ADD  R13, R17, R17
	VLD1 (R17), [V6.B16]
	VLD1 (R16), [V7.B16]
	VTBL V6.B16, [V7.B16], V1.B16
	B    lb_classify_v0_v3

lb_tail_ge32:
	FLDPQ (R12), (F0, F1)
	CMP   $48, R25
	BHS   lb_tail_48_63
	SUB   $32, R25, R15
	CBZ   R15, lb_classify_v0_v3
	MOVD  $16, R17
	SUB   R15, R17, R17
	ADD   R13, R17, R17
	VLD1  (R17), [V6.B16]
	VLD1  (R16), [V7.B16]
	VTBL  V6.B16, [V7.B16], V2.B16
	B     lb_classify_v0_v3

lb_tail_48_63:
	ADD  $32, R12, R14
	VLD1 (R14), [V2.B16]
	SUB  $48, R25, R15
	CBZ  R15, lb_classify_v0_v3
	MOVD $16, R17
	SUB  R15, R17, R17
	ADD  R13, R17, R17
	VLD1 (R17), [V6.B16]
	VLD1 (R16), [V7.B16]
	VTBL V6.B16, [V7.B16], V3.B16
	B    lb_classify_v0_v3

// Tiny (< 16B total input) exact byte loop: zero out-of-bounds reads.
lb_small_scalar:
	MOVD $0, R9   // quote
	MOVD $0, R10  // slash
	MOVD $0, R11  // ctrl
	MOVD $0, R13  // space
	MOVD $0, R14  // struct
	MOVD $0, R15  // k = 0
	ADD  R0, R2, R12

lb_small_byte:
	MOVBU (R12)(R15), R16
	MOVD  $1, R17
	LSL   R15, R17, R17
	TBZ   $0, R5, lb_sb_after_ctrl
	CMP   $0x20, R16
	BHS   lb_sb_after_ctrl
	ORR   R17, R11, R11
lb_sb_after_ctrl:
	CMP   $'"', R16
	BEQ   lb_sb_quote
	CMP   $'\\', R16
	BEQ   lb_sb_slash
	CMP   $' ', R16
	BEQ   lb_sb_space
	CMP   $'\n', R16
	BEQ   lb_sb_space
	CMP   $'\r', R16
	BEQ   lb_sb_space
	CMP   $'\t', R16
	BEQ   lb_sb_space
	CMP   $',', R16
	BEQ   lb_sb_struct
	CMP   $':', R16
	BEQ   lb_sb_struct
	CMP   $'{', R16
	BEQ   lb_sb_struct
	CMP   $'}', R16
	BEQ   lb_sb_struct
	CMP   $'[', R16
	BEQ   lb_sb_struct
	CMP   $']', R16
	BEQ   lb_sb_struct
	B     lb_sb_next
lb_sb_quote:
	ORR   R17, R9, R9
	B     lb_sb_next
lb_sb_slash:
	ORR   R17, R10, R10
	B     lb_sb_next
lb_sb_space:
	ORR   R17, R13, R13
	B     lb_sb_next
lb_sb_struct:
	ORR   R17, R14, R14
lb_sb_next:
	ADD   $1, R15
	CMP   R25, R15
	BLO   lb_small_byte

// Combine 64-bit masks for current block of width R25 (1..64):
//   R9=quote, R10=slash, R11=ctrl, R13=space, R14=struct
lb_combine:
	CMP  $64, R25
	BEQ  lb_full_mask
	MOVD $1, R26
	LSL  R25, R26, R26
	SUB  $1, R26, R26
	AND  R26, R9, R9
	AND  R26, R10, R10
	AND  R26, R11, R11
	AND  R26, R13, R13
	AND  R26, R14, R14
	B    lb_slash_escape
lb_full_mask:
	MOVD $-1, R26

lb_slash_escape:
	MOVD $0, R15 // escaped = 0
	ORR  R20, R10, R16
	CBZ  R16, lb_quotes
	BIC  R20, R10, R16          // s = slash &^ oddSlash
	LSL  $1, R16, R17
	ORR  R20, R17, R17          // follows = (s << 1) | oddSlash
	BIC  R17, R16, R15          // oddStarts = s &^ follows
	AND  R24, R15, R15          // evenStarts
	ADD  R16, R15, R15          // sum
	LSL  $1, R15, R15
	EOR  R23, R15, R15          // sumShifted
	AND  R17, R15, R15          // escaped
	BIC  R15, R10, R16          // unescSlashes = slash &^ escaped
	SUB  $1, R25, R17
	LSR  R17, R16, R20
	AND  $1, R20, R20

lb_quotes:
	BIC  R15, R9, R9            // unescQuotes = quote &^ escaped
	MOVD R9, R15
	EOR  R15<<1, R15, R15
	EOR  R15<<2, R15, R15
	EOR  R15<<4, R15, R15
	EOR  R15<<8, R15, R15
	EOR  R15<<16, R15, R15
	EOR  R15<<32, R15, R15
	EOR  R21, R15, R15          // stringMask = rawPrefix ^ inString

	CMP  $64, R25
	BNE  lb_partial_instring
	ASR  $63, R15, R21
	B    lb_masks_finish

lb_partial_instring:
	SUB  $1, R25, R17
	LSR  R17, R15, R21
	AND  $1, R21, R21
	NEG  R21, R21
	AND  R26, R15, R15
	AND  R26, R9, R9

lb_masks_finish:
	AND  R9, R15, R16           // openQuotes = unescQuotes & stringMask
	BIC  R15, R9, R17           // closeQuotes = unescQuotes &^ stringMask
	ORR  R11, R10, R10          // slash | ctrl
	AND  R15, R10, R10          // insideSpecial = (slash | ctrl) & stringMask

	ORR  R9, R15, R15           // stringMask | unescQuotes
	BIC  R15, R26, R15          // outsideString = ^(stringMask | unescQuotes) & validMask
	AND  R15, R14, R11          // structOutside = struc & outsideString
	ORR  R14, R13, R13          // space | struc
	BIC  R13, R15, R15          // scalarBytes = outsideString &^ (space | struc)
	LSL  $1, R15, R13
	ORR  R22, R13, R13          // (scalarBytes << 1) | prevScalar
	BIC  R13, R15, R13          // scalarStarts

	SUB  $1, R25, R14
	LSR  R14, R15, R22
	AND  $1, R22, R22           // prevScalar = (scalarBytes >> (width - 1)) & 1

	ORR  R11, R16, R16
	ORR  R13, R16, R16          // EventMask = openQuotes | structOutside | scalarStarts

	MOVD R16, LEX_BLK_EVENT_MASK(R3)
	MOVD R17, LEX_BLK_CLOSE_QUOTE(R3)
	MOVD R10, LEX_BLK_INSIDE_SPEC(R3)
	ADD  $LEX_BLK_SIZE, R3
	ADD  $1, R8
	ADD  R25, R2
	B    lb_loop

lb_done:
	MOVD R20, LEX_CARRY_ODD_SLASH(R6)
	MOVD R21, LEX_CARRY_IN_STRING(R6)
	MOVD R22, LEX_CARRY_PREV_SCALAR(R6)
	MOVD R8, LEX_OUT_BLOCKS(R7)
	SUB  R19, R2, R12
	MOVD R12, LEX_OUT_BYTES(R7)
	RET

lb_empty:
	MOVD ZR, LEX_OUT_BLOCKS(R7)
	MOVD ZR, LEX_OUT_BYTES(R7)
	RET

// 48-byte constant tables for LexBatch NEON classifier:
//   [0..15]  ws_lut:     exact match for ' ' (0x20), '\t' (0x09), '\n' (0x0a), '\r' (0x0d)
//   [16..31] struct_lut: exact match on (max_u8(b, 0x20) | 0x20) for ':' (0x3a), '['/'{' (0x7b), ',' (0x2c), ']'/'}' (0x7d)
//   [32..47] bitWeights: 1, 2, 4, 8, 16, 32, 64, 128 x 2
DATA lexBatchTables<>+0(SB)/8, $0x0000000000000020
DATA lexBatchTables<>+8(SB)/8, $0x00000d00000a0900
DATA lexBatchTables<>+16(SB)/8, $0x0000000000000000
DATA lexBatchTables<>+24(SB)/8, $0x00007d2c7b3a0000
DATA lexBatchTables<>+32(SB)/8, $0x8040201008040201
DATA lexBatchTables<>+40(SB)/8, $0x8040201008040201
GLOBL lexBatchTables<>(SB), RODATA|NOPTR, $48

// 32-byte index sequence [0..31] for VTBL right-shifting [size-16, size) tail loads.
DATA lexShiftSeq<>+0(SB)/8, $0x0706050403020100
DATA lexShiftSeq<>+8(SB)/8, $0x0f0e0d0c0b0a0908
DATA lexShiftSeq<>+16(SB)/8, $0x1716151413121110
DATA lexShiftSeq<>+24(SB)/8, $0x1f1e1d1c1b1a1918
GLOBL lexShiftSeq<>(SB), RODATA|NOPTR, $32
