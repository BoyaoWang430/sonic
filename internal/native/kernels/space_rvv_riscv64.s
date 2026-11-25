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

// func space(src *byte, size int) (n int)
// Use the normal Go ABI0 stack arguments and return slot. The assembler owns
// frame metadata. X27 (g), SP, and the return address are never modified here.
TEXT ·space(SB), NOSPLIT, $0-24
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X12
	BEQZ	X11, done
	MOV	$32, X15

loop:
	// VL is selected from the remaining length on every iteration. Even the
	// final load stays inside the logical input; no readable padding is used.
	VSETVLI	X11, E8, M4, TA, MA, X13
	VLE8V	(X10), V8
	VMSEQVX	X15, V8, V2
	// TAB and CR differ only in bit 2. NOR leaves non-whitespace lanes set.
	VANDVI	$-5, V8, V12
	VMSEQVI	$9, V12, V3
	VMORMM	V2, V3, V2
	VMSEQVI	$10, V8, V3
	VMNORMM	V2, V3, V2
	VFIRSTM	V2, X14
	BGEZ	X14, found
	ADD	X13, X10
	ADD	X13, X12
	SUB	X13, X11
	BNEZ	X11, loop
	JMP	done

found:
	ADD	X14, X12

done:
	MOV	X12, n+16(FP)
	RET
