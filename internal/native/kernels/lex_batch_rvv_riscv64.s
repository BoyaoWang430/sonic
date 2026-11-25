//go:build !sonic_compat && !sonic_scalar && riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

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

// func lexBatch(src *byte, size int, pos int, blocks *types.LexBlockMask, capBlocks int, flags uint64, carry *types.LexBlockCarry, out *types.LexBatchOut)
//
// RVA23U64 RVV 1.0 implementation (VSETVLI E8 M4, VLE8V, VMSEQ*, VMSLTUVX, VMORMM, VMVXS).
// M4 covers 64 bytes even at VLEN=128, and clamping VL <= 64 keeps every load
// strictly within [cur, cur+width) with zero out-of-bounds reads on any VLEN.
// Never touches X1 (RA), X2 (SP), X3 (GP), X4 (TP), X8 (FP), X27 (g), or X31 (TMP).
TEXT ·lexBatch(SB), NOSPLIT, $0-64
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV pos+16(FP), X12
	MOV blocks+24(FP), X13
	MOV capBlocks+32(FP), X14
	MOV flags+40(FP), X15
	MOV carry+48(FP), X16
	MOV out+56(FP), X17

	BGEU X12, X11, lb_empty
	BLEZ X14, lb_empty

	AND $LEX_FLAG_REJECT_CTRL, X15, X15
	MOV LEX_CARRY_ODD_SLASH(X16), X22
	AND $1, X22, X22
	MOV LEX_CARRY_IN_STRING(X16), X23
	MOV LEX_CARRY_PREV_SCALAR(X16), X24
	AND $1, X24, X24

	MOV $34, X28  // '"'
	MOV $92, X29  // '\\'
	MOV $32, X30  // ' ' and bracket fold bit
	MOV $0, X9    // nBlk = 0

lb_loop:
	BGEU X9, X14, lb_done
	SUB  X12, X11, X25
	BEQZ X25, lb_done
	MOV  $64, X5
	BLTU X25, X5, lb_width_ok
	MOV  $64, X25
lb_width_ok:

	VSETVLI X25, E8, M4, TA, MA, X0
	ADD     X10, X12, X5
	VLE8V   (X5), V8

	VMSEQVX X28, V8, V1          // '"'
	VMSEQVX X29, V8, V2          // '\\'

	BEQZ     X15, lb_no_ctrl_vec
	VMSLTUVX X30, V8, V3         // < 0x20
lb_no_ctrl_vec:

	// Whitespace: ('\t'=9 | '\r'=13 via &~4 == 9) | '\n'=10 | ' '=32
	VANDVI  $-5, V8, V12
	VMSEQVI $9, V12, V4
	VMSEQVI $10, V8, V5
	VMORMM  V4, V5, V4
	VMSEQVX X30, V8, V5
	VMORMM  V4, V5, V4

	// Structural: ('['|'{' via |32 == 123) | (']'|'}' via |32 == 125) | ','=44 | ':'=58
	VORVX   X30, V8, V12
	MOV     $123, X5
	VMSEQVX X5, V12, V6
	MOV     $125, X5
	VMSEQVX X5, V12, V5
	VMORMM  V6, V5, V6
	MOV     $44, X5
	VMSEQVX X5, V8, V5
	VMORMM  V6, V5, V6
	MOV     $58, X5
	VMSEQVX X5, V8, V5
	VMORMM  V6, V5, V6

	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS    V1, X18             // quote
	VMVXS    V2, X19             // slash
	MOV      $0, X20             // ctrl
	BEQZ     X15, lb_no_ctrl_mv
	VMVXS    V3, X20
lb_no_ctrl_mv:
	VMVXS    V4, X21             // space
	VMVXS    V6, X6              // struc

	// validMask = ^uint64(0) >> (64 - width) (works for width == 64 too since 0 & 63 == 0)
	MOV $64, X5
	SUB X25, X5, X5
	MOV $-1, X26
	SRL X5, X26, X26

	AND X26, X18, X18
	AND X26, X19, X19
	AND X26, X20, X20
	AND X26, X21, X21
	AND X26, X6, X6

	// Combine masks: X18=quote, X19=slash, X20=ctrl, X21=space, X6=struc, X26=validMask
	OR   X19, X20, X20           // X20 = slash | ctrl
	MOV  $0, X7                  // X7 = escaped = 0
	OR   X22, X19, X5
	BEQZ X5, lb_quotes

	ANDN X22, X19, X5            // X5 = s = slash &^ oddSlash
	SLL  $1, X5, X7
	OR   X22, X7, X7             // X7 = follows = (s << 1) | oddSlash
	ANDN X7, X5, X19             // X19 = oddStarts = s &^ follows
	MOV  $0xaaaaaaaaaaaaaaaa, X22
	AND  X22, X19, X19           // evenStarts
	ADD  X5, X19, X19            // sum = evenStarts + s
	SLL  $1, X19, X19
	MOV  $0x5555555555555555, X22
	XOR  X22, X19, X19           // sumShifted
	AND  X7, X19, X7             // X7 = escaped
	ANDN X7, X5, X5              // unescSlashes = s &^ escaped
	ADD  $-1, X25, X22
	SRL  X22, X5, X22
	AND  $1, X22, X22            // next oddSlash

lb_quotes:
	ANDN X7, X18, X18            // X18 = unescQuotes = quote &^ escaped
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
	XOR  X23, X5, X5             // X5 = stringMask = rawPrefix ^ inString

	ADD  $-1, X25, X7
	SRL  X7, X5, X23
	AND  $1, X23, X23
	NEG  X23, X23                // next inString (0 or ^0)

	AND  X26, X5, X5             // stringMask &= validMask
	AND  X26, X18, X18           // unescQuotes &= validMask

	AND  X5, X20, X20            // insideSpecial = (slash | ctrl) & stringMask
	MOV  X20, LEX_BLK_INSIDE_SPEC(X13)

	ANDN X5, X18, X7             // closeQuotes = unescQuotes &^ stringMask
	MOV  X7, LEX_BLK_CLOSE_QUOTE(X13)

	AND  X5, X18, X20            // openQuotes = unescQuotes & stringMask
	OR   X18, X5, X5             // stringMask | unescQuotes
	ANDN X5, X26, X5             // outsideString = ^(stringMask | unescQuotes) & validMask

	AND  X5, X6, X7              // structOutside = struc & outsideString
	OR   X7, X20, X20

	OR   X6, X21, X21            // space | struc
	ANDN X21, X5, X5             // scalarBytes = outsideString &^ (space | struc)

	SLL  $1, X5, X7
	OR   X24, X7, X7
	ANDN X7, X5, X7              // scalarStarts
	OR   X7, X20, X20            // EventMask
	MOV  X20, LEX_BLK_EVENT_MASK(X13)

	ADD  $-1, X25, X7
	SRL  X7, X5, X24
	AND  $1, X24, X24            // next prevScalar

	ADD  $LEX_BLK_SIZE, X13
	ADD  $1, X9
	ADD  X25, X12
	JMP  lb_loop

lb_done:
	MOV X22, LEX_CARRY_ODD_SLASH(X16)
	MOV X23, LEX_CARRY_IN_STRING(X16)
	MOV X24, LEX_CARRY_PREV_SCALAR(X16)
	MOV X9, LEX_OUT_BLOCKS(X17)
	MOV pos+16(FP), X5
	SUB X5, X12, X12
	MOV X12, LEX_OUT_BYTES(X17)
	RET

lb_empty:
	MOV X0, LEX_OUT_BLOCKS(X17)
	MOV X0, LEX_OUT_BYTES(X17)
	RET
