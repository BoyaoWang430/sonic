//go:build !sonic_compat && !sonic_scalar && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

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

// Pack 8 byte-MSB flags in `src` (masked with 0x8080...) into 8 consecutive bits
// shifted left by dynamic register X7, ORing into `dst`. Uses hoisted X26.
#define SWAR_PACK(src, dst) \
	MUL X26, src, X5; \
	SRL $56, X5, X5; \
	SLL X7, X5, X5; \
	OR  X5, dst, dst

// Word 0 of an unrolled 64-byte block: directly initializes dst without zeroing/SLL/OR.
#define SWAR_PACK_W0(src, dst) \
	MUL X26, src, X5; \
	SRL $56, X5, dst

// Words 1..7 of an unrolled 64-byte block: immediate shift, zero X7 dependency.
#define SWAR_PACK_IMM(src, dst, sh) \
	MUL X26, src, X5; \
	SRL $56, X5, X5; \
	SLL $sh, X5, X5; \
	OR  X5, dst, dst

// Classify one 8-byte word in X16 at dynamic bit offset X7 into quote(X18), slash(X19),
// space(X21), struct(X22) using hoisted 64-bit constant registers:
//   X9='"'  X10='\\' X11='\n' X12=0xfb.. X13=0x09.. X14='{'|'['
//   X20=':' X23='}'|']' X24=',' X26=PACK_MAGIC X28=0x80.. X29=0x20..
// Clobbers X5, X6, X17, X30.
#define CLASSIFY_WORD_NO_CTRL \
	XOR  X9, X16, X17; \
	ORCB X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK(X17, X18); \
	XOR  X10, X16, X17; \
	ORCB X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK(X17, X19); \
	XOR  X29, X16, X17; \
	XOR  X11, X16, X6; \
	AND  X12, X16, X30; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	XOR  X13, X30, X30; \
	AND  X6, X17, X17; \
	ORCB X30, X30; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK(X17, X21); \
	OR   X29, X16, X30; \
	XOR  X14, X30, X17; \
	XOR  X23, X30, X6; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	AND  X6, X17, X17; \
	XOR  X24, X16, X6; \
	XOR  X20, X16, X30; \
	ORCB X6, X6; \
	ORCB X30, X30; \
	AND  X6, X17, X17; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK(X17, X22)

// Unrolled word 0 classifier: uses preloaded `wreg`, pipelines load of word 1 into `nextreg`.
// Accumulates non-slash byte-mask in X19 (0xff per non-slash byte; X19 == -1 iff zero slashes).
#define CLASSIFY_UNROLLED_W0(wreg, nextreg) \
	MOV  8(X15), nextreg; \
	XOR  X9, wreg, X17; \
	XOR  X10, wreg, X19; \
	ORCB X17, X17; \
	ORCB X19, X19; \
	ANDN X17, X28, X17; \
	SWAR_PACK_W0(X17, X18); \
	XOR  X29, wreg, X17; \
	XOR  X11, wreg, X6; \
	AND  X12, wreg, X30; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	XOR  X13, X30, X30; \
	AND  X6, X17, X17; \
	ORCB X30, X30; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_W0(X17, X21); \
	OR   X29, wreg, X30; \
	XOR  X14, X30, X17; \
	XOR  X23, X30, X6; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	AND  X6, X17, X17; \
	XOR  X24, wreg, X6; \
	XOR  X20, wreg, X30; \
	ORCB X6, X6; \
	ORCB X30, X30; \
	AND  X6, X17, X17; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_W0(X17, X22)

// Unrolled words 1..6 classifier: classifies `wreg`, preloads next word at `nextoff` into `nextreg`.
#define CLASSIFY_UNROLLED_WMID(wreg, nextreg, nextoff, sh) \
	MOV  nextoff(X15), nextreg; \
	XOR  X9, wreg, X17; \
	XOR  X10, wreg, X6; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	AND  X6, X19, X19; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X18, sh); \
	XOR  X29, wreg, X17; \
	XOR  X11, wreg, X6; \
	AND  X12, wreg, X30; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	XOR  X13, X30, X30; \
	AND  X6, X17, X17; \
	ORCB X30, X30; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X21, sh); \
	OR   X29, wreg, X30; \
	XOR  X14, X30, X17; \
	XOR  X23, X30, X6; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	AND  X6, X17, X17; \
	XOR  X24, wreg, X6; \
	XOR  X20, wreg, X30; \
	ORCB X6, X6; \
	ORCB X30, X30; \
	AND  X6, X17, X17; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X22, sh)

// Unrolled word 7 classifier (no next-word preload).
#define CLASSIFY_UNROLLED_W7(wreg) \
	XOR  X9, wreg, X17; \
	XOR  X10, wreg, X6; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	AND  X6, X19, X19; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X18, 56); \
	XOR  X29, wreg, X17; \
	XOR  X11, wreg, X6; \
	AND  X12, wreg, X30; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	XOR  X13, X30, X30; \
	AND  X6, X17, X17; \
	ORCB X30, X30; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X21, 56); \
	OR   X29, wreg, X30; \
	XOR  X14, X30, X17; \
	XOR  X23, X30, X6; \
	ORCB X17, X17; \
	ORCB X6, X6; \
	AND  X6, X17, X17; \
	XOR  X24, wreg, X6; \
	XOR  X20, wreg, X30; \
	ORCB X6, X6; \
	ORCB X30, X30; \
	AND  X6, X17, X17; \
	AND  X30, X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X22, 56)

// Slash-only word unpack (called only for 64-byte blocks that contain at least one '\\').
#define UNPACK_SLASH_W0 \
	MOV  0(X15), X16; \
	XOR  X10, X16, X17; \
	ORCB X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_W0(X17, X19)

#define UNPACK_SLASH_W(off, sh) \
	MOV  off(X15), X16; \
	XOR  X10, X16, X17; \
	ORCB X17, X17; \
	ANDN X17, X28, X17; \
	SWAR_PACK_IMM(X17, X19, sh)

// Classify control bytes (< 0x20: top 3 bits 0xe0 == 0) in X16 at bit offset X7,
// ORing into local ctrlMask-32(SP). Clobbers X5, X6, X17.
#define CLASSIFY_WORD_CTRL \
	SRL  $1, X28, X5; \
	OR   X28, X5, X5; \
	OR   X29, X5, X5; \
	AND  X5, X16, X17; \
	ORCB X17, X17; \
	ANDN X17, X28, X17; \
	MUL  X26, X17, X5; \
	SRL  $56, X5, X5; \
	SLL  X7, X5, X5; \
	MOV  ctrlMask-32(SP), X6; \
	OR   X5, X6, X6; \
	MOV  X6, ctrlMask-32(SP)

// func lexBatch(src *byte, size int, pos int, blocks *types.LexBlockMask, capBlocks int, flags uint64, carry *types.LexBlockCarry, out *types.LexBatchOut)
//
// RVA22U64 Zbb/SWAR 8-byte word implementation (ORCB, ANDN, bounded overlapping word).
// Hoists all 12 SWAR 64-bit constants once per batch, unrolls full 64-byte blocks
// on the default (rejectCtrl == 0) fast path, and defers slash bitmask packing to
// blocks that actually contain a backslash.
// Never touches X1 (RA), X2 (SP), X3 (GP), X4 (TP), X8 (FP), X27 (g), or X31 (TMP).
TEXT ·lexBatch(SB), NOSPLIT, $40-64
	MOV size+8(FP), X15
	MOV pos+16(FP), X16
	MOV capBlocks+32(FP), X17

	BGEU X16, X15, lb_empty
	BLEZ X17, lb_empty

	// Normalize carry bit flags once per call.
	MOV carry+48(FP), X5
	MOV LEX_CARRY_ODD_SLASH(X5), X6
	AND $1, X6, X6
	MOV X6, LEX_CARRY_ODD_SLASH(X5)
	MOV LEX_CARRY_PREV_SCALAR(X5), X6
	AND $1, X6, X6
	MOV X6, LEX_CARRY_PREV_SCALAR(X5)

	MOV X16, curPos-8(SP)
	MOV blocks+24(FP), X6
	MOV X6, curBlkPtr-16(SP)
	MOV $0, X6
	MOV X6, nBlk-24(SP)
	MOV flags+40(FP), X6
	AND $LEX_FLAG_REJECT_CTRL, X6, X6
	MOV X6, rejectCtrl-40(SP)

	// Tiny (< 8B total input) fast exit before materializing SWAR constants.
	SLTI $8, X15, X5
	BNEZ X5, lb_small_bytes_entry

	// Hoist all 12 64-bit SWAR constants once for the entire batch.
	MOV $0x2222222222222222, X9
	MOV $0x5c5c5c5c5c5c5c5c, X10
	MOV $0x0a0a0a0a0a0a0a0a, X11
	MOV $0xfbfbfbfbfbfbfbfb, X12
	MOV $0x0909090909090909, X13
	MOV $0x7b7b7b7b7b7b7b7b, X14
	MOV $0x3a3a3a3a3a3a3a3a, X20
	MOV $0x7d7d7d7d7d7d7d7d, X23
	MOV $0x2c2c2c2c2c2c2c2c, X24
	MOV $0x0002040810204081, X26
	MOV $0x8080808080808080, X28
	MOV $0x2020202020202020, X29

	// Dedicated unrolled full-64B loop when rejectCtrl == 0 and >= 64B available.
	BNEZ X6, lb_loop
	SUB  X16, X15, X5             // X5 = size - pos
	SRL  $6, X5, X25              // X25 = full 64B blocks available
	BEQZ X25, lb_loop
	BLTU X25, X17, lb_fast64_cnt_ok
	MOV  X17, X25                 // X25 = min(capBlocks, (size - pos) >> 6)
lb_fast64_cnt_ok:
	MOV  X25, nBlk-24(SP)         // record final fast block count up front
	SLL  $6, X25, X5
	ADD  X5, X16, X5
	MOV  X5, curPos-8(SP)         // record curPos after fast64 loop up front
	MOV  src+0(FP), X15
	ADD  X16, X15, X15            // X15 = &src[pos]

lb_fast64_block:
	MOV  0(X15), X16
	CLASSIFY_UNROLLED_W0(X16, X7)
	CLASSIFY_UNROLLED_WMID(X7, X16, 16, 8)
	CLASSIFY_UNROLLED_WMID(X16, X7, 24, 16)
	CLASSIFY_UNROLLED_WMID(X7, X16, 32, 24)
	CLASSIFY_UNROLLED_WMID(X16, X7, 40, 32)
	CLASSIFY_UNROLLED_WMID(X7, X16, 48, 40)
	CLASSIFY_UNROLLED_WMID(X16, X7, 56, 48)
	CLASSIFY_UNROLLED_W7(X7)

	// X19 == -1 iff none of the 64 bytes was '\\'.
	ADD  $1, X19, X5
	BNEZ X5, lb_f64_unpack_slash
	MOV  $0, X19                  // slash = 0

lb_f64_have_slash:
	// Combine masks for width == 64, rejectCtrl == 0 (validMask == -1, ctrl == 0).
	MOV  carry+48(FP), X16
	MOV  LEX_CARRY_ODD_SLASH(X16), X30
	MOV  X19, X17                 // X17 = slash (ctrl == 0)
	MOV  $0, X7                   // X7 = escaped = 0
	OR   X30, X19, X5
	BEQZ X5, lb_f64_quotes

	ANDN X30, X19, X5             // s = slash &^ oddSlash
	SLL  $1, X5, X7
	OR   X30, X7, X7              // follows = (s << 1) | oddSlash
	ANDN X7, X5, X19              // oddStarts = s &^ follows
	MOV  $0xaaaaaaaaaaaaaaaa, X6
	AND  X6, X19, X19
	ADD  X5, X19, X19
	SLL  $1, X19, X19
	MOV  $0x5555555555555555, X6
	XOR  X6, X19, X19
	AND  X7, X19, X7              // escaped
	ANDN X7, X5, X5               // unescSlashes = s &^ escaped
	SRL  $63, X5, X6
	MOV  X6, LEX_CARRY_ODD_SLASH(X16)

lb_f64_quotes:
	ANDN X7, X18, X18             // unescQuotes = quote &^ escaped
	MOV  X18, X5
	SLL  $1, X5, X7
	XOR  X7, X5, X5
	SLL  $2, X5, X7
	XOR  X7, X5, X5
	SLL  $4, X5, X7
	XOR  X7, X5, X5
	SLL  $8, X5, X7
	XOR  X7, X5, X5
	SLL  $16, X5, X7
	XOR  X7, X5, X5
	SLL  $32, X5, X7
	XOR  X7, X5, X5
	MOV  LEX_CARRY_IN_STRING(X16), X6
	XOR  X6, X5, X5               // stringMask = rawPrefix ^ inString

	SRL  $63, X5, X6
	NEG  X6, X6
	MOV  X6, LEX_CARRY_IN_STRING(X16)

	MOV  curBlkPtr-16(SP), X7
	AND  X5, X17, X17             // insideSpecial
	MOV  X17, LEX_BLK_INSIDE_SPEC(X7)

	ANDN X5, X18, X6              // closeQuotes
	MOV  X6, LEX_BLK_CLOSE_QUOTE(X7)

	AND  X5, X18, X17             // openQuotes
	OR   X18, X5, X5
	XORI $-1, X5, X5              // outsideString (width == 64 -> validMask == -1)

	AND  X5, X22, X6              // structOutside
	OR   X6, X17, X17

	OR   X22, X21, X21            // space | struc
	ANDN X21, X5, X5              // scalarBytes

	MOV  LEX_CARRY_PREV_SCALAR(X16), X6
	SLL  $1, X5, X30
	OR   X6, X30, X30
	ANDN X30, X5, X30             // scalarStarts
	OR   X30, X17, X17            // EventMask
	MOV  X17, LEX_BLK_EVENT_MASK(X7)

	SRL  $63, X5, X6
	MOV  X6, LEX_CARRY_PREV_SCALAR(X16)

	ADD  $LEX_BLK_SIZE, X7
	MOV  X7, curBlkPtr-16(SP)
	ADD  $64, X15
	ADD  $-1, X25
	BNEZ X25, lb_fast64_block
	JMP  lb_loop

// Cold out-of-line slash bitmask unpack for blocks containing at least one '\\'.
lb_f64_unpack_slash:
	UNPACK_SLASH_W0
	UNPACK_SLASH_W(8, 8)
	UNPACK_SLASH_W(16, 16)
	UNPACK_SLASH_W(24, 24)
	UNPACK_SLASH_W(32, 32)
	UNPACK_SLASH_W(40, 40)
	UNPACK_SLASH_W(48, 48)
	UNPACK_SLASH_W(56, 56)
	JMP  lb_f64_have_slash

lb_loop:
	MOV  nBlk-24(SP), X5
	MOV  capBlocks+32(FP), X6
	BGEU X5, X6, lb_done
	MOV  curPos-8(SP), X15
	MOV  size+8(FP), X6
	SUB  X15, X6, X25             // X25 = remaining bytes
	BEQZ X25, lb_done

	// Zero block accumulators: X18=quote, X19=slash, X21=space, X22=struct
	MOV  $0, X18
	MOV  $0, X19
	MOV  $0, X21
	MOV  $0, X22
	MOV  src+0(FP), X5
	ADD  X5, X15, X15             // X15 = &src[curPos]

	MOV  $64, X5
	BLTU X25, X5, lb_partial_block
	MOV  $64, X25

	// Full 64-byte block with rejectCtrl != 0.
	MOV  $0, X6
	MOV  X6, ctrlMask-32(SP)
	MOV  $0, X7
lb_full64_ctrl_word:
	MOV  0(X15), X16
	CLASSIFY_WORD_NO_CTRL
	CLASSIFY_WORD_CTRL
	ADD  $8, X15
	ADD  $8, X7
	BLTU X7, X25, lb_full64_ctrl_word

	MOV  $-1, X30
	MOV  ctrlMask-32(SP), X17
	JMP  lb_combine_masks

// Remaining bytes 1..63 while total size >= 8.
lb_partial_block:
	MOV  $0, X6
	MOV  X6, ctrlMask-32(SP)
	MOV  $0, X7
	SLTI $8, X25, X5
	BNEZ X5, lb_lt8_overlap

	MOV  rejectCtrl-40(SP), X5
	BNEZ X5, lb_partial_ctrl_word

lb_partial_noctrl_word:
	MOV  0(X15), X16
lb_partial_noctrl_classify:
	CLASSIFY_WORD_NO_CTRL
	ADD  $8, X7
	BEQ  X7, X25, lb_partial_done
	SUB  X7, X25, X5
	SLTI $8, X5, X6
	ADD  $8, X15
	BEQZ X6, lb_partial_noctrl_word

	// Bounded overlapping final 8-byte word at offset (width - 8) within block.
	MOV  src+0(FP), X15
	MOV  curPos-8(SP), X5
	ADD  X5, X15, X15
	ADD  $-8, X25, X7
	ADD  X7, X15, X15
	MOV  0(X15), X16
	JMP  lb_partial_noctrl_classify

lb_partial_ctrl_word:
	MOV  0(X15), X16
lb_partial_ctrl_classify:
	CLASSIFY_WORD_NO_CTRL
	CLASSIFY_WORD_CTRL
	ADD  $8, X7
	BEQ  X7, X25, lb_partial_done
	SUB  X7, X25, X5
	SLTI $8, X5, X6
	ADD  $8, X15
	BEQZ X6, lb_partial_ctrl_word

	MOV  src+0(FP), X15
	MOV  curPos-8(SP), X5
	ADD  X5, X15, X15
	ADD  $-8, X25, X7
	ADD  X7, X15, X15
	MOV  0(X15), X16
	JMP  lb_partial_ctrl_classify

// width in 1..7 while total size >= 8: load [size-8, size), classify 8 bits, shift right by (8 - width).
lb_lt8_overlap:
	MOV  src+0(FP), X15
	MOV  size+8(FP), X5
	ADD  X5, X15, X15
	MOV  -8(X15), X16
	MOV  $0, X7
	CLASSIFY_WORD_NO_CTRL
	MOV  rejectCtrl-40(SP), X5
	BEQZ X5, lb_lt8_shift
	CLASSIFY_WORD_CTRL
lb_lt8_shift:
	MOV  $8, X5
	SUB  X25, X5, X5              // X5 = 8 - width (1..7)
	SRL  X5, X18, X18
	SRL  X5, X19, X19
	SRL  X5, X21, X21
	SRL  X5, X22, X22
	MOV  ctrlMask-32(SP), X6
	SRL  X5, X6, X6
	MOV  X6, ctrlMask-32(SP)

lb_partial_done:
	MOV  $64, X5
	SUB  X25, X5, X5
	MOV  $-1, X30
	SRL  X5, X30, X30             // X30 = validMask
	AND  X30, X18, X18
	AND  X30, X19, X19
	AND  X30, X21, X21
	AND  X30, X22, X22
	MOV  ctrlMask-32(SP), X17
	AND  X30, X17, X17
	JMP  lb_combine_masks

// Tiny (< 8B total input) exact byte loop: zero out-of-bounds reads, single block.
lb_small_bytes_entry:
	SUB  X16, X15, X25            // X25 = remaining (1..7)
	MOV  $0, X18
	MOV  $0, X19
	MOV  $0, X17                  // X17 = ctrl
	MOV  $0, X21
	MOV  $0, X22
	MOV  src+0(FP), X15
	ADD  X16, X15, X15
	MOV  $0, X7
lb_sb_loop:
	ADD   X7, X15, X6
	MOVBU 0(X6), X16
	MOV   $1, X30
	SLL   X7, X30, X30
	SLTIU $0x20, X16, X5
	BEQZ  X5, lb_sb_after_ctrl
	OR    X30, X17, X17
lb_sb_after_ctrl:
	MOV   $'"', X5
	BEQ   X16, X5, lb_sb_quote
	MOV   $'\\', X5
	BEQ   X16, X5, lb_sb_slash
	MOV   $' ', X5
	BEQ   X16, X5, lb_sb_space
	MOV   $'\n', X5
	BEQ   X16, X5, lb_sb_space
	MOV   $'\r', X5
	BEQ   X16, X5, lb_sb_space
	MOV   $'\t', X5
	BEQ   X16, X5, lb_sb_space
	MOV   $',', X5
	BEQ   X16, X5, lb_sb_struct
	MOV   $':', X5
	BEQ   X16, X5, lb_sb_struct
	MOV   $'{', X5
	BEQ   X16, X5, lb_sb_struct
	MOV   $'}', X5
	BEQ   X16, X5, lb_sb_struct
	MOV   $'[', X5
	BEQ   X16, X5, lb_sb_struct
	MOV   $']', X5
	BEQ   X16, X5, lb_sb_struct
	JMP   lb_sb_next
lb_sb_quote:
	OR    X30, X18, X18
	JMP   lb_sb_next
lb_sb_slash:
	OR    X30, X19, X19
	JMP   lb_sb_next
lb_sb_space:
	OR    X30, X21, X21
	JMP   lb_sb_next
lb_sb_struct:
	OR    X30, X22, X22
lb_sb_next:
	ADD   $1, X7
	BLTU  X7, X25, lb_sb_loop

	MOV   rejectCtrl-40(SP), X5
	BNEZ  X5, lb_sb_ctrl_kept
	MOV   $0, X17
lb_sb_ctrl_kept:
	MOV   $64, X5
	SUB   X25, X5, X5
	MOV   $-1, X30
	SRL   X5, X30, X30

// Combine per-block bitmasks using ONLY working registers (X5, X6, X7, X15, X16, X17, X18, X19, X21, X22, X25, X30).
// All 12 SWAR constant registers remain intact across blocks.
// Inputs: X18=quote, X19=slash, X17=ctrl, X21=space, X22=struct, X25=width, X30=validMask.
lb_combine_masks:
	MOV  carry+48(FP), X16
	MOV  LEX_CARRY_ODD_SLASH(X16), X15
	OR   X19, X17, X17            // X17 = slash | ctrl
	MOV  $0, X7                   // X7 = escaped = 0
	OR   X15, X19, X5
	BEQZ X5, lb_quotes

	ANDN X15, X19, X5             // s = slash &^ oddSlash
	SLL  $1, X5, X7
	OR   X15, X7, X7              // follows = (s << 1) | oddSlash
	ANDN X7, X5, X19              // oddStarts = s &^ follows
	MOV  $0xaaaaaaaaaaaaaaaa, X6
	AND  X6, X19, X19
	ADD  X5, X19, X19
	SLL  $1, X19, X19
	MOV  $0x5555555555555555, X6
	XOR  X6, X19, X19
	AND  X7, X19, X7              // escaped
	ANDN X7, X5, X5               // unescSlashes = s &^ escaped
	ADD  $-1, X25, X6
	SRL  X6, X5, X6
	AND  $1, X6, X6
	MOV  X6, LEX_CARRY_ODD_SLASH(X16)

lb_quotes:
	ANDN X7, X18, X18             // unescQuotes = quote &^ escaped
	MOV  X18, X5
	SLL  $1, X5, X7
	XOR  X7, X5, X5
	SLL  $2, X5, X7
	XOR  X7, X5, X5
	SLL  $4, X5, X7
	XOR  X7, X5, X5
	SLL  $8, X5, X7
	XOR  X7, X5, X5
	SLL  $16, X5, X7
	XOR  X7, X5, X5
	SLL  $32, X5, X7
	XOR  X7, X5, X5
	MOV  LEX_CARRY_IN_STRING(X16), X6
	XOR  X6, X5, X5               // stringMask = rawPrefix ^ inString

	ADD  $-1, X25, X7
	SRL  X7, X5, X6
	AND  $1, X6, X6
	NEG  X6, X6
	MOV  X6, LEX_CARRY_IN_STRING(X16)

	AND  X30, X5, X5
	AND  X30, X18, X18

	MOV  curBlkPtr-16(SP), X15
	AND  X5, X17, X17             // insideSpecial
	MOV  X17, LEX_BLK_INSIDE_SPEC(X15)

	ANDN X5, X18, X7              // closeQuotes
	MOV  X7, LEX_BLK_CLOSE_QUOTE(X15)

	AND  X5, X18, X17             // openQuotes
	OR   X18, X5, X5
	ANDN X5, X30, X5              // outsideString

	AND  X5, X22, X7              // structOutside
	OR   X7, X17, X17

	OR   X22, X21, X21            // space | struc
	ANDN X21, X5, X5              // scalarBytes

	MOV  LEX_CARRY_PREV_SCALAR(X16), X6
	SLL  $1, X5, X7
	OR   X6, X7, X7
	ANDN X7, X5, X7               // scalarStarts
	OR   X7, X17, X17             // EventMask
	MOV  X17, LEX_BLK_EVENT_MASK(X15)

	ADD  $-1, X25, X7
	SRL  X7, X5, X6
	AND  $1, X6, X6
	MOV  X6, LEX_CARRY_PREV_SCALAR(X16)

	ADD  $LEX_BLK_SIZE, X15
	MOV  X15, curBlkPtr-16(SP)
	MOV  nBlk-24(SP), X5
	ADD  $1, X5
	MOV  X5, nBlk-24(SP)
	MOV  curPos-8(SP), X6
	ADD  X25, X6, X6
	MOV  X6, curPos-8(SP)
	// For size < 8 (tiny entry), curPos == size so lb_loop exits immediately.
	JMP  lb_loop

lb_done:
	MOV out+56(FP), X17
	MOV nBlk-24(SP), X5
	MOV X5, LEX_OUT_BLOCKS(X17)
	MOV curPos-8(SP), X6
	MOV pos+16(FP), X5
	SUB X5, X6, X6
	MOV X6, LEX_OUT_BYTES(X17)
	RET

lb_empty:
	MOV out+56(FP), X17
	MOV X0, LEX_OUT_BLOCKS(X17)
	MOV X0, LEX_OUT_BYTES(X17)
	RET
