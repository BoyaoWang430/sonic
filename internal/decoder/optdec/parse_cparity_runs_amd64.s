//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"
#include "funcdata.h"
#include "go_asm.h"

// Private register convention for cParityParseRunsAsm and its static helpers:
// R12: p (*Parser)
// R13: state (*cParityRunsState)
// SI:  cur (Parser_cur(p) input cursor)
// BP:  ncur (WB_CUR(p) tape cursor)
// Stack frame of cParityParseRunsAsm ($64-40):
//   0(SP): saved BP
//   8(SP): saved key string length
//  16(SP): parent node pointer
//  24(SP): pos (active token position)
//  32(SP): context (cParityRunsContext*)
//  40(SP): scratch for runs_number<>
//  48(SP): rawBase
//  56(SP): nodesLast (last spare-safe node)
// Persistent SIMD registers (256-bit YMM, never touching Y15/X15 or R14):
//  Y11: broadcast 0x1f (control char threshold)
//  Y12: cParityRunsSpaceTables<> (VPSHUFB whitespace LUT)
//  Y13: broadcast '\\' (0x5c)
//  Y14: broadcast '"'  (0x22)

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
#define NBK_OFF (Parser__nbk+cParityNonspaceBlock_offPlus64)
#define NBK_BITS (Parser__nbk+cParityNonspaceBlock_bits)

#define RUNS_CAPACITY \
	CMPQ BP, 56(SP)

#define RUNS_SCAN_INIT \
	CMPB ·hasAVX2(SB), $0; \
	JE   runs_scan_init_sse; \
	VMOVDQU cParityRunsSpaceTables<>(SB), Y12; \
	VMOVDQU cParityRunsCtrlVec<>(SB), Y11; \
	VMOVDQU cParityRunsSlashVec<>(SB), Y13; \
	VMOVDQU cParityRunsQuoteVec<>(SB), Y14; \
	JMP  runs_scan_init_done; \
runs_scan_init_sse: \
	MOVOU cParityRunsSpaceTables<>(SB), X12; \
	MOVOU cParityRunsCtrlVec<>(SB), X11; \
	MOVOU cParityRunsSlashVec<>(SB), X13; \
	MOVOU cParityRunsQuoteVec<>(SB), X14; \
runs_scan_init_done:

// func cParityParseRunsAsm(p *Parser, state *cParityRunsState, rawBase *byte, nodesBase *node) (event int)
TEXT ·cParityParseRunsAsm(SB), NOSPLIT, $64-40
	NO_LOCAL_POINTERS
	MOVQ BP, 0(SP)
	MOVQ p+0(FP), R12
	MOVQ state+8(FP), R13
	MOVQ rawBase+16(FP), SI
	MOVQ SI, 48(SP)
	ADDQ Parser_pos(R12), SI
	MOVQ nodesBase+24(FP), AX
	MOVQ WB_CAP(R12), CX
	DECQ CX
	SHLQ $4, CX
	ADDQ AX, CX
	MOVQ CX, 56(SP)
	MOVQ WB_LEN(R12), BP
	SHLQ $4, BP
	ADDQ AX, BP

	MOVQ $const_KNull, AX
	MOVQ WB_PARENT(R12), BX
	CMPQ BX, $-1
	JE   runs_parent_loaded
	MOVQ nodesBase+24(FP), AX
	SHLQ $4, BX
	ADDQ BX, AX
runs_parent_loaded:
	MOVQ AX, 16(SP)
	RUNS_SCAN_INIT

	MOVQ cParityRunsState_phase(R13), AX
	CMPQ AX, $const_cParityRunsObjectStart
	JE   runs_open_object
	CMPQ AX, $const_cParityRunsArrayStart
	JE   runs_open_array
	CMPQ AX, $const_cParityRunsObjectValue
	JE   runs_object_value
	CMPQ AX, $const_cParityRunsParentContinue
	JE   runs_parent_continue
	CMPQ AX, $const_cParityRunsStringResume
	JE   runs_resume_string

	MOVQ $const_cParityRunsContextRoot, 32(SP)
	CALL runs_space<>(SB)
	MOVQ SI, BX
	SUBQ rawBase+16(FP), BX
	DECQ BX
	MOVQ BX, 24(SP)
	JMP  runs_value

runs_parent_continue:
	MOVBLZX (SI), AX
	CMPB AL, $32
	JBE  runs_parent_continue_space
	INCQ SI
runs_parent_continue_dispatch:
	MOVQ 16(SP), BX
	MOVBLZX (BX), CX
	CMPB CL, $const_KObject
	JE   runs_object_continue
	JMP  runs_array_continue
runs_parent_continue_space:
	CALL runs_space<>(SB)
	JMP  runs_parent_continue_dispatch

runs_object_key:
	CMPQ AX, $34
	JNE  runs_error_key
	MOVQ SI, BX
	SUBQ rawBase+16(FP), BX
	DECQ BX
	MOVQ BX, 24(SP)
	MOVQ $const_cParityRunsContextKey, 32(SP)
	CALL runs_string<>(SB)
runs_key_string_result:
	CMPQ AX, $-const_cParityRunsStringYield
	JE   runs_unicode_yield
	CMPQ BX, $-1
	JE   runs_key_escape_saved
	MOVQ BX, cParityRunsState_hasEsc(R13)
runs_key_escape_saved:
	CMPQ AX, $0
	JL   runs_string_error
	CMPB (SI), $58
	JNE  runs_key_colon_space
	INCQ SI
	MOVQ AX, DX
runs_key_colon_ok:
	MOVQ cParityRunsState_hasEsc(R13), BX
	SHLQ $3, BX
	ORQ  $const_KStringCommon, BX
	MOVQ 24(SP), CX
	INCQ CX
	SHLQ $32, CX
	ORQ  CX, BX
	MOVQ BX, 0(BP)
	MOVQ DX, 8(BP)
	ADDQ $16, BP
	RUNS_CAPACITY
	JBE  runs_object_value
	MOVB $1, WB_ISKEY(R12)
	JMP  runs_error_capacity
runs_key_colon_space:
	MOVQ AX, 8(SP)
	CALL runs_space<>(SB)
	CMPQ AX, $58
	JNE  runs_error_colon
	MOVQ 8(SP), DX
	JMP  runs_key_colon_ok

runs_object_value:
	MOVQ $const_cParityRunsContextObject, 32(SP)
	MOVBLZX (SI), AX
	CMPB AL, $32
	JA   runs_object_value_0sp
	JNE  runs_object_value_space
	MOVBLZX 1(SI), AX
	CMPB AL, $32
	JBE  runs_object_value_space
	MOVQ SI, BX
	SUBQ rawBase+16(FP), BX
	INCQ BX
	MOVQ BX, 24(SP)
	ADDQ $2, SI
	JMP  runs_value
runs_object_value_0sp:
	MOVQ SI, BX
	SUBQ rawBase+16(FP), BX
	MOVQ BX, 24(SP)
	INCQ SI
	JMP  runs_value
runs_object_value_space:
	CALL runs_space<>(SB)
	MOVQ SI, BX
	SUBQ rawBase+16(FP), BX
	DECQ BX
	MOVQ BX, 24(SP)
	JMP  runs_value

runs_array_value:
	MOVQ $const_cParityRunsContextArray, 32(SP)
	MOVQ SI, BX
	SUBQ rawBase+16(FP), BX
	DECQ BX
	MOVQ BX, 24(SP)

runs_value:
	CMPQ AX, $34
	JE   runs_value_string
	LEAQ -48(AX), BX
	CMPQ BX, $9
	JBE  runs_value_positive
	CMPQ AX, $123
	JE   runs_value_object
	CMPQ AX, $91
	JE   runs_value_array
	CMPQ AX, $45
	JE   runs_value_negative
	CMPQ AX, $116
	JE   runs_value_true
	CMPQ AX, $102
	JE   runs_value_false
	CMPQ AX, $110
	JE   runs_value_null
	MOVQ $const_SONIC_INVALID_CHAR, AX
	JMP  runs_done

runs_value_object:
	MOVQ 24(SP), CX
	SHLQ $32, CX
	ORQ  $const_KObject, CX
	MOVQ CX, 0(BP)
	MOVQ WB_PARENT(R12), DX
	MOVQ DX, 8(BP)
	MOVQ BP, AX
	SUBQ nodesBase+24(FP), AX
	SHRQ $4, AX
	MOVQ AX, WB_PARENT(R12)
	MOVQ BP, 16(SP)
	ADDQ $16, BP
	INCQ WB_DEPTH(R12)
	RUNS_CAPACITY
	JA   runs_error_capacity
	JMP  runs_open_object
runs_value_array:
	MOVQ 24(SP), CX
	SHLQ $32, CX
	ORQ  $const_KArray, CX
	MOVQ CX, 0(BP)
	MOVQ WB_PARENT(R12), DX
	MOVQ DX, 8(BP)
	MOVQ BP, AX
	SUBQ nodesBase+24(FP), AX
	SHRQ $4, AX
	MOVQ AX, WB_PARENT(R12)
	MOVQ BP, 16(SP)
	ADDQ $16, BP
	INCQ WB_DEPTH(R12)
	RUNS_CAPACITY
	JA   runs_error_capacity
	JMP  runs_open_array
runs_open_object:
	MOVBLZX (SI), AX
	CMPB AL, $32
	JBE  runs_open_object_space
	INCQ SI
	CMPQ AX, $125
	JNE  runs_object_key
	JMP  runs_close_container
runs_open_object_space:
	CALL runs_space<>(SB)
	CMPQ AX, $125
	JNE  runs_object_key
	JMP  runs_close_container
runs_open_array:
	MOVBLZX (SI), AX
	CMPB AL, $32
	JBE  runs_open_array_space
	INCQ SI
	CMPQ AX, $93
	JNE  runs_array_value
	JMP  runs_close_container
runs_open_array_space:
	CALL runs_space<>(SB)
	CMPQ AX, $93
	JNE  runs_array_value
	JMP  runs_close_container

runs_value_positive:
	DECQ SI
	XORQ BX, BX
	JMP  runs_value_number
runs_value_negative:
	MOVQ $1, BX
runs_value_number:
	TESTB $2, Parser_options(R12)
	JNZ  runs_number_yield
	CALL runs_number<>(SB)
	CMPQ AX, $const_cParityRunsNumber
	JE   runs_number_yield
	TESTQ AX, AX
	JNZ  runs_number_load_old
	MOVQ BX, W_NUM_TYPE(R13)
	MOVQ DX, W_NUM_VALUE(R13)
	JMP  runs_emit_number
runs_number_load_old:
	MOVQ W_NUM_TYPE(R13), BX
	MOVQ W_NUM_VALUE(R13), DX
	JMP  runs_emit_number
runs_number_yield:
	MOVQ $const_cParityRunsNumber, AX
	JMP  runs_yield
runs_emit_number:
	CMPQ BX, $const_KUint
	JE   runs_emit_valid_number
	CMPQ BX, $const_KSint
	JE   runs_emit_valid_number
	CMPQ BX, $const_KReal
	JE   runs_emit_valid_number
	TESTQ AX, AX
	JNZ  runs_done
	JMP  runs_error_capacity
runs_emit_valid_number:
	MOVQ 24(SP), CX
	SHLQ $32, CX
	ORQ  CX, BX
	MOVQ BX, 0(BP)
	MOVQ DX, 8(BP)
	ADDQ $16, BP
	INCL WS_NUMBER(R12)
	JMP  runs_after_scalar

runs_value_string:
	CALL runs_string<>(SB)
runs_value_string_result:
	CMPQ AX, $-const_cParityRunsStringYield
	JE   runs_unicode_yield
	CMPQ BX, $-1
	JE   runs_value_escape_saved
	MOVQ BX, cParityRunsState_hasEsc(R13)
runs_value_escape_saved:
	MOVQ AX, DX
	XORQ AX, AX
	CMPQ DX, $0
	JGE  runs_value_string_eof
	MOVQ DX, AX
	NEGQ AX
runs_value_string_eof:
	CMPQ 32(SP), $0
	JNE  runs_emit_string
	MOVQ rawBase+16(FP), CX
	ADDQ Parser_end(R12), CX
	CMPQ CX, SI
	JGE  runs_emit_string
	MOVQ $const_SONIC_EOF, AX
runs_emit_string:
	MOVQ cParityRunsState_hasEsc(R13), BX
	SHLQ $3, BX
	ORQ  $const_KStringCommon, BX
	MOVQ 24(SP), CX
	INCQ CX
	SHLQ $32, CX
	ORQ  CX, BX
	MOVQ BX, 0(BP)
	MOVQ DX, 8(BP)
	ADDQ $16, BP
	INCL WS_STRING(R12)
	JMP  runs_after_scalar

runs_resume_string:
	MOVQ cParityRunsState_pos(R13), CX
	MOVQ CX, 24(SP)
	MOVQ cParityRunsState_context(R13), BX
	MOVQ BX, 32(SP)
	MOVQ cParityRunsState_stringErr(R13), AX
	TESTQ AX, AX
	JNZ  runs_resume_string_error
	MOVQ rawBase+16(FP), DX
	LEAQ 1(DX)(CX*1), DX
	MOVQ rawBase+16(FP), DI
	ADDQ cParityRunsState_stringDst(R13), DI
	CALL runs_string_continue<>(SB)
	JMP  runs_resume_string_result
runs_resume_string_error:
	NEGQ AX
	MOVQ $1, BX
runs_resume_string_result:
	CMPQ 32(SP), $const_cParityRunsContextKey
	JE   runs_key_string_result
	JMP  runs_value_string_result

runs_unicode_yield:
	SUBQ rawBase+16(FP), DX
	MOVQ DX, cParityRunsState_stringDst(R13)
	MOVQ 32(SP), CX
	MOVQ CX, cParityRunsState_context(R13)
	MOVQ $const_cParityRunsUnicode, AX
	JMP  runs_yield

runs_value_true:
	MOVQ $const_KTrue, DX
	MOVL (SI), BX
	ANDL $0xffffff, BX
	CMPL BX, $0x657572
	JNE  runs_literal_yield
	ADDQ $3, SI
	JMP  runs_literal_ok
runs_value_false:
	MOVQ $const_KFalse, DX
	MOVL (SI), BX
	CMPL BX, $0x65736c61
	JNE  runs_literal_yield
	ADDQ $4, SI
	JMP  runs_literal_ok
runs_value_null:
	MOVQ $const_KNull, DX
	MOVL (SI), BX
	ANDL $0xffffff, BX
	CMPL BX, $0x6c6c75
	JNE  runs_literal_yield
	ADDQ $3, SI
runs_literal_ok:
	XORQ AX, AX
	MOVQ 24(SP), CX
	SHLQ $32, CX
	ORQ  CX, DX
	MOVQ DX, 0(BP)
	ADDQ $16, BP
	JMP  runs_after_scalar

runs_literal_yield:
	MOVQ $const_cParityRunsLiteral, AX
	JMP  runs_yield

runs_after_scalar:
	TESTQ AX, AX
	JNZ  runs_done
	RUNS_CAPACITY
	JA   runs_error_capacity
	CMPQ 32(SP), $0
	JE   runs_ok
	MOVBLZX (SI), AX
	CMPB AL, $32
	JBE  runs_after_scalar_space
	INCQ SI
	CMPQ 32(SP), $const_cParityRunsContextObject
	JE   runs_object_continue
	JMP  runs_array_continue
runs_after_scalar_space:
	CALL runs_space<>(SB)
	CMPQ 32(SP), $const_cParityRunsContextObject
	JE   runs_object_continue
	JMP  runs_array_continue

runs_object_continue:
	MOVQ 16(SP), BX
	ADDQ $256, (BX)
	CMPQ AX, $44
	JE   runs_next_key
	CMPQ AX, $125
	JNE  runs_error_object_end
	JMP  runs_close_container
runs_next_key:
	MOVBLZX (SI), AX
	CMPB AL, $32
	JBE  runs_next_key_space
	INCQ SI
	JMP  runs_object_key
runs_next_key_space:
	CALL runs_space<>(SB)
	JMP  runs_object_key

runs_array_continue:
	MOVQ 16(SP), BX
	ADDQ $256, (BX)
	CMPQ AX, $44
	JE   runs_next_array_value
	CMPQ AX, $93
	JNE  runs_error_array_end

runs_close_container:
	MOVQ 16(SP), BX
	MOVQ 0(BX), CX
	MOVL CX, DX
	SHRL $8, DX
	CMPB CL, $const_KObject
	JNE  runs_close_array_stat
	INCL WS_OBJECT(R12)
	ADDL DX, WS_OBJECT_KEYS(R12)
	JMP  runs_close_patch
runs_close_array_stat:
	INCL WS_ARRAY(R12)
	ADDL DX, WS_ARRAY_ELEMS(R12)
runs_close_patch:
	MOVQ 8(BX), AX
	MOVQ AX, WB_PARENT(R12)
	MOVQ BP, DI
	SUBQ BX, DI
	SHRQ $4, DI
	MOVL DI, DI
	SHLQ $32, DI
	ORQ  DX, DI
	MOVQ DI, 8(BX)
	MOVB $0, 1(BX)
	MOVW $0, 2(BX)
	MOVQ WB_DEPTH(R12), CX
	MOVL WS_MAX_DEPTH(R12), DI
	CMPQ CX, DI
	JBE  runs_close_depth_ok
	MOVL CX, WS_MAX_DEPTH(R12)
	CMPQ CX, $4096
	JA   runs_error_stack_overflow
runs_close_depth_ok:
	DECQ CX
	MOVQ CX, WB_DEPTH(R12)
	CMPQ AX, $-1
	JE   runs_ok
	MOVL WS_OBJECT(R12), DI
	ADDL WS_ARRAY(R12), DI
	TESTL $0xfff, DI
	JZ   runs_close_yield
	SHLQ $4, AX
	ADDQ nodesBase+24(FP), AX
	MOVQ AX, 16(SP)
	JMP  runs_parent_continue
runs_close_yield:
	MOVQ $const_cParityRunsCloseContainer, AX
	JMP  runs_done
runs_error_stack_overflow:
	MOVQ $const_SONIC_STACK_OVERFLOW, AX
	JMP  runs_done

runs_next_array_value:
	MOVBLZX (SI), AX
	CMPB AL, $32
	JA   runs_next_array_0sp
	JNE  runs_next_array_space
	MOVBLZX 1(SI), AX
	CMPB AL, $32
	JBE  runs_next_array_space
	ADDQ $2, SI
	JMP  runs_array_value
runs_next_array_0sp:
	INCQ SI
	JMP  runs_array_value
runs_next_array_space:
	CALL runs_space<>(SB)
	JMP  runs_array_value

runs_string_error:
	NEGQ AX
	JMP  runs_done
runs_error_key:
	MOVQ $const_SONIC_EXPECT_KEY, AX
	JMP  runs_done
runs_error_colon:
	MOVQ $const_SONIC_EXPECT_COLON, AX
	JMP  runs_done
runs_error_object_end:
	MOVQ $const_SONIC_EXPECT_OBJ_COMMA_OR_END, AX
	JMP  runs_done
runs_error_array_end:
	MOVQ $const_SONIC_EXPECT_ARR_COMMA_OR_END, AX
	JMP  runs_done
runs_error_capacity:
	MOVQ $const_SONIC_VISIT_FAILED, AX
	JMP  runs_done
runs_ok:
	XORQ AX, AX
	JMP  runs_done
runs_yield:
	MOVQ 24(SP), CX
	MOVQ CX, cParityRunsState_pos(R13)
runs_done:
	SUBQ rawBase+16(FP), SI
	MOVQ SI, Parser_pos(R12)
	SUBQ nodesBase+24(FP), BP
	SHRQ $4, BP
	MOVQ BP, WB_LEN(R12)
	MOVQ 0(SP), BP
	MOVQ AX, event+32(FP)
	CMPB ·hasAVX2(SB), $0
	JE   runs_done_ret
	VZEROUPPER
runs_done_ret:
	RET


// Input: SI is numeric body, BX is negative flag (0 or 1).
// Output: AX is error, BX is kind, DX is value bits, SI is updated cursor.
TEXT runs_number<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ SI, 48(SP)
	MOVBLZX (SI), CX
	XORQ DI, DI
	CMPB CL, $48
	JE   runs_number_zero
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JA   runs_number_invalid
	XORQ R8, R8
runs_number_digits:
	IMULQ $10, DI
	ADDQ AX, DI
	INCQ SI
	INCQ R8
	MOVBLZX (SI), CX
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JBE  runs_number_digits
	CMPQ R8, $19
	JA   runs_number_20digits
runs_number_integer_end:
	CMPB CL, $46
	JE   runs_number_fraction
	MOVQ CX, AX
	ORQ  $32, AX
	CMPB AL, $101
	JE   runs_number_exponent_zero
	TESTQ BX, BX
	JZ   runs_number_uint_success
	MOVQ $0x8000000000000000, AX
	CMPQ DI, AX
	JA   runs_number_negative_real
	NEGQ DI
	MOVQ $const_KSint, BX
	MOVQ DI, DX
	XORQ AX, AX
	RET
runs_number_uint_success:
	MOVQ $const_KUint, BX
	MOVQ DI, DX
	XORQ AX, AX
	RET

runs_number_zero:
	XORQ R8, R8
	INCQ SI
	MOVBLZX (SI), CX
	CMPB CL, $46
	JNE  runs_number_integer_end
	INCQ SI
	MOVBLZX (SI), CX
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JA   runs_number_fallback
	MOVQ SI, R10
runs_number_zero_skip:
	CMPB CL, $48
	JNE  runs_number_zero_done
	INCQ SI
	MOVBLZX (SI), CX
	JMP  runs_number_zero_skip
runs_number_zero_done:
	MOVQ CX, AX
	ORQ  $32, AX
	CMPB AL, $101
	JE   runs_number_exponent_zero
	MOVQ R10, R9
	SUBQ SI, R9
	MOVQ $16, R11
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JBE  runs_number_fraction_digits
	JMP  runs_number_fraction_end

runs_number_negative_real:
	MOVQ DI, AX
	SHRQ $1, AX
	ANDQ $1, DI
	ORQ  DI, AX
	CMPB ·hasAVX2(SB), $0
	JE   runs_number_negative_real_sse
	VXORPD X0, X0, X0
	VCVTSI2SDQ AX, X0, X0
	VADDSD X0, X0, X0
	VMOVQ X0, DX
	JMP  runs_number_negative_real_done
runs_number_negative_real_sse:
	XORPD X0, X0
	CVTSQ2SD AX, X0
	ADDSD X0, X0
	MOVQ X0, DX
runs_number_negative_real_done:
	BTSQ $63, DX
	MOVQ $const_KReal, BX
	XORQ AX, AX
	RET

runs_number_invalid:
	MOVQ $const_SONIC_INVALID_NUM, AX
	RET

runs_number_fallback:
	MOVQ 48(SP), SI
	MOVQ $const_cParityRunsNumber, AX
	RET

runs_number_fraction:
	XORQ R9, R9
	INCQ SI
	MOVBLZX (SI), CX
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JA   runs_number_fallback
	MOVQ $16, R11
	SUBQ R8, R11
runs_number_fraction_digits:
	CMPQ R11, $0
	JLE  runs_number_fallback
	IMULQ $10, DI
	ADDQ AX, DI
	DECQ R11
	DECQ R9
	INCQ SI
	MOVBLZX (SI), CX
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JBE  runs_number_fraction_digits
runs_number_fraction_end:
	MOVQ CX, AX
	ORQ  $32, AX
	CMPB AL, $101
	JNE  runs_number_float
	JMP  runs_number_exponent

runs_number_exponent_zero:
	XORQ R9, R9
runs_number_exponent:
	INCQ SI
	MOVBLZX (SI), CX
	XORQ R10, R10
	CMPB CL, $45
	JNE  runs_number_exponent_plus
	MOVQ $1, R10
	INCQ SI
	MOVBLZX (SI), CX
	JMP  runs_number_exponent_digits
runs_number_exponent_plus:
	CMPB CL, $43
	JNE  runs_number_exponent_digits
	INCQ SI
	MOVBLZX (SI), CX
runs_number_exponent_digits:
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JA   runs_number_fallback
	XORQ R11, R11
	XORQ R8, R8
runs_number_exponent_loop:
	IMULQ $10, R11
	ADDQ AX, R11
	INCQ R8
	INCQ SI
	MOVBLZX (SI), CX
	LEAQ -48(CX), AX
	CMPQ AX, $9
	JBE  runs_number_exponent_loop
	CMPQ R8, $3
	JA   runs_number_fallback
	TESTQ R10, R10
	JZ   runs_number_exponent_add
	NEGQ R11
runs_number_exponent_add:
	ADDQ R11, R9

runs_number_float:
	MOVQ DI, AX
	SHRQ $52, AX
	JNZ  runs_number_el_0
	CMPB ·hasAVX2(SB), $0
	JE   runs_number_float_sse
	VXORPD X0, X0, X0
	VCVTSI2SDQ DI, X0, X0
	TESTQ BX, BX
	JZ   runs_number_float_avx_sign
	MOVQ $0x8000000000000000, AX
	VMOVQ AX, X1
	VXORPD X1, X0, X0
runs_number_float_avx_sign:
	TESTQ DI, DI
	JZ   runs_number_float_avx_done
	TESTQ R9, R9
	JZ   runs_number_float_avx_done
	LEAQ runsPow10<>(SB), AX
	CMPQ R9, $0
	JL   runs_number_float_avx_divide
	CMPQ R9, $22
	JG   runs_number_el_0
	MOVQ $1000000000000000, CX
	CMPQ DI, CX
	JA   runs_number_el_0
	VMOVSD (AX)(R9*8), X1
	VMULSD X1, X0, X0
	JMP  runs_number_float_avx_done
runs_number_float_avx_divide:
	CMPQ R9, $-22
	JL   runs_number_el_0
	MOVQ R9, CX
	NEGQ CX
	VMOVSD (AX)(CX*8), X1
	VDIVSD X1, X0, X0
runs_number_float_avx_done:
	VMOVQ X0, DX
	MOVQ $const_KReal, BX
	XORQ AX, AX
	RET

runs_number_float_sse:
	XORPD X0, X0
	CVTSQ2SD DI, X0
	TESTQ BX, BX
	JZ   runs_number_float_sign
	MOVQ $0x8000000000000000, AX
	MOVQ AX, X1
	XORPD X1, X0
runs_number_float_sign:
	TESTQ DI, DI
	JZ   runs_number_float_done
	TESTQ R9, R9
	JZ   runs_number_float_done
	LEAQ runsPow10<>(SB), AX
	CMPQ R9, $0
	JL   runs_number_float_divide
	CMPQ R9, $22
	JG   runs_number_el_0
	MOVQ $1000000000000000, CX
	CMPQ DI, CX
	JA   runs_number_el_0
	MOVSD (AX)(R9*8), X1
	MULSD X1, X0
	JMP  runs_number_float_done
runs_number_float_divide:
	CMPQ R9, $-22
	JL   runs_number_el_0
	MOVQ R9, CX
	NEGQ CX
	MOVSD (AX)(CX*8), X1
	DIVSD X1, X0
runs_number_float_done:
	MOVQ X0, DX
	MOVQ $const_KReal, BX
	XORQ AX, AX
	RET

runs_number_20digits:
	CMPQ R8, $20
	JA   runs_number_fallback
	CMPB CL, $46
	JE   runs_number_fallback
	MOVQ CX, AX
	ORQ  $32, AX
	CMPB AL, $101
	JE   runs_number_fallback
	XORQ DI, DI
	MOVQ 48(SP), R8
	LEAQ 19(R8), R9
runs_number_reacc19:
	MOVBLZX (R8), AX
	SUBQ $48, AX
	IMULQ $10, DI
	ADDQ AX, DI
	INCQ R8
	CMPQ R8, R9
	JNE  runs_number_reacc19
	MOVQ $10, AX
	MULQ DI
	TESTQ DX, DX
	JNZ  runs_number_20_float
	MOVBLZX (R9), CX
	SUBQ $48, CX
	ADDQ CX, AX
	JC   runs_number_20_float
	MOVQ AX, DI
	TESTQ BX, BX
	JNZ  runs_number_negative_real
	MOVQ $const_KUint, BX
	MOVQ DI, DX
	XORQ AX, AX
	RET

runs_number_20_float:
	MOVQ $1, R9
	MOVQ $1, R11
	JMP  runs_number_eisel_lemire

runs_number_el_0:
	XORQ R11, R11
runs_number_eisel_lemire:
	LEAQ 348(R9), CX
	CMPQ CX, $695
	JA   runs_number_fallback
	BSRQ DI, AX
	XORQ $63, AX
	MOVQ DI, R8
	MOVQ CX, R10
	MOVQ AX, CX
	SHLQ CL, R8
	MOVQ R9, AX
	IMULQ $217706, AX
	SARQ $16, AX
	ADDQ $1087, AX
	SUBQ CX, AX
	SHLQ $4, R10
	LEAQ ·cParityPow10Mantissa+8(SB), CX
	MOVQ (CX)(R10*1), CX
	MOVQ AX, R10
	MOVQ R8, AX
	MULQ CX
	MOVQ DX, CX
	ANDQ $0x1ff, CX
	CMPQ CX, $0x1ff
	JE   runs_number_fallback
	MOVQ DX, AX
	SHRQ $63, AX
	MOVQ CX, R8
	LEAQ 9(AX), CX
	SHRQ CL, DX
	XORQ $1, AX
	SUBQ AX, R10
	TESTQ R8, R8
	JNZ  runs_number_el_round
	MOVQ DX, CX
	ANDQ $3, CX
	CMPQ CX, $1
	JE   runs_number_fallback
runs_number_el_round:
	MOVQ DX, AX
	ANDQ $1, AX
	ADDQ AX, DX
	SHRQ $1, DX
	MOVQ DX, AX
	SHRQ $53, AX
	JZ   runs_number_el_pack
	SHRQ $1, DX
	INCQ R10
runs_number_el_pack:
	LEAQ -1(R10), AX
	CMPQ AX, $0x7fd
	JA   runs_number_fallback
	MOVQ $0x000fffffffffffff, AX
	ANDQ AX, DX
	MOVQ R10, AX
	SHLQ $52, AX
	ORQ  AX, DX
	MOVQ BX, AX
	SHLQ $63, AX
	ORQ  AX, DX
	TESTQ R11, R11
	JZ   runs_number_el_done
	CMPQ R11, $1
	JNE  runs_number_el_check_trunc
	MOVQ DX, 16(SP)
	INCQ DI
	MOVQ $2, R11
	JMP  runs_number_eisel_lemire
runs_number_el_check_trunc:
	MOVQ 16(SP), AX
	CMPQ DX, AX
	JNE  runs_number_fallback
runs_number_el_done:
	MOVQ $const_KReal, BX
	XORQ AX, AX
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


// Input: SI follows opening quote; R12 holds *Parser.
// Output: AX length or negative error, BX escape status, SI updated cursor.
// On Unicode yield, DX returns dst.
TEXT runs_string<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ SI, DX
	MOVQ $-1, R11
	XORL R8, R8
	CMPB ·hasAVX2(SB), $0
	JE   runs_string_sse
	TESTB $32, Parser_options(R12)
	JNZ  runs_string_scan_avx2_val

runs_string_scan_avx2_fast:
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y13, Y0, Y4
	VPCMPEQB Y14, Y0, Y5
	VPOR Y5, Y4, Y4
	VPMOVMSKB Y4, AX
	TESTL AX, AX
	JNZ  runs_string_avx2_event_fast
	ADDQ $32, SI
	JMP  runs_string_scan_avx2_fast

runs_string_avx2_event_fast:
	BSFL AX, CX
	CMPB (SI)(CX*1), $34
	JE   runs_string_plain_at
	ADDQ CX, SI
	MOVQ SI, DI
	JMP  runs_string_continue<>(SB)

runs_string_scan_avx2_val:
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y13, Y0, Y4
	VPCMPEQB Y14, Y0, Y5
	VPOR Y5, Y4, Y4
	VPMAXUB Y11, Y0, Y6
	VPCMPEQB Y11, Y6, Y6
	VPOR Y6, Y4, Y4
	VPMOVMSKB Y4, AX
	TESTL AX, AX
	JNZ  runs_string_avx2_event_val
	ADDQ $32, SI
	JMP  runs_string_scan_avx2_val

runs_string_avx2_event_val:
	BSFL AX, CX
	MOVBLZX (SI)(CX*1), AX
	CMPB AL, $34
	JE   runs_string_plain_at
	CMPB AL, $92
	JE   runs_string_avx2_escape
	// Raw control char at CX; preserve C-parity backslash priority before first quote.
	MOVL CX, R8
	VPCMPEQB Y13, Y0, Y4
	VPMOVMSKB Y4, R9
	VPCMPEQB Y14, Y0, Y5
	VPMOVMSKB Y5, R10
	MOVL R10, AX
	DECL AX
	TESTL R9, AX
	JNZ  runs_string_first_escape
	ADDQ R8, SI
	MOVQ $-const_SONIC_CONTROL_CHAR, AX
	MOVQ R11, BX
	RET

runs_string_avx2_escape:
	ADDQ CX, SI
	MOVQ SI, DI
	JMP  runs_string_continue<>(SB)

runs_string_sse:
	MOVOU (SI), X0
	MOVO X0, X4; PCMPEQB X13, X4
	MOVO X0, X5; PCMPEQB X14, X5
	POR X5, X4
	TESTB $32, Parser_options(R12)
	JZ   runs_string_short_events
	MOVO X0, X6; PMAXUB X11, X6
	PCMPEQB X11, X6
	POR X6, X4
runs_string_short_events:
	PMOVMSKB X4, AX
	TESTL AX, AX
	JZ   runs_string_scan_sse
	BSFL AX, CX
	CMPB (SI)(CX*1), $34
	JE   runs_string_plain_at

runs_string_scan_sse:
	MOVOU 0(SI), X0
	MOVOU 16(SI), X1
	MOVO X0, X4; PCMPEQB X13, X4; PMOVMSKB X4, R9
	MOVO X1, X4; PCMPEQB X13, X4; PMOVMSKB X4, AX
	SHLL $16, AX; ORL AX, R9
	MOVO X0, X4; PCMPEQB X14, X4; PMOVMSKB X4, R10
	MOVO X1, X4; PCMPEQB X14, X4; PMOVMSKB X4, AX
	SHLL $16, AX; ORL AX, R10
	TESTB $32, Parser_options(R12)
	JZ   runs_string_scan_sse_events
	MOVO X0, X4; PMAXUB X11, X4; PCMPEQB X11, X4; PMOVMSKB X4, R8
	MOVO X1, X4; PMAXUB X11, X4; PCMPEQB X11, X4; PMOVMSKB X4, AX
	SHLL $16, AX; ORL AX, R8

runs_string_scan_sse_events:
	MOVL R9, AX
	ORL  R8, AX
	DECL AX
	TESTL R10, AX
	JNZ  runs_string_plain_done
	MOVL R10, AX
	DECL AX
	TESTL R9, AX
	JNZ  runs_string_first_escape
	TESTL R8, AX
	JNZ  runs_string_plain_control_error
	ADDQ $32, SI
	JMP  runs_string_scan_sse

runs_string_plain_done:
	BSFL R10, CX
runs_string_plain_at:
	ADDQ CX, SI
	MOVQ SI, AX
	SUBQ DX, AX
	INCQ SI
	XORQ R11, R11
	JMP  runs_string_plain_return

runs_string_first_escape:
	BSFL R9, CX
	ADDQ CX, SI
	MOVQ SI, DI
	JMP  runs_string_continue<>(SB)

runs_string_plain_control_error:
	BSFL R8, CX
	ADDQ CX, SI
	MOVQ $-const_SONIC_CONTROL_CHAR, AX
runs_string_plain_return:
	MOVQ R11, BX
	RET


// Input: SI source, DI next output byte, DX original content start, R12 *Parser.
TEXT runs_string_continue<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ $1, R11
	XORL R8, R8
	LEAQ cParityRunsEscapes<>(SB), R10
	MOVBLZX (SI), CX
	CMPB CL, $92
	JNE  runs_string_move

runs_string_escape:
	MOVBLZX 1(SI), CX
	CMPB CL, $117
	JE   runs_string_unicode
	LEAQ cParityRunsEscapes<>(SB), AX
	MOVBLZX (AX)(CX*1), CX
	TESTB CL, CL
	JZ   runs_string_escape_error
	ADDQ $2, SI
	MOVB CL, (DI)
	INCQ DI
runs_string_resume:
	MOVBLZX (SI), CX
	CMPB CL, $92
	JE   runs_string_escape
	TESTB $32, Parser_options(R12)
	JNZ  runs_string_move
	MOVBLZX 1(SI), AX
	CMPB AL, $92
	JNE  runs_string_move
	CMPB CL, $34
	JE   runs_string_move
	MOVB CL, (DI)
	INCQ DI
	INCQ SI
	JMP  runs_string_escape

runs_string_move:
	CMPB ·hasAVX2(SB), $0
	JE   runs_string_move_sse
runs_string_move_avx2:
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y13, Y0, Y4
	VPMOVMSKB Y4, R9
	VPCMPEQB Y14, Y0, Y4
	VPMOVMSKB Y4, R10
	TESTB $32, Parser_options(R12)
	JZ   runs_string_move_events
	VPMAXUB Y11, Y0, Y4
	VPCMPEQB Y11, Y4, Y4
	VPMOVMSKB Y4, R8

runs_string_move_events:
	MOVL R9, AX
	ORL  R8, AX
	DECL AX
	TESTL R10, AX
	JNZ  runs_string_copy_quote
	MOVL R10, AX
	DECL AX
	TESTL R8, AX
	JNZ  runs_string_control_error
	TESTL R9, AX
	JNZ  runs_string_copy_slash
	VMOVDQU Y0, 0(DI)
	ADDQ $32, SI
	ADDQ $32, DI
	JMP  runs_string_move_avx2

runs_string_move_sse:
	MOVOU 0(SI), X0
	MOVO X0, X4; PCMPEQB X13, X4; PMOVMSKB X4, R9
	MOVO X0, X4; PCMPEQB X14, X4; PMOVMSKB X4, R10
	TESTB $32, Parser_options(R12)
	JZ   runs_string_move_sse_events
	MOVO X0, X4; PMAXUB X11, X4; PCMPEQB X11, X4; PMOVMSKB X4, R8
runs_string_move_sse_events:
	MOVL R9, AX
	ORL  R8, AX
	DECL AX
	TESTL R10, AX
	JNZ  runs_string_copy_quote
	MOVL R10, AX
	DECL AX
	TESTL R8, AX
	JNZ  runs_string_control_error
	TESTL R9, AX
	JNZ  runs_string_copy_slash
	MOVOU X0, 0(DI)
	ADDQ $16, SI
	ADDQ $16, DI
	JMP  runs_string_move_sse

runs_string_copy_quote:
	BSFL R10, CX
	ADDQ CX, SI
	INCQ SI
	TESTL CX, CX
	JZ   runs_string_copy_done
	TESTL $16, CX
	JZ   runs_quote_lt16
	VMOVDQU X0, (DI)
	ADDQ $16, DI
	VEXTRACTI128 $1, Y0, X1
	VMOVQ X1, AX
	VPEXTRQ $1, X1, BX
	JMP  runs_quote_8
runs_quote_lt16:
	CMPB ·hasAVX2(SB), $0
	JE   runs_quote_lt16_sse
	VMOVQ X0, AX
	VPEXTRQ $1, X0, BX
	JMP  runs_quote_8
runs_quote_lt16_sse:
	MOVQ X0, AX
	PSRLDQ $8, X0
	MOVQ X0, BX
runs_quote_8:
	TESTL $8, CX
	JZ   runs_quote_4
	MOVQ AX, (DI)
	ADDQ $8, DI
	MOVQ BX, AX
runs_quote_4:
	TESTL $4, CX
	JZ   runs_quote_2
	MOVL AX, (DI)
	ADDQ $4, DI
	SHRQ $32, AX
runs_quote_2:
	TESTL $2, CX
	JZ   runs_quote_1
	MOVW AX, (DI)
	ADDQ $2, DI
	SHRQ $16, AX
runs_quote_1:
	TESTL $1, CX
	JZ   runs_string_copy_done
	MOVB AX, (DI)
	INCQ DI
runs_string_copy_done:
	MOVQ DI, AX
	SUBQ DX, AX
	JMP  runs_string_return

runs_string_copy_slash:
	BSFL R9, CX
	ADDQ CX, SI
	TESTL CX, CX
	JZ   runs_string_escape
	TESTL $16, CX
	JZ   runs_slash_lt16
	VMOVDQU X0, (DI)
	ADDQ $16, DI
	VEXTRACTI128 $1, Y0, X1
	VMOVQ X1, AX
	VPEXTRQ $1, X1, BX
	JMP  runs_slash_8
runs_slash_lt16:
	CMPB ·hasAVX2(SB), $0
	JE   runs_slash_lt16_sse
	VMOVQ X0, AX
	VPEXTRQ $1, X0, BX
	JMP  runs_slash_8
runs_slash_lt16_sse:
	MOVQ X0, AX
	PSRLDQ $8, X0
	MOVQ X0, BX
runs_slash_8:
	TESTL $8, CX
	JZ   runs_slash_4
	MOVQ AX, (DI)
	ADDQ $8, DI
	MOVQ BX, AX
runs_slash_4:
	TESTL $4, CX
	JZ   runs_slash_2
	MOVL AX, (DI)
	ADDQ $4, DI
	SHRQ $32, AX
runs_slash_2:
	TESTL $2, CX
	JZ   runs_slash_1
	MOVW AX, (DI)
	ADDQ $2, DI
	SHRQ $16, AX
runs_slash_1:
	TESTL $1, CX
	JZ   runs_string_escape
	MOVB AX, (DI)
	INCQ DI
	JMP  runs_string_escape

runs_string_unicode:
	MOVQ DI, DX
	MOVQ $-const_cParityRunsStringYield, AX
	JMP  runs_string_return

runs_string_control_error:
	BSFL R8, CX
	ADDQ CX, SI
	MOVQ $-1, AX
	JMP  runs_string_return
runs_string_escape_error:
	MOVQ $-2, AX
runs_string_return:
	MOVQ R11, BX
	RET


// Input: SI is cursor, R12 is *Parser.
// Output: AX is consumed first nonspace byte, SI is updated cursor.
TEXT runs_space<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVBLZX (SI), AX
	INCQ SI
	CMPB AL, $32
	JA   runs_space_return
	JE   runs_space_second
	CMPB AL, $9
	JE   runs_space_second
	CMPB AL, $10
	JE   runs_space_second
	CMPB AL, $13
	JNE  runs_space_return
runs_space_second:
	MOVBLZX (SI), AX
	INCQ SI
	CMPB AL, $32
	JA   runs_space_return
	JE   runs_space_cached
	CMPB AL, $9
	JE   runs_space_cached
	CMPB AL, $10
	JE   runs_space_cached
	CMPB AL, $13
	JNE  runs_space_return
runs_space_cached:
	MOVQ NBK_OFF(R12), BX
	ADDQ 56(SP), BX
	SUBQ $64, BX
	MOVQ SI, CX
	SUBQ BX, CX
	CMPQ CX, $64
	JAE  runs_space_scan
	MOVQ NBK_BITS(R12), DX
	MOVQ $-1, DI
	SHLQ CL, DI
	ANDQ DI, DX
	JNZ  runs_space_cache_hit
	LEAQ 64(BX), SI
runs_space_scan:
	CMPB ·hasAVX2(SB), $0
	JE   runs_space_scan_sse
runs_space_scan_avx2:
	VMOVDQU 0(SI), Y0
	VMOVDQU 32(SI), Y1

	VPSHUFB Y0, Y12, Y2
	VPCMPEQB Y0, Y2, Y2
	VPMOVMSKB Y2, DX

	VPSHUFB Y1, Y12, Y3
	VPCMPEQB Y1, Y3, Y3
	VPMOVMSKB Y3, CX

	SHLQ $32, CX
	ORQ  CX, DX
	NOTQ DX
	TESTQ DX, DX
	JNZ  runs_space_refilled
	ADDQ $64, SI
	JMP  runs_space_scan_avx2

runs_space_scan_sse:
	MOVOU 0(SI), X0
	MOVO X12, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, DX
	MOVOU 16(SI), X0
	MOVO X12, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, CX
	SHLQ $16, CX; ORQ CX, DX
	MOVOU 32(SI), X0
	MOVO X12, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, CX
	SHLQ $32, CX; ORQ CX, DX
	MOVOU 48(SI), X0
	MOVO X12, X2; PSHUFB X0, X2; PCMPEQB X0, X2; PMOVMSKB X2, CX
	SHLQ $48, CX; ORQ CX, DX
	NOTQ DX
	TESTQ DX, DX
	JNZ  runs_space_refilled
	ADDQ $64, SI
	JMP  runs_space_scan_sse

runs_space_refilled:
	MOVQ SI, BX
	SUBQ 56(SP), BX
	ADDQ $64, BX
	MOVQ BX, NBK_OFF(R12)
	MOVQ DX, NBK_BITS(R12)
	MOVQ SI, BX
runs_space_cache_hit:
	BSFQ DX, CX
	LEAQ (BX)(CX*1), SI
	MOVBLZX (SI), AX
	INCQ SI
runs_space_return:
	RET

DATA cParityRunsSpaceTables<>+0(SB)/8, $0x0000000000000020
DATA cParityRunsSpaceTables<>+8(SB)/8, $0x00000d00000a0900
DATA cParityRunsSpaceTables<>+16(SB)/8, $0x0000000000000020
DATA cParityRunsSpaceTables<>+24(SB)/8, $0x00000d00000a0900
GLOBL cParityRunsSpaceTables<>(SB), RODATA|NOPTR, $32

DATA cParityRunsCtrlVec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA cParityRunsCtrlVec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA cParityRunsCtrlVec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA cParityRunsCtrlVec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL cParityRunsCtrlVec<>(SB), RODATA|NOPTR, $32

DATA cParityRunsSlashVec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA cParityRunsSlashVec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA cParityRunsSlashVec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA cParityRunsSlashVec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL cParityRunsSlashVec<>(SB), RODATA|NOPTR, $32

DATA cParityRunsQuoteVec<>+0(SB)/8, $0x2222222222222222
DATA cParityRunsQuoteVec<>+8(SB)/8, $0x2222222222222222
DATA cParityRunsQuoteVec<>+16(SB)/8, $0x2222222222222222
DATA cParityRunsQuoteVec<>+24(SB)/8, $0x2222222222222222
GLOBL cParityRunsQuoteVec<>(SB), RODATA|NOPTR, $32

DATA cParityRunsEscapes<>+34(SB)/1, $34
DATA cParityRunsEscapes<>+47(SB)/1, $47
DATA cParityRunsEscapes<>+92(SB)/1, $92
DATA cParityRunsEscapes<>+98(SB)/1, $8
DATA cParityRunsEscapes<>+102(SB)/1, $12
DATA cParityRunsEscapes<>+110(SB)/1, $10
DATA cParityRunsEscapes<>+114(SB)/1, $13
DATA cParityRunsEscapes<>+116(SB)/1, $9
GLOBL cParityRunsEscapes<>(SB), RODATA|NOPTR, $256
