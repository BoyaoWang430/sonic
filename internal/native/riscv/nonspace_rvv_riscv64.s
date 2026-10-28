//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func nonSpaceMask(src *byte, size int) (mask uint64)
// size is in [0,64]. M4 holds all 64 lanes even at the minimum VLEN=128.
TEXT ·nonSpaceMask(SB), $0-24
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV $0, X12
	BEQZ X11, ns_done
	MOV $32, X15
	VSETVLI X11, E8, M4, TA, MA, X0
	VLE8V (X10), V8
	VMSEQVX X15, V8, V4
	// TAB and CR differ only in bit 2. NOR marks non-whitespace lanes.
	VANDVI $-5, V8, V12
	VMSEQVI $9, V12, V5
	VMORMM V4, V5, V4
	VMSEQVI $10, V8, V5
	VMNORMM V4, V5, V4
	// Extract the packed mask, then clear all agnostic tail bits.
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS V4, X12
	MOV $64, X16
	SUB X11, X16, X16
	MOV $-1, X17
	SRL X16, X17, X17
	AND X17, X12
ns_done:
	MOV X12, mask+16(FP)
	RET
