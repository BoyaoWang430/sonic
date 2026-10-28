//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// One bounded load supplies reusable string-event and whitespace masks.
// M4 covers 64 bytes at VLEN=128. Masks are truncated to the logical tail.
TEXT ·traversalMasks(SB), $0-40
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	validate+16(FP), X12
	BEQZ	X11, empty
	MOV	$34, X13
	MOV	$92, X14
	MOV	$32, X15
	VSETVLI X11, E8, M4, TA, MA, X0
	VLE8V	(X10), V8
	VMSEQVX X13, V8, V2
	VMSEQVX X14, V8, V3
	VMORMM	V2, V3, V2
	BEQZ	X12, whitespace
	VMSLTUVX X15, V8, V3
	VMORMM	V2, V3, V2
whitespace:
	// TAB and CR differ only in bit 2; combine their comparison.
	VANDVI	$-5, V8, V12
	VMSEQVI $9, V12, V4
	VMSEQVI $10, V8, V5
	VMORMM	V4, V5, V4
	VMSEQVX X15, V8, V5
	VMORMM	V4, V5, V4
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X12
	VMVXS	V4, X13
	NOT	X13
	MOV	$64, X16
	SUB	X11, X16, X16
	MOV	$-1, X17
	SRL	X16, X17, X17
	AND	X17, X12
	AND	X17, X13
	JMP	done
empty:
	MOV	$0, X12
	MOV	$0, X13
done:
	MOV	X12, events+24(FP)
	MOV	X13, nonspace+32(FP)
	RET
