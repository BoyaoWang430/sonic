//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// Test-only vector-group capacity observation; the production operation does not call it.
TEXT ·stringDecodeVectorBytes(SB), $0-8
	VSETVLI X0, E8, M4, TA, MA, X10
	MOV X10, n+0(FP)
	RET
