//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError)
// One complete relaxed structural scan. Each vector load is bounded by both the
// remaining input and 64 bytes, so masks fit in scalar registers at any VLEN.
// The assembler owns the ordinary Go frame; SP, RA, and g are never scratch.
TEXT ·skipContainer(SB), $0-40
	MOV	src+0(FP), X10
	MOV	size+8(FP), X11
	MOV	X11, X12                // original size
	MOV	open+16(FP), X16
	ADD	$2, X16, X17            // '[' -> ']', '{' -> '}'
	MOV	$1, X13                 // nesting depth
	MOV	$0, X14                 // preceding odd backslash run
	MOV	$0, X15                 // preceding in-string state: 0 or -1
	MOV	$34, X18
	MOV	$92, X19
	MOV	$0xaaaaaaaaaaaaaaaa, X20
	MOV	$0x5555555555555555, X21

structural_scan:
	BEQZ	X11, structural_eof
	MOV	$64, X5
	MINU	X11, X5, X5
	// M4 can cover 64 bytes even at VLEN=128. Always use the actual VL;
	// V8-V11 are disjoint from the four packed mask destinations.
	VSETVLI X5, E8, M4, TA, MA, X22
	VLE8V	(X10), V8
	VMSEQVX X18, V8, V2
	VMSEQVX X19, V8, V3
	VMSEQVX X16, V8, V4
	VMSEQVX X17, V8, V5
	// Mask registers hold packed bits. Changing SEW permits extracting all
	// 64 low bits without any vector spill or out-of-bounds mask store.
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS	V2, X24
	VMVXS	V3, X25
	VMVXS	V4, X26
	VMVXS	V5, X28
	MOV	$64, X5
	SUB	X22, X5, X5
	MOV	$-1, X23
	SRL	X5, X23, X23           // low VL bits; VL=64 shifts by zero
	AND	X23, X24
	AND	X23, X25
	AND	X23, X26
	AND	X23, X28

	// Compute every escaped position in the block with run arithmetic.
	// Carry-in consumes bit zero before finding starts of backslash runs.
	OR	X14, X25, X5
	BEQZ	X5, structural_quotes
	NOT	X14, X5
	AND	X25, X5, X29           // backslashes except an escaped first one
	SLL	$1, X29, X30
	OR	X14, X30               // positions following a backslash
	NOT	X30, X5
	AND	X29, X5
	AND	X20, X5                // odd-indexed run starts
	ADD	X29, X5
	SLL	$1, X5
	XOR	X21, X5
	AND	X30, X5                // escaped positions
	NOT	X5, X6
	AND	X6, X24                // unescaped quotes
	AND	X25, X6, X14
	ADD	$-1, X22, X7
	SRL	X7, X14
	AND	$1, X14                // trailing unescaped backslash, for any VL

structural_quotes:
	// Prefix XOR marks every byte inside a string. Six scalar shifts avoid
	// a carry-less multiply (Zbc) requirement.
	BEQZ	X24, structural_quote_state
	SLL	$1, X24, X5
	XOR	X5, X24
	SLL	$2, X24, X5
	XOR	X5, X24
	SLL	$4, X24, X5
	XOR	X5, X24
	SLL	$8, X24, X5
	XOR	X5, X24
	SLL	$16, X24, X5
	XOR	X5, X24
	SLL	$32, X24, X5
	XOR	X5, X24
structural_quote_state:
	XOR	X15, X24
	ADD	$-1, X22, X7
	SRL	X7, X24, X15
	AND	$1, X15
	NEG	X15, X15
	NOT	X24, X5
	AND	X5, X26
	AND	X5, X28

	// If there are fewer closes than the entry depth, this block cannot
	// finish the container. Otherwise inspect closes in position order.
	CPOP	X28, X5
	BLTU	X5, X13, structural_count
structural_close:
	BEQZ	X28, structural_count
	ADD	$-1, X28, X29
	AND	X26, X29, X30
	CPOP	X30, X31
	ADD	X31, X13
	XOR	X30, X26              // remove opening brackets already counted
	SUB	$1, X13
	BEQZ	X13, structural_success
	AND	X29, X28              // remove this closing bracket
	JMP	structural_close
structural_count:
	CPOP	X26, X5
	CPOP	X28, X6
	ADD	X5, X13
	SUB	X6, X13
	ADD	X22, X10
	SUB	X22, X11
	JMP	structural_scan

structural_success:
	CTZ	X28, X5
	SUB	X11, X12, X6
	ADD	X6, X5
	ADD	$1, X5
	MOV	$0, X6
	JMP	structural_done
structural_eof:
	MOV	X12, X5
	MOV	$1, X6                // types.ERR_EOF
structural_done:
	MOV	X5, end+24(FP)
	MOV	X6, err+32(FP)
	RET
