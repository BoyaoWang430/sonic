//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

#include "textflag.h"

// func cParityNonspace64(src unsafe.Pointer) (mask uint64)
// Bit i is set exactly when src[i] is not space, tab, line feed or carriage
// return. C reads a complete 64-byte padded block without a length check.
TEXT ·cParityNonspace64(SB), NOSPLIT, $0-16
	MOV src+0(FP), X10
	MOV $64, X11
	MOV $32, X15
	VSETVLI X11, E8, M4, TA, MA, X0
	VLE8V (X10), V8
	VMSEQVX X15, V8, V4
	// TAB and CR differ only in bit 2. NOR marks non-whitespace lanes.
	VANDVI $-5, V8, V12
	VMSEQVI $9, V12, V5
	VMORMM V4, V5, V4
	VMSEQVI $10, V8, V5
	VMNORMM V4, V5, V4
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS V4, X12
	MOV X12, mask+8(FP)
	RET

// func cParityStringMasks32(src unsafe.Pointer, opts uint64) (bs, quote, control uint32)
// C string_block_new: backslashes and quotes are always classified; bytes
// <= 31 only when option bit 5 is set. The caller pads all 32 bytes.
TEXT ·cParityStringMasks32(SB), NOSPLIT, $0-28
	MOV src+0(FP), X10
	MOV opts+8(FP), X19
	MOV $32, X11
	MOV $92, X12
	MOV $34, X13
	VSETVLI X11, E8, M2, TA, MA, X0
	VLE8V (X10), V8
	VMSEQVX X12, V8, V2
	VMSEQVX X13, V8, V3
	AND $32, X19, X19
	MOV $0, X16
	BEQZ X19, string_masks_extract
	VMSLTUVX X11, V8, V4
string_masks_extract:
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS V2, X14
	VMVXS V3, X15
	BEQZ X19, string_masks_store
	VMVXS V4, X16
string_masks_store:
	MOVW X14, bs+16(FP)
	MOVW X15, quote+20(FP)
	MOVW X16, control+24(FP)
	RET

// func cParityMove32(dst, src unsafe.Pointer)
// Load all 32 bytes before storing any, matching C's value semantics when the
// output trails the input by fewer than 32 bytes.
TEXT ·cParityMove32(SB), NOSPLIT, $0-16
	MOV dst+0(FP), X10
	MOV src+8(FP), X11
	MOV $32, X12
	VSETVLI X12, E8, M2, TA, MA, X0
	VLE8V (X11), V8
	VSE8V V8, (X10)
	RET

// func cParityNumberMasks16(src unsafe.Pointer) (decimal, exponent, sign, valid uint32)
// The C raw-number scanner classifies one sixteen-byte block. Valid bytes are
// ASCII digits plus the decimal point, both exponent letters and both signs.
TEXT ·cParityNumberMasks16(SB), NOSPLIT, $0-24
	MOV src+0(FP), X10
	MOV $46, X11
	MOV $32, X12
	MOV $101, X13
	MOV $43, X14
	MOV $45, X15
	MOV $48, X16
	MOV $10, X17
	VSETIVLI $16, E8, M1, TA, MA, X0
	VLE8V (X10), V8
	VMSEQVX X11, V8, V2
	VORVX X12, V8, V9
	VMSEQVX X13, V9, V3
	VMSEQVX X14, V8, V4
	VMSEQVX X15, V8, V5
	VMORMM V4, V5, V4
	VSUBVX X16, V8, V9
	VMSLTUVX X17, V9, V5
	VMORMM V2, V5, V5
	VMORMM V3, V5, V5
	VMORMM V4, V5, V5
	VSETIVLI $1, E64, M1, TA, MA, X0
	VMVXS V2, X20
	VMVXS V3, X21
	VMVXS V4, X22
	VMVXS V5, X23
	MOVHU X20, X20
	MOVHU X21, X21
	MOVHU X22, X22
	MOVHU X23, X23
	MOVW X20, decimal+8(FP)
	MOVW X21, exponent+12(FP)
	MOVW X22, sign+16(FP)
	MOVW X23, valid+20(FP)
	RET
