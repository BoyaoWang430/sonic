//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"

// func scanString(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError)
// Bounded words yield exact quote/escape/control masks. Consume every event
// before loading another word. A bounded unaligned word also covers the tail;
// its consumed prefix is never interpreted again.
// FP (X8), g (X27), SP, RA, and the assembler scratch X31 are not used.
TEXT ·scanString(SB), $0-56
#include "string_scan_scalar_riscv64.h"

// The scan itself is identical; only this entry exports already loaded events.
// func scanStringBlock(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringBlock(SB), $0-72
#define STRING_SCAN_BLOCK
#include "string_scan_scalar_riscv64.h"
#undef STRING_SCAN_BLOCK
