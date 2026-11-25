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

	"github.com/bytedance/sonic/internal/native/types"
)

// canEnterDefaultValidateOnly gates the zero-output leaf-loop fast path strictly
// to complete-input, root ModeValidateOnly scans with default (non-strict-string,
// zero-base-depth) profile and no schema, sub-mode, or event sink.
// ConfigStd.Valid, Compact, Schema, AST event consumers, strict ValidateOne,
// ValidateOneAtDepth(baseDepth>0), and streaming scans never enter this path.
func (m *Machine) canEnterDefaultValidateOnly(src string, final bool) bool {
	g := &m.Grammar
	b := &m.batch
	if !final || g.RootMode != ModeValidateOnly || m.Schema.HasPlan || g.SubModeActive || m.sink != nil {
		return false
	}
	p := &g.Profile
	if p.BaseDepth != 0 || p.RejectControl || p.ValidateEscapes || p.ValidateUTF8 || p.RejectLoneSurrogate || p.EscapeHTMLObserve {
		return false
	}
	if len(src) > LexMaxWindowBytes || b.capOverride != 0 || b.active {
		return false
	}
	if uint(g.Cur) > uint(len(src)) {
		return false
	}
	if g.Depth > 0 && g.ValueStart < 0 {
		return false
	}
	return g.Phase <= PhaseArrayAfterVal || g.Phase == PhaseTrailingSpace
}

// stepDefaultValidateOnly consumes 64-byte LexBlockMask batches over the single
// canonical structural transition table (grammar.go) without tape/unesc writes
// or per-token multi-mode checks. Every structural classification and delimiter
// transition (including in-block key -> colon -> value -> separator chaining)
// either queries lookupStructByteTransition directly or matches single-byte
// witnesses mechanically derived from structByteTransitionTable at init time,
// so no structural grammar rules are duplicated here. Any unsupported token
// boundary, depth overflow, NUL byte, or syntax error hands off cleanly at
// cur = tokStart to stepGeneral.
func (m *Machine) stepDefaultValidateOnly(src string) ScanResult {
	g := &m.Grammar
	b := &m.batch
	srcLen := len(src)

	phase := g.Phase
	cur := g.Cur
	valueStart := g.ValueStart
	depth := g.Depth
	maxDepthLim := g.Profile.MaxContainerDepth
	fullDoc := g.Profile.FullDocument
	forceScalar := b.forceScalar

	// Mechanically derived single-byte structural witnesses from grammar.go.
	// If grammar.go ever maps a phase action to 0 or >1 bytes, the witness is -1
	// and every fast check falls back automatically to lookupStructByteTransition.
	colonByte := uniqueObjColonByte
	objKeyOpenByte := uniqueObjKeyOpenByte
	objKeyNextByte := uniqueObjKeyNextByte
	objCommaByte := uniqueObjCommaByte
	objCloseByte := uniqueObjCloseByte
	arrCommaByte := uniqueArrCommaByte
	arrCloseByte := uniqueArrCloseByte

	bitWord0 := g.BitStack[0]
	var topArr bool
	if depth > 0 {
		pBitIdx := uint(depth - 1)
		if pBitIdx < 64 {
			topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
		} else {
			topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
		}
	}

	statMaxDepth := m.Stat.MaxDepth

	var carry types.LexBlockCarry
	startCur := cur
	var diagValOneLexBytes uint64
	batchCap := len(b.blocks)
	if !fullDoc {
		batchCap = 1
	}
	batchStart := cur
	batchEnd := cur
	nBlocks := 0
	blkIdx := 0
	var blockBase int
	var blockEnd int
	var blk string
	var ev uint64
	var cqMask uint64

	var coldTokStart int
	var coldStrAct structAction

	for {
		if ev == 0 {
			blkIdx++
			for blkIdx < nBlocks {
				ev = b.blocks[blkIdx].EventMask
				if ev != 0 {
					blockBase = batchStart + blkIdx*64
					blockEnd = blockBase + 64
					if blockEnd > batchEnd {
						blockEnd = batchEnd
					}
					blk = src[blockBase:blockEnd]
					cqMask = b.blocks[blkIdx].CloseQuote
					goto consumeEvents
				}
				blkIdx++
			}
			if batchEnd >= srcLen {
				cur = srcLen
				if phase == PhaseTrailingSpace {
					phase = PhaseDone
					goto finishDone
				}
				goto failEOFExit
			}
			batchStart = batchEnd
			out := b.fillLexBatch(src, batchStart, b.blocks[:batchCap], LexFlagNone, &carry, forceScalar)
			if batchCap < len(b.blocks) {
				batchCap <<= 1
				if batchCap > len(b.blocks) {
					batchCap = len(b.blocks)
				}
			}
			b.calls++
			nBlocks = int(out.BlocksProduced)
			batchEnd = batchStart + int(out.BytesScanned)
			if DiagEnabled {
				if !fullDoc {
					diagValOneLexBytes += uint64(out.BytesScanned)
				}
				var evCount uint64
				for k := 0; k < nBlocks; k++ {
					evCount += uint64(bits.OnesCount64(b.blocks[k].EventMask))
				}
				diagRecordLexBlocks(out.BlocksProduced, out.BytesScanned, evCount, b.diagRole)
			}

			blkIdx = 0
			for blkIdx < nBlocks {
				ev = b.blocks[blkIdx].EventMask
				if ev != 0 {
					blockBase = batchStart + blkIdx*64
					blockEnd = blockBase + 64
					if blockEnd > batchEnd {
						blockEnd = batchEnd
					}
					blk = src[blockBase:blockEnd]
					cqMask = b.blocks[blkIdx].CloseQuote
					goto consumeEvents
				}
				blkIdx++
			}
			continue
		}

	consumeEvents:
		for ev != 0 {
			bit := uint(bits.TrailingZeros64(ev))
			ev &= ev - 1
			ch := blk[bit]

			switch lookupStructByteTransition(phase, ch) {
			case actObjKey:
				goto objKeyInBlock

			case actObjColon:
				phase = PhaseValue
				if ev != 0 {
					goto objValueInBlock
				}
				continue

			case actObjComma:
				goto objAfterCommaInBlock

			case actArrComma:
				phase = PhaseValue
				continue

			case actValString:
				cq := cqMask >> bit
				if cq == 0 {
					tokStart := blockBase + int(bit)
					if depth == 0 && valueStart < 0 {
						valueStart = tokStart
					}
					coldTokStart = tokStart
					coldStrAct = actValString
					goto handleCrossBlockString
				}
				if depth == 0 {
					tokStart := blockBase + int(bit)
					if valueStart < 0 {
						valueStart = tokStart
					}
					cur = tokStart + bits.TrailingZeros64(cq) + 1
					if fullDoc {
						phase = PhaseTrailingSpace
						continue
					}
					phase = PhaseDone
					goto finishDone
				}
				if topArr {
					goto arrAfterValInBlock
				}
				goto objAfterValInBlock

			case actValNumber:
				goto handleNumberInBlock

			case actValTrue:
				goto handleTrueInBlock

			case actValFalse:
				goto handleFalseInBlock

			case actValNull:
				goto handleNullInBlock

			case actValObjOpen:
				goto handleObjOpenInBlock

			case actValArrOpen:
				goto handleArrOpenInBlock

			case actCloseObj, actCloseArr:
				goto closeContainerInBlock

			default: // actError (including ASTCompatLex NUL byte)
				cur = blockBase + int(bit)
				goto handoffGeneral
			}

		handleNumberInBlock:
			{
				tokStart := blockBase + int(bit)
				if depth == 0 && valueStart < 0 {
					valueStart = tokStart
				}
				q, ok := scanValidNumberFastInt(src, tokStart)
				if !ok {
					q, ok = scanValidNumberSpanSlow(src, tokStart, true)
					if !ok {
						cur = tokStart
						goto handoffGeneral
					}
				}
				if q >= blockEnd {
					cur = q
					goto advancePastMultiBlockScalar
				}
				if depth == 0 {
					cur = q
					if isScalarContByte[src[q]] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					if fullDoc {
						phase = PhaseTrailingSpace
						continue
					}
					phase = PhaseDone
					goto finishDone
				}
				nc := src[q]
				if topArr {
					if int(nc) == arrCommaByte {
						ev &= ev - 1
						phase = PhaseValue
						if ev != 0 {
							nBit := uint(bits.TrailingZeros64(ev))
							nCh := blk[nBit]
							if lookupStructByteTransition(PhaseValue, nCh) == actValNumber {
								ev &= ev - 1
								bit = nBit
								ch = nCh
								goto handleNumberInBlock
							}
						}
						continue
					}
					if int(nc) == arrCloseByte {
						ev &= ev - 1
						bit = uint(q - blockBase)
						goto closeContainerInBlock
					}
					if isScalarContByte[nc] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					goto arrAfterValInBlock
				}
				if int(nc) == objCommaByte {
					ev &= ev - 1
					goto objAfterCommaInBlock
				}
				if int(nc) == objCloseByte {
					ev &= ev - 1
					bit = uint(q - blockBase)
					goto closeContainerInBlock
				}
				if isScalarContByte[nc] {
					ev |= uint64(1) << uint(q-blockBase)
				}
				goto objAfterValInBlock
			}

		handleTrueInBlock:
			{
				tokStart := blockBase + int(bit)
				if depth == 0 && valueStart < 0 {
					valueStart = tokStart
				}
				if tokStart+4 > srcLen || src[tokStart:tokStart+4] != "true" {
					cur = tokStart
					goto handoffGeneral
				}
				q := tokStart + 4
				if q >= blockEnd {
					cur = q
					goto advancePastMultiBlockScalar
				}
				if depth == 0 {
					cur = q
					if isScalarContByte[src[q]] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					if fullDoc {
						phase = PhaseTrailingSpace
						continue
					}
					phase = PhaseDone
					goto finishDone
				}
				nc := src[q]
				if topArr {
					if int(nc) == arrCommaByte {
						ev &= ev - 1
						phase = PhaseValue
						continue
					}
					if int(nc) == arrCloseByte {
						ev &= ev - 1
						bit = uint(q - blockBase)
						goto closeContainerInBlock
					}
					if isScalarContByte[nc] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					goto arrAfterValInBlock
				}
				if int(nc) == objCommaByte {
					ev &= ev - 1
					goto objAfterCommaInBlock
				}
				if int(nc) == objCloseByte {
					ev &= ev - 1
					bit = uint(q - blockBase)
					goto closeContainerInBlock
				}
				if isScalarContByte[nc] {
					ev |= uint64(1) << uint(q-blockBase)
				}
				goto objAfterValInBlock
			}

		handleFalseInBlock:
			{
				tokStart := blockBase + int(bit)
				if depth == 0 && valueStart < 0 {
					valueStart = tokStart
				}
				if tokStart+5 > srcLen || src[tokStart+1:tokStart+5] != "alse" {
					cur = tokStart
					goto handoffGeneral
				}
				q := tokStart + 5
				if q >= blockEnd {
					cur = q
					goto advancePastMultiBlockScalar
				}
				if depth == 0 {
					cur = q
					if isScalarContByte[src[q]] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					if fullDoc {
						phase = PhaseTrailingSpace
						continue
					}
					phase = PhaseDone
					goto finishDone
				}
				nc := src[q]
				if topArr {
					if int(nc) == arrCommaByte {
						ev &= ev - 1
						phase = PhaseValue
						continue
					}
					if int(nc) == arrCloseByte {
						ev &= ev - 1
						bit = uint(q - blockBase)
						goto closeContainerInBlock
					}
					if isScalarContByte[nc] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					goto arrAfterValInBlock
				}
				if int(nc) == objCommaByte {
					ev &= ev - 1
					goto objAfterCommaInBlock
				}
				if int(nc) == objCloseByte {
					ev &= ev - 1
					bit = uint(q - blockBase)
					goto closeContainerInBlock
				}
				if isScalarContByte[nc] {
					ev |= uint64(1) << uint(q-blockBase)
				}
				goto objAfterValInBlock
			}

		handleNullInBlock:
			{
				tokStart := blockBase + int(bit)
				if depth == 0 && valueStart < 0 {
					valueStart = tokStart
				}
				if tokStart+4 > srcLen || src[tokStart:tokStart+4] != "null" {
					cur = tokStart
					goto handoffGeneral
				}
				q := tokStart + 4
				if q >= blockEnd {
					cur = q
					goto advancePastMultiBlockScalar
				}
				if depth == 0 {
					cur = q
					if isScalarContByte[src[q]] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					if fullDoc {
						phase = PhaseTrailingSpace
						continue
					}
					phase = PhaseDone
					goto finishDone
				}
				nc := src[q]
				if topArr {
					if int(nc) == arrCommaByte {
						ev &= ev - 1
						phase = PhaseValue
						continue
					}
					if int(nc) == arrCloseByte {
						ev &= ev - 1
						bit = uint(q - blockBase)
						goto closeContainerInBlock
					}
					if isScalarContByte[nc] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					goto arrAfterValInBlock
				}
				if int(nc) == objCommaByte {
					ev &= ev - 1
					goto objAfterCommaInBlock
				}
				if int(nc) == objCloseByte {
					ev &= ev - 1
					bit = uint(q - blockBase)
					goto closeContainerInBlock
				}
				if isScalarContByte[nc] {
					ev |= uint64(1) << uint(q-blockBase)
				}
				goto objAfterValInBlock
			}

		handleObjOpenInBlock:
			{
				if depth == 0 && valueStart < 0 {
					valueStart = blockBase + int(bit)
				}
				nextDepth := depth + 1
				if nextDepth > maxDepthLim {
					cur = blockBase + int(bit)
					goto handoffGeneral
				}
				bitIdx := uint(depth)
				if bitIdx < 64 {
					bitWord0 &^= uint64(1) << bitIdx
				} else {
					g.BitStack[bitIdx>>6] &^= uint64(1) << (bitIdx & 63)
				}
				depth = nextDepth
				if uint32(depth) > statMaxDepth {
					statMaxDepth = uint32(depth)
				}
				topArr = false
				phase = PhaseObjectOpen
				if ev != 0 {
					kBit := uint(bits.TrailingZeros64(ev))
					if int(blk[kBit]) == objKeyOpenByte {
						ev &= ev - 1
						bit = kBit
						goto objKeyInBlock
					}
				}
				continue
			}

		handleArrOpenInBlock:
			{
				if depth == 0 && valueStart < 0 {
					valueStart = blockBase + int(bit)
				}
				nextDepth := depth + 1
				if nextDepth > maxDepthLim {
					cur = blockBase + int(bit)
					goto handoffGeneral
				}
				bitIdx := uint(depth)
				if bitIdx < 64 {
					bitWord0 |= uint64(1) << bitIdx
				} else {
					g.BitStack[bitIdx>>6] |= uint64(1) << (bitIdx & 63)
				}
				depth = nextDepth
				if uint32(depth) > statMaxDepth {
					statMaxDepth = uint32(depth)
				}
				topArr = true
				phase = PhaseArrayOpen
				continue
			}

		objKeyInBlock:
			// Nested key inside an object (depth > 0, !topArr, valueStart >= 0):
			// pure validation only needs to confirm in-block closure, not the exact
			// closing-quote cursor.
			if (cqMask >> bit) == 0 {
				coldTokStart = blockBase + int(bit)
				coldStrAct = actObjKey
				goto handleCrossBlockString
			}
			if ev == 0 {
				phase = PhaseObjectColon
				break
			}
			{
				cBit := uint(bits.TrailingZeros64(ev))
				if int(blk[cBit]) != colonByte && lookupStructByteTransition(PhaseObjectColon, blk[cBit]) != actObjColon {
					phase = PhaseObjectColon
					cur = blockBase + int(cBit)
					goto handoffGeneral
				}
				ev &= ev - 1
				if ev == 0 {
					phase = PhaseValue
					break
				}
			}

		objValueInBlock:
			// Dispatch the object field's value directly from grammar.go's canonical
			// PhaseValue transition row with a dedicated branch site (avoiding BTB
			// aliasing with the outer loop and duplicating zero value/container logic).
			{
				vBit := uint(bits.TrailingZeros64(ev))
				ev &= ev - 1
				vCh := blk[vBit]
				vAct := lookupStructByteTransition(PhaseValue, vCh)
				if vAct == actValString {
					if (cqMask >> vBit) == 0 {
						phase = PhaseValue
						coldTokStart = blockBase + int(vBit)
						coldStrAct = actValString
						goto handleCrossBlockString
					}
					goto objAfterValInBlock
				}
				phase = PhaseValue
				bit = vBit
				ch = vCh
				switch vAct {
				case actValNumber:
					goto handleNumberInBlock
				case actValTrue:
					goto handleTrueInBlock
				case actValFalse:
					goto handleFalseInBlock
				case actValNull:
					goto handleNullInBlock
				case actValObjOpen:
					goto handleObjOpenInBlock
				case actValArrOpen:
					goto handleArrOpenInBlock
				default:
					cur = blockBase + int(vBit)
					goto handoffGeneral
				}
			}

		objAfterValInBlock:
			if ev == 0 {
				phase = PhaseObjectAfterVal
				break
			}
			{
				sBit := uint(bits.TrailingZeros64(ev))
				sCh := blk[sBit]
				if int(sCh) == objCommaByte {
					ev &= ev - 1
					goto objAfterCommaInBlock
				}
				if int(sCh) == objCloseByte {
					ev &= ev - 1
					bit = sBit
					goto closeContainerInBlock
				}
				phase = PhaseObjectAfterVal
				sAct := lookupStructByteTransition(PhaseObjectAfterVal, sCh)
				if sAct == actObjComma {
					ev &= ev - 1
					goto objAfterCommaInBlock
				}
				if sAct == actCloseObj {
					ev &= ev - 1
					bit = sBit
					goto closeContainerInBlock
				}
				cur = blockBase + int(sBit)
				goto handoffGeneral
			}

		objAfterCommaInBlock:
			phase = PhaseObjectKey
			if ev != 0 {
				kBit := uint(bits.TrailingZeros64(ev))
				if int(blk[kBit]) == objKeyNextByte {
					ev &= ev - 1
					bit = kBit
					goto objKeyInBlock
				}
			}
			continue

		arrAfterValInBlock:
			phase = PhaseArrayAfterVal
			if ev != 0 {
				cBit := uint(bits.TrailingZeros64(ev))
				cCh := blk[cBit]
				if int(cCh) == arrCommaByte {
					ev &= ev - 1
					phase = PhaseValue
				} else if int(cCh) == arrCloseByte {
					ev &= ev - 1
					bit = cBit
					goto closeContainerInBlock
				}
			}
			continue

		closeContainerInBlock:
			depth--
			if depth == 0 {
				cur = blockBase + int(bit) + 1
				if fullDoc {
					phase = PhaseTrailingSpace
					continue
				}
				phase = PhaseDone
				goto finishDone
			}
			{
				pBitIdx := uint(depth - 1)
				if pBitIdx < 64 {
					topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
				} else {
					topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
				}
				if topArr {
					goto arrAfterValInBlock
				}
				goto objAfterValInBlock
			}
		}
		continue

	advancePastMultiBlockScalar:
		{
			if depth == 0 {
				if !fullDoc {
					phase = PhaseDone
					goto finishDone
				}
				phase = PhaseTrailingSpace
			} else if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			if cur >= batchEnd {
				ev = 0
				blkIdx = nBlocks
				batchEnd = cur
				carry = types.LexBlockCarry{}
				continue
			}
			blkIdx = (cur - batchStart) >> 6
			blockBase = batchStart + blkIdx*64
			blockEnd = blockBase + 64
			if blockEnd > batchEnd {
				blockEnd = batchEnd
			}
			blk = src[blockBase:blockEnd]
			cqMask = b.blocks[blkIdx].CloseQuote
			relOff := uint(cur - blockBase)
			ev = b.blocks[blkIdx].EventMask &^ ((uint64(1) << relOff) - 1)
			if cur < srcLen && isScalarContByte[src[cur]] {
				ev |= uint64(1) << relOff
			}
			goto consumeEvents
		}

	handleCrossBlockString:
		{
			foundClose := false
			var closeBlk int
			var closeBit uint
			for nb := blkIdx + 1; nb < nBlocks; nb++ {
				if cqm := b.blocks[nb].CloseQuote; cqm != 0 {
					closeBlk = nb
					closeBit = uint(bits.TrailingZeros64(cqm))
					foundClose = true
					break
				}
			}
			for !foundClose && batchEnd < srcLen {
				batchStart = batchEnd
				out := b.fillLexBatch(src, batchStart, b.blocks[:batchCap], LexFlagNone, &carry, forceScalar)
				if batchCap < len(b.blocks) {
					batchCap <<= 1
					if batchCap > len(b.blocks) {
						batchCap = len(b.blocks)
					}
				}
				b.calls++
				nBlocks = int(out.BlocksProduced)
				batchEnd = batchStart + int(out.BytesScanned)
				if DiagEnabled {
					if !fullDoc {
						diagValOneLexBytes += uint64(out.BytesScanned)
					}
					var evCount uint64
					for k := 0; k < nBlocks; k++ {
						evCount += uint64(bits.OnesCount64(b.blocks[k].EventMask))
					}
					diagRecordLexBlocks(out.BlocksProduced, out.BytesScanned, evCount, b.diagRole)
				}
				for nb := 0; nb < nBlocks; nb++ {
					if cqm := b.blocks[nb].CloseQuote; cqm != 0 {
						closeBlk = nb
						closeBit = uint(bits.TrailingZeros64(cqm))
						foundClose = true
						break
					}
				}
			}
			if !foundClose {
				cur = coldTokStart
				goto handoffGeneral
			}

			blkIdx = closeBlk
			blockBase = batchStart + blkIdx*64
			blockEnd = blockBase + 64
			if blockEnd > batchEnd {
				blockEnd = batchEnd
			}
			blk = src[blockBase:blockEnd]
			cqMask = b.blocks[blkIdx].CloseQuote
			if closeBit == 63 {
				ev = 0
			} else {
				ev = b.blocks[blkIdx].EventMask &^ ((uint64(2) << closeBit) - 1)
			}

			if coldStrAct == actObjKey {
				phase = PhaseObjectColon
				if ev != 0 {
					cBit := uint(bits.TrailingZeros64(ev))
					if lookupStructByteTransition(PhaseObjectColon, blk[cBit]) == actObjColon {
						ev &= ev - 1
						phase = PhaseValue
					}
				}
				goto consumeEvents
			}
			if depth == 0 {
				cur = blockBase + int(closeBit) + 1
				if !fullDoc {
					phase = PhaseDone
					goto finishDone
				}
				phase = PhaseTrailingSpace
				goto consumeEvents
			}
			if topArr {
				phase = PhaseArrayAfterVal
				if ev != 0 {
					sBit := uint(bits.TrailingZeros64(ev))
					if lookupStructByteTransition(PhaseArrayAfterVal, blk[sBit]) == actArrComma {
						ev &= ev - 1
						phase = PhaseValue
					}
				}
			} else {
				phase = PhaseObjectAfterVal
				if ev != 0 {
					sBit := uint(bits.TrailingZeros64(ev))
					if lookupStructByteTransition(PhaseObjectAfterVal, blk[sBit]) == actObjComma {
						ev &= ev - 1
						phase = PhaseObjectKey
					}
				}
			}
			goto consumeEvents
		}
	}

finishDone:
	if DiagEnabled && !fullDoc {
		diagRecordValidateOne(diagValOneLexBytes, cur-startCur)
	}
	b.clearActive()
	g.BitStack[0] = bitWord0
	g.Phase = PhaseDone
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = 0
	m.Stat.MaxDepth = statMaxDepth
	m.diag.recordTapeComplete(cur, m.NodeLen, len(m.Unesc), b.diagRole)
	return ScanResult{
		Consumed:    cur,
		Produced:    m.NodeLen,
		ValueStart:  valueStart,
		Reason:      StopDone,
		ErrorOffset: -1,
		Code:        ErrNone,
	}

handoffGeneral:
	diagRecordFastHandoff()
	if DiagEnabled && !fullDoc {
		diagRecordValidateOne(diagValOneLexBytes, cur-startCur)
	}
	b.clearActive()
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	m.Stat.MaxDepth = statMaxDepth
	return ScanResult{Reason: stopHandoffGeneral}

failEOFExit:
	if DiagEnabled && !fullDoc {
		diagRecordValidateOne(diagValOneLexBytes, cur-startCur)
	}
	b.clearActive()
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	m.Stat.MaxDepth = statMaxDepth
	return m.failEOF(src, true)
}
