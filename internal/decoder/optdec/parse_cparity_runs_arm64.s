//go:build arm64 && go1.20 && !go1.28

#include "textflag.h"
#include "funcdata.h"
#include "go_asm.h"

// Private register convention for cParityParseRunsAsm and its static helpers.
// R12 whitespace cache; R13 nodesBase; R14 nodesLast (last spare-safe node);
// R16 token position; R17 root/object/array/key context.
// R19 Parser; R20 continuation; R21 options; R22 input; R23 tape; R24 parent;
// R25 input start; R26 input end. Helpers update R22 and preserve R12-R14, R16/R17,
// R19-R21 and R23-R26. They are stackless leaves using only private registers;
// Go calls only the outer parser. R18/R28/R29 are reserved, R27 is scratch.
#define WB_LEN (Parser_nbuf+nodeBuf_nlen)
#define WB_PARENT (Parser_nbuf+nodeBuf_parent)
#define WB_DEPTH (Parser_nbuf+nodeBuf_depth)
#define WB_CAP (Parser_nbuf+nodeBuf_ncap)
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

// Match C's reservation of one spare node after every scalar/key write.
#define RUNS_CAPACITY \
	CMP R14, R23

// These constants survive the entire parser invocation. Helpers and the core
// preserve V20/V21/V22/V25/V29/V30/V31; cold Go returns reinitialize them.
#define RUNS_SCAN_INIT \
	MOVD $cParityRunsSpaceTables<>(SB), R9; \
	VLD1 (R9), [V29.B16, V30.B16, V31.B16]; \
	VMOVI $143, V25.B16; \
	VMOVI $92, V20.B16; \
	VMOVI $34, V21.B16; \
	VEOR V22.B16, V22.B16, V22.B16

#define CP_RUNS_PACK(reg, output) \
	VAND V30.B16, reg.B16, reg.B16; \
	VTBL V31.B16, [reg.B16], reg.B16; \
	VADDV reg.H8, reg; \
	VMOV reg.H[0], output

// Classify one C-sized block; V0/V1 retain the entire original input snapshot
// for the overlapping in-place copy. R6/R7 are slash/quote bitmaps. Constants
// V20/V21 and packing tables V30/V31 survive all iterations and Unicode paths.
#define CP_RUNS_STRING_MASKS \
	FLDPQ (R22), (F0, F1); \
	VCMEQ V20.B16, V0.B16, V4.B16; \
	VCMEQ V20.B16, V1.B16, V5.B16; \
	VCMEQ V21.B16, V0.B16, V6.B16; \
	VCMEQ V21.B16, V1.B16, V7.B16; \
	VAND V30.B16, V4.B16, V4.B16; \
	VAND V30.B16, V5.B16, V5.B16; \
	VAND V30.B16, V6.B16, V6.B16; \
	VAND V30.B16, V7.B16, V7.B16; \
	VADDP V5.B16, V4.B16, V4.B16; \
	VADDP V7.B16, V6.B16, V6.B16; \
	VADDP V6.B16, V4.B16, V4.B16; \
	VADDP V4.B16, V4.B16, V4.B16; \
	VMOV V4.S[0], R6; \
	VMOV V4.S[1], R7

#define CP_RUNS_CONTROL_MASK \
	VUSHR $5, V0.B16, V8.B16; \
	VUSHR $5, V1.B16, V9.B16; \
	VCMEQ V22.B16, V8.B16, V8.B16; \
	VCMEQ V22.B16, V9.B16, V9.B16; \
	VAND V30.B16, V8.B16, V8.B16; \
	VAND V30.B16, V9.B16, V9.B16; \
	VADDP V9.B16, V8.B16, V8.B16; \
	VADDP V8.B16, V8.B16, V8.B16; \
	VADDP V8.B16, V8.B16, V8.B16; \
	VMOV V8.S[0], R8

#define CP_RUNS_COPY_QUOTE \
	MOVBU (R22), R9; \
	ADD $1, R22; \
	CMP $34, R9; \
	BEQ runs_string_copy_done; \
	MOVB R9, (R1); \
	ADD $1, R1

#define CP_RUNS_COPY_SLASH \
	MOVBU (R22), R9; \
	CMP $92, R9; \
	BEQ runs_string_escape; \
	MOVB R9, (R1); \
	ADD $1, R1; \
	ADD $1, R22


#define CP_RUNS_SPACE(input, output) \
	VAND V25.B16, input, output; \
	VTBL output, [V29.B16], output; \
	VCMEQ input, output, output

// The assembly handles contiguous scalar/key runs. Go owns container entry,
// closure, allocation, Unicode decoding and cold numeric/literal handling.
// All internal scanner calls use the private register convention above.
TEXT ·cParityParseRunsAsm(SB), NOSPLIT, $16-40
	NO_LOCAL_POINTERS
	MOVD p+0(FP), R19
	MOVD state+8(FP), R20
	ADD $Parser__nbk, R19, R12
	MOVD Parser_options(R19), R21
	MOVD rawBase+16(FP), R25
	MOVD Parser_pos(R19), R22
	ADD R25, R22, R22
	MOVD Parser_end(R19), R26
	ADD R25, R26, R26
	MOVD nodesBase+24(FP), R13
	MOVD WB_CAP(R19), R14
	SUB $1, R14, R14
	ADD R14<<4, R13, R14
	MOVD WB_LEN(R19), R23
	ADD R23<<4, R13, R23
	MOVD $const_KNull, R24
	MOVD WB_PARENT(R19), R0
	CMN $1, R0
	BEQ runs_parent_loaded
	ADD R0<<4, R13, R24
runs_parent_loaded:
	RUNS_SCAN_INIT
	MOVD cParityRunsState_phase(R20), R0
	CMP $const_cParityRunsObjectStart, R0
	BEQ runs_open_object
	CMP $const_cParityRunsArrayStart, R0
	BEQ runs_open_array
	CMP $const_cParityRunsObjectValue, R0
	BEQ runs_object_value
	CMP $const_cParityRunsParentContinue, R0
	BEQ runs_parent_continue
	CMP $const_cParityRunsStringResume, R0
	BEQ runs_resume_string
	MOVD $const_cParityRunsContextRoot, R17
	CALL runs_space<>(SB)
	SUB R25, R22, R16
	SUB $1, R16
	JMP runs_value

runs_parent_continue:
	CALL runs_space<>(SB)
	MOVBU (R24), R1
	CMP $const_KObject, R1
	BEQ runs_object_continue
	JMP runs_array_continue

runs_object_key:
	CMP $34, R0
	BNE runs_error_key
	SUB R25, R22, R16
	SUB $1, R16 // Keep the opening quote as the token position, as for values.
	MOVD $const_cParityRunsContextKey, R17
	CALL runs_string<>(SB)
runs_key_string_result:
	CMN $const_cParityRunsStringYield, R0
	BEQ runs_unicode_yield
	CMN $1, R1
	BEQ runs_key_escape_saved
	MOVD R1, cParityRunsState_hasEsc(R20)
runs_key_escape_saved:
	CMP $0, R0
	BLT runs_string_error
	MOVD R0, 8(RSP)
	CALL runs_space<>(SB)
	CMP $58, R0
	BNE runs_error_colon
	MOVD 8(RSP), R2
	MOVD cParityRunsState_hasEsc(R20), R1
	LSL $3, R1
	ORR $const_KStringCommon, R1
	ADD $1, R16, R3
	ORR R3<<32, R1, R1
	STP (R1, R2), (R23)
	ADD $16, R23
	RUNS_CAPACITY
	BLS runs_object_value
	MOVD $1, R1
	MOVB R1, WB_ISKEY(R19)
	JMP runs_error_capacity

runs_object_value:
	MOVD $const_cParityRunsContextObject, R17
	CALL runs_space<>(SB)
	SUB R25, R22, R16
	SUB $1, R16
	JMP runs_value
runs_array_value:
	MOVD $const_cParityRunsContextArray, R17
	SUB R25, R22, R16
	SUB $1, R16

runs_value:
	CMP $34, R0
	BEQ runs_value_string
	SUB $48, R0, R1
	CMP $9, R1
	BLS runs_value_positive
	CMP $123, R0
	BEQ runs_value_object
	CMP $91, R0
	BEQ runs_value_array
	CMP $45, R0
	BEQ runs_value_negative
	CMP $116, R0
	BEQ runs_value_true
	CMP $102, R0
	BEQ runs_value_false
	CMP $110, R0
	BEQ runs_value_null
	MOVD $const_SONIC_INVALID_CHAR, R0
	JMP runs_done

runs_value_object:
	MOVD $const_KObject, R1
	ORR R16<<32, R1, R1
	MOVD WB_PARENT(R19), R2
	STP (R1, R2), (R23)
	SUB R13, R23, R3
	LSR $4, R3
	MOVD R3, WB_PARENT(R19)
	MOVD R23, R24
	ADD $16, R23
	MOVD WB_DEPTH(R19), R4
	ADD $1, R4
	MOVD R4, WB_DEPTH(R19)
	RUNS_CAPACITY
	BHI runs_error_capacity
	JMP runs_open_object
runs_value_array:
	MOVD $const_KArray, R1
	ORR R16<<32, R1, R1
	MOVD WB_PARENT(R19), R2
	STP (R1, R2), (R23)
	SUB R13, R23, R3
	LSR $4, R3
	MOVD R3, WB_PARENT(R19)
	MOVD R23, R24
	ADD $16, R23
	MOVD WB_DEPTH(R19), R4
	ADD $1, R4
	MOVD R4, WB_DEPTH(R19)
	RUNS_CAPACITY
	BHI runs_error_capacity
	JMP runs_open_array
runs_open_object:
	CALL runs_space<>(SB)
	CMP $125, R0
	BNE runs_object_key
	JMP runs_close_container
runs_open_array:
	CALL runs_space<>(SB)
	CMP $93, R0
	BNE runs_array_value
	JMP runs_close_container

runs_value_positive:
	SUB $1, R22
	MOVD $0, R3
	JMP runs_value_number
runs_value_negative:
	MOVD $1, R3
runs_value_number:
	TBNZ $1, R21, runs_number_yield
	CALL runs_number<>(SB)
	CMP $const_cParityRunsNumber, R0
	BEQ runs_number_yield
	CBNZ R0, runs_number_load_old
	MOVD R1, W_NUM_TYPE(R20)
	MOVD R2, W_NUM_VALUE(R20)
	JMP runs_emit_number
runs_number_load_old:
	MOVD W_NUM_TYPE(R20), R1
	MOVD W_NUM_VALUE(R20), R2
	JMP runs_emit_number
runs_number_yield:
	MOVD $const_cParityRunsNumber, R0
	JMP runs_yield
runs_emit_number:
	CMP $const_KUint, R1
	BEQ runs_emit_valid_number
	CMP $const_KSint, R1
	BEQ runs_emit_valid_number
	CMP $const_KReal, R1
	BEQ runs_emit_valid_number
	CBNZ R0, runs_done
	JMP runs_error_capacity
runs_emit_valid_number:
	ORR R16<<32, R1, R1
	STP (R1, R2), (R23)
	ADD $16, R23
	MOVWU WS_NUMBER(R19), R1
	ADDW $1, R1
	MOVW R1, WS_NUMBER(R19)
	JMP runs_after_scalar

runs_value_string:
	CALL runs_string<>(SB)
runs_value_string_result:
	CMN $const_cParityRunsStringYield, R0
	BEQ runs_unicode_yield
	CMN $1, R1
	BEQ runs_value_escape_saved
	MOVD R1, cParityRunsState_hasEsc(R20)
runs_value_escape_saved:
	MOVD R0, R2
	MOVD $0, R0
	CMP $0, R2
	BGE runs_value_string_eof
	NEG R2, R0
runs_value_string_eof:
	CBNZ R17, runs_emit_string
	CMP R22, R26
	BGE runs_emit_string
	MOVD $const_SONIC_EOF, R0
runs_emit_string:
	MOVD cParityRunsState_hasEsc(R20), R1
	LSL $3, R1
	ORR $const_KStringCommon, R1
	ADD $1, R16, R3
	ORR R3<<32, R1, R1
	STP (R1, R2), (R23)
	ADD $16, R23
	MOVWU WS_STRING(R19), R1
	ADDW $1, R1
	MOVW R1, WS_STRING(R19)
	JMP runs_after_scalar

// Resume after Go processed adjacent Unicode escapes (including surrogate
// retries). Later Unicode runs may yield again within this string.
runs_resume_string:
	// Only a string continuation consumes saved token position and context.
	MOVD cParityRunsState_pos(R20), R16
	MOVD cParityRunsState_context(R20), R17
	MOVD cParityRunsState_stringErr(R20), R0
	CBNZ R0, runs_resume_string_error
	ADD R16, R25, R2
	ADD $1, R2
	MOVD cParityRunsState_stringDst(R20), R1
	ADD R25, R1, R1
	CALL runs_string_continue<>(SB)
	JMP runs_resume_string_result
runs_resume_string_error:
	NEG R0, R0
	MOVD $1, R1
runs_resume_string_result:
	CMP $const_cParityRunsContextKey, R17
	BEQ runs_key_string_result
	JMP runs_value_string_result
runs_unicode_yield:
	SUB R25, R2, R2
	MOVD R2, cParityRunsState_stringDst(R20)
	MOVD R17, cParityRunsState_context(R20)
	MOVD $const_cParityRunsUnicode, R0
	JMP runs_yield

runs_value_true:
	MOVD $const_KTrue, R2
	MOVWU (R22), R1
	AND $0xffffff, R1
	MOVD $0x657572, R3
	CMP R3, R1
	BNE runs_literal_yield
	ADD $3, R22
	JMP runs_literal_ok
runs_value_false:
	MOVD $const_KFalse, R2
	MOVWU (R22), R1
	MOVD $0x65736c61, R3
	CMP R3, R1
	BNE runs_literal_yield
	ADD $4, R22
	JMP runs_literal_ok
runs_value_null:
	MOVD $const_KNull, R2
	MOVWU (R22), R1
	AND $0xffffff, R1
	MOVD $0x6c6c75, R3
	CMP R3, R1
	BNE runs_literal_yield
	ADD $3, R22
	JMP runs_literal_ok
runs_literal_ok:
	MOVD $0, R0
	JMP runs_emit_literal
runs_emit_literal:
	ORR R16<<32, R2, R1
	MOVD R1, (R23)
	ADD $16, R23
	JMP runs_after_scalar

runs_literal_yield:
	MOVD $const_cParityRunsLiteral, R0
	JMP runs_yield

runs_after_scalar:
	CBNZ R0, runs_done
	RUNS_CAPACITY
	BHI runs_error_capacity
	CBZ R17, runs_ok
	CALL runs_space<>(SB)
	CMP $const_cParityRunsContextObject, R17
	BEQ runs_object_continue
	JMP runs_array_continue

runs_object_continue:
	MOVD (R24), R1
	ADD $256, R1
	MOVD R1, (R24)
	CMP $44, R0
	BEQ runs_next_key
	CMP $125, R0
	BNE runs_error_object_end
	JMP runs_close_container
runs_next_key:
	CALL runs_space<>(SB)
	JMP runs_object_key
runs_array_continue:
	MOVD (R24), R1
	ADD $256, R1
	MOVD R1, (R24)
	CMP $44, R0
	BEQ runs_next_array_value
	CMP $93, R0
	BNE runs_error_array_end

// The tape already records this container's kind and completed child count.
// Closing transfers no token position or string continuation to Go.
runs_close_container:
	LDP (R24), (R1, R2)
	UBFX $8, R1, $24, R3
	AND $0xff, R1, R4
	CMP $const_KObject, R4
	BNE runs_close_array_stat
	MOVWU WS_OBJECT(R19), R5
	ADD $1, R5
	MOVW R5, WS_OBJECT(R19)
	MOVWU WS_OBJECT_KEYS(R19), R6
	ADD R3, R6
	MOVW R6, WS_OBJECT_KEYS(R19)
	MOVWU WS_ARRAY(R19), R7
	JMP runs_close_patch
runs_close_array_stat:
	MOVWU WS_ARRAY(R19), R7
	ADD $1, R7
	MOVW R7, WS_ARRAY(R19)
	MOVWU WS_ARRAY_ELEMS(R19), R6
	ADD R3, R6
	MOVW R6, WS_ARRAY_ELEMS(R19)
	MOVWU WS_OBJECT(R19), R5
runs_close_patch:
	MOVD R2, WB_PARENT(R19)
	SUB R24, R23, R6
	LSR $4, R6
	ORR R6<<32, R3, R3
	AND $0xffffffff000000ff, R1, R1
	STP (R1, R3), (R24)
	MOVD WB_DEPTH(R19), R6
	MOVWU WS_MAX_DEPTH(R19), R8
	CMP R8, R6
	BLS runs_close_depth_ok
	MOVW R6, WS_MAX_DEPTH(R19)
	CMP $4096, R6
	BHI runs_error_stack_overflow
runs_close_depth_ok:
	SUB $1, R6
	MOVD R6, WB_DEPTH(R19)
	CMN $1, R2
	BEQ runs_ok
	ADD R5, R7, R5
	TST $0xfff, R5
	BEQ runs_close_yield
	ADD R2<<4, R13, R24
	JMP runs_parent_continue
runs_close_yield:
	MOVD $const_cParityRunsCloseContainer, R0
	JMP runs_done
runs_error_stack_overflow:
	MOVD $const_SONIC_STACK_OVERFLOW, R0
	JMP runs_done
runs_next_array_value:
	CALL runs_space<>(SB)
	JMP runs_array_value

runs_string_error:
	NEG R0, R0
	JMP runs_done
runs_error_key:
	MOVD $const_SONIC_EXPECT_KEY, R0
	JMP runs_done
runs_error_colon:
	MOVD $const_SONIC_EXPECT_COLON, R0
	JMP runs_done
runs_error_object_end:
	MOVD $const_SONIC_EXPECT_OBJ_COMMA_OR_END, R0
	JMP runs_done
runs_error_array_end:
	MOVD $const_SONIC_EXPECT_ARR_COMMA_OR_END, R0
	JMP runs_done
runs_error_capacity:
	MOVD $const_SONIC_VISIT_FAILED, R0
	JMP runs_done
runs_ok:
	MOVD $0, R0
	JMP runs_done
runs_yield:
	MOVD R16, cParityRunsState_pos(R20)
runs_done:
	SUB R25, R22, R22
	MOVD R22, Parser_pos(R19)
	SUB R13, R23, R23
	LSR $4, R23, R23
	MOVD R23, WB_LEN(R19)
	MOVD R0, event+32(FP)
	RET

// Input: R22 is the numeric body (after an optional minus), R3 is negative.
// Output: R0 is the error, R1 the kind, R2 the value bits, R22 the cursor.
// Error 16 requests the exact Go numeric fallback, with R22 restored.
// No parser or tape fields are accessed. F0/F1 and R0-R11 are scratch; the
// fixed registers above and persistent scanner vector constants are preserved.
TEXT runs_number<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVD R22, R6
	MOVBU (R22), R4
	MOVD ZR, R2
	MOVD $10, R7
	CMP $48, R4
	BEQ runs_number_zero
	SUB $48, R4, R5
	CMP $9, R5
	BHI runs_number_invalid
	MOVD ZR, R8
runs_number_digits:
	MADD R7, R5, R2, R2
	ADD $1, R22
	ADD $1, R8
	MOVBU (R22), R4
	SUB $48, R4, R5
	CMP $9, R5
	BLS runs_number_digits
	CMP $19, R8
	BHI runs_number_20digits
runs_number_integer_end:
	CMP $46, R4
	BEQ runs_number_fraction
	ORR $32, R4, R5
	CMP $101, R5
	BEQ runs_number_exponent_zero
	MOVD $const_KUint, R1 // KUint
	CBZ R3, runs_number_success
	MOVD $const_KSint, R1 // KSint
	MOVD $0x8000000000000000, R8
	CMP R8, R2
	BHI runs_number_negative_real
	NEG R2, R2
runs_number_success:
	MOVD ZR, R0
	RET
runs_number_zero:
	MOVD ZR, R8
	ADD $1, R22
	MOVBU (R22), R4
	CMP $46, R4
	BNE runs_number_integer_end
	// C skips zeros after "0." without counting them as significant digits.
	ADD $1, R22
	MOVBU (R22), R4
	SUB $48, R4, R5
	CMP $9, R5
	BHI runs_number_fallback
	MOVD R22, R10
runs_number_zero_skip:
	CMP $48, R4
	BNE runs_number_zero_done
	ADD $1, R22
	MOVBU (R22), R4
	JMP runs_number_zero_skip
runs_number_zero_done:
	ORR $32, R4, R5
	CMP $101, R5
	BEQ runs_number_exponent_zero
	SUB R22, R10, R9
	MOVD $16, R11
	SUB $48, R4, R5
	CMP $9, R5
	BLS runs_number_fraction_digits
	JMP runs_number_fraction_end
runs_number_negative_real:
	UCVTFD R2, F0
	FNEGD F0, F0
	FMOVD F0, R2
	MOVD $const_KReal, R1 // KReal
	JMP runs_number_success
runs_number_invalid:
	MOVD $const_SONIC_INVALID_NUM, R0 // SONIC_INVALID_NUM: the cursor and previous number survive.
	RET
runs_number_fallback:
	MOVD R6, R22
	MOVD $const_cParityRunsNumber, R0
	RET

// R2 holds the integer digits (R8 of them, 1..19) and R22 the decimal point.
// R9 is the decimal exponent and R11 the remaining significant digits.
runs_number_fraction:
	MOVD ZR, R9
	ADD $1, R22
	MOVBU (R22), R4
	SUB $48, R4, R5
	CMP $9, R5
	BHI runs_number_fallback
	MOVD $16, R11
	SUB R8, R11, R11
runs_number_fraction_digits:
	// R5 is the digit at R22; it has not been consumed yet. A seventeenth
	// significant digit makes the significand at least 1e16 > 2^52, outside
	// the exact subset, so long fractions go to Go without being scanned twice.
	CMP $0, R11
	BLE runs_number_fallback
	MADD R7, R5, R2, R2
	SUB $1, R11, R11
	SUB $1, R9, R9
	ADD $1, R22
	MOVBU (R22), R4
	SUB $48, R4, R5
	CMP $9, R5
	BLS runs_number_fraction_digits
runs_number_fraction_end:
	ORR $32, R4, R5
	CMP $101, R5
	BNE runs_number_float
	JMP runs_number_exponent

runs_number_exponent_zero:
	MOVD ZR, R9
runs_number_exponent:
	// R22 addresses the exponent marker. At most three digits stay here.
	ADD $1, R22
	MOVBU (R22), R4
	MOVD ZR, R10
	CMP $45, R4
	BNE runs_number_exponent_plus
	MOVD $1, R10
	ADD $1, R22
	MOVBU (R22), R4
	JMP runs_number_exponent_digits
runs_number_exponent_plus:
	CMP $43, R4
	BNE runs_number_exponent_digits
	ADD $1, R22
	MOVBU (R22), R4
runs_number_exponent_digits:
	SUB $48, R4, R5
	CMP $9, R5
	BHI runs_number_fallback
	MOVD ZR, R11
	MOVD ZR, R8
runs_number_exponent_loop:
	MADD R7, R5, R11, R11
	ADD $1, R8
	ADD $1, R22
	MOVBU (R22), R4
	SUB $48, R4, R5
	CMP $9, R5
	BLS runs_number_exponent_loop
	CMP $3, R8
	BHI runs_number_fallback
	CBZ R10, runs_number_exponent_add
	NEG R11, R11
runs_number_exponent_add:
	ADD R11, R9, R9

// cParityAtofFast: negate first, then one exact operation by 10^|R9|.
runs_number_float:
	LSR $52, R2, R4
	CBNZ R4, runs_number_el_0
	UCVTFD R2, F0
	CBZ R3, runs_number_float_sign
	FNEGD F0, F0
runs_number_float_sign:
	CBZ R2, runs_number_float_done
	CBZ R9, runs_number_float_done
	MOVD $runsPow10<>(SB), R4
	CMP $0, R9
	BLT runs_number_float_divide
	CMP $22, R9
	BGT runs_number_el_0
	MOVD $1000000000000000, R5
	CMP R5, R2
	BHI runs_number_el_0
	LSL $3, R9, R9
	ADD R9, R4, R4
	FMOVD (R4), F1
	FMULD F1, F0, F0
	JMP runs_number_float_done
runs_number_float_divide:
	CMN $22, R9
	BLT runs_number_el_0
	NEG R9, R9
	LSL $3, R9, R9
	ADD R9, R4, R4
	FMOVD (R4), F1
	FDIVD F1, F0, F0
runs_number_float_done:
	FMOVD F0, R2
	MOVD $const_KReal, R1
	MOVD ZR, R0
	RET

runs_number_20digits:
	CMP $20, R8
	BHI runs_number_fallback
	CMP $46, R4
	BEQ runs_number_fallback
	ORR $32, R4, R5
	CMP $101, R5
	BEQ runs_number_fallback
	MOVD ZR, R2
	MOVD R6, R8
	ADD $19, R6, R9
runs_number_reacc19:
	MOVBU (R8), R5
	SUB $48, R5, R5
	MADD R7, R5, R2, R2
	ADD $1, R8
	CMP R9, R8
	BNE runs_number_reacc19
	UMULH R7, R2, R10
	CBNZ R10, runs_number_20_float
	MUL R7, R2, R10
	MOVBU (R9), R5
	SUB $48, R5, R5
	ADDS R5, R10, R10
	BCS runs_number_20_float
	MOVD R10, R2
	MOVD $const_KUint, R1
	CBZ R3, runs_number_success
	JMP runs_number_negative_real
runs_number_20_float:
	MOVD $1, R9
	MOVD $1, R0
	JMP runs_number_eisel_lemire

runs_number_el_0:
	MOVD ZR, R0
runs_number_eisel_lemire:
	ADD $348, R9, R4
	CMP $695, R4
	BHI runs_number_fallback
	CLZ R2, R5
	LSL R5, R2, R8
	MOVD $217706, R10
	MUL R9, R10, R10
	ASR $16, R10, R10
	ADD $1087, R10, R10
	SUB R5, R10, R10
	MOVD $·cParityPow10Mantissa+8(SB), R11
	LSL $4, R4, R4
	MOVD (R11)(R4), R11
	UMULH R8, R11, R4
	AND $0x1ff, R4, R5
	CMP $0x1ff, R5
	BEQ runs_number_fallback
	LSR $63, R4, R11
	ADD $9, R11, R8
	LSR R8, R4, R4
	EOR $1, R11, R11
	SUB R11, R10, R10
	CBNZ R5, runs_number_el_round
	AND $3, R4, R5
	CMP $1, R5
	BEQ runs_number_fallback
runs_number_el_round:
	AND $1, R4, R11
	ADD R11, R4, R4
	LSR $1, R4, R4
	LSR $53, R4, R11
	CBZ R11, runs_number_el_pack
	LSR $1, R4, R4
	ADD $1, R10, R10
runs_number_el_pack:
	SUB $1, R10, R11
	CMP $0x7fd, R11
	BHI runs_number_fallback
	AND $0x000fffffffffffff, R4, R4
	LSL $52, R10, R10
	ORR R10, R4, R4
	LSL $63, R3, R11
	ORR R11, R4, R4
	CBZ R0, runs_number_el_done
	CMP $1, R0
	BNE runs_number_el_check_trunc
	MOVD R4, R7
	ADD $1, R2, R2
	MOVD $2, R0
	JMP runs_number_eisel_lemire
runs_number_el_check_trunc:
	CMP R7, R4
	BNE runs_number_fallback
runs_number_el_done:
	MOVD R4, R2
	MOVD $const_KReal, R1
	MOVD ZR, R0
	RET

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

// Input: R22 follows the opening quote; R21 holds options. Return R0 length
// or negative error, R1 escape status (-1 preserves the old flag on an initial
// control error), and the updated R22. A Unicode yield also returns dst in R2.
// Preserve the fixed registers and SIMD constants; never access parser/tape.
TEXT runs_string<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVD R22, R2
	MOVD $-1, R4
	MOVD $0, R8

	// Most keys fit in one vector. A single combined event mask is enough
	// when the first event is the closing quote. Every other outcome falls
	// back without changing the cursor or input, preserving C's full 32-byte
	// error priority. This probe runs only once, not in the long-string loop.
	VLD1 (R22), [V0.B16]
	VCMEQ V20.B16, V0.B16, V4.B16
	VCMEQ V21.B16, V0.B16, V6.B16
	VORR V6.B16, V4.B16, V4.B16
	TBZ $5, R21, runs_string_short_events
	VUSHR $5, V0.B16, V8.B16
	VCMEQ V22.B16, V8.B16, V8.B16
	VORR V8.B16, V4.B16, V4.B16
runs_string_short_events:
	CP_RUNS_PACK(V4, R6)
	CBZ R6, runs_string_scan
	RBIT R6, R9
	CLZ R9, R9
	MOVBU (R22)(R9), R11
	CMP $34, R11
	BEQ runs_string_plain_at

runs_string_scan:
	CP_RUNS_STRING_MASKS
	TBZ $5, R21, runs_string_scan_events
	CP_RUNS_CONTROL_MASK
runs_string_scan_events:
	ORRW R6, R8, R9
	SUBW $1, R9, R9
	TSTW R7, R9
	BNE runs_string_plain_done
	// Initial C scan prioritizes a backslash even if a control precedes it.
	SUBW $1, R7, R9
	TSTW R6, R9
	BNE runs_string_first_escape
	TSTW R8, R9
	BNE runs_string_plain_control_error
	ADD $32, R22
	B runs_string_scan

runs_string_plain_done:
	RBIT R7, R9
	CLZ R9, R9
runs_string_plain_at:
	ADD R9, R22
	SUB R2, R22, R9
	ADD $1, R22
	MOVD $0, R4
	B runs_string_plain_return

runs_string_first_escape:
	RBIT R6, R9
	CLZ R9, R9
	ADD R9, R22
	MOVD R22, R1
	// Keep the caller's LR: ASCII decoding and Unicode resumes share one
	// escaped-string body, without a nested call or a second stack frame.
	JMP runs_string_continue<>(SB)

runs_string_plain_control_error:
	RBIT R8, R9
	CLZ R9, R9
	ADD R9, R22
	MOVD $-const_SONIC_CONTROL_CHAR, R9
runs_string_plain_return:
	MOVD R9, R0
	MOVD R4, R1
	RET

// Input: R22 source, R1 next output byte, R2 original content start, R21 opts.
// Entry is either the first backslash or the byte after Go's Unicode run.
// Results match runs_string. Only the caller owns Unicode continuation state.
TEXT runs_string_continue<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVD $1, R4
	MOVD ZR, R8
	MOVD $cParityRunsEscapes<>(SB), R10
	MOVBU (R22), R9
	CMP $92, R9
	BNE runs_string_move

runs_string_escape:
	MOVBU 1(R22), R9
	CMP $117, R9
	BEQ runs_string_unicode
	MOVBU (R10)(R9), R9
	CBZ R9, runs_string_escape_error
	ADD $2, R22
	MOVB R9, (R1)
	ADD $1, R1
runs_string_resume:
	MOVBU (R22), R9
	CMP $92, R9
	BEQ runs_string_escape
	TBNZ $5, R21, runs_string_move
	MOVBU 1(R22), R11
	CMP $92, R11
	BNE runs_string_move
	CMP $34, R9
	BEQ runs_string_move
	MOVB R9, (R1)
	ADD $1, R1
	ADD $1, R22
	B runs_string_escape

runs_string_move:
	CP_RUNS_STRING_MASKS
	TBZ $5, R21, runs_string_move_events
	CP_RUNS_CONTROL_MASK
runs_string_move_events:
	ORRW R6, R8, R9
	SUBW $1, R9, R9
	TSTW R7, R9
	BNE runs_string_copy_quote
	// After the first escape, C checks controls before the next backslash.
	SUBW $1, R7, R9
	TSTW R8, R9
	BNE runs_string_control_error
	TSTW R6, R9
	BNE runs_string_copy_slash
	// Both vectors were loaded before either store, including dst/src overlap.
	FSTPQ (F0, F1), (R1)
	ADD $32, R22
	ADD $32, R1
	B runs_string_move

runs_string_copy_quote:
	RBIT R7, R9
	CLZ R9, R9
	ADD R9, R22
	ADD $1, R22
	CBZ R9, runs_string_copy_done
	TBZ $4, R9, runs_quote_lt16
	VST1 [V0.B16], (R1)
	ADD $16, R1
	VMOV V1.D[0], R3
	VMOV V1.D[1], R5
	B runs_quote_8
runs_quote_lt16:
	VMOV V0.D[0], R3
	VMOV V0.D[1], R5
runs_quote_8:
	TBZ $3, R9, runs_quote_4
	MOVD R3, (R1)
	ADD $8, R1
	MOVD R5, R3
runs_quote_4:
	TBZ $2, R9, runs_quote_2
	MOVW R3, (R1)
	ADD $4, R1
	LSR $32, R3, R3
runs_quote_2:
	TBZ $1, R9, runs_quote_1
	MOVH R3, (R1)
	ADD $2, R1
	LSR $16, R3, R3
runs_quote_1:
	TBZ $0, R9, runs_string_copy_done
	MOVB R3, (R1)
	ADD $1, R1
runs_string_copy_done:
	SUB R2, R1, R9
	B runs_string_return

runs_string_copy_slash:
	RBIT R6, R9
	CLZ R9, R9
	ADD R9, R22
	CBZ R9, runs_string_escape
	TBZ $4, R9, runs_slash_lt16
	VST1 [V0.B16], (R1)
	ADD $16, R1
	VMOV V1.D[0], R3
	VMOV V1.D[1], R5
	B runs_slash_8
runs_slash_lt16:
	VMOV V0.D[0], R3
	VMOV V0.D[1], R5
runs_slash_8:
	TBZ $3, R9, runs_slash_4
	MOVD R3, (R1)
	ADD $8, R1
	MOVD R5, R3
runs_slash_4:
	TBZ $2, R9, runs_slash_2
	MOVW R3, (R1)
	ADD $4, R1
	LSR $32, R3, R3
runs_slash_2:
	TBZ $1, R9, runs_slash_1
	MOVH R3, (R1)
	ADD $2, R1
	LSR $16, R3, R3
runs_slash_1:
	TBZ $0, R9, runs_string_escape
	MOVB R3, (R1)
	ADD $1, R1
	B runs_string_escape

// The caller publishes this output cursor before entering the Go Unicode
// handler. The scanner itself has no dependency on Parser or its state layout.
runs_string_unicode:
	MOVD R1, R2
	MOVD $-const_cParityRunsStringYield, R9
	B runs_string_return

runs_string_control_error:
	RBIT R8, R9
	CLZ R9, R9
	ADD R9, R22
	MOVD $-1, R9
	B runs_string_return
runs_string_escape_error:
	MOVD $-2, R9
	B runs_string_return
runs_string_return:
	MOVD R9, R0
	MOVD R4, R1
	RET

// Consume the first nonspace byte. The first two scalar probes deliberately
// leave the cached block untouched, matching skip_space_impl in the C parser.
// R12 addresses only the whitespace cache; R22 is input/output and R0 is the
// consumed byte. No parser, tape, or container layout is visible here.
TEXT runs_space<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVBU.P 1(R22), R0
	CMP $32, R0
	BHI runs_space_return
	BEQ runs_space_second
	CMP $9, R0
	BEQ runs_space_second
	CMP $10, R0
	BEQ runs_space_second
	CMP $13, R0
	BNE runs_space_return
runs_space_second:
	MOVBU.P 1(R22), R0
	CMP $32, R0
	BHI runs_space_return
	BEQ runs_space_cached
	CMP $9, R0
	BEQ runs_space_cached
	CMP $10, R0
	BEQ runs_space_cached
	CMP $13, R0
	BNE runs_space_return
runs_space_cached:
	MOVD cParityNonspaceBlock_offPlus64(R12), R1
	ADD R25, R1, R1
	SUB $64, R1, R1
	SUB R1, R22, R2
	CMP $64, R2
	BHS runs_space_scan
	MOVD cParityNonspaceBlock_bits(R12), R4
	MOVD $-1, R3
	LSL R2, R3, R3
	AND R3, R4, R4
	CBNZ R4, runs_space_cache_hit
	ADD $64, R1, R22
runs_space_scan:
	FLDPQ (R22), (F0, F1)
	FLDPQ 32(R22), (F2, F3)
	CP_RUNS_SPACE(V0.B16, V4.B16)
	CP_RUNS_SPACE(V1.B16, V5.B16)
	CP_RUNS_SPACE(V2.B16, V6.B16)
	CP_RUNS_SPACE(V3.B16, V7.B16)
	VAND V30.B16, V4.B16, V4.B16
	VAND V30.B16, V5.B16, V5.B16
	VAND V30.B16, V6.B16, V6.B16
	VAND V30.B16, V7.B16, V7.B16
	VADDP V5.B16, V4.B16, V4.B16
	VADDP V7.B16, V6.B16, V6.B16
	VADDP V6.B16, V4.B16, V4.B16
	VADDP V4.B16, V4.B16, V4.B16
	VMOV V4.D[0], R4
	MVN R4, R4
	CBNZ R4, runs_space_refilled
	ADD $64, R22
	B runs_space_scan
runs_space_refilled:
	SUB R25, R22, R1
	ADD $64, R1, R1
	MOVD R1, cParityNonspaceBlock_offPlus64(R12)
	MOVD R4, cParityNonspaceBlock_bits(R12)
	MOVD R22, R1
runs_space_cache_hit:
	RBIT R4, R6
	CLZ R6, R6
	ADD R6, R1, R22
	MOVBU.P 1(R22), R0
runs_space_return:
	RET

DATA cParityRunsSpaceTables<>+0(SB)/8, $0x0000000000000020
DATA cParityRunsSpaceTables<>+8(SB)/8, $0x00000d00000a0900
DATA cParityRunsSpaceTables<>+16(SB)/8, $0x8040201008040201
DATA cParityRunsSpaceTables<>+24(SB)/8, $0x8040201008040201
DATA cParityRunsSpaceTables<>+32(SB)/8, $0x0b030a0209010800
DATA cParityRunsSpaceTables<>+40(SB)/8, $0x0f070e060d050c04
GLOBL cParityRunsSpaceTables<>(SB), RODATA|NOPTR, $48

DATA cParityRunsEscapes<>+34(SB)/1, $34
DATA cParityRunsEscapes<>+47(SB)/1, $47
DATA cParityRunsEscapes<>+92(SB)/1, $92
DATA cParityRunsEscapes<>+98(SB)/1, $8
DATA cParityRunsEscapes<>+102(SB)/1, $12
DATA cParityRunsEscapes<>+110(SB)/1, $10
DATA cParityRunsEscapes<>+114(SB)/1, $13
DATA cParityRunsEscapes<>+116(SB)/1, $9
GLOBL cParityRunsEscapes<>(SB), RODATA|NOPTR, $256
