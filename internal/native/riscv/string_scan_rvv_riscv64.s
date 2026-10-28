//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func scanString(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError)
// Loads cover at most min(remaining, 64) bytes. Packed masks handle all
// quote/backslash runs and strict escape checks within each block.
// g (X27), SP, and RA are preserved.
TEXT ·scanString(SB), $0-56
#include "string_scan_rvv_riscv64.h"

// The existing scanString entry retains its ABI and instruction stream. Only
// callers that reuse the final block request these two additional results.
#define STRING_SCAN_BLOCK
TEXT ·scanStringBlock(SB), $0-72
#include "string_scan_rvv_riscv64.h"
#undef STRING_SCAN_BLOCK
