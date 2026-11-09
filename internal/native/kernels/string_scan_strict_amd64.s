//go:build amd64 && go1.17 && !go1.28

#include "textflag.h"

DATA ·ss_quote_vec<>+0(SB)/8, $0x2222222222222222
DATA ·ss_quote_vec<>+8(SB)/8, $0x2222222222222222
DATA ·ss_quote_vec<>+16(SB)/8, $0x2222222222222222
DATA ·ss_quote_vec<>+24(SB)/8, $0x2222222222222222
GLOBL ·ss_quote_vec<>(SB), RODATA|NOPTR, $32

DATA ·ss_slash_vec<>+0(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·ss_slash_vec<>+8(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·ss_slash_vec<>+16(SB)/8, $0x5c5c5c5c5c5c5c5c
DATA ·ss_slash_vec<>+24(SB)/8, $0x5c5c5c5c5c5c5c5c
GLOBL ·ss_slash_vec<>(SB), RODATA|NOPTR, $32

DATA ·ss_ctrl_vec<>+0(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·ss_ctrl_vec<>+8(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·ss_ctrl_vec<>+16(SB)/8, $0x1f1f1f1f1f1f1f1f
DATA ·ss_ctrl_vec<>+24(SB)/8, $0x1f1f1f1f1f1f1f1f
GLOBL ·ss_ctrl_vec<>(SB), RODATA|NOPTR, $32

// func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)
TEXT ·scanStringStrict(SB), NOSPLIT, $0-48
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	XORQ R15, R15         // block offset
	MOVQ $-1, R12         // first escape (-1 if none)
	TESTQ DX, DX
	JZ   ss_eof
	CMPQ DX, $16
	JB   ss_tail
	CMPQ DX, $32
	JB   ss_init_16
	CMPB ·hasAVX2(SB), $0
	JE   ss_init_16

	VMOVDQU ·ss_quote_vec<>(SB), Y8
	VMOVDQU ·ss_slash_vec<>(SB), Y9
	VMOVDQU ·ss_ctrl_vec<>(SB), Y10

ss_scan:
	CMPQ DX, $32
	JB   ss_scan_16

	MOVQ $32, R9
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y8, Y0, Y2
	VPCMPEQB Y9, Y0, Y3
	VPOR Y3, Y2, Y2
	VPMAXUB Y10, Y0, Y3
	VPCMPEQB Y10, Y3, Y3
	VPOR Y2, Y3, Y3
	VPMOVMSKB Y3, AX
	TESTL AX, AX
	JNZ  ss_event

	ADDQ $32, SI
	ADDQ $32, R15
	SUBQ $32, DX
	JZ   ss_eof
	CMPQ DX, $16
	JAE  ss_scan
	JMP  ss_tail

ss_init_16:
	MOVOU ·ss_quote_vec<>(SB), X8
	MOVOU ·ss_slash_vec<>(SB), X9
	MOVOU ·ss_ctrl_vec<>(SB), X10

ss_scan_16:
	MOVQ $16, R9
	MOVOU 0(SI), X0
	MOVO X0, X2; PCMPEQB X8, X2
	MOVO X0, X3; PCMPEQB X9, X3
	POR X3, X2
	MOVO X0, X3; PMAXUB X10, X3
	PCMPEQB X10, X3
	POR X2, X3
	PMOVMSKB X3, AX
	TESTL AX, AX
	JZ   ss_advance

ss_event:
	BSFQ AX, BX           // BX = event index in current block
	MOVBLZX (SI)(BX*1), CX
	CMPB CL, $'"'
	JE   ss_found
	CMPB CL, $'\\'
	JNE  ss_invalid

	CMPQ R12, $0
	JGE  ss_escape_seen
	LEAQ (R15)(BX*1), R12
ss_escape_seen:
	MOVQ DX, R13
	SUBQ BX, R13
	CMPQ R13, $2
	JB   ss_eof
	MOVBLZX 1(SI)(BX*1), CX
	MOVQ $2, R11
	CMPB CL, $'"'
	JE   ss_consume
	CMPB CL, $'\\'
	JE   ss_consume
	CMPB CL, $'/'
	JE   ss_consume
	CMPB CL, $'u'
	JE   ss_unicode

	SUBQ $98, CX
	CMPQ CX, $18
	JA   ss_invalid
	MOVQ $0x51011, DI
	SHRQ CL, DI
	TESTQ $1, DI
	JZ   ss_invalid
	JMP  ss_consume

ss_unicode:
	CMPQ R13, $6
	JB   ss_eof
	MOVQ $6, R11
	LEAQ 2(SI)(BX*1), DI
	MOVQ $4, R13
ss_hex:
	MOVBLZX (DI), CX
	MOVQ CX, R10
	SUBQ $48, R10
	CMPQ R10, $9
	JBE  ss_hex_valid
	ORQ  $32, CX
	SUBQ $97, CX
	CMPQ CX, $5
	JA   ss_invalid
ss_hex_valid:
	INCQ DI
	DECQ R13
	JNZ  ss_hex

ss_consume:
	LEAQ (BX)(R11*1), DI
	CMPQ DI, R9
	JAE  ss_crossing_escape
	MOVQ $-1, R10
	MOVQ DI, CX
	SHLQ CL, R10
	ANDQ R10, AX
	JNZ  ss_event
	JMP  ss_advance

ss_crossing_escape:
	MOVQ DI, R9

ss_advance:
	ADDQ R9, SI
	ADDQ R9, R15
	SUBQ R9, DX
	JZ   ss_eof
	CMPQ DX, $32
	JB   ss_advance_16
	CMPB ·hasAVX2(SB), $0
	JNE  ss_scan
ss_advance_16:
	CMPQ DX, $16
	JAE  ss_scan_16

ss_tail:
	MOVQ DX, R9
	XORQ AX, AX
	XORQ BX, BX
	MOVQ $1, DI
ss_scalar_classify:
	MOVBLZX (SI)(BX*1), CX
	CMPB CL, $'"'
	JE   ss_raw_event
	CMPB CL, $'\\'
	JE   ss_raw_event
	CMPB CL, $0x20
	JAE  ss_scalar_next
ss_raw_event:
	ORQ  DI, AX
ss_scalar_next:
	INCQ BX
	SHLQ $1, DI
	CMPQ BX, R9
	JB   ss_scalar_classify
	TESTQ AX, AX
	JNZ  ss_event
	JMP  ss_advance

ss_found:
	CMPQ size+8(FP), $32
	JB   ss_found_ret
	CMPB ·hasAVX2(SB), $0
	JE   ss_found_ret
	VZEROUPPER
ss_found_ret:
	LEAQ 1(R15)(BX*1), AX
	MOVQ AX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ $-1, errorPos+32(FP)
	MOVQ $0, err+40(FP)
	RET

ss_invalid:
	LEAQ (R15)(BX*1), AX
	MOVQ $2, CX
	JMP  ss_failure

ss_eof:
	MOVQ size+8(FP), AX
	MOVQ $1, CX

ss_failure:
	CMPQ size+8(FP), $32
	JB   ss_failure_ret
	CMPB ·hasAVX2(SB), $0
	JE   ss_failure_ret
	VZEROUPPER
ss_failure_ret:
	MOVQ AX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ AX, errorPos+32(FP)
	MOVQ CX, err+40(FP)
	RET


// func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)
TEXT ·scanStringStrictBlock(SB), NOSPLIT, $0-64
	MOVQ src+0(FP), SI
	MOVQ size+8(FP), DX
	XORQ R15, R15         // block offset
	MOVQ $-1, R12         // first escape (-1 if none)
	TESTQ DX, DX
	JZ   ssb_eof
	CMPQ DX, $16
	JB   ssb_tail
	CMPQ DX, $32
	JB   ssb_init_16
	CMPB ·hasAVX2(SB), $0
	JE   ssb_init_16

	VMOVDQU ·ss_quote_vec<>(SB), Y8
	VMOVDQU ·ss_slash_vec<>(SB), Y9
	VMOVDQU ·ss_ctrl_vec<>(SB), Y10

ssb_scan:
	CMPQ DX, $32
	JB   ssb_scan_16

	MOVQ $32, R9
	VMOVDQU 0(SI), Y0
	VPCMPEQB Y8, Y0, Y2
	VPCMPEQB Y9, Y0, Y3
	VPOR Y3, Y2, Y2       // Y2 = quote_slash
	VPMAXUB Y10, Y0, Y3
	VPCMPEQB Y10, Y3, Y3
	VPOR Y2, Y3, Y3       // Y3 = events
	VPMOVMSKB Y3, AX
	TESTL AX, AX
	JNZ  ssb_pack_event32

	ADDQ $32, SI
	ADDQ $32, R15
	SUBQ $32, DX
	JZ   ssb_eof
	CMPQ DX, $16
	JAE  ssb_scan
	JMP  ssb_tail

ssb_pack_event32:
	VPMOVMSKB Y2, R8
	JMP  ssb_event

ssb_init_16:
	MOVOU ·ss_quote_vec<>(SB), X8
	MOVOU ·ss_slash_vec<>(SB), X9
	MOVOU ·ss_ctrl_vec<>(SB), X10

ssb_scan_16:
	MOVQ $16, R9
	MOVOU 0(SI), X0
	MOVO X0, X2; PCMPEQB X8, X2
	MOVO X0, X3; PCMPEQB X9, X3
	POR X3, X2
	MOVO X0, X3; PMAXUB X10, X3
	PCMPEQB X10, X3
	POR X2, X3
	PMOVMSKB X3, AX
	TESTL AX, AX
	JZ   ssb_advance
	PMOVMSKB X2, R8

ssb_event:
	BSFQ AX, BX           // BX = event index in current block
	MOVBLZX (SI)(BX*1), CX
	CMPB CL, $'"'
	JE   ssb_found
	CMPB CL, $'\\'
	JNE  ssb_invalid

	CMPQ R12, $0
	JGE  ssb_escape_seen
	LEAQ (R15)(BX*1), R12
ssb_escape_seen:
	MOVQ DX, R13
	SUBQ BX, R13
	CMPQ R13, $2
	JB   ssb_eof
	MOVBLZX 1(SI)(BX*1), CX
	MOVQ $2, R11
	CMPB CL, $'"'
	JE   ssb_consume
	CMPB CL, $'\\'
	JE   ssb_consume
	CMPB CL, $'/'
	JE   ssb_consume
	CMPB CL, $'u'
	JE   ssb_unicode

	SUBQ $98, CX
	CMPQ CX, $18
	JA   ssb_invalid
	MOVQ $0x51011, DI
	SHRQ CL, DI
	TESTQ $1, DI
	JZ   ssb_invalid
	JMP  ssb_consume

ssb_unicode:
	CMPQ R13, $6
	JB   ssb_eof
	MOVQ $6, R11
	LEAQ 2(SI)(BX*1), DI
	MOVQ $4, R13
ssb_hex:
	MOVBLZX (DI), CX
	MOVQ CX, R10
	SUBQ $48, R10
	CMPQ R10, $9
	JBE  ssb_hex_valid
	ORQ  $32, CX
	SUBQ $97, CX
	CMPQ CX, $5
	JA   ssb_invalid
ssb_hex_valid:
	INCQ DI
	DECQ R13
	JNZ  ssb_hex

ssb_consume:
	LEAQ (BX)(R11*1), DI
	CMPQ DI, R9
	JAE  ssb_crossing_escape
	MOVQ $-1, R10
	MOVQ DI, CX
	SHLQ CL, R10
	ANDQ R10, AX
	JNZ  ssb_event
	JMP  ssb_advance

ssb_crossing_escape:
	MOVQ DI, R9

ssb_advance:
	ADDQ R9, SI
	ADDQ R9, R15
	SUBQ R9, DX
	JZ   ssb_eof
	CMPQ DX, $32
	JB   ssb_advance_16
	CMPB ·hasAVX2(SB), $0
	JNE  ssb_scan
ssb_advance_16:
	CMPQ DX, $16
	JAE  ssb_scan_16

ssb_tail:
	MOVQ DX, R9
	XORQ AX, AX
	XORQ R8, R8
	XORQ BX, BX
	MOVQ $1, DI
ssb_scalar_classify:
	MOVBLZX (SI)(BX*1), CX
	CMPB CL, $'"'
	JE   ssb_raw_event
	CMPB CL, $'\\'
	JE   ssb_raw_event
	CMPB CL, $0x20
	JAE  ssb_scalar_next
	ORQ  DI, AX
	JMP  ssb_scalar_next
ssb_raw_event:
	ORQ  DI, R8
	ORQ  DI, AX
ssb_scalar_next:
	INCQ BX
	SHLQ $1, DI
	CMPQ BX, R9
	JB   ssb_scalar_classify
	TESTQ AX, AX
	JNZ  ssb_event
	JMP  ssb_advance

ssb_found:
	CMPQ size+8(FP), $32
	JB   ssb_found_ret
	CMPB ·hasAVX2(SB), $0
	JE   ssb_found_ret
	VZEROUPPER
ssb_found_ret:
	LEAQ 1(BX), CX
	SUBQ CX, R9
	SHRQ CL, R8
	MOVQ R8, tailEvents+48(FP)
	MOVQ R9, tailWidth+56(FP)
	LEAQ 1(R15)(BX*1), AX
	MOVQ AX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ $-1, errorPos+32(FP)
	MOVQ $0, err+40(FP)
	RET

ssb_invalid:
	LEAQ (R15)(BX*1), AX
	MOVQ $2, CX
	JMP  ssb_failure

ssb_eof:
	MOVQ size+8(FP), AX
	MOVQ $1, CX

ssb_failure:
	CMPQ size+8(FP), $32
	JB   ssb_failure_ret
	CMPB ·hasAVX2(SB), $0
	JE   ssb_failure_ret
	VZEROUPPER
ssb_failure_ret:
	MOVQ $0, tailEvents+48(FP)
	MOVQ $0, tailWidth+56(FP)
	MOVQ AX, end+16(FP)
	MOVQ R12, escape+24(FP)
	MOVQ AX, errorPos+32(FP)
	MOVQ CX, err+40(FP)
	RET
