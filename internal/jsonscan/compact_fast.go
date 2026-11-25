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

// canEnterCompactObserve gates the zero-output leaf-loop fast path strictly to
// complete-input, root ModeCompactObserve scans with standard strict-string
// CompactProfile / CompactFirstDirtyProfile (depth 10000, lone surrogates
// allowed, no schema/sub-mode/sink).
func (m *Machine) canEnterCompactObserve(src string, final bool) bool {
	g := &m.Grammar
	b := &m.batch
	if !final || g.RootMode != ModeCompactObserve || m.Schema.HasPlan || g.SubModeActive || m.sink != nil {
		return false
	}
	p := &g.Profile
	if p.BaseDepth != 0 || !p.RejectControl || !p.ValidateEscapes || p.RejectLoneSurrogate || p.ASTCompatLex {
		return false
	}
	if len(src) > LexMaxWindowBytes || b.capOverride != 0 || b.active {
		return false
	}
	if uint(g.Cur) > uint(len(src)) {
		return false
	}
	return g.Phase <= PhaseArrayAfterVal || g.Phase == PhaseTrailingSpace
}

// stepCompactObserve consumes 64-byte LexBlockMask batches (with
// LexFlagRejectControl) over the canonical structural transition table
// (grammar.go), tracking HasSpace, NeedsRewrite, and FirstDirtyPos without
// per-token multi-mode dispatch or unnecessary batch invalidation on escaped
// strings. Any syntax/escape/control error, depth overflow, or unsupported
// boundary hands off cleanly at cur = tokStart to stepGeneral.
func (m *Machine) stepCompactObserve(src string) ScanResult {
	g := &m.Grammar
	b := &m.batch
	srcLen := len(src)

	phase := g.Phase
	cur := g.Cur
	valueStart := g.ValueStart
	depth := g.Depth
	maxDepthLim := g.Profile.MaxContainerDepth
	fullDoc := g.Profile.FullDocument
	escapeHTML := g.Profile.EscapeHTMLObserve
	validateUTF8 := g.Profile.ValidateUTF8
	stopOnDirty := g.Profile.CompactFirstDirtyOnly
	forceScalar := b.forceScalar

	hasSpace := m.CompactObs.HasSpace
	needsRewrite := m.CompactObs.NeedsRewrite
	firstDirtyPos := m.CompactObs.FirstDirtyPos
	skipObserve := (!escapeHTML && !validateUTF8) || needsRewrite || (stopOnDirty && firstDirtyPos >= 0)

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
	batchStart := cur
	batchEnd := cur
	nBlocks := 0
	blkIdx := 0
	var blockBase int
	var blockEnd int
	var ev uint64
	var cqMask uint64
	var specMask uint64

	var coldTokStart int
	var coldStrAct structAction
	var coldFirstSpec int
	var coldCloseAbs int

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
					cqMask = b.blocks[blkIdx].CloseQuote
					specMask = b.blocks[blkIdx].InsideSpecial
					goto consumeEvents
				}
				blkIdx++
			}
			if batchEnd >= srcLen {
				if srcLen > cur {
					if firstDirtyPos < 0 {
						firstDirtyPos = cur
					}
					hasSpace = true
				}
				cur = srcLen
				if phase == PhaseTrailingSpace {
					phase = PhaseDone
					goto finishDone
				}
				goto failEOFExit
			}
			batchStart = batchEnd
			out := b.fillLexBatch(src, batchStart, b.blocks[:], LexFlagRejectControl, &carry, forceScalar)
			b.calls++
			nBlocks = int(out.BlocksProduced)
			batchEnd = batchStart + int(out.BytesScanned)
			if DiagEnabled {
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
					cqMask = b.blocks[blkIdx].CloseQuote
					specMask = b.blocks[blkIdx].InsideSpecial
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
			tokStart := blockBase + int(bit)
			if uint(tokStart) >= uint(srcLen) {
				goto failEOFExit
			}
			if tokStart > cur {
				if firstDirtyPos < 0 {
					firstDirtyPos = cur
				}
				hasSpace = true
				if stopOnDirty {
					skipObserve = true
				}
			}

			ch := src[tokStart]
			act := lookupStructByteTransition(phase, ch)

			switch act {
			case actObjKey:
				cq := cqMask >> bit
				if cq == 0 {
					coldTokStart = tokStart
					coldStrAct = actObjKey
					goto handleCrossBlockString
				}
				relClose := uint(bits.TrailingZeros64(cq))
				closeBit := bit + relClose
				between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
				if between != 0 {
					coldTokStart = tokStart
					coldStrAct = actObjKey
					coldFirstSpec = blockBase + bits.TrailingZeros64(between)
					coldCloseAbs = blockBase + int(closeBit)
					goto handleSameBlockSpecialString
				}
				closeAbs := blockBase + int(closeBit)
				if !skipObserve && closeAbs > tokStart+1 {
					if observeCompactValidatedStringBody(src[tokStart+1:closeAbs], escapeHTML, validateUTF8) {
						if firstDirtyPos < 0 {
							firstDirtyPos = tokStart
						}
						needsRewrite = true
						skipObserve = true
					}
				}
				cur = closeAbs + 1
				phase = PhaseObjectColon
				if ev != 0 {
					cBit := uint(bits.TrailingZeros64(ev))
					cPos := blockBase + int(cBit)
					if cPos == cur && uint(cPos) < uint(srcLen) && lookupStructByteTransition(PhaseObjectColon, src[cPos]) == actObjColon {
						ev &= ev - 1
						cur = cPos + 1
						phase = PhaseValue
					}
				}
				continue

			case actObjColon:
				cur = tokStart + 1
				phase = PhaseValue
				continue

			case actObjComma:
				cur = tokStart + 1
				phase = PhaseObjectKey
				continue

			case actArrComma:
				cur = tokStart + 1
				phase = PhaseValue
				continue

			case actValString:
				if valueStart < 0 {
					valueStart = tokStart
				}
				cq := cqMask >> bit
				if cq == 0 {
					coldTokStart = tokStart
					coldStrAct = actValString
					goto handleCrossBlockString
				}
				relClose := uint(bits.TrailingZeros64(cq))
				closeBit := bit + relClose
				between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
				if between != 0 {
					coldTokStart = tokStart
					coldStrAct = actValString
					coldFirstSpec = blockBase + bits.TrailingZeros64(between)
					coldCloseAbs = blockBase + int(closeBit)
					goto handleSameBlockSpecialString
				}
				closeAbs := blockBase + int(closeBit)
				if !skipObserve && closeAbs > tokStart+1 {
					if observeCompactValidatedStringBody(src[tokStart+1:closeAbs], escapeHTML, validateUTF8) {
						if firstDirtyPos < 0 {
							firstDirtyPos = tokStart
						}
						needsRewrite = true
						skipObserve = true
					}
				}
				cur = closeAbs + 1

			case actValNumber:
				// Maintenance note: while container structure shares grammar.go's
				// transition table, number-boundary validation is mirrored across
				// grammar.go, validate_fast.go, and compact_fast.go; keep them in sync.
				if valueStart < 0 {
					valueStart = tokStart
				}
				q := tokStart
				if ch == '-' {
					q++
					if q >= srcLen {
						cur = tokStart
						goto handoffGeneral
					}
				}
				c0 := src[q]
				if c0 >= '1' && c0 <= '9' {
					q++
					for q < srcLen && src[q]-'0' <= 9 {
						q++
					}
					if q < srcLen {
						c := src[q]
						if c == '.' {
							q++
							if q >= srcLen || src[q]-'0' > 9 {
								cur = tokStart
								goto handoffGeneral
							}
							q++
							for q < srcLen && src[q]-'0' <= 9 {
								q++
							}
							if q < srcLen {
								c = src[q]
							} else {
								c = 0
							}
						}
						if c == 'e' || c == 'E' {
							q++
							if q < srcLen && (src[q] == '+' || src[q] == '-') {
								q++
							}
							if q >= srcLen || src[q]-'0' > 9 {
								cur = tokStart
								goto handoffGeneral
							}
							q++
							for q < srcLen && src[q]-'0' <= 9 {
								q++
							}
						}
						if q < srcLen && isNumberContByte(src[q]) {
							cur = tokStart
							goto handoffGeneral
						}
					}
				} else if c0 == '0' {
					q++
					if q < srcLen {
						c := src[q]
						hasFracOrExp := false
						if c == '.' {
							hasFracOrExp = true
							q++
							if q >= srcLen || src[q]-'0' > 9 {
								cur = tokStart
								goto handoffGeneral
							}
							q++
							for q < srcLen && src[q]-'0' <= 9 {
								q++
							}
							if q < srcLen {
								c = src[q]
							} else {
								c = 0
							}
						}
						if c == 'e' || c == 'E' {
							hasFracOrExp = true
							q++
							if q < srcLen && (src[q] == '+' || src[q] == '-') {
								q++
							}
							if q >= srcLen || src[q]-'0' > 9 {
								cur = tokStart
								goto handoffGeneral
							}
							q++
							for q < srcLen && src[q]-'0' <= 9 {
								q++
							}
						}
						if hasFracOrExp && q < srcLen && isNumberContByte(src[q]) {
							cur = tokStart
							goto handoffGeneral
						}
					}
				} else {
					cur = tokStart
					goto handoffGeneral
				}
				cur = q
				if q >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if q < srcLen && isScalarContByte[src[q]] {
					ev |= uint64(1) << uint(q-blockBase)
				}

			case actValTrue:
				if valueStart < 0 {
					valueStart = tokStart
				}
				if tokStart+4 > srcLen || src[tokStart+1] != 'r' || src[tokStart+2] != 'u' || src[tokStart+3] != 'e' {
					cur = tokStart
					goto handoffGeneral
				}
				cur = tokStart + 4
				if cur >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if cur < srcLen && isScalarContByte[src[cur]] {
					ev |= uint64(1) << uint(cur-blockBase)
				}

			case actValFalse:
				if valueStart < 0 {
					valueStart = tokStart
				}
				if tokStart+5 > srcLen || src[tokStart+1] != 'a' || src[tokStart+2] != 'l' || src[tokStart+3] != 's' || src[tokStart+4] != 'e' {
					cur = tokStart
					goto handoffGeneral
				}
				cur = tokStart + 5
				if cur >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if cur < srcLen && isScalarContByte[src[cur]] {
					ev |= uint64(1) << uint(cur-blockBase)
				}

			case actValNull:
				if valueStart < 0 {
					valueStart = tokStart
				}
				if tokStart+4 > srcLen || src[tokStart+1] != 'u' || src[tokStart+2] != 'l' || src[tokStart+3] != 'l' {
					cur = tokStart
					goto handoffGeneral
				}
				cur = tokStart + 4
				if cur >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if cur < srcLen && isScalarContByte[src[cur]] {
					ev |= uint64(1) << uint(cur-blockBase)
				}

			case actValObjOpen:
				if valueStart < 0 {
					valueStart = tokStart
				}
				nextDepth := depth + 1
				if nextDepth > maxDepthLim {
					cur = tokStart
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
				cur = tokStart + 1
				phase = PhaseObjectOpen
				continue

			case actValArrOpen:
				if valueStart < 0 {
					valueStart = tokStart
				}
				nextDepth := depth + 1
				if nextDepth > maxDepthLim {
					cur = tokStart
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
				cur = tokStart + 1
				phase = PhaseArrayOpen
				continue

			case actCloseObj, actCloseArr:
				cur = tokStart + 1
				depth--
				if depth > 0 {
					pBitIdx := uint(depth - 1)
					if pBitIdx < 64 {
						topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
					} else {
						topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
					}
				}

			default: // actError
				cur = tokStart
				goto handoffGeneral
			}

			if depth == 0 {
				if fullDoc {
					phase = PhaseTrailingSpace
					continue
				}
				phase = PhaseDone
				goto finishDone
			}
			if topArr {
				phase = PhaseArrayAfterVal
				if ev != 0 {
					cBit := uint(bits.TrailingZeros64(ev))
					cPos := blockBase + int(cBit)
					if cPos == cur && uint(cPos) < uint(srcLen) && lookupStructByteTransition(PhaseArrayAfterVal, src[cPos]) == actArrComma {
						ev &= ev - 1
						cur = cPos + 1
						phase = PhaseValue
					}
				}
			} else {
				phase = PhaseObjectAfterVal
				if ev != 0 {
					cBit := uint(bits.TrailingZeros64(ev))
					cPos := blockBase + int(cBit)
					if cPos == cur && uint(cPos) < uint(srcLen) && lookupStructByteTransition(PhaseObjectAfterVal, src[cPos]) == actObjComma {
						ev &= ev - 1
						cur = cPos + 1
						phase = PhaseObjectKey
					}
				}
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
			cqMask = b.blocks[blkIdx].CloseQuote
			specMask = b.blocks[blkIdx].InsideSpecial
			relOff := uint(cur - blockBase)
			ev = b.blocks[blkIdx].EventMask &^ ((uint64(1) << relOff) - 1)
			if cur < srcLen && isScalarContByte[src[cur]] {
				ev |= uint64(1) << relOff
			}
			goto consumeEvents
		}

	handleSameBlockSpecialString:
		{
			if src[coldFirstSpec] != '\\' {
				cur = coldTokStart
				goto handoffGeneral
			}
			endPos, _, _, perr := kernScanString(src, coldFirstSpec-1, true)
			if perr != 0 || endPos != coldCloseAbs+1 {
				cur = coldTokStart
				goto handoffGeneral
			}
			if !skipObserve && coldCloseAbs > coldTokStart+1 {
				if observeCompactValidatedStringBody(src[coldTokStart+1:coldCloseAbs], escapeHTML, validateUTF8) {
					if firstDirtyPos < 0 {
						firstDirtyPos = coldTokStart
					}
					needsRewrite = true
					skipObserve = true
				}
			}
			cur = coldCloseAbs + 1
			if coldStrAct == actObjKey {
				phase = PhaseObjectColon
				goto consumeEvents
			}
			if depth == 0 {
				if !fullDoc {
					phase = PhaseDone
					goto finishDone
				}
				phase = PhaseTrailingSpace
				goto consumeEvents
			}
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			goto consumeEvents
		}

	handleCrossBlockString:
		{
			openBit := uint(coldTokStart - blockBase)
			sawSpec := false
			firstSpecAbs := -1
			if openBit < 63 {
				tailSpec := specMask &^ ((uint64(2) << openBit) - 1)
				if tailSpec != 0 {
					sawSpec = true
					firstSpecAbs = blockBase + bits.TrailingZeros64(tailSpec)
				}
			}
			foundClose := false
			var closeBlk int
			var closeBit uint
			for nb := blkIdx + 1; nb < nBlocks; nb++ {
				cqm := b.blocks[nb].CloseQuote
				spm := b.blocks[nb].InsideSpecial
				if cqm != 0 {
					closeBlk = nb
					closeBit = uint(bits.TrailingZeros64(cqm))
					foundClose = true
					prefSpec := spm & ((uint64(1) << closeBit) - 1)
					if !sawSpec && prefSpec != 0 {
						sawSpec = true
						firstSpecAbs = batchStart + nb*64 + bits.TrailingZeros64(prefSpec)
					}
					break
				}
				if !sawSpec && spm != 0 {
					sawSpec = true
					firstSpecAbs = batchStart + nb*64 + bits.TrailingZeros64(spm)
				}
			}

			var endPos int
			if foundClose && !sawSpec {
				closeAbs := batchStart + closeBlk*64 + int(closeBit)
				if !skipObserve && closeAbs > coldTokStart+1 {
					if observeCompactValidatedStringBody(src[coldTokStart+1:closeAbs], escapeHTML, validateUTF8) {
						if firstDirtyPos < 0 {
							firstDirtyPos = coldTokStart
						}
						needsRewrite = true
						skipObserve = true
					}
				}
				endPos = closeAbs + 1
			} else {
				if sawSpec && src[firstSpecAbs] != '\\' {
					cur = coldTokStart
					goto handoffGeneral
				}
				scanFrom := coldTokStart + 1
				if sawSpec {
					scanFrom = firstSpecAbs
				} else if batchEnd > scanFrom {
					scanFrom = batchEnd
				}
				ep, _, _, perr := kernScanString(src, scanFrom-1, true)
				if perr != 0 {
					cur = coldTokStart
					goto handoffGeneral
				}
				if foundClose && ep != batchStart+closeBlk*64+int(closeBit)+1 {
					cur = coldTokStart
					goto handoffGeneral
				}
				if !skipObserve && ep-1 > coldTokStart+1 {
					if observeCompactValidatedStringBody(src[coldTokStart+1:ep-1], escapeHTML, validateUTF8) {
						if firstDirtyPos < 0 {
							firstDirtyPos = coldTokStart
						}
						needsRewrite = true
						skipObserve = true
					}
				}
				endPos = ep
			}

			cur = endPos
			if coldStrAct == actObjKey {
				phase = PhaseObjectColon
			} else if depth == 0 {
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

			if endPos >= batchEnd {
				ev = 0
				blkIdx = nBlocks
				batchEnd = endPos
				carry = types.LexBlockCarry{}
				continue
			}
			blkIdx = (endPos - batchStart) >> 6
			blockBase = batchStart + blkIdx*64
			blockEnd = blockBase + 64
			if blockEnd > batchEnd {
				blockEnd = batchEnd
			}
			cqMask = b.blocks[blkIdx].CloseQuote
			specMask = b.blocks[blkIdx].InsideSpecial
			relAfter := uint(endPos - blockBase)
			ev = b.blocks[blkIdx].EventMask &^ ((uint64(1) << relAfter) - 1)
			goto consumeEvents
		}
	}

finishDone:
	b.clearActive()
	g.BitStack[0] = bitWord0
	g.Phase = PhaseDone
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = 0
	m.Stat.MaxDepth = statMaxDepth
	m.CompactObs.HasSpace = hasSpace
	m.CompactObs.NeedsRewrite = needsRewrite
	m.CompactObs.FirstDirtyPos = firstDirtyPos
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
	b.clearActive()
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	m.Stat.MaxDepth = statMaxDepth
	m.CompactObs.HasSpace = hasSpace
	m.CompactObs.NeedsRewrite = needsRewrite
	m.CompactObs.FirstDirtyPos = firstDirtyPos
	return ScanResult{Reason: stopHandoffGeneral}

failEOFExit:
	b.clearActive()
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	m.Stat.MaxDepth = statMaxDepth
	m.CompactObs.HasSpace = hasSpace
	m.CompactObs.NeedsRewrite = needsRewrite
	m.CompactObs.FirstDirtyPos = firstDirtyPos
	return m.failEOF(src, true)
}
