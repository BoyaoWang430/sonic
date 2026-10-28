// Shared scalar string scanner body. Included once per ABI entry, without
// an include guard. STRING_SCAN_BLOCK adds the final block's tail metadata.

	// Strict adjacent escapes keep the original aligned byte-validation path.
	// Relaxed scans admit arbitrary bounded words, including after a complete
	// escape; this is an algorithm choice rather than hardware dispatch.
	MOV validate+16(FP), X5
	BEQZ X5, str_relaxed_alignment
	MOV $7, X19
	JMP str_init
str_relaxed_alignment:
	MOV $0, X19
	JMP str_init

	// Keep the hot loop's layout stable as surrounding Go code changes.
	PCALIGN $64
str_init:
	MOV src+0(FP), X10
	MOV size+8(FP), X11
	MOV validate+16(FP), X12
	MOV $0, X13                 // relative block offset
	MOV $-1, X14                // first backslash
	MOV X11, X15
	MOV $0x2222222222222222, X16
	MOV $0x5c5c5c5c5c5c5c5c, X17
	MOV $0xe0e0e0e0e0e0e0e0, X18
	MOV $0x8080808080808080, X20

str_scan:
	BEQZ X11, str_eof
	AND X19, X10, X5
	BNEZ X5, str_byte
	MOV $8, X24
	BLTU X11, X24, str_tail
	MOV 0(X10), X21
str_classify:
	// Non-matching bytes become 0xff. Intersect them, then invert once to
	// obtain exact event flags without carry or borrow between bytes.
	// Retain the quote classification for subsequent relaxed events.
	XOR X16, X21, X7
	ORCB X7, X7
	XOR X17, X21, X6
	ORCB X6, X6
	AND X6, X7, X5
	BEQZ X12, str_events_ready
	// A byte is a raw control exactly when its top three bits are zero.
	AND X18, X21, X6
	ORCB X6, X6
	AND X6, X5
str_events_ready:
	ANDN X5, X20, X22
str_event:
	BEQZ X22, str_advance
	BEQZ X12, str_relaxed_event
	CTZ X22, X23
	SRL $3, X23
	SLL $3, X23, X5
	SRL X5, X21, X25
	AND $255, X25
	JMP str_handle
str_byte:
	MOV $1, X24
	MOV $0, X23
	MOVBU 0(X10), X25
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BEQ X25, X5, str_escape
	BEQZ X12, str_advance
	MOV $32, X5
	BLTU X25, X5, str_invalid
	JMP str_advance
str_handle:
	MOV $34, X5
	BEQ X25, X5, str_quote
	MOV $92, X5
	BNE X25, X5, str_invalid
str_escape:
	BGEZ X14, str_escape_seen
	ADD X13, X23, X14
str_escape_seen:
	SUB X23, X11, X30
	MOV $2, X26
	BLTU X30, X26, str_eof
	BEQZ X12, str_relaxed_consume
	ADD X23, X10, X28
	MOVBU 1(X28), X25
	MOV $34, X5
	BEQ X25, X5, str_consume
	MOV $92, X5
	BEQ X25, X5, str_consume
	MOV $47, X5
	BEQ X25, X5, str_consume
	MOV $98, X5
	BEQ X25, X5, str_consume
	MOV $102, X5
	BEQ X25, X5, str_consume
	MOV $110, X5
	BEQ X25, X5, str_consume
	MOV $114, X5
	BEQ X25, X5, str_consume
	MOV $116, X5
	BEQ X25, X5, str_consume
	MOV $117, X5
	BNE X25, X5, str_invalid
	// Match native error precedence: missing Unicode bytes report EOF before
	// malformed available hex digits are inspected. Surrogates remain valid.
	MOV $6, X26
	BLTU X30, X26, str_eof
	ADD $2, X28
	MOV $4, X29
str_hex:
	MOVBU 0(X28), X25
	ADD $-48, X25, X5
	MOV $10, X6
	BLTU X5, X6, str_hex_valid
	OR $32, X25
	ADD $-97, X25
	MOV $6, X6
	BGEU X25, X6, str_invalid
str_hex_valid:
	ADD $1, X28
	SUB $1, X29
	BNEZ X29, str_hex
str_consume:
	ADD X23, X26
	BGEU X26, X24, str_crossing
	// Keep the already computed events strictly after this complete escape.
	// A shift of 64 is avoided by the preceding block-end comparison.
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	JMP str_event
str_crossing:
	MOV X26, X24
str_advance:
	ADD X24, X10
	ADD X24, X13
	SUB X24, X11
	JMP str_scan

str_quote:
	ADD X23, X13, X5
	ADD $1, X5
	MOV $-1, X6
	MOV $0, X7
	JMP str_done
str_invalid:
	ADD X23, X13, X5
	MOV X5, X6
	MOV $2, X7
	JMP str_done
str_eof:
	MOV X15, X5
	MOV X15, X6
	MOV $1, X7
str_done:
	MOV X5, end+24(FP)
	MOV X14, escape+32(FP)
	MOV X6, errorPos+40(FP)
	MOV X7, err+48(FP)
#ifdef STRING_SCAN_BLOCK
	// A successful word or overlapping tail can leave already loaded bytes
	// after its closing quote. Byte scans and errors cannot expose stale masks.
	MOV $0, X28
	MOV $0, X29
	BNEZ X7, str_block_done
	MOV $1, X9
	BGEU X9, X24, str_block_done
	ADD $1, X23, X9
	SUB X9, X24, X29
	BEQZ X29, str_block_done
	BEQZ X12, str_block_pack
	// Strict scans include controls in X22. Remove them using the original
	// word; the exported cache contains exact raw quote/backslash events only.
	AND X18, X21, X6
	ORCB X6, X6
	AND X6, X22
str_block_pack:
	// Gather exact byte high bits, then discard the closing quote and prefix.
	// The count is 1..7 here, so the final shift cannot wrap at 64 bits.
	SRL $7, X22, X28
	MOV $0x0102040810204080, X16
	MUL X16, X28
	SRL $56, X28
	SRL X9, X28
str_block_done:
	MOV X28, tailEvents+56(FP)
	MOV X29, tailWidth+64(FP)
#endif
	RET

// Dense relaxed escapes need neither escape classification nor another CTZ.
// Keep the original block and its exact event mask intact for a closing quote
// still inside that block. X9/X30 walk subsequent bytes independently; a quote
// after that block is a byte result and exports no stale block metadata.
str_relaxed_consume:
	// Byte mode has no valid word mask, and may remain unaligned throughout
	// adjacent escapes. Retain its bounded lookahead before consuming.
	BGEU X26, X24, str_relaxed_byte
	// Keep the completed escape boundary in X26 while locating the next
	// event in this word. Crossing escapes use the original advance path.
	ADD X23, X26
	BGEU X26, X24, str_crossing
	SLL $3, X26, X5
	MOV $-1, X6
	SLL X5, X6
	AND X6, X22
	BEQZ X22, str_advance
	CTZ X22, X23
	SRL X23, X7, X25
	SRL $3, X23
	AND $1, X25
	BEQZ X25, str_quote
	// Relaxed masks contain only quotes and slashes, so this is a slash.
	// Only enter the byte chain after observing a second nearby escape.
	SUB X26, X23, X5
	MOV $2, X26
	BLTU X26, X5, str_escape
	// The second slash may be the final byte of the input or loaded word.
	// Restore the same availability precondition as str_escape_seen.
	SUB X23, X11, X30
	BLTU X30, X26, str_eof
str_relaxed_enter:
	MOV $92, X28
	MOV $34, X29
	ADD X23, X26, X5
	ADD X5, X10, X9
	SUB X5, X11, X30
str_relaxed_next:
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	// Up to two ordinary bytes between escapes stay in the dense loop.
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	ADD $1, X9
	SUB $1, X30
	BEQZ X30, str_eof
	MOVBU 0(X9), X25
	BEQ X25, X28, str_relaxed_escape
	BEQ X25, X29, str_relaxed_quote
	// Resume bulk scanning after three ordinary bytes.
	ADD $1, X9
	SUB $1, X30
	SUB X30, X11, X5
	MOV X9, X10
	ADD X5, X13
	MOV X30, X11
	JMP str_scan
	PCALIGN $16
str_relaxed_escape:
	// X26 is still 2. Check the escaped byte exists before advancing either
	// pointer, including for a backslash at the end of a protected page.
	BLTU X30, X26, str_eof
	ADD $2, X9
	SUB $2, X30
	JMP str_relaxed_next
str_relaxed_quote:
	SUB X10, X9, X23
	BLTU X23, X24, str_quote
	// X21/X22 belong to an earlier block, so mark this as a byte result.
	ADD X23, X13
	MOV $0, X23
	MOV $1, X24
	JMP str_quote

// Adjacent two-byte escapes can remain unaligned indefinitely. A bounded
// lookahead admits that chain without reading a stale word event mask.
str_relaxed_byte:
	BEQ X30, X26, str_consume
	MOVBU 2(X10), X5
	MOV $92, X6
	BEQ X5, X6, str_relaxed_enter
	JMP str_consume

// Keep the uncommon overlapping tail outside the established hot-loop layout.
str_tail:
	BNEZ X19, str_strict_tail
	// The final word stays within this call's input. Shift away the consumed
	// prefix rather than replaying escapes or changing the logical cursor.
	BLTU X15, X24, str_byte
str_tail_load:
	ADD X11, X10, X5
	MOV -8(X5), X21
	SUB X11, X24, X5
	SLL $3, X5
	SRL X5, X21
	// Fill bytes outside the logical tail with non-events, including in strict
	// mode, so the normal classifier needs no per-word tail-mask branch.
	SLL $3, X11, X5
	MOV $-1, X6
	SLL X5, X6
	OR X6, X21
	MOV X11, X24
	JMP str_classify

// Relaxed events are already classified as quotes or backslashes. Reuse the
// quote mask instead of extracting and classifying the original byte again.
str_relaxed_event:
	CTZ X22, X23
	SRL X23, X7, X25
	SRL $3, X23
	AND $1, X25
	BEQZ X25, str_quote
	JMP str_escape

// Strict scans keep their aligned hot loop. A longer final suffix can use the
// same bounded word; keep this setup after the hot relaxed-event handler so
// that handler's instruction layout stays unchanged.
str_strict_tail:
	MOV $4, X5
	BLTU X11, X5, str_byte
	BLTU X15, X24, str_byte
	JMP str_tail_load
