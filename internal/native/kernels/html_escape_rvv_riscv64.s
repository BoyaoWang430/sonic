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

// func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)
// Complete HTML escaping in one ABI0 call, with ordinary frame metadata/RET.
// g (X27), SP, and RA are never used as scratch registers.
TEXT ·htmlEscape(SB), NOSPLIT, $0-48
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	dst+16(FP), X12
	MOV	capacity+24(FP), X13
	MOV	X11, X14
	MOV	X13, X15
	MOV	$60, X16
	MOV	$38, X18
	MOV	$226, X19

scan:
	BEQZ	X11, success
	BEQZ	X13, exhausted
	MOV	X11, X20
	BLEU	X11, X13, bounded
	MOV	X13, X20
bounded:
	VSETVLI	X20, E8, M4, TA, MA, X5
	VLE8V	(X10), V8
	// '<' and '>' differ only in bit 1; classify them together.
	VANDVI	$-3, V8, V16
	VMSEQVX	X16, V16, V2
	VMSEQVX	X18, V8, V3
	VMORMM	V2, V3, V2
	VMSEQVX	X19, V8, V3
	VMORMM	V2, V3, V2
	VFIRSTM	V2, X6
	BGEZ	X6, special
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
	JMP	scan

special:
	BEQZ	X6, classify
	VSETVLI	X6, E8, M4, TA, MA, X5
	VSE8V	V8, (X12)
	ADD	X5, X10
	SUB	X5, X11
	ADD	X5, X12
	SUB	X5, X13
classify:
	MOVBU	0(X10), X7
	MOV	$1, X21                  // input bytes consumed by this escape
	BNE	X7, X19, ascii_escape
	MOV	$3, X5
	BLTU	X11, X5, plain_e2
	MOVBU	1(X10), X6
	MOV	$128, X5
	BNE	X6, X5, plain_e2
	MOVBU	2(X10), X24
	MOV	$168, X5
	BEQ	X24, X5, unicode_escape
	MOV	$169, X5
	BNE	X24, X5, plain_e2
unicode_escape:
	MOV	$3, X21
	MOV	$0x3032755c, X22         // "\u20"
	ADD	$-112, X24               // '8' or '9'
	SLLI	$8, X24
	OR	$0x32, X24               // "28" or "29"
	JMP	emit
ascii_escape:
	MOV	$0x3030755c, X22         // "\u00"
	SRLI	$4, X7, X23
	ADD	$48, X23
	AND	$15, X7, X24
	MOV	$10, X5
	BLTU	X24, X5, hex_digit
	ADD	$87, X24
	JMP	hex_pack
hex_digit:
	ADD	$48, X24
hex_pack:
	SLLI	$8, X24
	OR	X23, X24
emit:
	MOV	$6, X5
	BLTU	X13, X5, exhausted
	MOVW	X22, 0(X12)
	MOVH	X24, 4(X12)
	ADD	X21, X10
	SUB	X21, X11
	ADD	$6, X12
	SUB	$6, X13
	JMP	scan
plain_e2:
	// An incomplete or different UTF-8 sequence is copied byte for byte.
	// At least one output byte remains because this byte was inside VL.
	MOVB	X7, 0(X12)
	ADD	$1, X10
	SUB	$1, X11
	ADD	$1, X12
	SUB	$1, X13
	JMP	scan

exhausted:
	MOV	X14, X5
	SUB	X11, X5
	XOR	$-1, X5
	JMP	done
success:
	MOV	X14, X5
done:
	MOV	X15, X6
	SUB	X13, X6
	MOV	X5, consumed+32(FP)
	MOV	X6, written+40(FP)
	RET
