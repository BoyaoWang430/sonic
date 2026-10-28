// Shared scanString and scanStringBlock body. Include after the TEXT directive.
// STRING_SCAN_BLOCK adds the unused final-load suffix to the result frame.
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	validate+16(FP), X12
	MOV	$0, X13                 // block's relative offset
	MOV	$-1, X14                // first backslash
	MOV	X11, X15                // original length, for EOF
	MOV	$34, X16
	MOV	$92, X17
	MOV	$32, X18
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
	BEQZ	X12, masks
	VMSLTUVX X18, V8, V4
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
	MOV	$98, X6                 // b
	BEQ	X7, X6, consume_escape
	MOV	$102, X6                // f
	BEQ	X7, X6, consume_escape
	MOV	$110, X6                // n
	BEQ	X7, X6, consume_escape
	MOV	$114, X6                // r
	BEQ	X7, X6, consume_escape
	MOV	$116, X6                // t
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
#ifdef STRING_SCAN_BLOCK
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
	MOV	X22, tailEvents+56(FP)
	MOV	X23, tailWidth+64(FP)
#endif
	MOV	X5, end+24(FP)
	MOV	X14, escape+32(FP)
	MOV	X6, errorPos+40(FP)
	MOV	X7, err+48(FP)
	RET
