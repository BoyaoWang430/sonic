/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package types

import "unsafe"

// LexMaxWindowBytes bounds a single window slice to <= 1 GiB for window-relative
// span helpers, while DefaultLexBatchCap bounds one 64-byte block-mask batch to
// 16 * 64B = 1 KiB (384 bytes of pointer-free LexBlockMask records).
const (
	LexMaxWindowBytes  = 1 << 30
	DefaultLexBatchCap = 16
)

// TokenKind classifies a pure lexical or punctuation JSON token start.
// TokenKind NEVER encodes container context (key vs value, depth, parent, or Schema).
type TokenKind uint8

const (
	TokInvalid TokenKind = iota
	TokLBrace            // '{'
	TokRBrace            // '}'
	TokLBrack            // '['
	TokRBrack            // ']'
	TokColon             // ':'
	TokComma             // ','
	TokString            // '"'
	TokNumber            // '-' or '0'..'9'
	TokTrue              // 't'
	TokFalse             // 'f'
	TokNull              // 'n'
)

// LexFlags controls optional lexical checks performed inside LexBatch.
// LexFlags MUST NEVER carry structural/consumer flags (depth, key/value, Schema, UseNumber).
type LexFlags uint64

const (
	LexFlagNone          LexFlags = 0
	LexFlagRejectControl LexFlags = 1 << 0 // flag raw bytes < 0x20 inside '"' strings in InsideSpecial
)

// LexBlockMask holds the fixed 24-byte pointer-free lexical fact bitmasks for one
// 64-byte JSON block (P2/P3 winning Stage-1 block transport).
//
//	+0:  EventMask     (uint64: openQuote | structOutside | scalarStarts)
//	+8:  CloseQuote    (uint64: unescaped closing '"' positions)
//	+16: InsideSpecial (uint64: '\\' | optional raw <0x20 inside strings)
type LexBlockMask struct {
	EventMask     uint64
	CloseQuote    uint64
	InsideSpecial uint64
}

// LexBlockCarry holds the fixed 24-byte pointer-free narrow cross-block lexical
// carry state. It NEVER carries container phase, nesting depth, or schema state:
//
//	+0:  OddSlash   (uint64: 0 or 1)
//	+8:  InString   (uint64: 0 or ^uint64(0))
//	+16: PrevScalar (uint64: 0 or 1)
type LexBlockCarry struct {
	OddSlash   uint64
	InString   uint64
	PrevScalar uint64
}

// LexBatchOut is the fixed 16-byte pointer-free summary populated by LexBatch kernels:
//
//	+0:  BlocksProduced (uint64)
//	+8:  BytesScanned   (uint64)
type LexBatchOut struct {
	BlocksProduced uint64
	BytesScanned   uint64
}

// LexProducer identifies the outer lexical block-mask implementation family
// dispatched at the Go/kernel boundary when witness instrumentation is compiled
// in (-tags=sonic_witness or -tags=sonic_diag; N3/N4).
//
// Important granularity boundary: native_* values identify the outer
// dispatched implementation family/profile rather than proving that every block
// executed a specific SIMD instruction (for example, amd64 assembly handles
// <32B windows via legacy SSE and <16B tails via assembly scalar). When witness
// instrumentation is disabled in normal release builds (!sonic_witness && !sonic_diag),
// LexProducerDisabled indicates NOT_COLLECTED (never "0 calls").
type LexProducer uint8

const (
	LexProducerNone           LexProducer = 0
	LexProducerGoScalarForced LexProducer = 1
	LexProducerGoScalarGlobal LexProducer = 2
	LexProducerGoScalarBuild  LexProducer = 3
	LexProducerGoScalarCompat LexProducer = 4
	LexProducerAsmAMD64AVX2   LexProducer = 5
	LexProducerAsmAMD64SSSE3  LexProducer = 6
	LexProducerAsmAMD64SSE2   LexProducer = 7
	LexProducerAsmARM64NEON   LexProducer = 8
	LexProducerAsmRISCV64Zbb  LexProducer = 9
	LexProducerAsmRISCV64RVV  LexProducer = 10
	lexProducerObservedMax    LexProducer = 11
	LexProducerDisabled       LexProducer = 12
)

func (p LexProducer) String() string {
	switch p {
	case LexProducerNone:
		return "none"
	case LexProducerDisabled:
		return "disabled"
	case LexProducerGoScalarForced:
		return "go_scalar_forced"
	case LexProducerGoScalarGlobal:
		return "go_scalar_global"
	case LexProducerGoScalarBuild:
		return "go_scalar_build"
	case LexProducerGoScalarCompat:
		return "go_scalar_compat"
	case LexProducerAsmAMD64AVX2:
		return "native_amd64_avx2_tier"
	case LexProducerAsmAMD64SSSE3:
		return "native_amd64_ssse3_tier"
	case LexProducerAsmAMD64SSE2:
		return "native_amd64_sse2_tier"
	case LexProducerAsmARM64NEON:
		return "native_arm64_neon_family"
	case LexProducerAsmRISCV64Zbb:
		return "native_riscv64_zbb_profile"
	case LexProducerAsmRISCV64RVV:
		return "native_riscv64_rvv_profile"
	default:
		return "unknown"
	}
}

// LexProducerMask is a compact 16-bit bitset of outer LexProducer families
// observed during one scan request when witness instrumentation is enabled.
type LexProducerMask uint16

func LexProducerBit(p LexProducer) LexProducerMask {
	if p == LexProducerNone || p >= lexProducerObservedMax {
		return 0
	}
	return LexProducerMask(1) << uint(p)
}

func (m LexProducerMask) Add(p LexProducer) LexProducerMask {
	return m | LexProducerBit(p)
}

func (m LexProducerMask) Has(p LexProducer) bool {
	return (m & LexProducerBit(p)) != 0
}

func (m LexProducerMask) Names() []string {
	var out []string
	for p := LexProducer(1); p < lexProducerObservedMax; p++ {
		if m.Has(p) {
			out = append(out, p.String())
		}
	}
	return out
}

// Compile-time ABI layout verification for LexBlockMask (24B), LexBlockCarry (24B), and LexBatchOut (16B).
const (
	LexBlockMaskSize      = 24
	LexBlockOffEvent      = 0
	LexBlockOffCloseQuote = 8
	LexBlockOffInsideSpec = 16

	LexCarrySize          = 24
	LexCarryOffOddSlash   = 0
	LexCarryOffInString   = 8
	LexCarryOffPrevScalar = 16

	LexOutSize      = 16
	LexOutOffBlocks = 0
	LexOutOffBytes  = 8
)

var (
	_ [0]struct{} = [unsafe.Sizeof(LexBlockMask{}) - LexBlockMaskSize]struct{}{}
	_ [0]struct{} = [LexBlockMaskSize - unsafe.Sizeof(LexBlockMask{})]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBlockMask{}.EventMask) - LexBlockOffEvent]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBlockMask{}.CloseQuote) - LexBlockOffCloseQuote]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBlockMask{}.InsideSpecial) - LexBlockOffInsideSpec]struct{}{}

	_ [0]struct{} = [unsafe.Sizeof(LexBlockCarry{}) - LexCarrySize]struct{}{}
	_ [0]struct{} = [LexCarrySize - unsafe.Sizeof(LexBlockCarry{})]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBlockCarry{}.OddSlash) - LexCarryOffOddSlash]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBlockCarry{}.InString) - LexCarryOffInString]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBlockCarry{}.PrevScalar) - LexCarryOffPrevScalar]struct{}{}

	_ [0]struct{} = [unsafe.Sizeof(LexBatchOut{}) - LexOutSize]struct{}{}
	_ [0]struct{} = [LexOutSize - unsafe.Sizeof(LexBatchOut{})]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBatchOut{}.BlocksProduced) - LexOutOffBlocks]struct{}{}
	_ [0]struct{} = [unsafe.Offsetof(LexBatchOut{}.BytesScanned) - LexOutOffBytes]struct{}{}
)

// LexBatchScalar is the bounded, zero-allocation pure-Go reference implementation
// of the LexBatch 64-byte block-mask protocol. It reads strictly within
// src[pos:len(src)] and never inspects or encodes JSON container grammar state.
func LexBatchScalar(src string, pos int, dst []LexBlockMask, flags LexFlags, carry *LexBlockCarry) LexBatchOut {
	size := len(src)
	if pos < 0 || pos > size {
		panic("sonic: invalid LexBatch bounds")
	}
	if pos == size || len(dst) == 0 {
		return LexBatchOut{}
	}
	var out LexBatchOut
	LexBatchScalarRaw(unsafe.StringData(src), size, pos, unsafe.SliceData(dst), len(dst), uint64(flags), carry, &out)
	return out
}

// LexBatchScalarRaw implements the exact raw ABI shared by all ISA lexBatch kernels.
func LexBatchScalarRaw(src *byte, size int, pos int, blocks *LexBlockMask, capBlocks int, flags uint64, carry *LexBlockCarry, out *LexBatchOut) {
	if pos >= size || capBlocks <= 0 {
		*out = LexBatchOut{}
		return
	}
	in := unsafe.Slice(src, size)
	dst := unsafe.Slice(blocks, capBlocks)
	rejectCtrl := (flags & uint64(LexFlagRejectControl)) != 0

	oddSlash := carry.OddSlash & 1
	inString := carry.InString
	prevScalar := carry.PrevScalar & 1

	cur := pos
	nBlk := 0
	for nBlk < capBlocks && cur < size {
		width := size - cur
		if width > 64 {
			width = 64
		}
		var quote, slash, ctrl, space, struc uint64
		chunk := in[cur : cur+width]
		for k := 0; k < width; k++ {
			b := chunk[k]
			bit := uint64(1) << uint(k)
			if rejectCtrl && b < 0x20 {
				ctrl |= bit
			}
			switch b {
			case '"':
				quote |= bit
			case '\\':
				slash |= bit
			case ' ', '\t', '\n', '\r':
				space |= bit
			case '{', '}', '[', ']', ':', ',':
				struc |= bit
			}
		}

		var escaped uint64
		if (slash | oddSlash) != 0 {
			s := slash &^ oddSlash
			follows := (s << 1) | oddSlash
			oddStarts := s &^ follows
			evenStarts := oddStarts & 0xaaaaaaaaaaaaaaaa
			sum := evenStarts + s
			sumShifted := (sum << 1) ^ 0x5555555555555555
			escaped = sumShifted & follows
			unescSlashes := slash &^ escaped
			oddSlash = (unescSlashes >> uint(width-1)) & 1
		}

		unescQuotes := quote &^ escaped
		rawPrefix := unescQuotes
		rawPrefix ^= rawPrefix << 1
		rawPrefix ^= rawPrefix << 2
		rawPrefix ^= rawPrefix << 4
		rawPrefix ^= rawPrefix << 8
		rawPrefix ^= rawPrefix << 16
		rawPrefix ^= rawPrefix << 32
		stringMask := rawPrefix ^ inString
		if width == 64 {
			inString = uint64(int64(stringMask) >> 63)
		} else {
			if ((stringMask >> uint(width-1)) & 1) != 0 {
				inString = ^uint64(0)
			} else {
				inString = 0
			}
		}

		var validMask uint64 = ^uint64(0)
		if width < 64 {
			validMask = (uint64(1) << uint(width)) - 1
		}
		stringMask &= validMask
		unescQuotes &= validMask

		openQuotes := unescQuotes & stringMask
		closeQuotes := unescQuotes &^ stringMask
		insideSpecial := (slash | ctrl) & stringMask

		outsideString := ^(stringMask | unescQuotes) & validMask
		structOutside := struc & outsideString
		scalarBytes := outsideString &^ (space | struc)
		scalarStarts := scalarBytes &^ ((scalarBytes << 1) | prevScalar)
		prevScalar = (scalarBytes >> uint(width-1)) & 1

		dst[nBlk] = LexBlockMask{
			EventMask:     openQuotes | structOutside | scalarStarts,
			CloseQuote:    closeQuotes,
			InsideSpecial: insideSpecial,
		}
		nBlk++
		cur += width
	}

	carry.OddSlash = oddSlash
	carry.InString = inString
	carry.PrevScalar = prevScalar
	*out = LexBatchOut{
		BlocksProduced: uint64(nBlk),
		BytesScanned:   uint64(cur - pos),
	}
}
