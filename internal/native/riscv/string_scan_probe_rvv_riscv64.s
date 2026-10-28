//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// Test probe: func stringScanVectorBytes() (n int)
TEXT ·stringScanVectorBytes(SB), $0-8
	// The production scanner caps each logical block at 64 bytes.
	MOV	$64, X10
	VSETVLI	X10, E8, M4, TA, MA, X10
	MOV	X10, n+0(FP)
	RET
