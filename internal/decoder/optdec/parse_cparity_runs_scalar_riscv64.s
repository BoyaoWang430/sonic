//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

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

#define WB_CUR (Parser_nbuf+nodeBuf_ncur)
#define WB_PARENT (Parser_nbuf+nodeBuf_parent)
#define WB_DEPTH (Parser_nbuf+nodeBuf_depth)
#define WB_START (Parser_nbuf+nodeBuf_nstart)
#define WB_END (Parser_nbuf+nodeBuf_nend)
#define WB_ISKEY (Parser_nbuf+nodeBuf_iskey)
#define WS_OBJECT (Parser_nbuf+nodeBuf_stat+jsonStat_object)
#define WS_ARRAY (Parser_nbuf+nodeBuf_stat+jsonStat_array)
#define WS_STRING (Parser_nbuf+nodeBuf_stat+jsonStat_str)
#define WS_NUMBER (Parser_nbuf+nodeBuf_stat+jsonStat_number)
#define WS_ARRAY_ELEMS (Parser_nbuf+nodeBuf_stat+jsonStat_array_elems)
#define WS_OBJECT_KEYS (Parser_nbuf+nodeBuf_stat+jsonStat_object_keys)
#define WS_MAX_DEPTH (Parser_nbuf+nodeBuf_stat+jsonStat_max_depth)
#define W_NUM_TYPE (cParityRunsState_num+cParityNum_typ)
#define W_NUM_VALUE (cParityRunsState_num+cParityNum_val)

// Match C's reservation of one spare node after every scalar/key write:
// X15 is the next-but-one node and X16 the end of the node buffer.
#define RUNS_CAPACITY \
	ADD $16, X22, X15; \
	MOV WB_END(X9), X16

// These constants survive the entire parser invocation; cold Go returns
// reinitialize them on the next entry.
#define RUNS_SCAN_INIT \
	MOV $0x2222222222222222, X29; \
	MOV $0x5c5c5c5c5c5c5c5c, X30

// The assembly handles contiguous scalar/key runs. Go owns container entry,
// closure, allocation, Unicode decoding and cold numeric/literal handling.
TEXT ·cParityParseRunsAsm(SB), NOSPLIT, $16-24
	NO_LOCAL_POINTERS
	MOV p+0(FP), X9
	MOV state+8(FP), X18
	MOV Parser_options(X9), X20
	MOV Parser_cur(X9), X21
	MOV WB_CUR(X9), X22
	MOV Parser_start(X9), X24
	MOV Parser_end(X9), X25
	MOV $const_KNull, X23
	MOV WB_PARENT(X9), X10
	MOV $-1, X19
	BEQ X10, X19, runs_parent_loaded
	MOV WB_START(X9), X11
	SLL $4, X10
	ADD X10, X11, X23
runs_parent_loaded:
	RUNS_SCAN_INIT
	MOV cParityRunsState_phase(X18), X10
	MOV $const_cParityRunsObjectStart, X19
	BEQ X10, X19, runs_open_object
	MOV $const_cParityRunsArrayStart, X19
	BEQ X10, X19, runs_open_array
	MOV $const_cParityRunsObjectValue, X19
	BEQ X10, X19, runs_object_value
	MOV $const_cParityRunsParentContinue, X19
	BEQ X10, X19, runs_parent_continue
	MOV $const_cParityRunsStringResume, X19
	BEQ X10, X19, runs_resume_string
	MOV $const_cParityRunsContextRoot, X28
	CALL runs_space<>(SB)
	SUB X24, X21, X26
	ADD $-1, X26
	JMP runs_value

runs_parent_continue:
	CALL runs_space<>(SB)
	MOVBU 0(X23), X11
	MOV $const_KObject, X19
	BEQ X11, X19, runs_object_continue
	JMP runs_array_continue

runs_object_key:
	MOV $34, X19
	BNE X10, X19, runs_error_key
	SUB X24, X21, X26
	ADD $-1, X26 // Keep the opening quote as the token position, as for values.
	MOV $const_cParityRunsContextKey, X28
	CALL runs_string<>(SB)
runs_key_string_result:
	MOV $-const_cParityRunsStringYield, X19
	BEQ X10, X19, runs_unicode_yield
	MOV $-1, X19
	BEQ X11, X19, runs_key_escape_saved
	MOV X11, cParityRunsState_hasEsc(X18)
runs_key_escape_saved:
	BLT X10, X0, runs_string_error
	MOV X10, 8(X2)
	CALL runs_space<>(SB)
	MOV $58, X19
	BNE X10, X19, runs_error_colon
	MOV 8(X2), X12
	MOV cParityRunsState_hasEsc(X18), X11
	SLL $3, X11
	OR $const_KStringCommon, X11
	ADD $1, X26, X13
	SLL $32, X13
	OR X13, X11
	MOV X11, 0(X22)
	MOV X12, 8(X22)
	ADD $16, X22
	RUNS_CAPACITY
	BLEU X15, X16, runs_object_value
	MOV $1, X11
	MOVB X11, WB_ISKEY(X9)
	JMP runs_error_capacity

runs_object_value:
	MOV $const_cParityRunsContextObject, X28
	CALL runs_space<>(SB)
	SUB X24, X21, X26
	ADD $-1, X26
	JMP runs_value
runs_array_value:
	MOV $const_cParityRunsContextArray, X28
	SUB X24, X21, X26
	ADD $-1, X26

runs_value:
	MOV $34, X19
	BEQ X10, X19, runs_value_string
	ADD $-48, X10, X11
	MOV $9, X19
	BLEU X11, X19, runs_value_positive
	MOV $123, X19
	BEQ X10, X19, runs_value_object
	MOV $91, X19
	BEQ X10, X19, runs_value_array
	MOV $45, X19
	BEQ X10, X19, runs_value_negative
	MOV $116, X19
	BEQ X10, X19, runs_value_true
	MOV $102, X19
	BEQ X10, X19, runs_value_false
	MOV $110, X19
	BEQ X10, X19, runs_value_null
	MOV $const_SONIC_INVALID_CHAR, X10
	JMP runs_done

runs_value_object:
	SLL $32, X26, X11
	OR $const_KObject, X11
	MOV WB_PARENT(X9), X12
	MOV X11, 0(X22)
	MOV X12, 8(X22)
	MOV WB_START(X9), X13
	SUB X13, X22, X13
	SRL $4, X13
	MOV X13, WB_PARENT(X9)
	MOV X22, X23
	ADD $16, X22
	MOV WB_DEPTH(X9), X14
	ADD $1, X14
	MOV X14, WB_DEPTH(X9)
	RUNS_CAPACITY
	BGTU X15, X16, runs_error_capacity
	JMP runs_open_object
runs_value_array:
	SLL $32, X26, X11
	OR $const_KArray, X11
	MOV WB_PARENT(X9), X12
	MOV X11, 0(X22)
	MOV X12, 8(X22)
	MOV WB_START(X9), X13
	SUB X13, X22, X13
	SRL $4, X13
	MOV X13, WB_PARENT(X9)
	MOV X22, X23
	ADD $16, X22
	MOV WB_DEPTH(X9), X14
	ADD $1, X14
	MOV X14, WB_DEPTH(X9)
	RUNS_CAPACITY
	BGTU X15, X16, runs_error_capacity
	JMP runs_open_array
runs_open_object:
	CALL runs_space<>(SB)
	MOV $125, X19
	BNE X10, X19, runs_object_key
	JMP runs_close_container
runs_open_array:
	CALL runs_space<>(SB)
	MOV $93, X19
	BNE X10, X19, runs_array_value
	JMP runs_close_container

runs_value_positive:
	ADD $-1, X21
	MOV $0, X13
	JMP runs_value_number
runs_value_negative:
	MOV $1, X13
runs_value_number:
	AND $2, X20, X19
	BNEZ X19, runs_number_yield
	CALL runs_number<>(SB)
	MOV $const_cParityRunsNumber, X19
	BEQ X10, X19, runs_number_yield
	BNEZ X10, runs_number_load_old
	MOV X11, W_NUM_TYPE(X18)
	MOV X12, W_NUM_VALUE(X18)
	JMP runs_emit_number
runs_number_load_old:
	MOV W_NUM_TYPE(X18), X11
	MOV W_NUM_VALUE(X18), X12
	JMP runs_emit_number
runs_number_yield:
	MOV $const_cParityRunsNumber, X10
	JMP runs_yield
runs_emit_number:
	MOV $const_KUint, X19
	BEQ X11, X19, runs_emit_valid_number
	MOV $const_KSint, X19
	BEQ X11, X19, runs_emit_valid_number
	MOV $const_KReal, X19
	BEQ X11, X19, runs_emit_valid_number
	BNEZ X10, runs_done
	JMP runs_error_capacity
runs_emit_valid_number:
	SLL $32, X26, X19
	OR X19, X11
	MOV X11, 0(X22)
	MOV X12, 8(X22)
	ADD $16, X22
	MOVWU WS_NUMBER(X9), X11
	ADD $1, X11
	MOVW X11, WS_NUMBER(X9)
	JMP runs_after_scalar

runs_value_string:
	CALL runs_string<>(SB)
runs_value_string_result:
	MOV $-const_cParityRunsStringYield, X19
	BEQ X10, X19, runs_unicode_yield
	MOV $-1, X19
	BEQ X11, X19, runs_value_escape_saved
	MOV X11, cParityRunsState_hasEsc(X18)
runs_value_escape_saved:
	MOV X10, X12
	MOV $0, X10
	BGE X12, X0, runs_value_string_eof
	SUB X12, X0, X10
runs_value_string_eof:
	BNEZ X28, runs_emit_string
	BGE X25, X21, runs_emit_string
	MOV $const_SONIC_EOF, X10
runs_emit_string:
	MOV cParityRunsState_hasEsc(X18), X11
	SLL $3, X11
	OR $const_KStringCommon, X11
	ADD $1, X26, X13
	SLL $32, X13
	OR X13, X11
	MOV X11, 0(X22)
	MOV X12, 8(X22)
	ADD $16, X22
	MOVWU WS_STRING(X9), X11
	ADD $1, X11
	MOVW X11, WS_STRING(X9)
	JMP runs_after_scalar

// Resume after Go processed adjacent Unicode escapes (including surrogate
// retries). Later Unicode runs may yield again within this string.
runs_resume_string:
	// Only a string continuation consumes saved token position and context.
	MOV cParityRunsState_pos(X18), X26
	MOV cParityRunsState_context(X18), X28
	MOV cParityRunsState_stringErr(X18), X10
	BNEZ X10, runs_resume_string_error
	ADD X26, X24, X12
	ADD $1, X12
	MOV cParityRunsState_stringDst(X18), X11
	CALL runs_string_continue<>(SB)
	JMP runs_resume_string_result
runs_resume_string_error:
	SUB X10, X0, X10
	MOV $1, X11
runs_resume_string_result:
	MOV $const_cParityRunsContextKey, X19
	BEQ X28, X19, runs_key_string_result
	JMP runs_value_string_result
runs_unicode_yield:
	MOV X12, cParityRunsState_stringDst(X18)
	MOV X28, cParityRunsState_context(X18)
	MOV $const_cParityRunsUnicode, X10
	JMP runs_yield

// Literal bodies are compared as one little-endian word; the input is padded.
runs_value_true:
	MOV $const_KTrue, X12
	MOVWU 0(X21), X11
	MOV $0xffffff, X19
	AND X19, X11
	MOV $0x657572, X13
	BNE X11, X13, runs_literal_yield
	ADD $3, X21
	JMP runs_literal_ok
runs_value_false:
	MOV $const_KFalse, X12
	MOVWU 0(X21), X11
	MOV $0x65736c61, X13
	BNE X11, X13, runs_literal_yield
	ADD $4, X21
	JMP runs_literal_ok
runs_value_null:
	MOV $const_KNull, X12
	MOVWU 0(X21), X11
	MOV $0xffffff, X19
	AND X19, X11
	MOV $0x6c6c75, X13
	BNE X11, X13, runs_literal_yield
	ADD $3, X21
runs_literal_ok:
	MOV $0, X10
	SLL $32, X26, X11
	OR X12, X11
	MOV X11, 0(X22)
	ADD $16, X22
	JMP runs_after_scalar

runs_literal_yield:
	MOV $const_cParityRunsLiteral, X10
	JMP runs_yield

runs_after_scalar:
	BNEZ X10, runs_done
	RUNS_CAPACITY
	BLTU X16, X15, runs_error_capacity
	BEQZ X28, runs_ok
	CALL runs_space<>(SB)
	MOV $const_cParityRunsContextObject, X19
	BEQ X28, X19, runs_object_continue
	JMP runs_array_continue

runs_object_continue:
	MOV 0(X23), X11
	ADD $256, X11
	MOV X11, 0(X23)
	MOV $44, X19
	BEQ X10, X19, runs_next_key
	MOV $125, X19
	BNE X10, X19, runs_error_object_end
	JMP runs_close_container
runs_next_key:
	CALL runs_space<>(SB)
	JMP runs_object_key
runs_array_continue:
	MOV 0(X23), X11
	ADD $256, X11
	MOV X11, 0(X23)
	MOV $44, X19
	BEQ X10, X19, runs_next_array_value
	MOV $93, X19
	BNE X10, X19, runs_error_array_end

// The tape already records this container's kind and completed child count.
// Closing transfers no token position or string continuation to Go.
runs_close_container:
	MOV 0(X23), X11
	MOVWU 0(X23), X12
	SRL $8, X12
	AND $0xff, X11, X13
	MOV $const_KObject, X19
	BNE X13, X19, runs_close_array_stat
	MOVWU WS_OBJECT(X9), X14
	ADD $1, X14
	MOVW X14, WS_OBJECT(X9)
	MOVWU WS_OBJECT_KEYS(X9), X15
	ADD X12, X15
	MOVW X15, WS_OBJECT_KEYS(X9)
	MOVWU WS_ARRAY(X9), X16
	JMP runs_close_patch
runs_close_array_stat:
	MOVWU WS_ARRAY(X9), X16
	ADD $1, X16
	MOVW X16, WS_ARRAY(X9)
	MOVWU WS_ARRAY_ELEMS(X9), X15
	ADD X12, X15
	MOVW X15, WS_ARRAY_ELEMS(X9)
	MOVWU WS_OBJECT(X9), X14
runs_close_patch:
	MOV 8(X23), X10
	MOV X10, WB_PARENT(X9)
	SUB X23, X22, X15
	SRL $4, X15
	SLL $32, X15
	OR X12, X15
	MOV X15, 8(X23)
	SRL $32, X11, X15
	SLL $32, X15
	OR X13, X15
	MOV X15, 0(X23)
	MOV WB_DEPTH(X9), X15
	MOVWU WS_MAX_DEPTH(X9), X17
	BLEU X15, X17, runs_close_depth_ok
	MOVW X15, WS_MAX_DEPTH(X9)
	MOV $4096, X19
	BGTU X15, X19, runs_error_stack_overflow
runs_close_depth_ok:
	ADD $-1, X15
	MOV X15, WB_DEPTH(X9)
	MOV $-1, X19
	BEQ X10, X19, runs_ok
	ADD X14, X16, X14
	AND $0xfff, X14, X14
	BEQZ X14, runs_close_yield
	MOV WB_START(X9), X11
	SLL $4, X10
	ADD X10, X11, X23
	JMP runs_parent_continue
runs_close_yield:
	MOV $const_cParityRunsCloseContainer, X10
	JMP runs_done
runs_error_stack_overflow:
	MOV $const_SONIC_STACK_OVERFLOW, X10
	JMP runs_done
runs_next_array_value:
	CALL runs_space<>(SB)
	JMP runs_array_value

runs_string_error:
	SUB X10, X0, X10
	JMP runs_done
runs_error_key:
	MOV $const_SONIC_EXPECT_KEY, X10
	JMP runs_done
runs_error_colon:
	MOV $const_SONIC_EXPECT_COLON, X10
	JMP runs_done
runs_error_object_end:
	MOV $const_SONIC_EXPECT_OBJ_COMMA_OR_END, X10
	JMP runs_done
runs_error_array_end:
	MOV $const_SONIC_EXPECT_ARR_COMMA_OR_END, X10
	JMP runs_done
runs_error_capacity:
	MOV $const_SONIC_VISIT_FAILED, X10
	JMP runs_done
runs_ok:
	MOV $0, X10
	JMP runs_done
runs_yield:
	MOV X26, cParityRunsState_pos(X18)
runs_done:
	MOV X21, Parser_cur(X9)
	MOV X22, WB_CUR(X9)
	MOV X10, event+16(FP)
	RET

// Input: X21 is the numeric body (after an optional minus), X13 is negative.
// Output: X10 is the error, X11 the kind, X12 the value bits, X21 the cursor.
// cParityRunsNumber requests the exact Go numeric fallback, with X21 restored.
// No parser or tape fields are accessed.
//
// Besides integers of at most 19 digits, this converts the decimal subset that
// cParityParseNumberValue finishes with one exact multiplication or division
// in cParityAtofFast: at most 17 significant digits accumulated as in C (later
// fraction digits would be truncated, but such significands already exceed
// 2^52 and are left to Go), a significand below 2^52, and a decimal
// exponent in [-22, 22] (positive exponents only for significands up to 1e15).
// The operations and their order match the Go fast path, so results are
// bit-identical. Every other number, including all malformed ones, restores
// the cursor and lets Go reproduce the complete C behavior.
	PCALIGN $64
TEXT runs_number<>(SB), NOSPLIT|NOFRAME, $0-0
	MOV X21, X16
	MOVBU 0(X21), X14
	MOV $0, X12
	MOV $0, X5
	MOV $10, X17
	MOV $9, X6
	MOV $48, X7
	BEQ X14, X7, runs_number_zero
	ADD $-48, X14, X15
	BLTU X6, X15, runs_number_invalid
runs_number_digits:
	MUL X17, X12
	ADD X15, X12
	ADD $1, X21
	ADD $1, X5
	MOVBU 0(X21), X14
	ADD $-48, X14, X15
	BGEU X6, X15, runs_number_digits
	MOV $19, X7
	BLTU X7, X5, runs_number_20digits
runs_number_integer_end:
	MOV $46, X7
	BEQ X14, X7, runs_number_fraction
	OR $32, X14, X15
	MOV $101, X7
	BEQ X15, X7, runs_number_exponent_zero
	MOV $const_KUint, X11
	BEQZ X13, runs_number_success
	MOV $const_KSint, X11
	MOV $0x8000000000000000, X5
	BLTU X5, X12, runs_number_negative_real
	SUB X12, X0, X12
runs_number_success:
	MOV $0, X10
	RET
runs_number_zero:
	ADD $1, X21
	MOVBU 0(X21), X14
	MOV $46, X7
	BNE X14, X7, runs_number_integer_end
	// C skips zeros after "0." without counting them as significant digits.
	ADD $1, X21
	MOVBU 0(X21), X14
	ADD $-48, X14, X15
	BLTU X6, X15, runs_number_fallback
	MOV X21, X10
	MOV $48, X7
runs_number_zero_skip:
	BNE X14, X7, runs_number_zero_done
	ADD $1, X21
	MOVBU 0(X21), X14
	JMP runs_number_zero_skip
runs_number_zero_done:
	OR $32, X14, X15
	MOV $101, X11
	BEQ X15, X11, runs_number_exponent_zero
	SUB X21, X10, X7
	MOV $16, X11
	ADD $-48, X14, X15
	BGEU X6, X15, runs_number_fraction_digits
	JMP runs_number_fraction_end
runs_number_negative_real:
	FCVTDLU X12, F0
	FNEGD F0, F0
	FMVXD F0, X12
	MOV $const_KReal, X11
	JMP runs_number_success
runs_number_invalid:
	MOV $const_SONIC_INVALID_NUM, X10 // The cursor and previous number survive.
	RET
runs_number_fallback:
	MOV X16, X21
	MOV $const_cParityRunsNumber, X10
	RET

// X12 holds the integer digits (X5 of them, 1..19) and X21 the decimal point.
// X7 is the decimal exponent and X11 the remaining significant digits.
runs_number_fraction:
	MOV $0, X7
	ADD $1, X21
	MOVBU 0(X21), X14
	ADD $-48, X14, X15
	BLTU X6, X15, runs_number_fallback
	MOV $16, X11
	SUB X5, X11
runs_number_fraction_digits:
	// X15 is the digit at X21; it has not been consumed yet. A seventeenth
	// significant digit makes the significand at least 1e16 > 2^52, outside
	// the exact subset, so long fractions go to Go without being scanned twice.
	BGE X0, X11, runs_number_fallback
	MUL X17, X12
	ADD X15, X12
	ADD $-1, X11
	ADD $-1, X7
	ADD $1, X21
	MOVBU 0(X21), X14
	ADD $-48, X14, X15
	BGEU X6, X15, runs_number_fraction_digits
runs_number_fraction_end:
	OR $32, X14, X15
	MOV $101, X5
	BNE X15, X5, runs_number_float
	JMP runs_number_exponent

runs_number_exponent_zero:
	MOV $0, X7
runs_number_exponent:
	// X21 addresses the exponent marker. At most three digits stay here.
	ADD $1, X21
	MOVBU 0(X21), X14
	MOV $0, X10
	MOV $45, X15
	BNE X14, X15, runs_number_exponent_plus
	MOV $1, X10
	ADD $1, X21
	MOVBU 0(X21), X14
	JMP runs_number_exponent_digits
runs_number_exponent_plus:
	MOV $43, X15
	BNE X14, X15, runs_number_exponent_digits
	ADD $1, X21
	MOVBU 0(X21), X14
runs_number_exponent_digits:
	ADD $-48, X14, X15
	BLTU X6, X15, runs_number_fallback
	MOV $0, X11
	MOV $0, X5
runs_number_exponent_loop:
	MUL X17, X11
	ADD X15, X11
	ADD $1, X5
	ADD $1, X21
	MOVBU 0(X21), X14
	ADD $-48, X14, X15
	BGEU X6, X15, runs_number_exponent_loop
	MOV $3, X14
	BLTU X14, X5, runs_number_fallback
	BEQZ X10, runs_number_exponent_add
	SUB X11, X0, X11
runs_number_exponent_add:
	ADD X11, X7

// cParityAtofFast: negate first, then one exact operation by 10^|X7|.
runs_number_float:
	SRL $52, X12, X14
	BNEZ X14, runs_number_el_0
	FCVTDLU X12, F0
	BEQZ X13, runs_number_float_sign
	FNEGD F0, F0
runs_number_float_sign:
	BEQZ X12, runs_number_float_done
	BEQZ X7, runs_number_float_done
	MOV $runsPow10<>(SB), X14
	BLT X7, X0, runs_number_float_divide
	MOV $22, X15
	BLT X15, X7, runs_number_el_0
	MOV $1000000000000000, X15
	BLTU X15, X12, runs_number_el_0
	SLL $3, X7
	ADD X7, X14
	MOVD 0(X14), F1
	FMULD F1, F0, F0
	JMP runs_number_float_done
runs_number_float_divide:
	MOV $-22, X15
	BLT X7, X15, runs_number_el_0
	SUB X7, X0, X7
	SLL $3, X7
	ADD X7, X14
	MOVD 0(X14), F1
	FDIVD F1, F0, F0
runs_number_float_done:
	FMVXD F0, X12
	MOV $const_KReal, X11
	MOV $0, X10
	RET

runs_number_20digits:
	MOV $20, X7
	BLTU X7, X5, runs_number_fallback
	MOV $46, X7
	BEQ X14, X7, runs_number_fallback
	OR $32, X14, X15
	MOV $101, X7
	BEQ X15, X7, runs_number_fallback
	MOV $0, X12
	MOV X16, X5
	ADD $19, X16, X7
runs_number_reacc19:
	MOVBU 0(X5), X15
	ADD $-48, X15
	MUL X17, X12
	ADD X15, X12
	ADD $1, X5
	BNE X5, X7, runs_number_reacc19
	MULHU X17, X12, X10
	BNEZ X10, runs_number_20_float
	MUL X17, X12, X10
	MOVBU 0(X7), X15
	ADD $-48, X15
	ADD X15, X10, X10
	BLTU X10, X15, runs_number_20_float
	MOV X10, X12
	MOV $const_KUint, X11
	BEQZ X13, runs_number_success
	JMP runs_number_negative_real
runs_number_20_float:
	MOV $1, X7
	MOV $1, X10
	JMP runs_number_eisel_lemire

runs_number_el_0:
	MOV $0, X10
runs_number_eisel_lemire:
	ADD $348, X7, X14
	MOV $695, X15
	BLTU X15, X14, runs_number_fallback
	CLZ X12, X15
	SLL X15, X12, X5
	MOV $217706, X6
	MUL X7, X6
	SRA $16, X6
	ADD $1087, X6
	SUB X15, X6
	MOV $·cParityPow10Mantissa+8(SB), X11
	SLL $4, X14
	ADD X14, X11
	MOV 0(X11), X11
	MULHU X5, X11, X14
	AND $0x1ff, X14, X15
	MOV $0x1ff, X11
	BEQ X15, X11, runs_number_fallback
	SRL $63, X14, X11
	ADD $9, X11, X5
	SRL X5, X14, X14
	XOR $1, X11, X11
	SUB X11, X6
	BNEZ X15, runs_number_el_round
	AND $3, X14, X15
	MOV $1, X11
	BEQ X15, X11, runs_number_fallback
runs_number_el_round:
	AND $1, X14, X11
	ADD X11, X14
	SRL $1, X14
	SRL $53, X14, X11
	BEQZ X11, runs_number_el_pack
	SRL $1, X14
	ADD $1, X6
runs_number_el_pack:
	ADD $-1, X6, X11
	MOV $0x7fd, X15
	BLTU X15, X11, runs_number_fallback
	MOV $0x000fffffffffffff, X15
	AND X15, X14
	SLL $52, X6
	OR X6, X14
	SLL $63, X13, X11
	OR X11, X14
	BEQZ X10, runs_number_el_done
	MOV $1, X15
	BNE X10, X15, runs_number_el_check_trunc
	MOV X14, X17
	ADD $1, X12
	MOV $2, X10
	JMP runs_number_eisel_lemire
runs_number_el_check_trunc:
	BNE X14, X17, runs_number_fallback
runs_number_el_done:
	MOV X14, X12
	MOV $const_KReal, X11
	MOV $0, X10
	RET

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
	RUNS_STRING_INIT
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
	// A raw control comes first. The first backslash before the block's first
	// quote still takes priority; otherwise report this control.
	MOV X6, X19
runs_string_control_tail:
	ADD $1, X6
	MOV $32, X15
	BGEU X6, X15, runs_string_plain_control_error
	ADD X6, X21, X7
	MOVBU 0(X7), X10
	MOV $34, X15
	BEQ X10, X15, runs_string_plain_control_error
	MOV $92, X15
	BEQ X10, X15, runs_string_first_escape
	JMP runs_string_control_tail

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

runs_string_plain_control_error:
	ADD X19, X21
	MOV $-const_SONIC_CONTROL_CHAR, X10
	MOV X14, X11
	RET

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
	MOV $0x2020202020202020, X11
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

DATA cParityRunsEscapes<>+34(SB)/1, $34
DATA cParityRunsEscapes<>+47(SB)/1, $47
DATA cParityRunsEscapes<>+92(SB)/1, $92
DATA cParityRunsEscapes<>+98(SB)/1, $8
DATA cParityRunsEscapes<>+102(SB)/1, $12
DATA cParityRunsEscapes<>+110(SB)/1, $10
DATA cParityRunsEscapes<>+114(SB)/1, $13
DATA cParityRunsEscapes<>+116(SB)/1, $9
GLOBL cParityRunsEscapes<>(SB), RODATA|NOPTR, $256

// Exact binary64 powers 10^0 .. 10^22 for the cParityAtofFast subset.
DATA runsPow10<>+0(SB)/8, $0x3ff0000000000000
DATA runsPow10<>+8(SB)/8, $0x4024000000000000
DATA runsPow10<>+16(SB)/8, $0x4059000000000000
DATA runsPow10<>+24(SB)/8, $0x408f400000000000
DATA runsPow10<>+32(SB)/8, $0x40c3880000000000
DATA runsPow10<>+40(SB)/8, $0x40f86a0000000000
DATA runsPow10<>+48(SB)/8, $0x412e848000000000
DATA runsPow10<>+56(SB)/8, $0x416312d000000000
DATA runsPow10<>+64(SB)/8, $0x4197d78400000000
DATA runsPow10<>+72(SB)/8, $0x41cdcd6500000000
DATA runsPow10<>+80(SB)/8, $0x4202a05f20000000
DATA runsPow10<>+88(SB)/8, $0x42374876e8000000
DATA runsPow10<>+96(SB)/8, $0x426d1a94a2000000
DATA runsPow10<>+104(SB)/8, $0x42a2309ce5400000
DATA runsPow10<>+112(SB)/8, $0x42d6bcc41e900000
DATA runsPow10<>+120(SB)/8, $0x430c6bf526340000
DATA runsPow10<>+128(SB)/8, $0x4341c37937e08000
DATA runsPow10<>+136(SB)/8, $0x4376345785d8a000
DATA runsPow10<>+144(SB)/8, $0x43abc16d674ec800
DATA runsPow10<>+152(SB)/8, $0x43e158e460913d00
DATA runsPow10<>+160(SB)/8, $0x4415af1d78b58c40
DATA runsPow10<>+168(SB)/8, $0x444b1ae4d6e2ef50
DATA runsPow10<>+176(SB)/8, $0x4480f0cf064dd592
GLOBL runsPow10<>(SB), RODATA|NOPTR, $184
