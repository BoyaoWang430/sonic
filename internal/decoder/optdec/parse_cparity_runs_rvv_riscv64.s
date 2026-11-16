//go:build !sonic_compat && riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"
#include "funcdata.h"
#include "go_asm.h"

// RISC-V RVA23 RVV implementation of cParityParseRunsAsm. It shares the outer
// state machine and exact numeric fast path with the RVA22 scalar parser while
// accelerating whitespace and string runs with RVV vector instructions.
//
// Private register convention for the parser and its static helpers:
//   X9  Parser            X18 continuation state   X20 options
//   X21 input cursor      X22 tape cursor          X23 parent node
//   X24 input start       X25 input end            X26 token position
//   X28 root/object/array/key context
//   X29 34 (quote) and X30 92 (backslash), set by RUNS_SCAN_INIT
// Helpers update X21 and preserve all registers above. They may clobber
// X5-X7, X10-X17, X19, F0, F1, and vector registers. X19 is also the main
// body's scratch, never live across a helper call. FP (X8), g (X27), SP, RA
// (except through CALL), and the assembler scratch X31 are not otherwise used.


// These constants survive the entire parser invocation; cold Go returns
// reinitialize them on the next entry.
#define RUNS_SCAN_INIT \
	MOV $34, X29; \
	MOV $92, X30

#include "parse_cparity_runs_common_riscv64.h"

// Input: X21 follows the opening quote; X20 holds options. Return X10 length
// or negative error, X11 escape status (-1 preserves the old flag on an initial
// control error), and the updated X21. A Unicode yield also returns dst in X12.
// Like C, decisions are made per 32-byte block from the string start: before
// the first escape, a backslash ahead of the block's first quote wins even if a
// raw control precedes it.
	PCALIGN $64
TEXT runs_string<>(SB), NOSPLIT|NOFRAME, $0-0
	MOV X21, X12
	AND $32, X20, X13
	MOV $32, X15
	VSETVLI X15, E8, M2, TA, MA, X0
	BNEZ X13, runs_string_scan_val
runs_string_scan_fast:
	VLE8V (X21), V8
	VMSEQVX X30, V8, V2
	VMSEQVX X29, V8, V3
	VMORMM V2, V3, V4
	VFIRSTM V4, X6
	BGEZ X6, runs_string_event_fast
	ADD $32, X21
	JMP runs_string_scan_fast
runs_string_event_fast:
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	BEQ X10, X29, runs_string_plain_at
	JMP runs_string_first_escape

runs_string_scan_val:
	VLE8V (X21), V8
	VMSEQVX X30, V8, V2
	VMSEQVX X29, V8, V3
	VMORMM V2, V3, V4
	VMSLTUVX X15, V8, V5
	VMORMM V4, V5, V4
	VFIRSTM V4, X6
	BGEZ X6, runs_string_event
	ADD $32, X21
	JMP runs_string_scan_val

runs_string_event:
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	BEQ X10, X29, runs_string_plain_at
	BEQ X10, X30, runs_string_first_escape
	ADD X6, X21
	MOV $-const_SONIC_CONTROL_CHAR, X10
	MOV $-1, X11
	RET

runs_string_plain_at:
	ADD X6, X21
	SUB X12, X21, X10
	ADD $1, X21
	MOV $0, X11
	RET

runs_string_first_escape:
	ADD X6, X21
	MOV X21, X11
	// ASCII decoding and Unicode resumes share one escaped-string body,
	// without a nested call.
	JMP runs_string_continue<>(SB)

// Input: X21 source, X11 next output byte, X12 original content start, X20
// options. Entry is either the first backslash or the byte after Go's Unicode
// run. Results match runs_string. Only the caller owns Unicode continuation.
// After the first escape, a raw control before the block's first quote wins
// over a backslash; a quote ahead of both ends the string.
TEXT runs_string_continue<>(SB), NOSPLIT|NOFRAME, $0-0
	MOV $1, X14
	AND $32, X20, X13
	MOV $32, X15
	MOV $cParityRunsEscapes<>(SB), X17
	MOVBU 0(X21), X10
	BNE X10, X30, runs_string_move

runs_string_escape:
	MOVBU 1(X21), X10
	MOV $117, X5
	BEQ X10, X5, runs_string_unicode
	ADD X10, X17, X7
	MOVBU 0(X7), X10
	BEQZ X10, runs_string_escape_error
	ADD $2, X21
	MOVB X10, 0(X11)
	ADD $1, X11
runs_string_resume:
	MOVBU 0(X21), X10
	BEQ X10, X30, runs_string_escape

runs_string_move:
	VSETVLI X15, E8, M2, TA, MA, X0
	BNEZ X13, runs_string_move_loop_val
runs_string_move_loop_fast:
	// V8 retains the 32-byte snapshot for overlapping in-place copies.
	VLE8V (X21), V8
	VMSEQVX X30, V8, V2
	VMSEQVX X29, V8, V3
	VMORMM V2, V3, V4
	VFIRSTM V4, X6
	BGEZ X6, runs_string_move_event_fast
	VSE8V V8, (X11)
	ADD $32, X21
	ADD $32, X11
	JMP runs_string_move_loop_fast
runs_string_move_event_fast:
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	BEQ X10, X29, runs_string_copy_quote
	JMP runs_string_copy_slash

runs_string_move_loop_val:
	// V8 retains the 32-byte snapshot for overlapping in-place copies.
	VLE8V (X21), V8
	VMSEQVX X30, V8, V2
	VMSEQVX X29, V8, V3
	VMORMM V2, V3, V4
	VMSLTUVX X15, V8, V5
	VMORMM V4, V5, V4
	VFIRSTM V4, X6
	BGEZ X6, runs_string_move_event
	VSE8V V8, (X11)
	ADD $32, X21
	ADD $32, X11
	JMP runs_string_move_loop_val

runs_string_move_event:
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	BEQ X10, X29, runs_string_copy_quote
	BNE X10, X30, runs_string_control_error
	// After the first escape, C checks controls before the next backslash.
	BEQZ X13, runs_string_copy_slash
	VFIRSTM V5, X16
	BLTZ X16, runs_string_copy_slash
	VFIRSTM V3, X7
	BLTZ X7, runs_string_control_at_x16
	BLTU X7, X16, runs_string_copy_slash
runs_string_control_at_x16:
	MOV X16, X6
	JMP runs_string_control_error

runs_string_copy_quote:
	BEQZ X6, runs_string_copy_done
	VSETVLI X6, E8, M2, TA, MA, X0
	VSE8V V8, (X11)
	ADD X6, X21
	ADD X6, X11
runs_string_copy_done:
	ADD $1, X21
	SUB X12, X11, X10
	MOV X14, X11
	RET

runs_string_copy_slash:
	BEQZ X6, runs_string_escape
	VSETVLI X6, E8, M2, TA, MA, X0
	VSE8V V8, (X11)
	ADD X6, X21
	ADD X6, X11
	JMP runs_string_escape

// The caller publishes this output cursor before entering the Go Unicode
// handler. The scanner itself has no dependency on Parser or its state layout.
runs_string_unicode:
	MOV X11, X12
	MOV $-const_cParityRunsStringYield, X10
	MOV X14, X11
	RET

runs_string_control_error:
	ADD X6, X21
	MOV $-1, X10
	MOV X14, X11
	RET
runs_string_escape_error:
	MOV $-2, X10
	MOV X14, X11
	RET

// Consume the first nonspace byte. The first two scalar probes leave the
// cached 64-byte block untouched, matching skip_space_impl in the C parser.
	PCALIGN $64
TEXT runs_space<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVBU 0(X21), X10
	ADD $1, X21
	MOV $32, X6
	BLTU X6, X10, runs_space_return
	BEQ X10, X6, runs_space_second
	MOV $9, X7
	BEQ X10, X7, runs_space_second
	MOV $10, X7
	BEQ X10, X7, runs_space_second
	MOV $13, X7
	BNE X10, X7, runs_space_return
runs_space_second:
	MOVBU 0(X21), X10
	ADD $1, X21
	BLTU X6, X10, runs_space_return
	BEQ X10, X6, runs_space_cached
	MOV $9, X7
	BEQ X10, X7, runs_space_cached
	MOV $10, X7
	BEQ X10, X7, runs_space_cached
	MOV $13, X7
	BNE X10, X7, runs_space_return
runs_space_cached:
	MOV WB_NBK_OFF(X9), X11
	ADD X24, X11, X11
	ADD $-64, X11, X11
	SUB X11, X21, X12
	MOV $64, X15
	BGEU X12, X15, runs_space_scan
	MOV WB_NBK_BITS(X9), X14
	SRL X12, X14, X14
	BNEZ X14, runs_space_cache_hit
	ADD $64, X11, X21
runs_space_scan:
	VSETVLI X15, E8, M4, TA, MA, X0
	VLE8V (X21), V8
	VMSEQVX X6, V8, V4
	VANDVI $-5, V8, V12
	VMSEQVI $9, V12, V5
	VMORMM V4, V5, V4
	VMSEQVI $10, V8, V5
	VMNORMM V4, V5, V4
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS V4, X14
	BNEZ X14, runs_space_refilled
	ADD $64, X21
	JMP runs_space_scan
runs_space_refilled:
	SUB X24, X21, X11
	ADD $64, X11, X11
	MOV X11, WB_NBK_OFF(X9)
	MOV X14, WB_NBK_BITS(X9)
runs_space_cache_hit:
	CTZ X14, X6
	ADD X6, X21
	MOVBU 0(X21), X10
	ADD $1, X21
runs_space_return:
	RET
