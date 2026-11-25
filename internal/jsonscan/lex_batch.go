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

package jsonscan

import (
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// Re-export the frozen P3 LexBatch block-mask protocol types in jsonscan so Go
// grammar consumers and contract tests work against a single authoritative definition.
type (
	TokenKind       = types.TokenKind
	LexFlags        = types.LexFlags
	LexBlockMask    = types.LexBlockMask
	LexBlockCarry   = types.LexBlockCarry
	LexBatchOut     = types.LexBatchOut
	LexProducer     = types.LexProducer
	LexProducerMask = types.LexProducerMask
)

const (
	LexMaxWindowBytes  = types.LexMaxWindowBytes
	DefaultLexBatchCap = types.DefaultLexBatchCap

	TokInvalid = types.TokInvalid
	TokLBrace  = types.TokLBrace
	TokRBrace  = types.TokRBrace
	TokLBrack  = types.TokLBrack
	TokRBrack  = types.TokRBrack
	TokColon   = types.TokColon
	TokComma   = types.TokComma
	TokString  = types.TokString
	TokNumber  = types.TokNumber
	TokTrue    = types.TokTrue
	TokFalse   = types.TokFalse
	TokNull    = types.TokNull

	LexFlagNone          = types.LexFlagNone
	LexFlagRejectControl = types.LexFlagRejectControl

	LexProducerNone           = types.LexProducerNone
	LexProducerDisabled       = types.LexProducerDisabled
	LexProducerGoScalarForced = types.LexProducerGoScalarForced
	LexProducerGoScalarGlobal = types.LexProducerGoScalarGlobal
	LexProducerGoScalarBuild  = types.LexProducerGoScalarBuild
	LexProducerGoScalarCompat = types.LexProducerGoScalarCompat
	LexProducerAsmAMD64AVX2   = types.LexProducerAsmAMD64AVX2
	LexProducerAsmAMD64SSSE3  = types.LexProducerAsmAMD64SSSE3
	LexProducerAsmAMD64SSE2   = types.LexProducerAsmAMD64SSE2
	LexProducerAsmARM64NEON   = types.LexProducerAsmARM64NEON
	LexProducerAsmRISCV64Zbb  = types.LexProducerAsmRISCV64Zbb
	LexProducerAsmRISCV64RVV  = types.LexProducerAsmRISCV64RVV
)

func LexProducerBit(p LexProducer) LexProducerMask {
	return types.LexProducerBit(p)
}

// ExpectedDispatchedLexFamily reports the expected outer lexical implementation
// family under witness builds, or LexProducerDisabled in release builds (!sonic_witness && !sonic_diag).
func ExpectedDispatchedLexFamily(forceScalar bool) LexProducer {
	return expectedDispatchedLexFamily(forceScalar)
}

func checkDisjointWindowBlocks(window string, dst []LexBlockMask) {
	if len(window) == 0 || len(dst) == 0 {
		return
	}
	s0 := uintptr(unsafe.Pointer(unsafe.StringData(window)))
	s1 := s0 + uintptr(len(window))
	d0 := uintptr(unsafe.Pointer(unsafe.SliceData(dst)))
	d1 := d0 + uintptr(len(dst))*types.LexBlockMaskSize
	if s0 < d1 && d0 < s1 {
		panic("sonic: LexBatch input window and output block buffer must not overlap")
	}
}

// CheckedWindowSpan converts a window-relative [start, end) span into absolute
// document coordinates [absStart, absEnd) with overflow checking.
func CheckedWindowSpan(winBase int, start, end uint32, srcLen int) (absStart, absEnd int, ok bool) {
	if winBase < 0 || start > end || winBase > srcLen {
		return 0, 0, false
	}
	rem := srcLen - winBase
	if uint64(end) > uint64(rem) {
		return 0, 0, false
	}
	return winBase + int(start), winBase + int(end), true
}

// LexBatchWindow classifies up to len(dst) 64-byte JSON blocks from src[pos:]
// clamped to LexMaxWindowBytes.
func LexBatchWindow(src string, pos int, dst []LexBlockMask, flags LexFlags, carry *LexBlockCarry) (winBase int, out LexBatchOut) {
	return LexBatchWindowMode(src, pos, dst, flags, carry, false)
}

// LexBatchWindowMode is like LexBatchWindow with an explicit forceScalar flag.
func LexBatchWindowMode(src string, pos int, dst []LexBlockMask, flags LexFlags, carry *LexBlockCarry, forceScalar bool) (winBase int, out LexBatchOut) {
	var b blockBatchState
	return b.fillLexBatchWindowRole(src, pos, dst, flags, carry, forceScalar, DiagRoleMain)
}

func (b *blockBatchState) fillLexBatchWindowRole(src string, pos int, dst []LexBlockMask, flags LexFlags, carry *LexBlockCarry, forceScalar bool, role uint8) (winBase int, out LexBatchOut) {
	if pos < 0 || pos > len(src) {
		panic("sonic: invalid LexBatchWindow offset")
	}
	winEnd := len(src)
	if winEnd-pos > LexMaxWindowBytes {
		winEnd = pos + LexMaxWindowBytes
	}
	window := src[pos:winEnd]
	checkDisjointWindowBlocks(window, dst)
	out = b.fillLexBatch(window, 0, dst, flags, carry, forceScalar)
	if DiagEnabled {
		var evCount uint64
		n := int(out.BlocksProduced)
		for k := 0; k < n; k++ {
			evCount += uint64(bits.OnesCount64(dst[k].EventMask))
		}
		diagRecordLexBlocks(out.BlocksProduced, out.BytesScanned, evCount, role)
	}
	return pos, out
}
