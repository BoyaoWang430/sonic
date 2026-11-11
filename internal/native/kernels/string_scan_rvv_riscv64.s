//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// The four entries match the shared Go declarations in scanning_native.go.
// Each call scans a complete string in one RVV invocation. The block entries
// also return raw quote/backslash events in the already loaded suffix after the
// closing quote; errors return an empty suffix.

// func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringRelaxed(SB), NOSPLIT, $0-48
// Shared string scanner body. Include after the TEXT directive.
// 0 (0 or 1) selects relaxed or strict scanning.
// STRING_SCAN_BLOCK adds the unused final-load suffix to the result frame.
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X12
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18
	MOV	$0, X19                 // relaxed-mode odd backslash carry
	BEQZ	X11, eof
	MOV	$8, X5
	BLTU	X11, X5, relaxed_short_tail
	MOV	0(X10), X5
	MOV	$0x2222222222222222, X6
	XOR	X5, X6, X6
	ORCB	X6, X6
	MOV	$0x5c5c5c5c5c5c5c5c, X7
	XOR	X5, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	XOR	$-1, X6, X6
	BNEZ	X6, relaxed_word0_event
	ADD	$8, X10
	MOV	$8, X13
	SUB	$8, X11
	JMP	scan
relaxed_word0_event:
	CTZ	X6, X20
	SRL	X20, X5, X7
	AND	$0xff, X7
	BNE	X7, X16, scan
	SRL	$3, X20, X5
	ADD	$1, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
relaxed_short_tail:
	MOVBU	0(X10), X7
	BEQ	X7, X16, relaxed_short_quote
	BEQ	X7, X17, scan
	ADD	$1, X10
	ADD	$1, X13
	SUB	$1, X11
	BNEZ	X11, relaxed_short_tail
	JMP	eof
relaxed_short_quote:
	ADD	$1, X13, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done

scan:
	BEQZ	X11, eof
	VSETVLI X11, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VMORMM	V2, V3, V5
	BEQZ	X12, check_first
	VMSLTUVX X18, V8, V4
	VMORMM	V5, V4, V5
check_first:
	MOV	$64, X5
	BNEZ	X19, clamp_64
	VFIRSTM	V5, X6
	BLTZ	X6, advance
	BLTU	X6, X5, clamp_64
	AND	$-64, X6, X20
	JMP	advance
clamp_64:
	MINU	X5, X20, X20
masks:
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
	BEQZ	X5, unescaped_quotes
	// Plain strings need no parity constants. A block containing a backslash
	// either returns or records firstEscape before advancing to another block.
	BGEZ	X14, parity_ready
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
parity_ready:
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
unescaped_quotes:
	BNEZ	X12, strict

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
	JMP	advance

strict:
	// Strict mode consumes a crossing escape completely before the next load,
	// so it enters each block with no pending escape and X19 remains zero.
	VMVXS	V4, X24
	AND	X21, X24
	AND	X25, X23
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
	MOV	$47, X6                 // /
	BEQ	X7, X6, consume_escape
	MOV	$117, X6                // u
	BEQ	X7, X6, unicode
	ADD	$-98, X7, X6
	MOV	$19, X5
	BGEU	X6, X5, invalid
	MOV	$0x51011, X5
	SRL	X6, X5
	AND	$1, X5
	BEQZ	X5, invalid
	JMP	consume_escape
unicode:

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

quote:
	CTZ	X22, X30

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

// func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringStrict(SB), NOSPLIT, $0-48
// Shared string scanner body. Include after the TEXT directive.
// 1 (0 or 1) selects relaxed or strict scanning.
// STRING_SCAN_BLOCK adds the unused final-load suffix to the result frame.
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$1, X12
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18
	MOV	$0, X19                 // relaxed-mode odd backslash carry
	BEQZ	X11, eof
	MOV	$8, X5
	BLTU	X11, X5, strict_short_tail
	MOV	0(X10), X5
	MOV	$0x2222222222222222, X6
	XOR	X5, X6, X6
	ORCB	X6, X6
	MOV	$0x5c5c5c5c5c5c5c5c, X7
	XOR	X5, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	MOV	$0xe0e0e0e0e0e0e0e0, X7
	AND	X5, X7, X7
	ORCB	X7, X7
	AND	X7, X6
	XOR	$-1, X6, X6
	BNEZ	X6, strict_word0_event
	ADD	$8, X10
	MOV	$8, X13
	SUB	$8, X11
	JMP	scan
strict_word0_event:
	CTZ	X6, X20
	SRL	X20, X5, X7
	AND	$0xff, X7
	BEQ	X7, X17, scan
	SRL	$3, X20, X5
	BLTU	X7, X18, strict_word0_invalid
	ADD	$1, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
strict_word0_invalid:
	MOV	X5, X6
	MOV	$2, X7
	JMP	done
strict_short_tail:
	MOVBU	0(X10), X7
	BLTU	X7, X18, strict_short_invalid
	BEQ	X7, X16, strict_short_quote
	BEQ	X7, X17, scan
	ADD	$1, X10
	ADD	$1, X13
	SUB	$1, X11
	BNEZ	X11, strict_short_tail
	JMP	eof
strict_short_quote:
	ADD	$1, X13, X5
	MOV	$-1, X6
	MOV	$0, X7
	JMP	done
strict_short_invalid:
	MOV	X13, X5
	MOV	X13, X6
	MOV	$2, X7
	JMP	done

scan:
	BEQZ	X11, eof
	VSETVLI X11, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VMORMM	V2, V3, V5
	BEQZ	X12, check_first
	VMSLTUVX X18, V8, V4
	VMORMM	V5, V4, V5
check_first:
	MOV	$64, X5
	BNEZ	X19, clamp_64
	VFIRSTM	V5, X6
	BLTZ	X6, advance
	BLTU	X6, X5, clamp_64
	AND	$-64, X6, X20
	JMP	advance
clamp_64:
	MINU	X5, X20, X20
masks:
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
	BEQZ	X5, unescaped_quotes
	// Plain strings need no parity constants. A block containing a backslash
	// either returns or records firstEscape before advancing to another block.
	BGEZ	X14, parity_ready
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
parity_ready:
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
unescaped_quotes:
	BNEZ	X12, strict

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
	JMP	advance

strict:
	// Strict mode consumes a crossing escape completely before the next load,
	// so it enters each block with no pending escape and X19 remains zero.
	VMVXS	V4, X24
	AND	X21, X24
	AND	X25, X23
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
	MOV	$47, X6                 // /
	BEQ	X7, X6, consume_escape
	MOV	$117, X6                // u
	BEQ	X7, X6, unicode
	ADD	$-98, X7, X6
	MOV	$19, X5
	BGEU	X6, X5, invalid
	MOV	$0x51011, X5
	SRL	X6, X5
	AND	$1, X5
	BEQZ	X5, invalid
	JMP	consume_escape
unicode:

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

quote:
	CTZ	X22, X30

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
TEXT ·scanStringRelaxedBlock(SB), NOSPLIT, $0-64
// Shared string scanner body. Include after the TEXT directive.
// 0 (0 or 1) selects relaxed or strict scanning.
// STRING_SCAN_BLOCK adds the unused final-load suffix to the result frame.
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$0, X12
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18
	MOV	$0, X19                 // relaxed-mode odd backslash carry

scan:
	BEQZ	X11, eof
	VSETVLI X11, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VMORMM	V2, V3, V5
	BEQZ	X12, check_first
	VMSLTUVX X18, V8, V4
	VMORMM	V5, V4, V5
check_first:
	MOV	$64, X5
	BNEZ	X19, clamp_64
	VFIRSTM	V5, X6
	BLTZ	X6, advance
	BLTU	X6, X5, clamp_64
	AND	$-64, X6, X20
	JMP	advance
clamp_64:
	MINU	X5, X20, X20
masks:
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
	BEQZ	X5, unescaped_quotes
	// Plain strings need no parity constants. A block containing a backslash
	// either returns or records firstEscape before advancing to another block.
	BGEZ	X14, parity_ready
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
parity_ready:
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
unescaped_quotes:
	BNEZ	X12, strict

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
	JMP	advance

strict:
	// Strict mode consumes a crossing escape completely before the next load,
	// so it enters each block with no pending escape and X19 remains zero.
	VMVXS	V4, X24
	AND	X21, X24
	AND	X25, X23
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
	MOV	$47, X6                 // /
	BEQ	X7, X6, consume_escape
	MOV	$117, X6                // u
	BEQ	X7, X6, unicode
	ADD	$-98, X7, X6
	MOV	$19, X5
	BGEU	X6, X5, invalid
	MOV	$0x51011, X5
	SRL	X6, X5
	AND	$1, X5
	BEQZ	X5, invalid
	JMP	consume_escape
unicode:

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

quote:
	CTZ	X22, X30

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

// func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringStrictBlock(SB), NOSPLIT, $0-64
// Shared string scanner body. Include after the TEXT directive.
// 1 (0 or 1) selects relaxed or strict scanning.
// STRING_SCAN_BLOCK adds the unused final-load suffix to the result frame.
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	$1, X12
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18
	MOV	$0, X19                 // relaxed-mode odd backslash carry

scan:
	BEQZ	X11, eof
	VSETVLI X11, E8, M4, TA, MA, X20
	VLE8V	(X10), V8
	VMSEQVX X16, V8, V2
	VMSEQVX X17, V8, V3
	VMORMM	V2, V3, V5
	BEQZ	X12, check_first
	VMSLTUVX X18, V8, V4
	VMORMM	V5, V4, V5
check_first:
	MOV	$64, X5
	BNEZ	X19, clamp_64
	VFIRSTM	V5, X6
	BLTZ	X6, advance
	BLTU	X6, X5, clamp_64
	AND	$-64, X6, X20
	JMP	advance
clamp_64:
	MINU	X5, X20, X20
masks:
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
	BEQZ	X5, unescaped_quotes
	// Plain strings need no parity constants. A block containing a backslash
	// either returns or records firstEscape before advancing to another block.
	BGEZ	X14, parity_ready
	MOV	$0x5555555555555555, X28
	NOT	X28, X29
parity_ready:
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
unescaped_quotes:
	BNEZ	X12, strict

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
	JMP	advance

strict:
	// Strict mode consumes a crossing escape completely before the next load,
	// so it enters each block with no pending escape and X19 remains zero.
	VMVXS	V4, X24
	AND	X21, X24
	AND	X25, X23
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
	MOV	$47, X6                 // /
	BEQ	X7, X6, consume_escape
	MOV	$117, X6                // u
	BEQ	X7, X6, unicode
	ADD	$-98, X7, X6
	MOV	$19, X5
	BGEU	X6, X5, invalid
	MOV	$0x51011, X5
	SRL	X6, X5
	AND	$1, X5
	BEQZ	X5, invalid
	JMP	consume_escape
unicode:

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

quote:
	CTZ	X22, X30

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
