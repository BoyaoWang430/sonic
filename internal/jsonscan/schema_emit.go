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
	"math"
	"math/bits"

	"github.com/bytedance/sonic/internal/native/types"
)

// canEnterSchemaFullEmit gates the leaf-loop Schema projection fast path to
// full-buffer ModeEmit scans with an attached Plan, zero base depth, inline
// plan stack headroom, and no active sub-mode or event sink.
func (m *Machine) canEnterSchemaFullEmit(src string, final bool) bool {
	g := &m.Grammar
	b := &m.batch
	s := &m.Schema
	if !final || g.RootMode != ModeEmit || !s.HasPlan || g.SubModeActive || m.sink != nil {
		return false
	}
	if g.Profile.ASTCompatLex || g.Profile.BaseDepth != 0 || len(src) > LexMaxWindowBytes || b.capOverride != 0 {
		return false
	}
	if len(m.Nodes)-m.NodeLen < plainEmitBlockMaxNodes {
		return false
	}
	if b.active && !b.hasReusableSchemaWindow(g.Cur) {
		return false
	}
	if s.PlanDepth != g.Depth || g.Depth >= len(s.InlinePlans) {
		return false
	}
	return g.Phase <= PhaseArrayAfterVal || g.Phase == PhaseTrailingSpace
}

// stepSchemaFullEmit drives Schema-projected ModeEmit scans over the canonical
// structural transition table (grammar.go) in a tight register loop, keeping
// container plan frames and expected field-order indices in registers and
// executing relaxed unknown-field skips inline without bouncing through
// stepGeneral. Cold cases (escaped/cross-block keys, PlanRaw subtrees,
// DisallowUnknown or strict unknown validation, depth>=32, buffer growth, or
// syntax errors) hand off cleanly at cur = tokStart to stepGeneral.
func (m *Machine) stepSchemaFullEmit(src string) ScanResult {
	g := &m.Grammar
	b := &m.batch
	s := &m.Schema
	nodes := m.Nodes
	nodesLen := len(nodes)
	nlen := m.NodeLen
	srcLen := len(src)

	phase := g.Phase
	cur := g.Cur
	valueStart := g.ValueStart
	depth := g.Depth
	parentIdx := g.ParentNodeIdx
	maxDepthLim := g.Profile.MaxContainerDepth
	fullDoc := g.Profile.FullDocument
	useNumber := g.Profile.UseNumber
	deferFloatOverflow := g.Profile.DeferFloatOverflow
	rejectControl := g.Profile.RejectControl
	rejectLoneSurrogate := g.Profile.RejectLoneSurrogate
	forceScalar := b.forceScalar

	// Mechanically derived single-byte structural witnesses from grammar.go.
	colonByte := uniqueObjColonByte
	objKeyOpenByte := uniqueObjKeyOpenByte
	objKeyNextByte := uniqueObjKeyNextByte
	objCommaByte := uniqueObjCommaByte
	objCloseByte := uniqueObjCloseByte
	arrCommaByte := uniqueArrCommaByte
	arrCloseByte := uniqueArrCloseByte

	caseSensitive := s.CaseSensitive
	disallowUnknown := s.DisallowUnknown
	relaxedUnknownSkip := s.RelaxedUnknownSkip
	activePlan := s.ActivePlan

	var lexFlags LexFlags
	if rejectControl {
		lexFlags = LexFlagRejectControl
	}

	bitWord0 := g.BitStack[0]
	var parentLink uint64
	var curCount uint32
	var topArr bool
	var topPlan *Plan
	var topExpectedIdx int
	if depth > 0 {
		openVal := nodes[parentIdx].Val
		parentLink = openVal & 0xFFFFFFFF00000000
		curCount = uint32(openVal)
		pBitIdx := uint(depth - 1)
		if pBitIdx < 64 {
			topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
		} else {
			topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
		}
		pf := &s.InlinePlans[depth-1]
		topPlan = pf.plan
		topExpectedIdx = pf.expectedIdx
	}

	statObj := m.Stat.Object
	statArr := m.Stat.Array
	statStr := m.Stat.Str
	statNum := m.Stat.Number
	statArrElems := m.Stat.ArrayElems
	statObjKeys := m.Stat.ObjectKeys
	statMaxDepth := m.Stat.MaxDepth

	var carry types.LexBlockCarry
	batchStart := cur
	batchEnd := cur
	nBlocks := 0
	blkIdx := 0
	var blockBase int
	var blockEnd int
	var blk string
	var ev uint64
	var cqMask uint64
	var specMask uint64

	var coldTokStart int
	var coldNumEnd int
	var coldWantTypedNum bool
	var coldFirstSpec int
	var coldCloseAbs int
	var errOff int
	var nextCur int
	var errCode ScanError

	if b.active {
		var adopted bool
		carry, batchStart, batchEnd, nBlocks, blkIdx, ev, adopted = b.takeReusableSchemaWindow(cur)
		if adopted && ev != 0 {
			blockBase = batchStart + blkIdx*64
			blockEnd = blockBase + 64
			if blockEnd > batchEnd {
				blockEnd = batchEnd
			}
			blk = src[blockBase:blockEnd]
			cqMask = b.blocks[blkIdx].CloseQuote
			specMask = b.blocks[blkIdx].InsideSpecial
		}
	}

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
					specMask = b.blocks[blkIdx].InsideSpecial
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
			capBlk := len(b.blocks)
			if relaxedUnknownSkip {
				if srcLen-batchStart <= 128 {
					capBlk = 2
				} else {
					capBlk = 1
				}
			}
			out := b.fillLexBatch(src, batchStart, b.blocks[:capBlk], lexFlags, &carry, forceScalar)
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
					blk = src[blockBase:blockEnd]
					cqMask = b.blocks[blkIdx].CloseQuote
					specMask = b.blocks[blkIdx].InsideSpecial
					goto consumeEvents
				}
				blkIdx++
			}
			continue
		}

	consumeEvents:
		if nlen+plainEmitBlockMaxNodes > nodesLen {
			goto handoffGeneral
		}

		for ev != 0 {
			bit := uint(bits.TrailingZeros64(ev))
			ev &= ev - 1
			ch := blk[bit]
			act := lookupStructByteTransition(phase, ch)

			if phase == PhaseArrayOpen && act != actCloseArr && act != actError {
				if topPlan != nil {
					activePlan = topPlan.Elem
				}
				phase = PhaseValue
			}

			switch act {
			case actObjKey:
				goto objKeyInBlock

			case actObjColon:
				cur = blockBase + int(bit) + 1
				phase = PhaseValue
				if ev != 0 {
					goto objValueInBlock
				}
				continue

			case actObjComma:
				cur = blockBase + int(bit) + 1
				goto objAfterCommaInBlock

			case actArrComma:
				cur = blockBase + int(bit) + 1
				if topPlan != nil {
					activePlan = topPlan.Elem
				}
				phase = PhaseValue
				if ev != 0 {
					goto objValueInBlock
				}
				continue

			case actValString:
				goto handleValString

			case actValNumber:
				goto handleValNumber

			case actValTrue:
				goto handleValTrue

			case actValFalse:
				goto handleValFalse

			case actValNull:
				goto handleValNull

			case actValObjOpen:
				goto handleValObjOpen

			case actValArrOpen:
				goto handleValArrOpen

			case actCloseObj:
				goto handleCloseObj

			case actCloseArr:
				goto handleCloseArr

			default: // actError
				cur = blockBase + int(bit)
				goto handoffGeneral
			}

		objKeyInBlock:
			{
				tokStart := blockBase + int(bit)
				var rawStart, closeAbs int
				crossedBlock := false
				cq := cqMask >> bit
				if cq != 0 {
					relClose := uint(bits.TrailingZeros64(cq))
					closeBit := bit + relClose
					between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
					if between != 0 {
						cur = tokStart
						goto handoffGeneral
					}
					rawStart = tokStart + 1
					closeAbs = tokStart + int(relClose)
				} else {
					sawSpec := false
					if bit < 63 && (specMask&^((uint64(2)<<bit)-1)) != 0 {
						sawSpec = true
					}
					foundClose := false
					var closeBlk int
					var closeBit uint
					if !sawSpec {
						for nb := blkIdx + 1; nb < nBlocks; nb++ {
							cqm := b.blocks[nb].CloseQuote
							spm := b.blocks[nb].InsideSpecial
							if cqm != 0 {
								closeBlk = nb
								closeBit = uint(bits.TrailingZeros64(cqm))
								if (spm & ((uint64(1) << closeBit) - 1)) != 0 {
									sawSpec = true
								} else {
									foundClose = true
								}
								break
							}
							if spm != 0 {
								sawSpec = true
								break
							}
						}
						for !foundClose && !sawSpec && batchEnd < srcLen {
							batchStart = batchEnd
							capBlk := len(b.blocks)
							if relaxedUnknownSkip {
								if srcLen-batchStart <= 128 {
									capBlk = 2
								} else {
									capBlk = 1
								}
							}
							out := b.fillLexBatch(src, batchStart, b.blocks[:capBlk], lexFlags, &carry, forceScalar)
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
							for nb := 0; nb < nBlocks; nb++ {
								cqm := b.blocks[nb].CloseQuote
								spm := b.blocks[nb].InsideSpecial
								if cqm != 0 {
									closeBlk = nb
									closeBit = uint(bits.TrailingZeros64(cqm))
									if (spm & ((uint64(1) << closeBit) - 1)) != 0 {
										sawSpec = true
									} else {
										foundClose = true
									}
									break
								}
								if spm != 0 {
									sawSpec = true
									break
								}
							}
						}
					}
					if !foundClose {
						cur = tokStart
						goto handoffGeneral
					}
					crossedBlock = true
					blkIdx = closeBlk
					blockBase = batchStart + blkIdx*64
					blockEnd = blockBase + 64
					if blockEnd > batchEnd {
						blockEnd = batchEnd
					}
					blk = src[blockBase:blockEnd]
					cqMask = b.blocks[blkIdx].CloseQuote
					specMask = b.blocks[blkIdx].InsideSpecial
					if closeBit == 63 {
						ev = 0
					} else {
						ev = b.blocks[blkIdx].EventMask &^ ((uint64(2) << closeBit) - 1)
					}
					rawStart = tokStart + 1
					closeAbs = blockBase + int(closeBit)
				}
				rawLen := closeAbs - rawStart
				if topPlan == nil {
					nodes[nlen] = Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
					nlen++
					activePlan = nil
				} else if topPlan.Kind == PlanStruct {
					keyStr := src[rawStart:closeAbs]
					idx := -1
					if topExpectedIdx < len(topPlan.FieldNames) && topPlan.FieldNames[topExpectedIdx] == keyStr {
						idx = topExpectedIdx
						topExpectedIdx = idx + 1
					} else if len(topPlan.FieldNames) == 0 && topExpectedIdx < topPlan.Struct.FieldCount() && topPlan.Struct.FieldName(topExpectedIdx) == keyStr {
						idx = topExpectedIdx
						topExpectedIdx = idx + 1
					} else {
						keyASCII := false
						if !caseSensitive {
							keyASCII = isShortASCII(keyStr)
						}
						idx = topPlan.Struct.MatchField(keyStr, caseSensitive, keyASCII)
						if idx >= 0 {
							topExpectedIdx = idx + 1
						}
					}
					if idx < 0 {
						if disallowUnknown {
							cur = tokStart
							goto handoffGeneral
						}
						if !relaxedUnknownSkip {
							cur = tokStart
							goto handoffGeneralKeepWindow
						}
						coldTokStart = tokStart
						cur = closeAbs + 1
						goto handleInlineRelaxedSkipUnknown
					}
					keyTyp := KStringCommon | uint64(rawStart)<<PosBits
					if idx < 0xFFFFFE {
						keyTyp |= uint64(idx+1) << 8
					}
					nodes[nlen] = Node{Typ: keyTyp, Val: uint64(rawLen)}
					nlen++
					if idx < len(topPlan.Fields) {
						activePlan = topPlan.Fields[idx]
					} else {
						activePlan = nil
					}
				} else if topPlan.Kind == PlanMap {
					nodes[nlen] = Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
					nlen++
					activePlan = topPlan.Elem
				} else {
					nodes[nlen] = Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
					nlen++
					activePlan = nil
				}
				cur = closeAbs + 1
				phase = PhaseObjectColon
				if ev == 0 {
					break
				}
				cBit := uint(bits.TrailingZeros64(ev))
				cCh := blk[cBit]
				if int(cCh) != colonByte && lookupStructByteTransition(PhaseObjectColon, cCh) != actObjColon {
					cur = blockBase + int(cBit)
					goto handoffGeneral
				}
				ev &= ev - 1
				cur = blockBase + int(cBit) + 1
				phase = PhaseValue
				if crossedBlock {
					goto consumeEvents
				}
				if ev == 0 {
					break
				}
			}

		objValueInBlock:
			// Dedicated PhaseValue dispatch site from grammar.go (avoids BTB aliasing
			// with the outer structural loop while maintaining a single grammar authority).
			{
				vBit := uint(bits.TrailingZeros64(ev))
				ev &= ev - 1
				vCh := blk[vBit]
				phase = PhaseValue
				bit = vBit
				ch = vCh
				switch lookupStructByteTransition(PhaseValue, vCh) {
				case actValString:
					goto handleValString
				case actValNumber:
					goto handleValNumber
				case actValTrue:
					goto handleValTrue
				case actValFalse:
					goto handleValFalse
				case actValNull:
					goto handleValNull
				case actValObjOpen:
					goto handleValObjOpen
				case actValArrOpen:
					goto handleValArrOpen
				default:
					cur = blockBase + int(vBit)
					goto handoffGeneral
				}
			}

		handleValString:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				if activePlan != nil && activePlan.Kind == PlanRaw {
					cur = tokStart
					goto handoffGeneral
				}
				activePlan = nil
				cq := cqMask >> bit
				if cq == 0 {
					coldTokStart = tokStart
					goto handleCrossBlockValString
				}
				relClose := uint(bits.TrailingZeros64(cq))
				closeBit := bit + relClose
				between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
				if between != 0 {
					coldTokStart = tokStart
					coldFirstSpec = blockBase + bits.TrailingZeros64(between)
					coldCloseAbs = blockBase + int(closeBit)
					goto handleSameBlockSpecialValString
				}
				nodes[nlen] = Node{Typ: KStringCommon | uint64(tokStart+1)<<PosBits, Val: uint64(relClose - 1)}
				nlen++
				statStr++
				cur = tokStart + int(relClose) + 1
				goto afterValInline
			}

		handleValNumber:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				if activePlan != nil && activePlan.Kind == PlanRaw {
					cur = tokStart
					goto handoffGeneral
				}
				wantTypedNum := activePlan != nil && activePlan.TypedNumber
				activePlan = nil
				if useNumber && !wantTypedNum {
					q, ok := scanValidNumberFastInt(src, tokStart)
					if !ok {
						q, ok = scanValidNumberSpanSlow(src, tokStart, true)
						if !ok {
							cur = tokStart
							goto handoffGeneral
						}
					}
					nodes[nlen] = Node{Typ: KRawNumber | uint64(tokStart)<<PosBits, Val: uint64(q - tokStart)}
					nlen++
					statNum++
					cur = q
					if q >= blockEnd {
						goto advancePastMultiBlockScalar
					}
					if q < srcLen && isScalarContByte[src[q]] {
						ev |= uint64(1) << uint(q-blockBase)
					}
					goto afterValInline
				}
				neg := ch == '-'
				q := tokStart
				if neg {
					q++
					if q >= srcLen {
						coldTokStart = tokStart
						coldWantTypedNum = wantTypedNum
						goto handleColdNumber
					}
				}
				c0 := src[q]
				if c0 >= '1' && c0 <= '9' {
					v := uint64(c0 - '0')
					q++
					intDigits := 1
					lim := q + 18
					if lim > srcLen {
						lim = srcLen
					}
					for q < lim {
						d := src[q] - '0'
						if d > 9 {
							break
						}
						v = v*10 + uint64(d)
						q++
						intDigits++
					}
					if q < srcLen {
						term := src[q]
						if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
							if useNumber && isNumberContByte(term) {
								cur = tokStart
								goto handoffGeneral
							}
							if !neg {
								nodes[nlen] = Node{Typ: KUint | uint64(tokStart)<<PosBits, Val: v}
							} else if v <= 1<<63 {
								nodes[nlen] = Node{Typ: KSint | uint64(tokStart)<<PosBits, Val: -v}
							} else {
								coldTokStart = tokStart
								coldWantTypedNum = wantTypedNum
								goto handleColdNumber
							}
							nlen++
							statNum++
							cur = q
							if q >= blockEnd {
								goto advancePastMultiBlockScalar
							}
							if isScalarContByte[term] {
								ev |= uint64(1) << uint(q-blockBase)
							}
							goto afterValInline
						}
						if term == '.' && intDigits <= 15 && q+1 < srcLen && src[q+1]-'0' <= 9 {
							q++
							fracDigits := 0
							maxFrac := 15 - intDigits
							for fracDigits < maxFrac && q < srcLen {
								d := src[q] - '0'
								if d > 9 {
									break
								}
								v = v*10 + uint64(d)
								q++
								fracDigits++
							}
							if fracDigits >= 1 && v>>52 == 0 {
								if q == srcLen || (src[q] != '.' && src[q] != 'e' && src[q] != 'E' && (src[q]-'0' > 9)) {
									if useNumber && q < srcLen && isNumberContByte(src[q]) {
										cur = tokStart
										goto handoffGeneral
									}
									fv := float64(v)
									if neg {
										fv = -fv
									}
									fv /= exactPow10[fracDigits]
									nodes[nlen] = Node{Typ: KReal | uint64(tokStart)<<PosBits, Val: math.Float64bits(fv)}
									nlen++
									statNum++
									cur = q
									if q >= blockEnd {
										goto advancePastMultiBlockScalar
									}
									if q < srcLen && isScalarContByte[src[q]] {
										ev |= uint64(1) << uint(q-blockBase)
									}
									goto afterValInline
								}
							}
							if fracDigits >= 1 {
								for q < srcLen && src[q]-'0' <= 9 {
									q++
								}
								if q == srcLen || (src[q] != '.' && src[q] != 'e' && src[q] != 'E' && (!useNumber || !isNumberContByte(src[q]))) {
									coldTokStart = tokStart
									coldNumEnd = q
									goto handleLongDecimal
								}
							}
						}
					} else if intDigits <= 19 {
						if !neg {
							nodes[nlen] = Node{Typ: KUint | uint64(tokStart)<<PosBits, Val: v}
						} else if v <= 1<<63 {
							nodes[nlen] = Node{Typ: KSint | uint64(tokStart)<<PosBits, Val: -v}
						} else {
							coldTokStart = tokStart
							coldWantTypedNum = wantTypedNum
							goto handleColdNumber
						}
						nlen++
						statNum++
						cur = q
						if q >= blockEnd {
							goto advancePastMultiBlockScalar
						}
						goto afterValInline
					}
					coldTokStart = tokStart
					coldWantTypedNum = wantTypedNum
					goto handleColdNumber
				} else if c0 == '0' {
					q++
					if q == srcLen || (src[q] != '.' && src[q] != 'e' && src[q] != 'E') {
						ndTyp := KUint
						if neg {
							ndTyp = KSint
						}
						nodes[nlen] = Node{Typ: ndTyp | uint64(tokStart)<<PosBits, Val: 0}
						nlen++
						statNum++
						cur = q
						if q >= blockEnd {
							goto advancePastMultiBlockScalar
						}
						if q < srcLen && isScalarContByte[src[q]] {
							ev |= uint64(1) << uint(q-blockBase)
						}
						goto afterValInline
					}
					if q+1 < srcLen && src[q] == '.' && src[q+1]-'0' <= 9 {
						q++
						var v uint64
						fracDigits := 0
						for fracDigits < 15 && q < srcLen {
							d := src[q] - '0'
							if d > 9 {
								break
							}
							v = v*10 + uint64(d)
							q++
							fracDigits++
						}
						if fracDigits >= 1 && v>>52 == 0 && (q == srcLen || (src[q] != '.' && src[q] != 'e' && src[q] != 'E' && (src[q]-'0' > 9))) {
							if useNumber && q < srcLen && isNumberContByte(src[q]) {
								cur = tokStart
								goto handoffGeneral
							}
							var fbits uint64
							if v == 0 {
								if neg {
									fbits = 1 << 63
								}
							} else {
								fv := float64(v)
								if neg {
									fv = -fv
								}
								fv /= exactPow10[fracDigits]
								fbits = math.Float64bits(fv)
							}
							nodes[nlen] = Node{Typ: KReal | uint64(tokStart)<<PosBits, Val: fbits}
							nlen++
							statNum++
							cur = q
							if q >= blockEnd {
								goto advancePastMultiBlockScalar
							}
							if q < srcLen && isScalarContByte[src[q]] {
								ev |= uint64(1) << uint(q-blockBase)
							}
							goto afterValInline
						}
						if fracDigits >= 1 {
							for q < srcLen && src[q]-'0' <= 9 {
								q++
							}
							if q == srcLen || (src[q] != '.' && src[q] != 'e' && src[q] != 'E' && (!useNumber || !isNumberContByte(src[q]))) {
								coldTokStart = tokStart
								coldNumEnd = q
								goto handleLongDecimal
							}
						}
					}
					coldTokStart = tokStart
					coldWantTypedNum = wantTypedNum
					goto handleColdNumber
				} else {
					coldTokStart = tokStart
					coldWantTypedNum = wantTypedNum
					goto handleColdNumber
				}

			}

		handleValTrue:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				if activePlan != nil && activePlan.Kind == PlanRaw {
					cur = tokStart
					goto handoffGeneral
				}
				activePlan = nil
				if tokStart+4 > srcLen || src[tokStart+1] != 'r' || src[tokStart+2] != 'u' || src[tokStart+3] != 'e' {
					cur = tokStart
					goto handoffGeneral
				}
				nodes[nlen] = Node{Typ: KTrue | uint64(tokStart)<<PosBits}
				nlen++
				cur = tokStart + 4
				if cur >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if cur < srcLen && isScalarContByte[src[cur]] {
					ev |= uint64(1) << uint(cur-blockBase)
				}
				goto afterValInline
			}

		handleValFalse:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				if activePlan != nil && activePlan.Kind == PlanRaw {
					cur = tokStart
					goto handoffGeneral
				}
				activePlan = nil
				if tokStart+5 > srcLen || src[tokStart+1] != 'a' || src[tokStart+2] != 'l' || src[tokStart+3] != 's' || src[tokStart+4] != 'e' {
					cur = tokStart
					goto handoffGeneral
				}
				nodes[nlen] = Node{Typ: KFalse | uint64(tokStart)<<PosBits}
				nlen++
				cur = tokStart + 5
				if cur >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if cur < srcLen && isScalarContByte[src[cur]] {
					ev |= uint64(1) << uint(cur-blockBase)
				}
				goto afterValInline
			}

		handleValNull:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				activePlan = nil
				if tokStart+4 > srcLen || src[tokStart+1] != 'u' || src[tokStart+2] != 'l' || src[tokStart+3] != 'l' {
					cur = tokStart
					goto handoffGeneral
				}
				nodes[nlen] = Node{Typ: KNull | uint64(tokStart)<<PosBits}
				nlen++
				cur = tokStart + 4
				if cur >= blockEnd {
					goto advancePastMultiBlockScalar
				}
				if cur < srcLen && isScalarContByte[src[cur]] {
					ev |= uint64(1) << uint(cur-blockBase)
				}
				goto afterValInline
			}

		handleValObjOpen:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				if activePlan != nil && activePlan.Kind == PlanRaw {
					cur = tokStart
					goto handoffGeneral
				}
				nextDepth := depth + 1
				if nextDepth > maxDepthLim || depth >= len(s.InlinePlans) {
					cur = tokStart
					goto handoffGeneral
				}
				if depth > 0 {
					nodes[parentIdx].Val = parentLink | uint64(curCount)
					s.InlinePlans[depth-1].expectedIdx = topExpectedIdx
				}
				var cplan *Plan
				if activePlan != nil && (activePlan.Kind == PlanStruct || activePlan.Kind == PlanMap) {
					cplan = activePlan
				}
				activePlan = nil
				s.InlinePlans[depth] = planFrame{plan: cplan}
				topPlan = cplan
				topExpectedIdx = 0

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
				parentLink = packOpenContainerVal(parentIdx)
				idx := nlen
				nodes[idx] = Node{Typ: KObject | uint64(tokStart)<<PosBits, Val: parentLink}
				nlen = idx + 1
				parentIdx = idx
				curCount = 0
				topArr = false
				cur = tokStart + 1
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

		handleValArrOpen:
			{
				tokStart := blockBase + int(bit)
				if valueStart < 0 {
					valueStart = tokStart
				}
				if activePlan != nil && activePlan.Kind == PlanRaw {
					cur = tokStart
					goto handoffGeneral
				}
				nextDepth := depth + 1
				if nextDepth > maxDepthLim || depth >= len(s.InlinePlans) {
					cur = tokStart
					goto handoffGeneral
				}
				if depth > 0 {
					nodes[parentIdx].Val = parentLink | uint64(curCount)
					s.InlinePlans[depth-1].expectedIdx = topExpectedIdx
				}
				var cplan *Plan
				if activePlan != nil && (activePlan.Kind == PlanSlice || activePlan.Kind == PlanBytes) {
					cplan = activePlan
				}
				activePlan = nil
				s.InlinePlans[depth] = planFrame{plan: cplan}
				topPlan = cplan
				topExpectedIdx = 0

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
				parentLink = packOpenContainerVal(parentIdx)
				idx := nlen
				nodes[idx] = Node{Typ: KArray | uint64(tokStart)<<PosBits, Val: parentLink}
				nlen = idx + 1
				parentIdx = idx
				curCount = 0
				topArr = true
				cur = tokStart + 1
				phase = PhaseArrayOpen
				continue
			}

		handleCloseObj:
			cur = blockBase + int(bit) + 1
			statObj++
			statObjKeys += curCount
			{
				span := uint64(nlen - parentIdx)
				nodes[parentIdx].Val = (span << ConLenBits) | uint64(curCount)
			}
			parentIdx = unpackOpenParentIdx(parentLink)
			depth--
			s.InlinePlans[depth] = planFrame{}
			activePlan = nil
			if depth > 0 {
				openVal := nodes[parentIdx].Val
				parentLink = openVal & 0xFFFFFFFF00000000
				curCount = uint32(openVal)
				pBitIdx := uint(depth - 1)
				if pBitIdx < 64 {
					topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
				} else {
					topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
				}
				pf := &s.InlinePlans[depth-1]
				topPlan = pf.plan
				topExpectedIdx = pf.expectedIdx
			} else {
				topPlan = nil
				topExpectedIdx = 0
			}
			goto afterValInline

		handleCloseArr:
			cur = blockBase + int(bit) + 1
			statArr++
			statArrElems += curCount
			{
				span := uint64(nlen - parentIdx)
				nodes[parentIdx].Val = (span << ConLenBits) | uint64(curCount)
			}
			parentIdx = unpackOpenParentIdx(parentLink)
			depth--
			s.InlinePlans[depth] = planFrame{}
			activePlan = nil
			if depth > 0 {
				openVal := nodes[parentIdx].Val
				parentLink = openVal & 0xFFFFFFFF00000000
				curCount = uint32(openVal)
				pBitIdx := uint(depth - 1)
				if pBitIdx < 64 {
					topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
				} else {
					topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
				}
				pf := &s.InlinePlans[depth-1]
				topPlan = pf.plan
				topExpectedIdx = pf.expectedIdx
			} else {
				topPlan = nil
				topExpectedIdx = 0
			}

		afterValInline:
			if depth == 0 {
				if fullDoc {
					phase = PhaseTrailingSpace
					continue
				}
				phase = PhaseDone
				goto finishDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
				if ev == 0 {
					break
				}
				cBit := uint(bits.TrailingZeros64(ev))
				cCh := blk[cBit]
				if int(cCh) == arrCommaByte {
					ev &= ev - 1
					cur = blockBase + int(cBit) + 1
					if topPlan != nil {
						activePlan = topPlan.Elem
					}
					phase = PhaseValue
					if ev != 0 {
						goto objValueInBlock
					}
					break
				}
				if int(cCh) == arrCloseByte {
					ev &= ev - 1
					bit = cBit
					goto handleCloseArr
				}
				cAct := lookupStructByteTransition(PhaseArrayAfterVal, cCh)
				if cAct == actArrComma {
					ev &= ev - 1
					cur = blockBase + int(cBit) + 1
					if topPlan != nil {
						activePlan = topPlan.Elem
					}
					phase = PhaseValue
					if ev != 0 {
						goto objValueInBlock
					}
					break
				}
				if cAct == actCloseArr {
					ev &= ev - 1
					bit = cBit
					goto handleCloseArr
				}
				cur = blockBase + int(cBit)
				goto handoffGeneral
			}
			phase = PhaseObjectAfterVal
			if ev == 0 {
				break
			}
			{
				sBit := uint(bits.TrailingZeros64(ev))
				sCh := blk[sBit]
				if int(sCh) == objCommaByte {
					ev &= ev - 1
					cur = blockBase + int(sBit) + 1
					goto objAfterCommaInBlock
				}
				if int(sCh) == objCloseByte {
					ev &= ev - 1
					bit = sBit
					goto handleCloseObj
				}
				sAct := lookupStructByteTransition(PhaseObjectAfterVal, sCh)
				if sAct == actObjComma {
					ev &= ev - 1
					cur = blockBase + int(sBit) + 1
					goto objAfterCommaInBlock
				}
				if sAct == actCloseObj {
					ev &= ev - 1
					bit = sBit
					goto handleCloseObj
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
		}
		continue

	handleInlineRelaxedSkipUnknown:
		{
			// Consume ':' following the skipped unknown key.
			inCurrentBlock := false
			if ev != 0 {
				cBit := uint(bits.TrailingZeros64(ev))
				cPos := blockBase + int(cBit)
				if uint(cPos) < uint(srcLen) && src[cPos] == ':' {
					ev &= ev - 1
					cur = cPos + 1
					inCurrentBlock = true
				} else {
					cur = coldTokStart
					goto handoffGeneral
				}
			} else {
				cPos, found := m.skipSpace(src, cur)
				if !found || src[cPos] != ':' {
					cur = coldTokStart
					goto handoffGeneral
				}
				cur = cPos + 1
			}

			// Locate value start byte.
			var valPos int
			if inCurrentBlock && ev != 0 {
				vBit := uint(bits.TrailingZeros64(ev))
				valPos = blockBase + int(vBit)
			} else {
				var found bool
				valPos, found = m.skipSpace(src, cur)
				if !found {
					phase = PhaseValue
					cur = srcLen
					goto failEOFExit
				}
			}
			if uint(valPos) >= uint(srcLen) {
				phase = PhaseValue
				cur = srcLen
				goto failEOFExit
			}

			var endPos int
			switch src[valPos] {
			case '{', '[':
				ep, perr := kernSkipContainerRelaxed(src, valPos)
				if perr != 0 {
					phase = PhaseValue
					cur = srcLen
					goto failEOFExit
				}
				endPos = ep
			case '"':
				relEnd, perr := kernSkipStringRelaxed(src[valPos+1:])
				if perr != 0 {
					phase = PhaseValue
					cur = srcLen
					goto failEOFExit
				}
				endPos = valPos + 1 + relEnd
			case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				endPos = kernSkipNumberRelaxed(src, valPos+1)
			case 't', 'n':
				if srcLen-valPos < 4 {
					phase = PhaseValue
					cur = srcLen
					goto failEOFExit
				}
				endPos = valPos + 4
			case 'f':
				if srcLen-valPos < 5 {
					phase = PhaseValue
					cur = srcLen
					goto failEOFExit
				}
				endPos = valPos + 5
			default:
				errOff = valPos
				nextCur = valPos + 1
				errCode = ErrInvalidChar
				phase = PhaseValue
				cur = valPos
				goto failErrExit
			}

			cur = endPos
			activePlan = nil
			phase = PhaseObjectAfterVal
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
			blk = src[blockBase:blockEnd]
			cqMask = b.blocks[blkIdx].CloseQuote
			specMask = b.blocks[blkIdx].InsideSpecial
			relOff := uint(endPos - blockBase)
			ev = b.blocks[blkIdx].EventMask &^ ((uint64(1) << relOff) - 1)
			if endPos < srcLen && isScalarContByte[src[endPos]] {
				ev |= uint64(1) << relOff
			}
			if ev != 0 {
				cBit := uint(bits.TrailingZeros64(ev))
				cCh := blk[cBit]
				if int(cCh) == objCommaByte || lookupStructByteTransition(PhaseObjectAfterVal, cCh) == actObjComma {
					ev &= ev - 1
					cur = blockBase + int(cBit) + 1
					phase = PhaseObjectKey
				}
			}
			goto consumeEvents
		}

	advancePastMultiBlockScalar:
		{
			if depth == 0 {
				if !fullDoc {
					phase = PhaseDone
					goto finishDone
				}
				phase = PhaseTrailingSpace
			} else {
				curCount++
				if topArr {
					phase = PhaseArrayAfterVal
				} else {
					phase = PhaseObjectAfterVal
				}
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
			specMask = b.blocks[blkIdx].InsideSpecial
			relOff := uint(cur - blockBase)
			ev = b.blocks[blkIdx].EventMask &^ ((uint64(1) << relOff) - 1)
			if cur < srcLen && isScalarContByte[src[cur]] {
				ev |= uint64(1) << relOff
			}
			goto consumeEvents
		}

	handleSameBlockSpecialValString:
		{
			if src[coldFirstSpec] != '\\' {
				errOff = coldFirstSpec
				nextCur = coldFirstSpec + 1
				errCode = ErrControlChar
				goto failErrExit
			}
			if rejectControl {
				nd, endPos, ok := m.scanPlainColdStringNode(src, coldTokStart, coldFirstSpec, true, rejectLoneSurrogate)
				if !ok || endPos != coldCloseAbs+1 {
					cur = coldTokStart
					goto handoffGeneral
				}
				nodes[nlen] = nd
			} else {
				nd, _, errRel, uerr := m.decodeEscapedStringToUnesc(src, coldTokStart+1, coldFirstSpec, coldCloseAbs, rejectLoneSurrogate, false)
				if uerr != ErrNone {
					if uerr == ErrSpanOverflow {
						errOff = coldTokStart
						nextCur = coldCloseAbs + 1
						errCode = ErrSpanOverflow
						goto failErrExit
					}
					errOff = coldFirstSpec + errRel
					nextCur = errOff + 1
					errCode = uerr
					goto failErrExit
				}
				nodes[nlen] = nd
			}
			nlen++
			statStr++
			cur = coldCloseAbs + 1
			if depth == 0 {
				if fullDoc {
					phase = PhaseTrailingSpace
					goto consumeEvents
				}
				phase = PhaseDone
				goto finishDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			goto consumeEvents
		}

	handleCrossBlockValString:
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
				rawStart := coldTokStart + 1
				rawLen := closeAbs - rawStart
				nodes[nlen] = Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
				nlen++
				endPos = closeAbs + 1
			} else {
				scanFrom := coldTokStart + 1
				if sawSpec {
					scanFrom = firstSpecAbs
				} else if batchEnd > scanFrom {
					scanFrom = batchEnd
				}
				nd, ep, ok := m.scanPlainColdStringNode(src, coldTokStart, scanFrom, rejectControl, rejectLoneSurrogate)
				if !ok {
					cur = coldTokStart
					goto handoffGeneral
				}
				nodes[nlen] = nd
				nlen++
				endPos = ep
			}

			cur = endPos
			statStr++
			if depth == 0 {
				if !fullDoc {
					phase = PhaseDone
					goto finishDone
				}
				phase = PhaseTrailingSpace
			} else {
				curCount++
				if topArr {
					phase = PhaseArrayAfterVal
				} else {
					phase = PhaseObjectAfterVal
				}
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
			blk = src[blockBase:blockEnd]
			cqMask = b.blocks[blkIdx].CloseQuote
			specMask = b.blocks[blkIdx].InsideSpecial
			relAfter := uint(endPos - blockBase)
			ev = b.blocks[blkIdx].EventMask &^ ((uint64(1) << relAfter) - 1)
			goto consumeEvents
		}

	handleLongDecimal:
		{
			nd, ecode := parseValidatedNumberSpanNode(src, coldTokStart, coldNumEnd, deferFloatOverflow || useNumber)
			if ecode != ErrNone {
				cur = coldTokStart
				goto handoffGeneral
			}
			nodes[nlen] = nd
			nlen++
			statNum++
			cur = coldNumEnd
			if coldNumEnd >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if coldNumEnd < srcLen && isScalarContByte[src[coldNumEnd]] {
				ev |= uint64(1) << uint(coldNumEnd-blockBase)
			}
			if depth == 0 {
				if fullDoc {
					phase = PhaseTrailingSpace
					goto consumeEvents
				}
				phase = PhaseDone
				goto finishDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			goto consumeEvents
		}

	handleColdNumber:
		{
			nd, endPos, ok := scanPlainColdNumberNode(src, coldTokStart, useNumber && !coldWantTypedNum, useNumber, deferFloatOverflow || useNumber)
			if !ok {
				cur = coldTokStart
				goto handoffGeneral
			}
			nodes[nlen] = nd
			nlen++
			statNum++
			cur = endPos
			if endPos >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if endPos < srcLen && isScalarContByte[src[endPos]] {
				ev |= uint64(1) << uint(endPos-blockBase)
			}
			if depth == 0 {
				if fullDoc {
					phase = PhaseTrailingSpace
					goto consumeEvents
				}
				phase = PhaseDone
				goto finishDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			goto consumeEvents
		}
	}

finishDone:
	b.clearActive()
	s.PlanDepth = 0
	s.ActivePlan = nil
	g.BitStack[0] = bitWord0
	g.Phase = PhaseDone
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = 0
	g.ParentNodeIdx = -1
	m.NodeLen = nlen
	m.Stat = Stat{
		Object:     statObj,
		Array:      statArr,
		Str:        statStr,
		Number:     statNum,
		ArrayElems: statArrElems,
		ObjectKeys: statObjKeys,
		MaxDepth:   statMaxDepth,
	}
	if g.Profile.ValidateUTF8 {
		return m.finishResult(src, true, StopDone, ErrNone)
	}
	m.diag.recordTapeComplete(cur, nlen, len(m.Unesc), b.diagRole)
	return ScanResult{
		Consumed:    cur,
		Produced:    nlen,
		ValueStart:  valueStart,
		Reason:      StopDone,
		ErrorOffset: -1,
		Code:        ErrNone,
	}

handoffGeneral:
	diagRecordFastHandoff()
	b.clearActive()
	goto handoffGeneralSave

handoffGeneralKeepWindow:
	diagRecordFastHandoff()
	b.publishSchemaWindow(carry, batchStart, batchEnd, nBlocks, cur)

handoffGeneralSave:
	if depth > 0 {
		nodes[parentIdx].Val = parentLink | uint64(curCount)
		s.InlinePlans[depth-1].expectedIdx = topExpectedIdx
	}
	s.PlanDepth = depth
	s.ActivePlan = activePlan
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = parentIdx
	m.NodeLen = nlen
	m.Stat = Stat{
		Object:     statObj,
		Array:      statArr,
		Str:        statStr,
		Number:     statNum,
		ArrayElems: statArrElems,
		ObjectKeys: statObjKeys,
		MaxDepth:   statMaxDepth,
	}
	return ScanResult{Reason: stopHandoffGeneral}

failEOFExit:
	b.clearActive()
	if depth > 0 {
		nodes[parentIdx].Val = parentLink | uint64(curCount)
		s.InlinePlans[depth-1].expectedIdx = topExpectedIdx
	}
	s.PlanDepth = depth
	s.ActivePlan = activePlan
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = parentIdx
	m.NodeLen = nlen
	m.Stat = Stat{
		Object:     statObj,
		Array:      statArr,
		Str:        statStr,
		Number:     statNum,
		ArrayElems: statArrElems,
		ObjectKeys: statObjKeys,
		MaxDepth:   statMaxDepth,
	}
	return m.failEOF(src, true)

failErrExit:
	b.clearActive()
	if depth > 0 {
		nodes[parentIdx].Val = parentLink | uint64(curCount)
		s.InlinePlans[depth-1].expectedIdx = topExpectedIdx
	}
	s.PlanDepth = depth
	s.ActivePlan = activePlan
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = parentIdx
	m.NodeLen = nlen
	m.Stat = Stat{
		Object:     statObj,
		Array:      statArr,
		Str:        statStr,
		Number:     statNum,
		ArrayElems: statArrElems,
		ObjectKeys: statObjKeys,
		MaxDepth:   statMaxDepth,
	}
	return m.failAt(src, true, errOff, nextCur, errCode)
}
