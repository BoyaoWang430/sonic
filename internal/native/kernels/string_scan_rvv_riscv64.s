//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// The four entries match the shared Go declarations in scanning_native.go.
// Each call scans a complete string in one RVV invocation. The block entries
// also return raw quote/backslash events in the already loaded suffix after the
// closing quote; errors return an empty suffix.

// func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringRelaxed(SB), $0-48
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$0, X19                 // relaxed-mode odd backslash carry
	MOV	$24, X5
	BLTU	X11, X5, relaxed_small

	PCALIGN $64
scan:
	BEQZ	X11, eof
	MOV	$64, X5
	MINU	X11, X5, X5
	// The M4 input group V8-V11 fits 64 bytes at the minimum VLEN=128.
	VSETVLI X5, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X22
	VMVXS	V3, X23
	MOV	$64, X5
	SUB	X20, X5, X5
	MOV	$-1, X21
	SRL	X5, X21
	AND	X21, X22
	AND	X21, X23

	// Positions escaped by odd backslash runs, including carry-in. Arithmetic
	// handles all runs together. Tail bits from mask-agnostic vector results
	// were cleared above, and carry-out is derived from the last active byte.
	MOV	$-1, X25
	OR	X19, X23, X5
	BEQZ	X5, relaxed_quote
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
	NOT	X19, X5
	AND	X23, X5, X6
	SLL	$1, X6, X7
	OR	X19, X7                 // positions following a backslash
	NOT	X7, X5
	AND	X6, X5
	AND	X29, X5                 // odd-indexed run starts
	ADD	X6, X5
	SLL	$1, X5
	XOR	X28, X5
	AND	X7, X5                  // escaped positions
	NOT	X5, X25
	AND	X25, X22                // unescaped quotes

	// A backslash after the closing quote is not part of this string.
	BGEZ	X14, relaxed_quote
	BEQZ	X23, relaxed_quote
	CTZ	X23, X5
	CTZ	X22, X6
	BGEU	X5, X6, relaxed_quote
	ADD	X13, X5, X14
relaxed_quote:
	BNEZ	X22, quote
	AND	X23, X25, X19
	ADD	$-1, X20, X5
	SRL	X5, X19
	AND	$1, X19                 // correct even when actual VL is odd
	ADD	X20, X10
	ADD	X20, X13
	SUB	X20, X11
	JMP	scan

relaxed_small:
	MOV	$8, X24
	BLTU	X11, X24, relaxed_short_loop
	MOV	$0x2222222222222222, X28
	MOV	$0x5c5c5c5c5c5c5c5c, X29
	MOV	$0x8080808080808080, X31
relaxed_word_loop:
	MOV	0(X10), X21
relaxed_word_classify:
	XOR	X28, X21, X7
	ORCB	X7, X7
	XOR	X29, X21, X6
	ORCB	X6, X6
	AND	X6, X7, X5
	ANDN	X5, X31, X22
	BNEZ	X22, relaxed_word_event
	ADD	X24, X10
	ADD	X24, X13
	SUB	X24, X11
	BEQZ	X11, eof
	BLTU	X11, X24, relaxed_word_tail
	JMP	relaxed_word_loop
relaxed_word_tail:
	MOV	$4, X5
	BLTU	X11, X5, relaxed_short_loop
	ADD	X11, X10, X5
	MOV	-8(X5), X21
	SUB	X11, X24, X5
	SLL	$3, X5
	SRL	X5, X21
	MOV	X11, X24
	JMP	relaxed_word_classify
relaxed_word_event:
	CTZ	X22, X30
	SRL	X30, X7, X25
	SRL	$3, X30
	AND	$1, X25
	BEQZ	X25, event_quote
	ADD	X30, X10
	ADD	X30, X13
	SUB	X30, X11
	MOV	$8, X5
	BGEU	X11, X5, scan

relaxed_short_loop:
	BEQZ	X11, eof
	MOVBU	0(X10), X7
	ADD	$1, X10
	ADD	$1, X13
	SUB	$1, X11
	BEQ	X7, X16, short_quote
	BNE	X7, X17, relaxed_short_loop
	BGEZ	X14, relaxed_short_escape_seen
	ADD	$-1, X13, X14
relaxed_short_escape_seen:
	BEQZ	X11, eof
	ADD	$1, X10
	ADD	$1, X13
	SUB	$1, X11
	JMP	relaxed_short_loop

quote:
	CTZ	X22, X30
event_quote:
	ADD	X30, X13, X5
	ADD	$1, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
short_quote:
	MOV	X13, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
eof:
	MOV	X15, X5
	MOV	X15, X6
	MOV	$1, X7                  // types.ERR_EOF
done:
	MOV	X5, end+16(FP)
	MOV	X14, escape+24(FP)
	MOV	X6, errorPos+32(FP)
	MOV	X7, err+40(FP)
	RET

// func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringStrict(SB), $0-48
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18
	MOV	$24, X5
	BLTU	X11, X5, strict_small

	PCALIGN $64
scan:
	BEQZ	X11, eof
	MOV	$8, X5
	BLTU	X11, X5, strict_short
	MOV	$64, X5
	MINU	X11, X5, X5
	// The M4 input group V8-V11 fits 64 bytes at the minimum VLEN=128.
	VSETVLI X5, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VMSLTUVX X18, V8, V4
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X22
	VMVXS	V3, X23
	MOV	$64, X5
	SUB	X20, X5, X5
	MOV	$-1, X21
	SRL	X5, X21
	AND	X21, X22
	AND	X21, X23

	// Strict mode consumes a crossing escape completely before the next load,
	// so it enters each block with no pending escape.
	BEQZ	X23, strict
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
	SLL	$1, X23, X7             // positions following a backslash
	NOT	X7, X5
	AND	X23, X5
	AND	X29, X5                 // odd-indexed run starts
	ADD	X23, X5
	SLL	$1, X5
	XOR	X28, X5
	AND	X7, X5                  // escaped positions
	NOT	X5, X25
	AND	X25, X22                // unescaped quotes
	AND	X25, X23                // unescaped backslashes

strict:
	VMVXS	V4, X24
	AND	X21, X24
	OR	X22, X23, X26
	OR	X24, X26                // ordered quote / escape / raw-control events
strict_event:
	BEQZ	X26, advance
	CTZ	X26, X30
	ADD	X30, X10, X8
	MOVBU	0(X8), X7
	BEQ	X7, X16, event_quote
	BNE	X7, X17, invalid
	BGEZ	X14, escape_seen
	ADD	X13, X30, X14
escape_seen:
	SUB	X30, X11, X9           // bytes available at this backslash
	MOV	$2, X31
	BLT	X9, X31, eof
	MOVBU	1(X8), X7
	BEQ	X7, X16, consume_escape
	BEQ	X7, X17, consume_escape
	MOV	$110, X6                // n
	BEQ	X7, X6, consume_escape
	MOV	$116, X6                // t
	BEQ	X7, X6, consume_escape
	MOV	$114, X6                // r
	BEQ	X7, X6, consume_escape
	MOV	$47, X6                 // /
	BEQ	X7, X6, consume_escape
	MOV	$98, X6                 // b
	BEQ	X7, X6, consume_escape
	MOV	$102, X6                // f
	BEQ	X7, X6, consume_escape
	MOV	$117, X6                // u
	BNE	X7, X6, invalid

	// Require all four hex bytes before checking their syntax, retaining the
	// EOF/backslash error-position contract. Lone surrogates remain allowed.
	MOV	$6, X31
	BLT	X9, X31, eof
	ADD	$2, X8
	MOV	$4, X9
hex:
	MOVBU	0(X8), X7
	ADD	$-48, X7, X5
	MOV	$10, X6
	BLTU	X5, X6, hex_valid
	OR	$32, X7
	ADD	$-97, X7
	MOV	$6, X6
	BGEU	X7, X6, invalid
hex_valid:
	ADD	$1, X8
	SUB	$1, X9
	BNEZ	X9, hex
consume_escape:
	ADD	X30, X31
	BLTU	X20, X31, crossing_escape
	// The second byte of a simple escape is already absent from the event
	// mask. A valid Unicode escape's hex digits cannot contain any events.
	ADD	$-1, X26, X5
	AND	X5, X26
	JMP	strict_event
crossing_escape:
	MOV	X31, X20                // advance beyond the consumed escape
advance:
	ADD	X20, X10
	ADD	X20, X13
	SUB	X20, X11
	JMP	scan

strict_small:
	MOV	$8, X24
	BLTU	X11, X24, strict_short
	MOV	$0x2222222222222222, X28
	MOV	$0x5c5c5c5c5c5c5c5c, X29
	MOV	$0xe0e0e0e0e0e0e0e0, X25
	MOV	$0x8080808080808080, X31
strict_word_loop:
	MOV	0(X10), X21
strict_word_classify:
	XOR	X28, X21, X7
	ORCB	X7, X7
	XOR	X29, X21, X6
	ORCB	X6, X6
	AND	X6, X7, X5
	AND	X25, X21, X6
	ORCB	X6, X6
	AND	X6, X5
	ANDN	X5, X31, X22
	BNEZ	X22, strict_word_event
	ADD	X24, X10
	ADD	X24, X13
	SUB	X24, X11
	BEQZ	X11, eof
	BLTU	X11, X24, strict_word_tail
	JMP	strict_word_loop
strict_word_tail:
	MOV	$4, X5
	BLTU	X11, X5, strict_short
	ADD	X11, X10, X5
	MOV	-8(X5), X21
	SUB	X11, X24, X5
	SLL	$3, X5
	SRL	X5, X21
	SLL	$3, X11, X5
	MOV	$-1, X6
	SLL	X5, X6
	OR	X6, X21
	MOV	X11, X24
	JMP	strict_word_classify
strict_word_event:
	CTZ	X22, X30
	SRL	$3, X30
	ADD	X30, X10, X8
	MOVBU	0(X8), X7
	BEQ	X7, X16, event_quote
	BNE	X7, X17, invalid
	ADD	X30, X10
	ADD	X30, X13
	SUB	X30, X11
	JMP	scan

strict_short:
	MOV	$0, X30
	MOV	$1, X20
strict_short_loop:
	BEQZ	X11, eof
	MOVBU	0(X10), X7
	BEQ	X7, X16, event_quote
	BLTU	X7, X18, invalid
	BEQ	X7, X17, strict_short_escape
	ADD	$1, X10
	ADD	$1, X13
	SUB	$1, X11
	JMP	strict_short_loop
strict_short_escape:
	MOV	X10, X8
	BGEZ	X14, escape_seen
	MOV	X13, X14
	JMP	escape_seen

event_quote:
	ADD	X30, X13, X5
	ADD	$1, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
invalid:
	ADD	X30, X13, X5
	MOV	X5, X6
	MOV	$2, X7                  // types.ERR_INVALID_CHAR
	JMP	done
eof:
	MOV	X15, X5
	MOV	X15, X6
	MOV	$1, X7                  // types.ERR_EOF
done:
	MOV	X5, end+16(FP)
	MOV	X14, escape+24(FP)
	MOV	X6, errorPos+32(FP)
	MOV	X7, err+40(FP)
	RET

// func scanStringRelaxedBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringRelaxedBlock(SB), $0-64
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$0, X19                 // relaxed-mode odd backslash carry

scan:
	BEQZ	X11, eof
	MOV	$64, X5
	MINU	X11, X5, X5
	// The M4 input group V8-V11 fits 64 bytes at the minimum VLEN=128.
	VSETVLI X5, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X22
	VMVXS	V3, X23
	MOV	$64, X5
	SUB	X20, X5, X5
	MOV	$-1, X21
	SRL	X5, X21
	AND	X21, X22
	AND	X21, X23

	// Positions escaped by odd backslash runs, including carry-in. Arithmetic
	// handles all runs together. Tail bits from mask-agnostic vector results
	// were cleared above, and carry-out is derived from the last active byte.
	MOV	$-1, X25
	OR	X19, X23, X5
	BEQZ	X5, relaxed_quote
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
	NOT	X19, X5
	AND	X23, X5, X6
	SLL	$1, X6, X7
	OR	X19, X7                 // positions following a backslash
	NOT	X7, X5
	AND	X6, X5
	AND	X29, X5                 // odd-indexed run starts
	ADD	X6, X5
	SLL	$1, X5
	XOR	X28, X5
	AND	X7, X5                  // escaped positions
	NOT	X5, X25
	AND	X25, X22                // unescaped quotes

	// A backslash after the closing quote is not part of this string.
	BGEZ	X14, relaxed_quote
	BEQZ	X23, relaxed_quote
	CTZ	X23, X5
	CTZ	X22, X6
	BGEU	X5, X6, relaxed_quote
	ADD	X13, X5, X14
relaxed_quote:
	BNEZ	X22, quote
	AND	X23, X25, X19
	ADD	$-1, X20, X5
	SRL	X5, X19
	AND	$1, X19                 // correct even when actual VL is odd
	ADD	X20, X10
	ADD	X20, X13
	SUB	X20, X11
	JMP	scan

quote:
	CTZ	X22, X30
	ADD	X30, X13, X5
	ADD	$1, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
eof:
	MOV	X15, X5
	MOV	X15, X6
	MOV	$1, X7                  // types.ERR_EOF
done:
	// Publish only the unused suffix of the final bounded load. V2/V3 still
	// contain the raw quote/backslash masks; the scalar copies were filtered
	// for escapes while scanning. Current E64/VL=1 permits both extractions.
	MOV	$0, X22
	MOV	$0, X23
	BNEZ	X7, block_tail_done
	ADD	$1, X30, X24
	SUB	X24, X20, X23
	BEQZ	X23, block_tail_done     // also avoids shifting by 64
	VMVXS	V2, X22
	VMVXS	V3, X25
	OR	X25, X22
	AND	X21, X22                // clear mask-agnostic inactive lanes
	SRL	X24, X22
block_tail_done:
	MOV	X22, tailEvents+48(FP)
	MOV	X23, tailWidth+56(FP)
	MOV	X5, end+16(FP)
	MOV	X14, escape+24(FP)
	MOV	X6, errorPos+32(FP)
	MOV	X7, err+40(FP)
	RET

// func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringStrictBlock(SB), $0-64
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18

scan:
	BEQZ	X11, eof
	MOV	$64, X5
	MINU	X11, X5, X5
	// The M4 input group V8-V11 fits 64 bytes at the minimum VLEN=128.
	VSETVLI X5, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VMSLTUVX X18, V8, V4
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X22
	VMVXS	V3, X23
	MOV	$64, X5
	SUB	X20, X5, X5
	MOV	$-1, X21
	SRL	X5, X21
	AND	X21, X22
	AND	X21, X23

	// Strict mode consumes a crossing escape completely before the next load,
	// so it enters each block with no pending escape.
	BEQZ	X23, strict
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
	SLL	$1, X23, X7             // positions following a backslash
	NOT	X7, X5
	AND	X23, X5
	AND	X29, X5                 // odd-indexed run starts
	ADD	X23, X5
	SLL	$1, X5
	XOR	X28, X5
	AND	X7, X5                  // escaped positions
	NOT	X5, X25
	AND	X25, X22                // unescaped quotes
	AND	X25, X23                // unescaped backslashes

strict:
	VMVXS	V4, X24
	AND	X21, X24
	OR	X22, X23, X26
	OR	X24, X26                // ordered quote / escape / raw-control events
strict_event:
	BEQZ	X26, advance
	CTZ	X26, X30
	ADD	X30, X10, X8
	MOVBU	0(X8), X7
	BEQ	X7, X16, event_quote
	BNE	X7, X17, invalid
	BGEZ	X14, escape_seen
	ADD	X13, X30, X14
escape_seen:
	SUB	X30, X11, X9           // bytes available at this backslash
	MOV	$2, X31
	BLT	X9, X31, eof
	MOVBU	1(X8), X7
	BEQ	X7, X16, consume_escape
	BEQ	X7, X17, consume_escape
	MOV	$110, X6                // n
	BEQ	X7, X6, consume_escape
	MOV	$116, X6                // t
	BEQ	X7, X6, consume_escape
	MOV	$114, X6                // r
	BEQ	X7, X6, consume_escape
	MOV	$47, X6                 // /
	BEQ	X7, X6, consume_escape
	MOV	$98, X6                 // b
	BEQ	X7, X6, consume_escape
	MOV	$102, X6                // f
	BEQ	X7, X6, consume_escape
	MOV	$117, X6                // u
	BNE	X7, X6, invalid

	// Require all four hex bytes before checking their syntax, retaining the
	// EOF/backslash error-position contract. Lone surrogates remain allowed.
	MOV	$6, X31
	BLT	X9, X31, eof
	ADD	$2, X8
	MOV	$4, X9
hex:
	MOVBU	0(X8), X7
	ADD	$-48, X7, X5
	MOV	$10, X6
	BLTU	X5, X6, hex_valid
	OR	$32, X7
	ADD	$-97, X7
	MOV	$6, X6
	BGEU	X7, X6, invalid
hex_valid:
	ADD	$1, X8
	SUB	$1, X9
	BNEZ	X9, hex
consume_escape:
	ADD	X30, X31
	BLTU	X20, X31, crossing_escape
	// The second byte of a simple escape is already absent from the event
	// mask. A valid Unicode escape's hex digits cannot contain any events.
	ADD	$-1, X26, X5
	AND	X5, X26
	JMP	strict_event
crossing_escape:
	MOV	X31, X20                // advance beyond the consumed escape
advance:
	ADD	X20, X10
	ADD	X20, X13
	SUB	X20, X11
	JMP	scan

event_quote:
	ADD	X30, X13, X5
	ADD	$1, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
invalid:
	ADD	X30, X13, X5
	MOV	X5, X6
	MOV	$2, X7                  // types.ERR_INVALID_CHAR
	JMP	done
eof:
	MOV	X15, X5
	MOV	X15, X6
	MOV	$1, X7                  // types.ERR_EOF
done:
	// Publish only the unused suffix of the final bounded load. V2/V3 still
	// contain the raw quote/backslash masks; the scalar copies were filtered
	// for escapes while scanning. Current E64/VL=1 permits both extractions.
	MOV	$0, X22
	MOV	$0, X23
	BNEZ	X7, block_tail_done
	ADD	$1, X30, X24
	SUB	X24, X20, X23
	BEQZ	X23, block_tail_done     // also avoids shifting by 64
	VMVXS	V2, X22
	VMVXS	V3, X25
	OR	X25, X22
	AND	X21, X22                // clear mask-agnostic inactive lanes
	SRL	X24, X22
block_tail_done:
	MOV	X22, tailEvents+48(FP)
	MOV	X23, tailWidth+56(FP)
	MOV	X5, end+16(FP)
	MOV	X14, escape+24(FP)
	MOV	X6, errorPos+32(FP)
	MOV	X7, err+40(FP)
	RET
