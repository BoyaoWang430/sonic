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
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// isScalarContByte is true for any byte that LexBatch groups into a contiguous
// outside-string scalarBytes run (everything except JSON whitespace, the 6
// structural characters, and '"').
var isScalarContByte = func() [256]bool {
	var t [256]bool
	for i := 0; i < 256; i++ {
		t[i] = true
	}
	for _, c := range []byte{' ', '\t', '\n', '\r', '{', '}', '[', ']', ':', ',', '"'} {
		t[c] = false
	}
	return t
}()

const (
	// plainEmitBlockMaxNodes bounds the maximum number of Node entries a single
	// 64-byte LexBatch block can emit. Every emitted Node corresponds to at
	// least one distinct starting byte within the 64-byte block (including the
	// fused PhaseObjectKey -> ':' -> Value fast path, where key and value occupy
	// distinct bits in ev). Multi-block and cold handlers emit at most one Node
	// for the token starting in the current block and then jump back to
	// consumeEvents, which re-checks remaining writable capacity before any
	// further block events are consumed.
	plainEmitBlockMaxNodes = 64
	plainEmitNodeStride    = unsafe.Sizeof(Node{})
)

func (m *Machine) canEnterPlainFullEmit(src string, final bool) bool {
	g := &m.Grammar
	b := &m.batch
	if !final || g.RootMode != ModeEmit || m.Schema.HasPlan || g.SubModeActive {
		return false
	}
	if g.Profile.ASTCompatLex || g.Profile.BaseDepth != 0 || len(src) > LexMaxWindowBytes || b.capOverride != 0 {
		return false
	}
	if b.active || len(m.Nodes)-m.NodeLen < plainEmitBlockMaxNodes {
		return false
	}
	return g.Phase <= PhaseArrayAfterVal || g.Phase == PhaseTrailingSpace
}

//go:noinline
func (m *Machine) finishPlainDone(src string, cur, valueStart, nlen int, bitWord0 uint64) ScanResult {
	m.batch.clearActive()
	g := &m.Grammar
	g.BitStack[0] = bitWord0
	g.Phase = PhaseDone
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = 0
	g.ParentNodeIdx = -1
	m.NodeLen = nlen
	if g.Profile.ValidateUTF8 {
		return m.finishResult(src, true, StopDone, ErrNone)
	}
	m.diag.recordTapeComplete(cur, nlen, len(m.Unesc), m.batch.diagRole)
	return ScanResult{
		Consumed:    cur,
		Produced:    nlen,
		ValueStart:  valueStart,
		Reason:      StopDone,
		ErrorOffset: -1,
		Code:        ErrNone,
	}
}

//go:noinline
func (m *Machine) handoffPlainGeneral(src string, phase GrammarPhase, cur, valueStart, depth, parentIdx, nlen int, parentLink uint64, curCount uint32, bitWord0 uint64) ScanResult {
	diagRecordFastHandoff()
	m.batch.clearActive()
	if depth > 0 {
		m.Nodes[parentIdx].Val = parentLink | uint64(curCount)
	}
	g := &m.Grammar
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = parentIdx
	m.NodeLen = nlen
	return ScanResult{Reason: stopHandoffGeneral}
}

//go:noinline
func (m *Machine) failPlainEOF(src string, phase GrammarPhase, cur, valueStart, depth, parentIdx, nlen int, parentLink uint64, curCount uint32, bitWord0 uint64) ScanResult {
	m.batch.clearActive()
	if depth > 0 {
		m.Nodes[parentIdx].Val = parentLink | uint64(curCount)
	}
	g := &m.Grammar
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = parentIdx
	m.NodeLen = nlen
	return m.failEOF(src, true)
}

//go:noinline
func (m *Machine) failPlainErr(src string, phase GrammarPhase, cur, valueStart, depth, parentIdx, nlen int, parentLink uint64, curCount uint32, bitWord0 uint64, errOff, nextCur int, errCode ScanError) ScanResult {
	m.batch.clearActive()
	if depth > 0 {
		m.Nodes[parentIdx].Val = parentLink | uint64(curCount)
	}
	g := &m.Grammar
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = parentIdx
	m.NodeLen = nlen
	return m.failAt(src, true, errOff, nextCur, errCode)
}

// stepPlainFullEmit consumes 64-byte structural, quote,
// and scalar-start bitmask batches (LexBlockMask) over the exact same canonical
// transition table (grammar.go) -> Node Tape -> FromDom downstream.
func (m *Machine) stepPlainFullEmit(src string) ScanResult {
	g := &m.Grammar
	b := &m.batch
	nodes := m.Nodes
	// nodesLen is strictly the writable slice length len(m.Nodes) supplied by
	// the caller (excluding any trailing guard slot reserved outside m.Nodes).
	nodesLen := len(nodes)
	nodesPtr := unsafe.Pointer(unsafe.SliceData(nodes))
	nlen := m.NodeLen
	srcLen := len(src)
	srcPtr := unsafe.Pointer(unsafe.StringData(src))

	phase := g.Phase
	cur := g.Cur
	valueStart := g.ValueStart
	depth := g.Depth
	parentIdx := g.ParentNodeIdx

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

	var lexFlags LexFlags
	if g.Profile.RejectControl {
		lexFlags = LexFlagRejectControl
	}

	bitWord0 := g.BitStack[0]
	var parentLink uint64
	var curCount uint32
	var topArr bool
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
	}

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
	var coldNumEnd int
	var coldStrAct structAction
	var coldFirstSpec int
	var coldCloseAbs int

	for {
		if ev == 0 {
			blkIdx++
			for blkIdx < nBlocks {
				blk := &b.blocks[blkIdx]
				ev = blk.EventMask
				if ev != 0 {
					blockBase = batchStart + blkIdx*64
					blockEnd = blockBase + 64
					if blockEnd > batchEnd {
						blockEnd = batchEnd
					}
					cqMask = blk.CloseQuote
					specMask = blk.InsideSpecial
					if valueStart < 0 {
						valueStart = blockBase + bits.TrailingZeros64(ev)
					}
					goto consumeEvents
				}
				blkIdx++
			}
			if batchEnd >= srcLen {
				cur = srcLen
				if phase == PhaseTrailingSpace {
					return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
				}
				return m.failPlainEOF(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
			}
			batchStart = batchEnd
			out := b.fillLexBatch(src, batchStart, b.blocks[:], lexFlags, &carry, b.forceScalar)
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
				blk := &b.blocks[blkIdx]
				ev = blk.EventMask
				if ev != 0 {
					blockBase = batchStart + blkIdx*64
					blockEnd = blockBase + 64
					if blockEnd > batchEnd {
						blockEnd = batchEnd
					}
					cqMask = blk.CloseQuote
					specMask = blk.InsideSpecial
					if valueStart < 0 {
						valueStart = blockBase + bits.TrailingZeros64(ev)
					}
					goto consumeEvents
				}
				blkIdx++
			}
			continue
		}

	consumeEvents:
		// Block-level writable capacity gate: guarantees all Node writes inside
		// this 64-byte block (<= plainEmitBlockMaxNodes) stay strictly within
		// [0, nodesLen), without touching any caller-reserved trailing guard
		// slot. Multi-block/cold handlers emit at most one Node for the current
		// block's token and return here to re-check capacity before continuing.
		if nlen+plainEmitBlockMaxNodes > nodesLen {
			return m.handoffPlainGeneral(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
		}

		// Pure leaf inner loop: ZERO function calls and ZERO per-token bounds checks.
		for ev != 0 {
			bit := uint(bits.TrailingZeros64(ev))
			ev &= ev - 1
			tokStart := blockBase + int(bit)
			ch := *(*byte)(unsafe.Add(srcPtr, tokStart))

			switch lookupStructByteTransition(phase, ch) {
			case actObjKey:
				goto objKeyInBlock

			case actObjColon:
				cur = tokStart + 1
				phase = PhaseValue
				if ev != 0 {
					goto objValueInBlock
				}
				continue

			case actObjComma:
				cur = tokStart + 1
				goto objAfterCommaInBlock

			case actArrComma:
				cur = tokStart + 1
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
				return m.failPlainErr(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, tokStart, tokStart+1, structPhaseError(phase))
			}

		objKeyInBlock:
			{
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
				*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KStringCommon | uint64(tokStart+1)<<PosBits, Val: uint64(relClose - 1)}
				nlen++
				cur = tokStart + int(relClose) + 1
				if ev == 0 {
					phase = PhaseObjectColon
					break
				}
				cBit := uint(bits.TrailingZeros64(ev))
				cPos := blockBase + int(cBit)
				cCh := *(*byte)(unsafe.Add(srcPtr, cPos))
				if int(cCh) != colonByte && lookupStructByteTransition(PhaseObjectColon, cCh) != actObjColon {
					return m.failPlainErr(src, PhaseObjectColon, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, cPos, cPos+1, structPhaseError(PhaseObjectColon))
				}
				ev &= ev - 1
				cur = cPos + 1
				if ev == 0 {
					phase = PhaseValue
					break
				}
			}

		objValueInBlock:
			// Dedicated PhaseValue dispatch site from grammar.go (avoids BTB aliasing
			// with the outer structural loop while maintaining a single grammar authority).
			{
				vBit := uint(bits.TrailingZeros64(ev))
				ev &= ev - 1
				vPos := blockBase + int(vBit)
				vCh := *(*byte)(unsafe.Add(srcPtr, vPos))
				phase = PhaseValue
				bit = vBit
				tokStart = vPos
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
					return m.failPlainErr(src, PhaseValue, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, vPos, vPos+1, structPhaseError(PhaseValue))
				}
			}

		handleValString:
			{
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
				*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KStringCommon | uint64(tokStart+1)<<PosBits, Val: uint64(relClose - 1)}
				nlen++
				m.Stat.Str++
				cur = tokStart + int(relClose) + 1
				goto afterValInline
			}

		handleValNumber:
			{
				if g.Profile.UseNumber {
					q, ok := scanValidNumberFastInt(src, tokStart)
					if !ok {
						q, ok = scanValidNumberSpanSlow(src, tokStart, true)
						if !ok {
							return m.handoffPlainGeneral(src, phase, tokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
						}
					}
					*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KRawNumber | uint64(tokStart)<<PosBits, Val: uint64(q - tokStart)}
					nlen++
					m.Stat.Number++
					cur = q
					if q >= blockEnd {
						goto advancePastMultiBlockScalar
					}
					if q < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, q))] {
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
						goto handleColdNumber
					}
				}
				c0 := *(*byte)(unsafe.Add(srcPtr, q))
				if c0 >= '1' && c0 <= '9' {
					v := uint64(c0 - '0')
					q++
					intDigits := 1
					lim := q + 18
					if lim > srcLen {
						lim = srcLen
					}
					for q < lim {
						d := *(*byte)(unsafe.Add(srcPtr, q)) - '0'
						if d > 9 {
							break
						}
						v = v*10 + uint64(d)
						q++
						intDigits++
					}
					if q < srcLen {
						term := *(*byte)(unsafe.Add(srcPtr, q))
						if term != '.' && term != 'e' && term != 'E' && (term-'0' > 9) {
							if !neg {
								*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KUint | uint64(tokStart)<<PosBits, Val: v}
							} else if v <= 1<<63 {
								*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KSint | uint64(tokStart)<<PosBits, Val: -v}
							} else {
								coldTokStart = tokStart
								goto handleColdNumber
							}
							nlen++
							m.Stat.Number++
							cur = q
							if q >= blockEnd {
								goto advancePastMultiBlockScalar
							}
							if isScalarContByte[term] {
								ev |= uint64(1) << uint(q-blockBase)
							}
							goto afterValInline
						}
						if term == '.' && intDigits <= 15 && q+1 < srcLen && *(*byte)(unsafe.Add(srcPtr, q+1))-'0' <= 9 {
							q++
							fracDigits := 0
							maxFrac := 15 - intDigits
							for fracDigits < maxFrac && q < srcLen {
								d := *(*byte)(unsafe.Add(srcPtr, q)) - '0'
								if d > 9 {
									break
								}
								v = v*10 + uint64(d)
								q++
								fracDigits++
							}
							if fracDigits >= 1 && v>>52 == 0 {
								if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && *(*byte)(unsafe.Add(srcPtr, q)) != 'e' && *(*byte)(unsafe.Add(srcPtr, q)) != 'E' && (*(*byte)(unsafe.Add(srcPtr, q))-'0' > 9)) {
									fv := float64(v)
									if neg {
										fv = -fv
									}
									fv /= exactPow10[fracDigits]
									*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KReal | uint64(tokStart)<<PosBits, Val: math.Float64bits(fv)}
									nlen++
									m.Stat.Number++
									cur = q
									if q >= blockEnd {
										goto advancePastMultiBlockScalar
									}
									if q < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, q))] {
										ev |= uint64(1) << uint(q-blockBase)
									}
									goto afterValInline
								}
							}
							if fracDigits >= 1 {
								for q < srcLen && *(*byte)(unsafe.Add(srcPtr, q))-'0' <= 9 {
									q++
								}
								if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && *(*byte)(unsafe.Add(srcPtr, q)) != 'e' && *(*byte)(unsafe.Add(srcPtr, q)) != 'E') {
									coldTokStart = tokStart
									coldNumEnd = q
									goto handleLongDecimal
								}
							}
						}
					} else if intDigits <= 19 {
						if !neg {
							*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KUint | uint64(tokStart)<<PosBits, Val: v}
						} else if v <= 1<<63 {
							*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KSint | uint64(tokStart)<<PosBits, Val: -v}
						} else {
							coldTokStart = tokStart
							goto handleColdNumber
						}
						nlen++
						m.Stat.Number++
						cur = q
						if q >= blockEnd {
							goto advancePastMultiBlockScalar
						}
						goto afterValInline
					}
					coldTokStart = tokStart
					goto handleColdNumber
				} else if c0 == '0' {
					q++
					if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && *(*byte)(unsafe.Add(srcPtr, q)) != 'e' && *(*byte)(unsafe.Add(srcPtr, q)) != 'E') {
						ndTyp := KUint
						if neg {
							ndTyp = KSint
						}
						*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: ndTyp | uint64(tokStart)<<PosBits, Val: 0}
						nlen++
						m.Stat.Number++
						cur = q
						if q >= blockEnd {
							goto advancePastMultiBlockScalar
						}
						if q < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, q))] {
							ev |= uint64(1) << uint(q-blockBase)
						}
						goto afterValInline
					}
					if q+1 < srcLen && *(*byte)(unsafe.Add(srcPtr, q)) == '.' && *(*byte)(unsafe.Add(srcPtr, q+1))-'0' <= 9 {
						q++
						var v uint64
						fracDigits := 0
						for fracDigits < 15 && q < srcLen {
							d := *(*byte)(unsafe.Add(srcPtr, q)) - '0'
							if d > 9 {
								break
							}
							v = v*10 + uint64(d)
							q++
							fracDigits++
						}
						if fracDigits >= 1 && v>>52 == 0 && (q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && *(*byte)(unsafe.Add(srcPtr, q)) != 'e' && *(*byte)(unsafe.Add(srcPtr, q)) != 'E' && (*(*byte)(unsafe.Add(srcPtr, q))-'0' > 9))) {
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
							*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KReal | uint64(tokStart)<<PosBits, Val: fbits}
							nlen++
							m.Stat.Number++
							cur = q
							if q >= blockEnd {
								goto advancePastMultiBlockScalar
							}
							if q < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, q))] {
								ev |= uint64(1) << uint(q-blockBase)
							}
							goto afterValInline
						}
						if fracDigits >= 1 {
							for q < srcLen && *(*byte)(unsafe.Add(srcPtr, q))-'0' <= 9 {
								q++
							}
							if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && *(*byte)(unsafe.Add(srcPtr, q)) != 'e' && *(*byte)(unsafe.Add(srcPtr, q)) != 'E') {
								coldTokStart = tokStart
								coldNumEnd = q
								goto handleLongDecimal
							}
						}
					}
					coldTokStart = tokStart
					goto handleColdNumber
				} else {
					coldTokStart = tokStart
					goto handleColdNumber
				}
			}

		handleValTrue:
			if tokStart+4 > srcLen || *(*byte)(unsafe.Add(srcPtr, tokStart+1)) != 'r' || *(*byte)(unsafe.Add(srcPtr, tokStart+2)) != 'u' || *(*byte)(unsafe.Add(srcPtr, tokStart+3)) != 'e' {
				return m.handoffPlainGeneral(src, phase, tokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
			}
			*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KTrue | uint64(tokStart)<<PosBits}
			nlen++
			cur = tokStart + 4
			if cur >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
				ev |= uint64(1) << uint(cur-blockBase)
			}
			goto afterValInline

		handleValFalse:
			if tokStart+5 > srcLen || *(*byte)(unsafe.Add(srcPtr, tokStart+1)) != 'a' || *(*byte)(unsafe.Add(srcPtr, tokStart+2)) != 'l' || *(*byte)(unsafe.Add(srcPtr, tokStart+3)) != 's' || *(*byte)(unsafe.Add(srcPtr, tokStart+4)) != 'e' {
				return m.handoffPlainGeneral(src, phase, tokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
			}
			*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KFalse | uint64(tokStart)<<PosBits}
			nlen++
			cur = tokStart + 5
			if cur >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
				ev |= uint64(1) << uint(cur-blockBase)
			}
			goto afterValInline

		handleValNull:
			if tokStart+4 > srcLen || *(*byte)(unsafe.Add(srcPtr, tokStart+1)) != 'u' || *(*byte)(unsafe.Add(srcPtr, tokStart+2)) != 'l' || *(*byte)(unsafe.Add(srcPtr, tokStart+3)) != 'l' {
				return m.handoffPlainGeneral(src, phase, tokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
			}
			*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KNull | uint64(tokStart)<<PosBits}
			nlen++
			cur = tokStart + 4
			if cur >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
				ev |= uint64(1) << uint(cur-blockBase)
			}
			goto afterValInline

		handleValObjOpen:
			{
				nextDepth := depth + 1
				if nextDepth > g.Profile.MaxContainerDepth {
					m.Stat.MaxDepth = uint32(nextDepth)
					return m.failPlainErr(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, tokStart, tokStart+1, ErrDepthOverflow)
				}
				if depth > 0 {
					(*Node)(unsafe.Add(nodesPtr, uintptr(parentIdx)*plainEmitNodeStride)).Val = parentLink | uint64(curCount)
				}
				bitIdx := uint(depth)
				if bitIdx < 64 {
					bitWord0 &^= uint64(1) << bitIdx
				} else {
					g.BitStack[bitIdx>>6] &^= uint64(1) << (bitIdx & 63)
				}
				depth = nextDepth
				if uint32(depth) > m.Stat.MaxDepth {
					m.Stat.MaxDepth = uint32(depth)
				}
				parentLink = packOpenContainerVal(parentIdx)
				idx := nlen
				*(*Node)(unsafe.Add(nodesPtr, uintptr(idx)*plainEmitNodeStride)) = Node{Typ: KObject | uint64(tokStart)<<PosBits, Val: parentLink}
				nlen = idx + 1
				parentIdx = idx
				curCount = 0
				topArr = false
				cur = tokStart + 1
				phase = PhaseObjectOpen
				if ev != 0 {
					kBit := uint(bits.TrailingZeros64(ev))
					kPos := blockBase + int(kBit)
					if int(*(*byte)(unsafe.Add(srcPtr, kPos))) == objKeyOpenByte {
						ev &= ev - 1
						bit = kBit
						tokStart = kPos
						goto objKeyInBlock
					}
				}
				continue
			}

		handleValArrOpen:
			{
				nextDepth := depth + 1
				if nextDepth > g.Profile.MaxContainerDepth {
					m.Stat.MaxDepth = uint32(nextDepth)
					return m.failPlainErr(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, tokStart, tokStart+1, ErrDepthOverflow)
				}
				if depth > 0 {
					(*Node)(unsafe.Add(nodesPtr, uintptr(parentIdx)*plainEmitNodeStride)).Val = parentLink | uint64(curCount)
				}
				bitIdx := uint(depth)
				if bitIdx < 64 {
					bitWord0 |= uint64(1) << bitIdx
				} else {
					g.BitStack[bitIdx>>6] |= uint64(1) << (bitIdx & 63)
				}
				depth = nextDepth
				if uint32(depth) > m.Stat.MaxDepth {
					m.Stat.MaxDepth = uint32(depth)
				}
				parentLink = packOpenContainerVal(parentIdx)
				idx := nlen
				*(*Node)(unsafe.Add(nodesPtr, uintptr(idx)*plainEmitNodeStride)) = Node{Typ: KArray | uint64(tokStart)<<PosBits, Val: parentLink}
				nlen = idx + 1
				parentIdx = idx
				curCount = 0
				topArr = true
				cur = tokStart + 1
				phase = PhaseArrayOpen
				continue
			}

		handleCloseObj:
			cur = tokStart + 1
			m.Stat.Object++
			m.Stat.ObjectKeys += curCount
			{
				span := uint64(nlen - parentIdx)
				(*Node)(unsafe.Add(nodesPtr, uintptr(parentIdx)*plainEmitNodeStride)).Val = (span << ConLenBits) | uint64(curCount)
			}
			parentIdx = unpackOpenParentIdx(parentLink)
			depth--
			if depth > 0 {
				openVal := (*Node)(unsafe.Add(nodesPtr, uintptr(parentIdx)*plainEmitNodeStride)).Val
				parentLink = openVal & 0xFFFFFFFF00000000
				curCount = uint32(openVal)
				pBitIdx := uint(depth - 1)
				if pBitIdx < 64 {
					topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
				} else {
					topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
				}
			}
			goto afterValInline

		handleCloseArr:
			cur = tokStart + 1
			m.Stat.Array++
			m.Stat.ArrayElems += curCount
			{
				span := uint64(nlen - parentIdx)
				(*Node)(unsafe.Add(nodesPtr, uintptr(parentIdx)*plainEmitNodeStride)).Val = (span << ConLenBits) | uint64(curCount)
			}
			parentIdx = unpackOpenParentIdx(parentLink)
			depth--
			if depth > 0 {
				openVal := (*Node)(unsafe.Add(nodesPtr, uintptr(parentIdx)*plainEmitNodeStride)).Val
				parentLink = openVal & 0xFFFFFFFF00000000
				curCount = uint32(openVal)
				pBitIdx := uint(depth - 1)
				if pBitIdx < 64 {
					topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
				} else {
					topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
				}
			}

		afterValInline:
			if depth == 0 {
				if g.Profile.FullDocument {
					phase = PhaseTrailingSpace
					continue
				}
				return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
				if ev == 0 {
					break
				}
				cBit := uint(bits.TrailingZeros64(ev))
				cPos := blockBase + int(cBit)
				cCh := *(*byte)(unsafe.Add(srcPtr, cPos))
				if int(cCh) == arrCommaByte {
					ev &= ev - 1
					cur = cPos + 1
					phase = PhaseValue
					if ev != 0 {
						goto objValueInBlock
					}
					break
				}
				if int(cCh) == arrCloseByte {
					ev &= ev - 1
					bit = cBit
					tokStart = cPos
					goto handleCloseArr
				}
				cAct := lookupStructByteTransition(PhaseArrayAfterVal, cCh)
				if cAct == actArrComma {
					ev &= ev - 1
					cur = cPos + 1
					phase = PhaseValue
					if ev != 0 {
						goto objValueInBlock
					}
					break
				}
				if cAct == actCloseArr {
					ev &= ev - 1
					bit = cBit
					tokStart = cPos
					goto handleCloseArr
				}
				return m.failPlainErr(src, PhaseArrayAfterVal, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, cPos, cPos+1, structPhaseError(PhaseArrayAfterVal))
			}
			phase = PhaseObjectAfterVal
			if ev == 0 {
				break
			}
			{
				sBit := uint(bits.TrailingZeros64(ev))
				sPos := blockBase + int(sBit)
				sCh := *(*byte)(unsafe.Add(srcPtr, sPos))
				if int(sCh) == objCommaByte {
					ev &= ev - 1
					cur = sPos + 1
					goto objAfterCommaInBlock
				}
				if int(sCh) == objCloseByte {
					ev &= ev - 1
					bit = sBit
					tokStart = sPos
					goto handleCloseObj
				}
				sAct := lookupStructByteTransition(PhaseObjectAfterVal, sCh)
				if sAct == actObjComma {
					ev &= ev - 1
					cur = sPos + 1
					goto objAfterCommaInBlock
				}
				if sAct == actCloseObj {
					ev &= ev - 1
					bit = sBit
					tokStart = sPos
					goto handleCloseObj
				}
				return m.failPlainErr(src, PhaseObjectAfterVal, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, sPos, sPos+1, structPhaseError(PhaseObjectAfterVal))
			}

		objAfterCommaInBlock:
			phase = PhaseObjectKey
			if ev != 0 {
				kBit := uint(bits.TrailingZeros64(ev))
				kPos := blockBase + int(kBit)
				if int(*(*byte)(unsafe.Add(srcPtr, kPos))) == objKeyNextByte {
					ev &= ev - 1
					bit = kBit
					tokStart = kPos
					goto objKeyInBlock
				}
			}
			continue
		}
		continue

	advancePastMultiBlockScalar:
		{
			// Scalar ended at cur >= blockEnd. Advance blkIdx/batch to cur BEFORE
			// updating phase for the next token, then re-enter consumeEvents so
			// the block-level writable capacity gate runs again.
			if depth == 0 {
				if !g.Profile.FullDocument {
					return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
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
			blk := &b.blocks[blkIdx]
			cqMask = blk.CloseQuote
			specMask = blk.InsideSpecial
			relOff := uint(cur - blockBase)
			ev = blk.EventMask &^ ((uint64(1) << relOff) - 1)
			if cur < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
				ev |= uint64(1) << relOff
			}
			goto consumeEvents
		}

	handleSameBlockSpecialString:
		{
			if *(*byte)(unsafe.Add(srcPtr, coldFirstSpec)) != '\\' {
				// Raw control byte (< 0x20) inside string.
				return m.failPlainErr(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, coldFirstSpec, coldFirstSpec+1, ErrControlChar)
			}
			if g.Profile.RejectControl {
				nd, endPos, ok := m.scanPlainColdStringNode(src, coldTokStart, coldFirstSpec, true, g.Profile.RejectLoneSurrogate)
				if !ok || endPos != coldCloseAbs+1 {
					return m.handoffPlainGeneral(src, phase, coldTokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
				}
				*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = nd
			} else {
				nd, _, errRel, uerr := m.decodeEscapedStringToUnesc(src, coldTokStart+1, coldFirstSpec, coldCloseAbs, g.Profile.RejectLoneSurrogate, false)
				if uerr != ErrNone {
					if uerr == ErrSpanOverflow {
						return m.failPlainErr(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, coldTokStart, coldCloseAbs+1, ErrSpanOverflow)
					}
					errOff := coldFirstSpec + errRel
					return m.failPlainErr(src, phase, cur, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0, errOff, errOff+1, uerr)
				}
				*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = nd
			}
			nlen++
			cur = coldCloseAbs + 1
			if coldStrAct == actObjKey {
				phase = PhaseObjectColon
				goto consumeEvents
			}
			m.Stat.Str++
			if depth == 0 {
				if g.Profile.FullDocument {
					phase = PhaseTrailingSpace
					goto consumeEvents
				}
				return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
			}
			curCount++
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
				blk := &b.blocks[nb]
				cqm := blk.CloseQuote
				spm := blk.InsideSpecial
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
				*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = Node{Typ: KStringCommon | uint64(rawStart)<<PosBits, Val: uint64(rawLen)}
				nlen++
				endPos = closeAbs + 1
			} else {
				scanFrom := coldTokStart + 1
				if sawSpec {
					scanFrom = firstSpecAbs
				} else if batchEnd > scanFrom {
					scanFrom = batchEnd
				}
				nd, ep, ok := m.scanPlainColdStringNode(src, coldTokStart, scanFrom, g.Profile.RejectControl, g.Profile.RejectLoneSurrogate)
				if !ok {
					return m.handoffPlainGeneral(src, phase, coldTokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
				}
				*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = nd
				nlen++
				endPos = ep
			}

			cur = endPos
			if coldStrAct == actObjKey {
				phase = PhaseObjectColon
			} else {
				m.Stat.Str++
				if depth == 0 {
					if !g.Profile.FullDocument {
						return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
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
			blk := &b.blocks[blkIdx]
			cqMask = blk.CloseQuote
			specMask = blk.InsideSpecial
			relAfter := uint(endPos - blockBase)
			ev = blk.EventMask &^ ((uint64(1) << relAfter) - 1)
			goto consumeEvents
		}

	handleLongDecimal:
		{
			nd, ecode := parseValidatedNumberSpanNode(src, coldTokStart, coldNumEnd, g.Profile.DeferFloatOverflow)
			if ecode != ErrNone {
				return m.handoffPlainGeneral(src, phase, coldTokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
			}
			*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = nd
			nlen++
			m.Stat.Number++
			cur = coldNumEnd
			if coldNumEnd >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if coldNumEnd < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, coldNumEnd))] {
				ev |= uint64(1) << uint(coldNumEnd-blockBase)
			}
			if depth == 0 {
				if g.Profile.FullDocument {
					phase = PhaseTrailingSpace
					goto consumeEvents
				}
				return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
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
			nd, endPos, ok := scanPlainColdNumberNode(src, coldTokStart, g.Profile.UseNumber, g.Profile.UseNumber, g.Profile.DeferFloatOverflow)
			if !ok {
				return m.handoffPlainGeneral(src, phase, coldTokStart, valueStart, depth, parentIdx, nlen, parentLink, curCount, bitWord0)
			}
			*(*Node)(unsafe.Add(nodesPtr, uintptr(nlen)*plainEmitNodeStride)) = nd
			nlen++
			m.Stat.Number++
			cur = endPos
			if endPos >= blockEnd {
				goto advancePastMultiBlockScalar
			}
			if endPos < srcLen && isScalarContByte[*(*byte)(unsafe.Add(srcPtr, endPos))] {
				ev |= uint64(1) << uint(endPos-blockBase)
			}
			if depth == 0 {
				if g.Profile.FullDocument {
					phase = PhaseTrailingSpace
					goto consumeEvents
				}
				return m.finishPlainDone(src, cur, valueStart, nlen, bitWord0)
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
}
