//go:build !sonic_compat && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

#include "textflag.h"
#include "funcdata.h"
#include "go_asm.h"

// RISC-V RVA22 scalar implementation of cParityParseRunsAsm. It follows the
// ARM64 state machine in parse_cparity_runs_arm64.s event for event; only the
// scanners differ. Strings and whitespace are classified one 8-byte word at a
// time with Zbb ORCB, while C's 32-byte string-block decisions are preserved.
//
// Private register convention for the parser and its static helpers:
//   X9  Parser            X18 continuation state   X20 options
//   X21 input cursor      X22 tape cursor          X23 parent node
//   X24 input start       X25 input end            X26 token position
//   X28 root/object/array/key context
//   X29 0x22 bytes (quote) and X30 0x5c bytes (backslash), set by RUNS_SCAN_INIT
// Helpers update X21 and preserve all registers above. They may clobber
// X5-X7, X10-X17, X19, F0 and F1. X19 is also the main body's scratch,
// never live across a helper call. FP (X8), g (X27), SP, RA (except through
// CALL), and the assembler scratch X31 are not otherwise used.


// These constants survive the entire parser invocation; cold Go returns
// reinitialize them on the next entry.
#define RUNS_SCAN_INIT \
	MOV $0x2222222222222222, X29; \
	MOV $0x5c5c5c5c5c5c5c5c, X30

#include "parse_cparity_runs_common_riscv64.h"

// out receives 0xff in each byte of word that is a quote, a backslash or, when
// X13 is zero, a raw control byte (top three bits clear). X13 is all ones when
// controls are accepted and X17 holds 0xe0 bytes. ORCB maps each nonzero byte
// to 0xff, so no carry or borrow crosses bytes.
#define RUNS_STRING_EVENTS(word, out, tmp) \
	XOR X29, word, out; \
	XOR X30, word, tmp; \
	ORCB out, out; \
	ORCB tmp, tmp; \
	AND tmp, out; \
	AND X17, word, tmp; \
	ORCB tmp, tmp; \
	OR X13, tmp; \
	AND tmp, out; \
	XOR $-1, out, out

#define RUNS_STRING_EVENTS_FAST(word, out, tmp) \
	XOR X29, word, out; \
	XOR X30, word, tmp; \
	ORCB out, out; \
	ORCB tmp, tmp; \
	AND tmp, out; \
	XOR $-1, out, out

// X13 = 0 when option bit 5 rejects raw controls, otherwise all ones.
#define RUNS_STRING_INIT \
	AND $32, X20, X13; \
	SLTU $1, X13, X13; \
	SUB X13, X0, X13; \
	MOV $0xe0e0e0e0e0e0e0e0, X17

// Forward copies of X6 bytes from X21 to X11 (runs_string_copy_quote and
// runs_string_copy_slash) move whole words first. The destination never
// follows the source and each word is loaded before it is stored, so the
// in-place overlap is safe.

// Input: X21 follows the opening quote; X20 holds options. Return X10 length
// or negative error, X11 escape status (-1 preserves the old flag on an initial
// control error), and the updated X21. A Unicode yield also returns dst in X12.
// Like C, decisions are made per 32-byte block from the string start: before
// the first escape, a backslash ahead of the block's first quote wins even if a
// raw control precedes it. The first event is found word by word; only a
// leading control needs the rest of its block.
	PCALIGN $64
TEXT runs_string<>(SB), NOSPLIT|NOFRAME, $0-0
	MOV X21, X12
	MOV $-1, X14
	AND $32, X20, X13
	BNEZ X13, runs_string_val
runs_string_block_fast:
	MOV 0(X21), X5
	RUNS_STRING_EVENTS_FAST(X5, X6, X10)
	MOV $0, X16
	BNEZ X6, runs_string_event_fast
	MOV 8(X21), X5
	RUNS_STRING_EVENTS_FAST(X5, X6, X10)
	MOV $8, X16
	BNEZ X6, runs_string_event_fast
	MOV 16(X21), X5
	RUNS_STRING_EVENTS_FAST(X5, X6, X10)
	MOV $16, X16
	BNEZ X6, runs_string_event_fast
	MOV 24(X21), X5
	RUNS_STRING_EVENTS_FAST(X5, X6, X10)
	MOV $24, X16
	BNEZ X6, runs_string_event_fast
	ADD $32, X21
	JMP runs_string_block_fast

runs_string_event_fast:
	CTZ X6, X6
	SRL $3, X6
	ADD X16, X6
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	MOV $34, X15
	BEQ X10, X15, runs_string_plain_at
	JMP runs_string_first_escape

runs_string_val:
	MOV $0, X13
	MOV $0xe0e0e0e0e0e0e0e0, X17
runs_string_block:
	MOV 0(X21), X5
	RUNS_STRING_EVENTS(X5, X6, X10)
	MOV $0, X16
	BNEZ X6, runs_string_event
	MOV 8(X21), X5
	RUNS_STRING_EVENTS(X5, X6, X10)
	MOV $8, X16
	BNEZ X6, runs_string_event
	MOV 16(X21), X5
	RUNS_STRING_EVENTS(X5, X6, X10)
	MOV $16, X16
	BNEZ X6, runs_string_event
	MOV 24(X21), X5
	RUNS_STRING_EVENTS(X5, X6, X10)
	MOV $24, X16
	BNEZ X6, runs_string_event
	ADD $32, X21
	JMP runs_string_block

runs_string_event:
	CTZ X6, X6
	SRL $3, X6
	ADD X16, X6
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	MOV $34, X15
	BEQ X10, X15, runs_string_plain_at
	MOV $92, X15
	BEQ X10, X15, runs_string_first_escape
	ADD X6, X21
	MOV $-const_SONIC_CONTROL_CHAR, X10
	MOV X14, X11
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
	RUNS_STRING_INIT
	MOVBU 0(X21), X10
	MOV $92, X15
	BNE X10, X15, runs_string_move

runs_string_escape:
	MOVBU 1(X21), X10
	MOV $117, X15
	BEQ X10, X15, runs_string_unicode
	MOV $cParityRunsEscapes<>(SB), X7
	ADD X10, X7
	MOVBU 0(X7), X10
	BEQZ X10, runs_string_escape_error
	ADD $2, X21
	MOVB X10, 0(X11)
	ADD $1, X11
runs_string_resume:
	MOVBU 0(X21), X10
	MOV $92, X15
	BEQ X10, X15, runs_string_escape
	BEQZ X13, runs_string_move
	MOVBU 1(X21), X6
	BNE X6, X15, runs_string_move
	MOV $34, X15
	BEQ X10, X15, runs_string_copy_done
	MOVB X10, 0(X11)
	ADD $1, X21
	ADD $1, X11
	JMP runs_string_escape

runs_string_move:
	// Load and classify the complete block before storing any of it.
	MOV 0(X21), X5
	RUNS_STRING_EVENTS(X5, X6, X10)
	MOV $0, X16
	BNEZ X6, runs_string_move_event
	MOV 8(X21), X7
	RUNS_STRING_EVENTS(X7, X6, X10)
	MOV $8, X16
	BNEZ X6, runs_string_move_event
	MOV 16(X21), X15
	RUNS_STRING_EVENTS(X15, X6, X10)
	MOV $16, X16
	BNEZ X6, runs_string_move_event
	MOV 24(X21), X19
	RUNS_STRING_EVENTS(X19, X6, X10)
	MOV $24, X16
	BNEZ X6, runs_string_move_event
	MOV X5, 0(X11)
	MOV X7, 8(X11)
	MOV X15, 16(X11)
	MOV X19, 24(X11)
	ADD $32, X21
	ADD $32, X11
	JMP runs_string_move

runs_string_move_event:
	CTZ X6, X6
	SRL $3, X6
	ADD X16, X6
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	MOV $92, X15
	BNE X10, X15, runs_string_move_other
	// Without control validation nothing outranks a leading backslash. Dense
	// escapes usually leave fewer than eight plain bytes before it: move them
	// here and decode the escape without the general block-copy path.
	BEQZ X13, runs_string_move_slash
	MOV $8, X15
	BGEU X6, X15, runs_string_move_slash
	BEQZ X6, runs_string_escape
runs_string_move_short:
	ADD X6, X21
	ADD X6, X11, X7
	MOV $4, X15
	BLTU X6, X15, runs_string_move_short_lt4
	MOVWU -4(X21), X15
	MOVW X5, 0(X11)
	MOVW X15, -4(X7)
	MOV X7, X11
	JMP runs_string_escape
runs_string_move_short_lt4:
	MOV $1, X15
	BEQ X6, X15, runs_string_move_short_1
	MOVHU -2(X21), X15
	MOVH X5, 0(X11)
	MOVH X15, -2(X7)
	MOV X7, X11
	JMP runs_string_escape
runs_string_move_short_1:
	MOVB X5, 0(X11)
	MOV X7, X11
	JMP runs_string_escape
runs_string_move_other:
	MOV $34, X15
	BEQ X10, X15, runs_string_copy_quote
	// A leading raw control is reported before anything in this block moves.
	ADD X6, X21
	JMP runs_string_control_error

runs_string_move_slash:
	// The backslash moves unless a raw control precedes the block's first
	// quote. Without control validation nothing can outrank it.
	MOV X6, X19
	BNEZ X13, runs_string_copy_slash
	// Scan the rest of the block one word at a time, starting after the
	// backslash: the first quote or raw control decides.
	SUB X16, X6, X7
	ADD $1, X7
	SLL $3, X7
	MOV $0, X15
	MOV $64, X10
	BEQ X7, X10, runs_string_slash_scan
	MOV $-1, X15
	SLL X7, X15, X15
runs_string_slash_scan:
	ADD X16, X21, X7
	MOV 0(X7), X5
	XOR X29, X5, X10
	ORCB X10, X10
	AND X17, X5, X7
	ORCB X7, X7
	AND X7, X10
	XOR $-1, X10, X10
	AND X15, X10
	BNEZ X10, runs_string_slash_hit
	ADD $8, X16
	MOV $-1, X15
	MOV $32, X7
	BLTU X16, X7, runs_string_slash_scan
	JMP runs_string_copy_slash
runs_string_slash_hit:
	CTZ X10, X10
	SRL $3, X10
	ADD X16, X10
	ADD X10, X21, X7
	MOVBU 0(X7), X5
	MOV $34, X7
	BEQ X5, X7, runs_string_copy_slash
	ADD X10, X21
	JMP runs_string_control_error

runs_string_copy_quote:
	BEQZ X6, runs_string_copy_done
	MOV $8, X15
	BLTU X6, X15, runs_string_quote_short
	ADD X6, X21, X7
	MOV -8(X7), X16
runs_string_quote_words:
	MOV 0(X21), X10
	MOV X10, 0(X11)
	ADD $8, X21
	ADD $8, X11
	ADD $-8, X6
	BGEU X6, X15, runs_string_quote_words
	ADD X6, X21
	ADD X6, X11
	MOV X16, -8(X11)
	JMP runs_string_copy_done
runs_string_quote_short:
	MOV 0(X21), X5
	ADD X6, X21
	ADD X6, X11, X7
	MOV $4, X15
	BLTU X6, X15, runs_string_quote_lt4
	MOVWU -4(X21), X15
	MOVW X5, 0(X11)
	MOVW X15, -4(X7)
	MOV X7, X11
	JMP runs_string_copy_done
runs_string_quote_lt4:
	MOV $1, X15
	BEQ X6, X15, runs_string_quote_1
	MOVHU -2(X21), X15
	MOVH X5, 0(X11)
	MOVH X15, -2(X7)
	MOV X7, X11
	JMP runs_string_copy_done
runs_string_quote_1:
	MOVB X5, 0(X11)
	MOV X7, X11
runs_string_copy_done:
	ADD $1, X21
	SUB X12, X11, X10
	MOV X14, X11
	RET

runs_string_copy_slash:
	MOV X19, X6
	BEQZ X6, runs_string_escape
	MOV $8, X15
	BLTU X6, X15, runs_string_slash_short
	ADD X6, X21, X7
	MOV -8(X7), X16
runs_string_slash_words:
	MOV 0(X21), X10
	MOV X10, 0(X11)
	ADD $8, X21
	ADD $8, X11
	ADD $-8, X6
	BGEU X6, X15, runs_string_slash_words
	ADD X6, X21
	ADD X6, X11
	MOV X16, -8(X11)
	JMP runs_string_escape
runs_string_slash_short:
	MOV 0(X21), X5
	JMP runs_string_move_short

// The caller publishes this output cursor before entering the Go Unicode
// handler. The scanner itself has no dependency on Parser or its state layout.
runs_string_unicode:
	MOV X11, X12
	MOV $-const_cParityRunsStringYield, X10
	MOV X14, X11
	RET

runs_string_control_error:
	MOV $-1, X10
	MOV X14, X11
	RET
runs_string_escape_error:
	MOV $-2, X10
	MOV X14, X11
	RET

// Consume the first nonspace byte. Two scalar probes cover the common short
// separators; longer runs are scanned one word at a time. The padded input
// always ends in a nonspace byte, so the scan is bounded. X21 is input/output
// and X10 is the consumed byte. No parser, tape, or container state is used.
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
	BEQ X10, X6, runs_space_scan
	MOV $9, X7
	BEQ X10, X7, runs_space_scan
	MOV $10, X7
	BEQ X10, X7, runs_space_scan
	MOV $13, X7
	BNE X10, X7, runs_space_return
runs_space_scan:
	// Derive 0x2020202020202020 in 2 cycles from X29 (0x22...) & ~(X30 (0x5c...) >> 2).
	SRL $2, X30, X11
	ANDN X11, X29, X11
runs_space_pure:
	MOV 0(X21), X5
	XOR X11, X5, X6
	BEQZ X6, runs_space_pure_next
	CTZ X6, X7
	AND $-8, X7
	SRL X7, X5, X10
	AND $0xff, X10
	MOV $32, X15
	BGTU X10, X15, runs_space_pure_hit
	JMP runs_space_mixed
runs_space_pure_next:
	ADD $8, X21
	AND $-8, X21
	JMP runs_space_pure
runs_space_pure_hit:
	SRL $3, X7
	ADD X7, X21
	ADD $1, X21
	RET
runs_space_mixed:
	MOV $0x0909090909090909, X12
	MOV $0x0a0a0a0a0a0a0a0a, X13
	MOV $0x0d0d0d0d0d0d0d0d, X14
runs_space_word:
	// 0xff marks bytes differing from all four whitespace characters.
	MOV 0(X21), X5
	XOR X11, X5, X6
	XOR X12, X5, X7
	ORCB X6, X6
	ORCB X7, X7
	AND X7, X6
	XOR X13, X5, X7
	XOR X14, X5, X15
	ORCB X7, X7
	ORCB X15, X15
	AND X15, X7
	AND X7, X6
	BNEZ X6, runs_space_found
	ADD $8, X21
	AND $-8, X21
	JMP runs_space_word
runs_space_found:
	CTZ X6, X6
	SRL $3, X6
	ADD X6, X21
	MOVBU 0(X21), X10
	ADD $1, X21
runs_space_return:
	RET
