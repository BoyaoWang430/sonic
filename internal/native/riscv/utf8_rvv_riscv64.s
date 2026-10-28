//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool)
// X10: input pointer, X11: bytes remaining, X12: absolute input offset.
// X13: output pointer, X14: output slots remaining, X15: offsets written.
// X16-X23, masks V0-V6, and data groups V8/V16/V24 are scratch.
// X23 bounds scalar work after a bad block.
// No calls, stack spills, or retained pointers.
// The ordinary ABI0 frame preserves g (X27), SP, and the return address.
TEXT ·utf8Scan(SB), $0-57
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	base+16(FP), X12
	MOV	positions+24(FP), X13
	MOV	limit+32(FP), X14
	MOV	$0, X15

utf8_loop:
	BEQZ	X11, utf8_done
	// Every load uses the current remaining length, including short tails.
	// Signed E8 comparison finds bytes with the high bit set. A vector with
	// no such byte is a complete ASCII run and is consumed using actual VL.
	VSETVLI	X11, E8, M4, TA, MA, X16
	VLE8V	(X10), V8
	VMSLTVI	$0, V8, V0
	VFIRSTM	V0, X17
	BGEZ	X17, utf8_multibyte
	ADD	X16, X10
	ADD	X16, X12
	SUB	X16, X11
	JMP	utf8_loop

utf8_multibyte:
	// Short tails use the exact scalar decoder. Full blocks validate every
	// byte in parallel, including non-ASCII bytes, with no table or extra ISA.
	MOV	$16, X21
	BLTU	X16, X21, utf8_bad_block

	// Continuations are 80..BF. C0/C1 and F5..FF are illegal leading bytes.
	// V8 is the original data group; V16 and V24 hold prior-byte data.
	// Single-register masks V0..V6 are disjoint from the data groups.
	// Signed bytes below -64 are exactly continuation bytes 80..BF.
	MOV	$-64, X21
	VMSLTVX X21, V8, V1
	VMXORMM	V0, V1, V2
	MOV	$0xc2, X21
	VMSLTUVX X21, V8, V3
	VMANDMM	V2, V3, V3
	MOV	$0xf4, X21
	VMSGTUVX X21, V8, V2
	VMORMM	V3, V2, V2

	// Consume all comparisons needing the previous byte before reusing its
	// data group for the previous-third byte. Zero-filled slides never read
	// outside the input.
	VSLIDE1UPVX X0, V8, V16

	// E0 requires a second byte >= A0; ED requires one < A0.
	MOV	$0xe0, X21
	VMSEQVX	X21, V16, V4
	MOV	$0xed, X21
	VMSEQVX	X21, V16, V5
	MOV	$0xa0, X21
	VMSLTUVX X21, V8, V6
	VMANDMM	V4, V6, V4
	VMANDNMM V6, V5, V5
	VMORMM	V4, V5, V4
	VMORMM	V2, V4, V2

	// F0 requires a second byte >= 90; F4 requires one < 90.
	MOV	$0xf0, X21
	VMSEQVX	X21, V16, V4
	MOV	$0xf4, X21
	VMSEQVX	X21, V16, V5
	MOV	$0x90, X21
	VMSLTUVX X21, V8, V6
	VMANDMM	V4, V6, V4
	VMANDNMM V6, V5, V5
	VMORMM	V4, V5, V4
	VMORMM	V2, V4, V2

	// Build required continuation positions with two alternating slide
	// groups. V16's previous-byte value is dead after its >BF comparison.
	VSLIDE1UPVX X0, V16, V24
	MOV	$0xbf, X21
	VMSGTUVX X21, V16, V3
	MOV	$0xdf, X21
	VMSGTUVX X21, V24, V4
	VMORMM	V3, V4, V3
	VSLIDE1UPVX X0, V24, V16
	MOV	$0xef, X21
	VMSGTUVX X21, V16, V4
	VMORMM	V3, V4, V3
	VMXORMM	V1, V3, V3
	VMORMM	V2, V3, V3
	VFIRSTM	V3, X18
	BGEZ	X18, utf8_bad_block

	// Do not commit a leading byte until its whole sequence is checked.
	// Retain at most three bytes at the block end and reload them with the
	// next block. All offsets use actual VL, including a shortened final VL.
	// This also leaves incomplete sequences at EOF for exact scalar errors.
	MOV	X16, X20
	ADD	X16, X10, X19
	MOVBU	-1(X19), X18
	MOV	$0xc0, X21
	BGEU	X18, X21, utf8_retain_one
	MOVBU	-2(X19), X18
	MOV	$0xe0, X21
	BGEU	X18, X21, utf8_retain_two
	MOVBU	-3(X19), X18
	MOV	$0xf0, X21
	BLTU	X18, X21, utf8_block_valid
	ADD	$-1, X20
utf8_retain_two:
	ADD	$-1, X20
utf8_retain_one:
	ADD	$-1, X20
utf8_block_valid:
	ADD	X20, X10
	ADD	X20, X12
	SUB	X20, X11
	JMP	utf8_loop

utf8_bad_block:
	// Rescan this exceptional block with the exact decoder, rather than
	// retrying the vector validator after every bad byte. Scalar sequences
	// may cross the block boundary, but never the logical input boundary.
	MOV	X16, X23
utf8_nonascii:
	// Consume only the ASCII prefix before the first high byte. A scalar
	// sequence can cross the vector boundary; its loads use X11, not VL.
	ADD	X17, X10
	ADD	X17, X12
	SUB	X17, X11
	SUB	X17, X23
utf8_scalar:
	MOVBU	(X10), X18
	MOV	$0x80, X21
	BGEU	X18, X21, utf8_scalar_nonascii
	MOV	$1, X20
	JMP	utf8_advance
utf8_scalar_nonascii:
	MOV	$0xc2, X21
	BLTU	X18, X21, utf8_invalid
	MOV	$0xe0, X21
	BLTU	X18, X21, utf8_two
	MOV	$0xf0, X21
	BLTU	X18, X21, utf8_three
	MOV	$0xf5, X21
	BLTU	X18, X21, utf8_four
	JMP	utf8_invalid

utf8_two:
	MOV	$2, X20
	BLTU	X11, X20, utf8_invalid
	MOVBU	1(X10), X19
	ADD	$-0x80, X19, X22
	MOV	$0x40, X21
	BGEU	X22, X21, utf8_invalid
	JMP	utf8_advance

utf8_three:
	MOV	$3, X20
	BLTU	X11, X20, utf8_invalid
	MOVBU	1(X10), X19
	ADD	$-0x80, X19, X22
	MOV	$0x40, X21
	BGEU	X22, X21, utf8_invalid
	// E0 must not encode an overlong sequence; ED must not encode a surrogate.
	MOV	$0xe0, X21
	BNE	X18, X21, utf8_three_surrogate
	MOV	$0xa0, X21
	BLTU	X19, X21, utf8_invalid
utf8_three_surrogate:
	MOV	$0xed, X21
	BNE	X18, X21, utf8_three_last
	MOV	$0xa0, X21
	BGEU	X19, X21, utf8_invalid
utf8_three_last:
	MOVBU	2(X10), X19
	ADD	$-0x80, X19, X22
	MOV	$0x40, X21
	BGEU	X22, X21, utf8_invalid
	JMP	utf8_advance

utf8_four:
	MOV	$4, X20
	BLTU	X11, X20, utf8_invalid
	MOVBU	1(X10), X19
	ADD	$-0x80, X19, X22
	MOV	$0x40, X21
	BGEU	X22, X21, utf8_invalid
	// F0 requires U+10000 or above, while F4 is limited to U+10FFFF.
	MOV	$0xf0, X21
	BNE	X18, X21, utf8_four_max
	MOV	$0x90, X21
	BLTU	X19, X21, utf8_invalid
utf8_four_max:
	MOV	$0xf4, X21
	BNE	X18, X21, utf8_four_tail
	MOV	$0x90, X21
	BGEU	X19, X21, utf8_invalid
utf8_four_tail:
	MOVBU	2(X10), X19
	ADD	$-0x80, X19, X22
	MOV	$0x40, X21
	BGEU	X22, X21, utf8_invalid
	MOVBU	3(X10), X19
	ADD	$-0x80, X19, X22
	BGEU	X22, X21, utf8_invalid
	JMP	utf8_advance

utf8_invalid:
	// Do not consume an invalid byte that cannot be recorded. A full output
	// still permits a valid suffix to be scanned before reporting exhaustion.
	BEQZ	X14, utf8_full
	MOV	X12, (X13)
	ADD	$8, X13
	ADD	$-1, X14
	ADD	$1, X15
	MOV	$1, X20

utf8_advance:
	ADD	X20, X10
	ADD	X20, X12
	SUB	X20, X11
	SUB	X20, X23
	BGTZ	X23, utf8_scalar
	JMP	utf8_loop

utf8_full:
	MOV	$1, X16
	MOVB	X16, full+56(FP)
	JMP	utf8_return

utf8_done:
	MOVB	X0, full+56(FP)
utf8_return:
	MOV	X12, next+40(FP)
	MOV	X15, count+48(FP)
	RET
