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
	"encoding/binary"
	"math"
	"math/bits"
	"unsafe"
)

// IdxKind identifies the verified token/container kind in the low 4 bits of an
// 8-byte pointer-free ValidatedDoc index entry.
const (
	IdxKindMask uint64 = 0x0f

	IdxNull         uint64 = 0  // 0
	IdxTrue         uint64 = 1  // 1
	IdxFalse        uint64 = 2  // 2
	IdxUintInline   uint64 = 3  // (v << 4) | IdxUintInline, where 0 <= v < (1 << 60), excluding -0
	IdxSintInline   uint64 = 4  // (uint64(sv) << 4) | IdxSintInline, where -(1 << 59) <= sv < 0
	IdxNumberSpan   uint64 = 5  // (uint64(nlen) << 36) | (uint64(tokStart) << 4) | IdxNumberSpan
	IdxStringPlain  uint64 = 6  // (uint64(slen) << 36) | (uint64(rawStart) << 4) | IdxStringPlain
	IdxStringEsc    uint64 = 7  // (uint64(ulen) << 36) | (uint64(uoff) << 4) | IdxStringEsc
	IdxObject       uint64 = 8  // (uint64(nextIdx) << 36) | (uint64(pairCount) << 4) | IdxObject
	IdxArray        uint64 = 9  // (uint64(nextIdx) << 36) | (uint64(elemCount) << 4) | IdxArray
	IdxDecimalExact uint64 = 10 // (v << 10) | (uint64(frac) << 5) | (negBit << 4) | IdxDecimalExact, v < 2^53, 1 <= frac <= 22

	MaxValidatedDocBytes   = 1 << 27 // 128 MiB: guarantees start < 2^32 and nextIdx < 2^28
	minValidatedDstCap     = 64      // guarantees 1 full 64-byte block of events fits without per-token cap checks
	maxIdxInlineUint       = (uint64(1) << 60) - 1
	maxIdxInlineSintMag    = uint64(1) << 59
	maxIdxExactDecimalMant = uint64(1) << 53
	maxIdxExactDecimalFrac = 22
	maxIdxSpanOrNext       = (1 << 28) - 1
	openIdxParentLinkShift = 32

	trueLitTailU24  = uint32('r') | uint32('u')<<8 | uint32('e')<<16
	nullLitTailU24  = uint32('u') | uint32('l')<<8 | uint32('l')<<16
	falseLitTailU32 = uint32('a') | uint32('l')<<8 | uint32('s')<<16 | uint32('e')<<24
)

// ValidatedDoc represents a completely syntax- and escape-validated single JSON
// top-level value together with its compact 8B-per-node structural/lexical index.
// Its unexported `validated` token guarantees external callers cannot forge a
// ValidatedDoc without passing the full validation barrier in ScanValidatedDoc.
type ValidatedDoc struct {
	src        string
	entries    []uint64
	unesc      []byte
	consumed   int
	valueStart int
	validated  bool
}

func (d ValidatedDoc) IsValid() bool     { return d.validated }
func (d ValidatedDoc) Src() string       { return d.src }
func (d ValidatedDoc) Entries() []uint64 { return d.entries }
func (d ValidatedDoc) Unesc() []byte     { return d.unesc }
func (d ValidatedDoc) Consumed() int     { return d.consumed }
func (d ValidatedDoc) ValueStart() int   { return d.valueStart }

// ValidatedScanStatus classifies the outcome of ScanValidatedDocBounded without
// conflating "input exceeds index representation bounds" with "output capacity
// needs growth".
type ValidatedScanStatus uint8

const (
	ValidatedScanComplete ValidatedScanStatus = iota
	ValidatedScanNeedsCapacity
	ValidatedScanNotApplicable
	ValidatedScanInvalidJSON
)

// ValidatedScanOutcome pairs the unambiguous ValidatedScanStatus with ScanResult.
type ValidatedScanOutcome struct {
	Status ValidatedScanStatus
	Result ScanResult
}

// ScanValidatedDoc validates one top-level JSON value starting at m.Grammar.Cur
// in src using the canonical structural transition table (grammar.go) and
// LexBatch block-mask kernel, emitting compact 8-byte index entries into dst.
func (m *Machine) ScanValidatedDoc(src string, dst []uint64) (ValidatedDoc, ScanResult) {
	var doc ValidatedDoc
	res := m.ScanValidatedDocInto(src, dst, &doc)
	return doc, res
}

// ScanValidatedDocInto writes the validated document into *outDoc without copying
// the 88-byte ValidatedDoc struct by value across the call boundary.
// Inputs exceeding MaxValidatedDocBytes return StopError/ErrSpanOverflow (never
// StopNeedOutput) so direct callers cannot enter a non-terminating growth loop.
func (m *Machine) ScanValidatedDocInto(src string, dst []uint64, outDoc *ValidatedDoc) ScanResult {
	out := m.ScanValidatedDocBounded(src, dst, MaxValidatedDocBytes, outDoc)
	if out.Status == ValidatedScanNotApplicable {
		return ScanResult{
			Consumed:    m.Grammar.Cur,
			Produced:    0,
			ValueStart:  m.Grammar.ValueStart,
			Reason:      StopError,
			ErrorOffset: m.Grammar.Cur,
			Code:        ErrSpanOverflow,
		}
	}
	return out.Result
}

// ValidatedScanCheckpoint captures the exact scanner state at the 64-entry
// block capacity boundary so callers can grow dst (copying dst[:elen]) and
// resume without re-lexing or re-unescaping any prefix bytes.
type ValidatedScanCheckpoint struct {
	active     bool
	topArr     bool
	phase      GrammarPhase
	cur        int
	valueStart int
	depth      int
	parentIdx  int
	elen       int
	bitWord0   uint64
	parentLink uint64
	curCount   uint32
	batchStart int
	batchEnd   int
	nBlocks    int
	blkIdx     int
	blockBase  int
	blockEnd   int
	ev         uint64
	cqMask     uint64
	specMask   uint64
}

func (c *ValidatedScanCheckpoint) HasProgress() bool {
	return c != nil && c.active
}

// ScanValidatedDocBounded executes the validated-index barrier scan with an
// explicit maximum source byte limit (clamped to <= MaxValidatedDocBytes).
func (m *Machine) ScanValidatedDocBounded(src string, dst []uint64, maxDocBytes int, outDoc *ValidatedDoc) ValidatedScanOutcome {
	return m.ScanValidatedDocResumable(src, dst, maxDocBytes, nil, outDoc)
}

// ScanValidatedDocResumable executes or resumes the validated-index barrier scan.
// On initial entry (*ckpt == ValidatedScanCheckpoint{}), m must have been initialized
// via ResetValidatedDoc. When ValidatedScanNeedsCapacity is returned with
// ckpt.HasProgress() == true, copying dst[:res.Produced] into a larger slice and
// calling ScanValidatedDocResumable again resumes in place with zero prefix rescan.
func (m *Machine) ScanValidatedDocResumable(
	src string,
	dst []uint64,
	maxDocBytes int,
	ckpt *ValidatedScanCheckpoint,
	outDoc *ValidatedDoc,
) ValidatedScanOutcome {
	*outDoc = ValidatedDoc{}
	g := &m.Grammar
	if g.Profile.BaseDepth < 0 || g.Profile.BaseDepth > g.Profile.MaxContainerDepth {
		g.ErrOffset = g.Cur
		return ValidatedScanOutcome{
			Status: ValidatedScanInvalidJSON,
			Result: m.finishResult(src, true, StopError, ErrDepthOverflow),
		}
	}
	if maxDocBytes <= 0 || maxDocBytes > MaxValidatedDocBytes {
		maxDocBytes = MaxValidatedDocBytes
	}
	srcLen := len(src)
	if g.Cur < 0 || g.Cur > srcLen || srcLen > MaxValidatedDocBytes || srcLen-g.Cur > maxDocBytes {
		return ValidatedScanOutcome{Status: ValidatedScanNotApplicable}
	}
	if len(dst) < minValidatedDstCap || (ckpt != nil && ckpt.active && len(dst) < ckpt.elen+64) {
		cur := g.Cur
		prod := 0
		if ckpt != nil && ckpt.active {
			cur = ckpt.cur
			prod = ckpt.elen
		}
		return ValidatedScanOutcome{
			Status: ValidatedScanNeedsCapacity,
			Result: ScanResult{Consumed: cur, Produced: prod, Reason: StopNeedOutput},
		}
	}
	res := m.scanValidatedDocCore(src, dst, ckpt, outDoc)
	switch res.Reason {
	case StopDone:
		return ValidatedScanOutcome{Status: ValidatedScanComplete, Result: res}
	case StopNeedOutput:
		return ValidatedScanOutcome{Status: ValidatedScanNeedsCapacity, Result: res}
	default:
		return ValidatedScanOutcome{Status: ValidatedScanInvalidJSON, Result: res}
	}
}

func (m *Machine) scanValidatedDocCore(src string, dst []uint64, ckpt *ValidatedScanCheckpoint, outDoc *ValidatedDoc) ScanResult {
	g := &m.Grammar
	b := &m.batch
	srcLen := len(src)
	dstLen := len(dst)

	srcPtr := unsafe.Pointer(unsafe.StringData(src))
	dstPtr := unsafe.Pointer(unsafe.SliceData(dst))

	maxDepthLim := g.Profile.MaxContainerDepth
	var lexFlags LexFlags
	if g.Profile.RejectControl {
		lexFlags = LexFlagRejectControl
	}
	forceScalar := b.forceScalar

	var (
		phase      GrammarPhase
		cur        int
		valueStart int
		depth      int
		parentIdx  int
		elen       int
		bitWord0   uint64
		parentLink uint64
		curCount   uint32
		topArr     bool
		batchStart int
		batchEnd   int
		nBlocks    int
		blkIdx     int
		blockBase  int
		blockEnd   int
		ev         uint64
		cqMask     uint64
		specMask   uint64
	)

	var coldTokStart int
	var coldFirstSpec int
	var coldCloseAbs int
	var coldIsKey bool

	if ckpt != nil && ckpt.active {
		phase = ckpt.phase
		cur = ckpt.cur
		valueStart = ckpt.valueStart
		depth = ckpt.depth
		parentIdx = ckpt.parentIdx
		elen = ckpt.elen
		bitWord0 = ckpt.bitWord0
		parentLink = ckpt.parentLink
		curCount = ckpt.curCount
		topArr = ckpt.topArr
		batchStart = ckpt.batchStart
		batchEnd = ckpt.batchEnd
		nBlocks = ckpt.nBlocks
		blkIdx = ckpt.blkIdx
		blockBase = ckpt.blockBase
		blockEnd = ckpt.blockEnd
		ev = ckpt.ev
		cqMask = ckpt.cqMask
		specMask = ckpt.specMask
		ckpt.active = false
	} else {
		phase = g.Phase
		cur = g.Cur
		valueStart = g.ValueStart
		depth = g.Depth
		parentIdx = -1
		elen = 0
		bitWord0 = g.BitStack[0]
		b.carry = LexBlockCarry{}
		batchStart = cur
		batchEnd = cur
		nBlocks = 0
		blkIdx = 0
	}

	for {
		if ev == 0 {
			blkIdx++
			for blkIdx < nBlocks {
				blk := &b.blocks[blkIdx&15]
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
					goto consumeIdxEvents
				}
				blkIdx++
			}
			if batchEnd >= srcLen {
				return m.finishIdxEOF(src, phase, srcLen, valueStart, depth, bitWord0)
			}
			batchStart = batchEnd
			out := b.fillLexBatch(src, batchStart, b.blocks[:], lexFlags, &b.carry, forceScalar)
			b.calls++
			nBlocks = int(out.BlocksProduced) & 31
			batchEnd = batchStart + int(out.BytesScanned)
			if DiagEnabled {
				var evCount uint64
				for k := 0; k < nBlocks; k++ {
					evCount += uint64(bits.OnesCount64(b.blocks[k&15].EventMask))
				}
				diagRecordLexBlocks(out.BlocksProduced, out.BytesScanned, evCount, b.diagRole)
			}

			blkIdx = 0
			for blkIdx < nBlocks {
				blk := &b.blocks[blkIdx&15]
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
					goto consumeIdxEvents
				}
				blkIdx++
			}
			continue
		}

	consumeIdxEvents:
		if elen+64 > dstLen {
			if ckpt != nil {
				*ckpt = ValidatedScanCheckpoint{
					active:     true,
					topArr:     topArr,
					phase:      phase,
					cur:        cur,
					valueStart: valueStart,
					depth:      depth,
					parentIdx:  parentIdx,
					elen:       elen,
					bitWord0:   bitWord0,
					parentLink: parentLink,
					curCount:   curCount,
					batchStart: batchStart,
					batchEnd:   batchEnd,
					nBlocks:    nBlocks,
					blkIdx:     blkIdx,
					blockBase:  blockBase,
					blockEnd:   blockEnd,
					ev:         ev,
					cqMask:     cqMask,
					specMask:   specMask,
				}
			}
			return ScanResult{
				Consumed: cur,
				Produced: elen,
				Reason:   StopNeedOutput,
			}
		}
		for ev != 0 {
			bit := uint(bits.TrailingZeros64(ev))
			ev &= ev - 1
			tokStart := blockBase + int(bit)
			ch := *(*byte)(unsafe.Add(srcPtr, tokStart))

			if phase == PhaseObjectKey {
				if lookupStructByteTransition(PhaseObjectKey, ch) != actObjKey {
					return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, tokStart, tokStart+1, structPhaseError(PhaseObjectKey))
				}
				cq := cqMask >> bit
				if cq == 0 {
					coldTokStart = tokStart
					coldIsKey = true
					goto handleCrossBlockIdxString
				}
				relClose := uint(bits.TrailingZeros64(cq))
				closeBit := bit + relClose
				between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
				if between != 0 {
					coldTokStart = tokStart
					coldIsKey = true
					coldFirstSpec = blockBase + bits.TrailingZeros64(between)
					coldCloseAbs = blockBase + int(closeBit)
					goto handleSameBlockSpecialIdxString
				}
				*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = (uint64(relClose-1) << 36) | (uint64(tokStart+1) << 4) | IdxStringPlain
				elen++
				cur = tokStart + int(relClose) + 1
				phase = PhaseObjectColon
				if ev == 0 {
					break
				}
				cBit := uint(bits.TrailingZeros64(ev))
				cPos := blockBase + int(cBit)
				if lookupStructByteTransition(PhaseObjectColon, *(*byte)(unsafe.Add(srcPtr, cPos))) != actObjColon {
					continue
				}
				ev &= ev - 1
				cur = cPos + 1
				phase = PhaseValue
				if ev == 0 {
					break
				}
				bit = uint(bits.TrailingZeros64(ev))
				ev &= ev - 1
				tokStart = blockBase + int(bit)
				ch = *(*byte)(unsafe.Add(srcPtr, tokStart))
			}

			act := lookupStructByteTransition(phase, ch)
			switch act {
			case actObjKey:
				cq := cqMask >> bit
				if cq == 0 {
					coldTokStart = tokStart
					coldIsKey = true
					goto handleCrossBlockIdxString
				}
				relClose := uint(bits.TrailingZeros64(cq))
				closeBit := bit + relClose
				between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
				if between != 0 {
					coldTokStart = tokStart
					coldIsKey = true
					coldFirstSpec = blockBase + bits.TrailingZeros64(between)
					coldCloseAbs = blockBase + int(closeBit)
					goto handleSameBlockSpecialIdxString
				}
				*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = (uint64(relClose-1) << 36) | (uint64(tokStart+1) << 4) | IdxStringPlain
				elen++
				cur = tokStart + int(relClose) + 1
				phase = PhaseObjectColon
				if ev != 0 {
					cBit := uint(bits.TrailingZeros64(ev))
					cPos := blockBase + int(cBit)
					if lookupStructByteTransition(PhaseObjectColon, *(*byte)(unsafe.Add(srcPtr, cPos))) == actObjColon {
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
				cq := cqMask >> bit
				if cq == 0 {
					coldTokStart = tokStart
					coldIsKey = false
					goto handleCrossBlockIdxString
				}
				relClose := uint(bits.TrailingZeros64(cq))
				closeBit := bit + relClose
				between := specMask & (((uint64(1) << closeBit) - 1) &^ ((uint64(2) << bit) - 1))
				if between != 0 {
					coldTokStart = tokStart
					coldIsKey = false
					coldFirstSpec = blockBase + bits.TrailingZeros64(between)
					coldCloseAbs = blockBase + int(closeBit)
					goto handleSameBlockSpecialIdxString
				}
				*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = (uint64(relClose-1) << 36) | (uint64(tokStart+1) << 4) | IdxStringPlain
				elen++
				cur = tokStart + int(relClose) + 1
				if cur < blockEnd && depth > 0 {
					nc := *(*byte)(unsafe.Add(srcPtr, cur))
					if !topArr {
						if lookupStructByteTransition(PhaseObjectAfterVal, nc) == actObjComma {
							ev &^= uint64(1) << (closeBit + 1)
							curCount++
							cur++
							phase = PhaseObjectKey
							continue
						}
					} else if lookupStructByteTransition(PhaseArrayAfterVal, nc) == actArrComma {
						ev &^= uint64(1) << (closeBit + 1)
						curCount++
						cur++
						phase = PhaseValue
						continue
					}
				}

			case actValNumber:
				neg := ch == '-'
				q := tokStart
				if neg {
					q++
					if q >= srcLen {
						coldTokStart = tokStart
						goto handleColdIdxNumber
					}
				}
				c0 := *(*byte)(unsafe.Add(srcPtr, q))
				var v uint64
				if c0 >= '1' && c0 <= '9' {
					v = uint64(c0 - '0')
					q++
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
					}
					if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && (*(*byte)(unsafe.Add(srcPtr, q))|0x20) != 'e' && (*(*byte)(unsafe.Add(srcPtr, q))-'0' > 9)) {
						var entry uint64
						if !neg && v <= maxIdxInlineUint {
							entry = (v << 4) | IdxUintInline
						} else if neg && v <= maxIdxInlineSintMag {
							entry = (uint64(-int64(v)) << 4) | IdxSintInline
						} else {
							entry = (uint64(q-tokStart) << 36) | (uint64(tokStart) << 4) | IdxNumberSpan
						}
						*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = entry
						elen++
						cur = q
						goto finishInlineIdxNumber
					}
				} else if c0 == '0' {
					q++
					if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && (*(*byte)(unsafe.Add(srcPtr, q))|0x20) != 'e') {
						var entry uint64
						if !neg {
							entry = IdxUintInline
						} else {
							entry = (uint64(2) << 36) | (uint64(tokStart) << 4) | IdxNumberSpan
						}
						*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = entry
						elen++
						cur = q
						goto finishInlineIdxNumber
					}
				} else {
					coldTokStart = tokStart
					goto handleColdIdxNumber
				}

				if *(*byte)(unsafe.Add(srcPtr, q)) == '.' {
					q++
					if q >= srcLen || *(*byte)(unsafe.Add(srcPtr, q))-'0' > 9 {
						coldTokStart = tokStart
						goto handleColdIdxNumber
					}
					frac := 0
					exactMant := v < maxIdxExactDecimalMant
					for q < srcLen {
						d := *(*byte)(unsafe.Add(srcPtr, q)) - '0'
						if d > 9 {
							break
						}
						frac++
						if exactMant {
							v = v*10 + uint64(d)
							if v >= maxIdxExactDecimalMant || frac > maxIdxExactDecimalFrac {
								exactMant = false
							}
						}
						q++
					}
					if q == srcLen || (*(*byte)(unsafe.Add(srcPtr, q)) != '.' && (*(*byte)(unsafe.Add(srcPtr, q))|0x20) != 'e') {
						var entry uint64
						if exactMant {
							var negBit uint64
							if neg {
								negBit = 1 << 4
							}
							entry = (v << 10) | (uint64(frac) << 5) | negBit | IdxDecimalExact
						} else {
							entry = (uint64(q-tokStart) << 36) | (uint64(tokStart) << 4) | IdxNumberSpan
						}
						*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = entry
						elen++
						cur = q
						goto finishInlineIdxNumber
					}
				}
				coldTokStart = tokStart
				goto handleColdIdxNumber

			finishInlineIdxNumber:
				if cur >= blockEnd {
					goto advanceIdxMultiBlockScalar
				}
				{
					nc := *(*byte)(unsafe.Add(srcPtr, cur))
					if depth > 0 {
						if !topArr {
							if lookupStructByteTransition(PhaseObjectAfterVal, nc) == actObjComma {
								ev &^= uint64(1) << uint(cur-blockBase)
								curCount++
								cur++
								phase = PhaseObjectKey
								continue
							}
						} else if lookupStructByteTransition(PhaseArrayAfterVal, nc) == actArrComma {
							ev &^= uint64(1) << uint(cur-blockBase)
							curCount++
							cur++
							phase = PhaseValue
							continue
						}
					}
					if isScalarContByte[nc] {
						ev |= uint64(1) << uint(cur-blockBase)
					}
				}

			case actValTrue:
				if tokStart+4 > srcLen || (binary.LittleEndian.Uint32((*[4]byte)(unsafe.Add(srcPtr, tokStart))[:])>>8) != trueLitTailU24 {
					return m.delegateIdxErrorAt(src, tokStart, valueStart, depth, bitWord0, phase, false)
				}
				*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = IdxTrue
				elen++
				cur = tokStart + 4
				if cur >= blockEnd {
					goto advanceIdxMultiBlockScalar
				}
				if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
					ev |= uint64(1) << uint(cur-blockBase)
				}

			case actValFalse:
				if tokStart+5 > srcLen || binary.LittleEndian.Uint32((*[4]byte)(unsafe.Add(srcPtr, tokStart+1))[:]) != falseLitTailU32 {
					return m.delegateIdxErrorAt(src, tokStart, valueStart, depth, bitWord0, phase, false)
				}
				*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = IdxFalse
				elen++
				cur = tokStart + 5
				if cur >= blockEnd {
					goto advanceIdxMultiBlockScalar
				}
				if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
					ev |= uint64(1) << uint(cur-blockBase)
				}

			case actValNull:
				if tokStart+4 > srcLen || (binary.LittleEndian.Uint32((*[4]byte)(unsafe.Add(srcPtr, tokStart))[:])>>8) != nullLitTailU24 {
					return m.delegateIdxErrorAt(src, tokStart, valueStart, depth, bitWord0, phase, false)
				}
				*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = IdxNull
				elen++
				cur = tokStart + 4
				if cur >= blockEnd {
					goto advanceIdxMultiBlockScalar
				}
				if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
					ev |= uint64(1) << uint(cur-blockBase)
				}

			case actValObjOpen:
				nextDepth := depth + 1
				if nextDepth > maxDepthLim {
					return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, tokStart, tokStart+1, ErrDepthOverflow)
				}
				if depth > 0 {
					*(*uint64)(unsafe.Add(dstPtr, uintptr(parentIdx)*8)) = parentLink | uint64(curCount)
				}
				bitIdx := uint(depth)
				if bitIdx < 64 {
					bitWord0 &^= uint64(1) << bitIdx
				} else {
					g.BitStack[bitIdx>>6] &^= uint64(1) << (bitIdx & 63)
				}
				depth = nextDepth
				parentLink = uint64(uint32(parentIdx+1)) << openIdxParentLinkShift
				idx := elen
				*(*uint64)(unsafe.Add(dstPtr, uintptr(idx)*8)) = parentLink
				elen = idx + 1
				parentIdx = idx
				curCount = 0
				topArr = false
				cur = tokStart + 1
				phase = PhaseObjectOpen
				continue

			case actValArrOpen:
				nextDepth := depth + 1
				if nextDepth > maxDepthLim {
					return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, tokStart, tokStart+1, ErrDepthOverflow)
				}
				if depth > 0 {
					*(*uint64)(unsafe.Add(dstPtr, uintptr(parentIdx)*8)) = parentLink | uint64(curCount)
				}
				bitIdx := uint(depth)
				if bitIdx < 64 {
					bitWord0 |= uint64(1) << bitIdx
				} else {
					g.BitStack[bitIdx>>6] |= uint64(1) << (bitIdx & 63)
				}
				depth = nextDepth
				parentLink = uint64(uint32(parentIdx+1)) << openIdxParentLinkShift
				idx := elen
				*(*uint64)(unsafe.Add(dstPtr, uintptr(idx)*8)) = parentLink
				elen = idx + 1
				parentIdx = idx
				curCount = 0
				topArr = true
				cur = tokStart + 1
				phase = PhaseArrayOpen
				continue

			case actCloseObj:
				cur = tokStart + 1
				*(*uint64)(unsafe.Add(dstPtr, uintptr(parentIdx)*8)) = (uint64(elen) << 36) | (uint64(curCount) << 4) | IdxObject
				parentIdx = int(uint32(parentLink>>openIdxParentLinkShift)) - 1
				depth--
				if depth > 0 {
					openVal := *(*uint64)(unsafe.Add(dstPtr, uintptr(parentIdx)*8))
					parentLink = openVal & 0xFFFFFFFF00000000
					curCount = uint32(openVal)
					pBitIdx := uint(depth - 1)
					if pBitIdx < 64 {
						topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
					} else {
						topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
					}
				}

			case actCloseArr:
				cur = tokStart + 1
				*(*uint64)(unsafe.Add(dstPtr, uintptr(parentIdx)*8)) = (uint64(elen) << 36) | (uint64(curCount) << 4) | IdxArray
				parentIdx = int(uint32(parentLink>>openIdxParentLinkShift)) - 1
				depth--
				if depth > 0 {
					openVal := *(*uint64)(unsafe.Add(dstPtr, uintptr(parentIdx)*8))
					parentLink = openVal & 0xFFFFFFFF00000000
					curCount = uint32(openVal)
					pBitIdx := uint(depth - 1)
					if pBitIdx < 64 {
						topArr = (bitWord0 & (uint64(1) << pBitIdx)) != 0
					} else {
						topArr = (g.BitStack[pBitIdx>>6] & (uint64(1) << (pBitIdx & 63))) != 0
					}
				}

			default: // actError
				if (phase == PhaseValue || phase == PhaseArrayOpen) && valueStart < 0 {
					valueStart = tokStart
				}
				return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, tokStart, tokStart+1, structPhaseError(phase))
			}

			if depth == 0 {
				goto finishIdxDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
				if ev != 0 {
					cBit := uint(bits.TrailingZeros64(ev))
					cPos := blockBase + int(cBit)
					if lookupStructByteTransition(PhaseArrayAfterVal, *(*byte)(unsafe.Add(srcPtr, cPos))) == actArrComma {
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
					if lookupStructByteTransition(PhaseObjectAfterVal, *(*byte)(unsafe.Add(srcPtr, cPos))) == actObjComma {
						ev &= ev - 1
						cur = cPos + 1
						phase = PhaseObjectKey
					}
				}
			}
		}
		continue

	advanceIdxMultiBlockScalar:
		{
			if depth == 0 {
				goto finishIdxDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			if cur >= batchEnd {
				ev = 0
				blkIdx = nBlocks
				batchEnd = cur
				b.carry = LexBlockCarry{}
				continue
			}
			blkIdx = (cur - batchStart) >> 6
			blk := &b.blocks[blkIdx&15]
			blockBase = batchStart + blkIdx*64
			blockEnd = blockBase + 64
			if blockEnd > batchEnd {
				blockEnd = batchEnd
			}
			cqMask = blk.CloseQuote
			specMask = blk.InsideSpecial
			relOff := uint(cur - blockBase)
			ev = blk.EventMask &^ ((uint64(1) << relOff) - 1)
			if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, cur))] {
				ev |= uint64(1) << relOff
			}
			goto consumeIdxEvents
		}

	handleSameBlockSpecialIdxString:
		{
			entry, errOff, nextCur, errCode, delegated, delRes := m.scanIdxSameBlockSpecialStr(
				src, coldTokStart, coldFirstSpec, coldCloseAbs, valueStart, depth, bitWord0, phase, coldIsKey,
			)
			if delegated {
				return delRes
			}
			if errCode != ErrNone {
				return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, errOff, nextCur, errCode)
			}
			*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = entry
			elen++
			cur = coldCloseAbs + 1
			if coldIsKey {
				phase = PhaseObjectColon
				goto consumeIdxEvents
			}
			if depth == 0 {
				goto finishIdxDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			goto consumeIdxEvents
		}

	handleCrossBlockIdxString:
		{
			entry, endPos, errOff, nextCur, errCode, delegated, delRes := m.scanIdxCrossBlockStr(
				src, coldTokStart, blockBase, batchStart, batchEnd, blkIdx, nBlocks, specMask,
				valueStart, depth, bitWord0, phase, coldIsKey,
			)
			if delegated {
				return delRes
			}
			if errCode != ErrNone {
				return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, errOff, nextCur, errCode)
			}
			*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = entry
			elen++
			cur = endPos
			if coldIsKey {
				phase = PhaseObjectColon
			} else {
				if depth == 0 {
					goto finishIdxDone
				}
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
				b.carry = LexBlockCarry{}
				continue
			}
			blkIdx = (endPos - batchStart) >> 6
			blk := &b.blocks[blkIdx&15]
			blockBase = batchStart + blkIdx*64
			blockEnd = blockBase + 64
			if blockEnd > batchEnd {
				blockEnd = batchEnd
			}
			cqMask = blk.CloseQuote
			specMask = blk.InsideSpecial
			relAfter := uint(endPos - blockBase)
			ev = blk.EventMask &^ ((uint64(1) << relAfter) - 1)
			goto consumeIdxEvents
		}

	handleColdIdxNumber:
		{
			entry, endPos, errOff, nextCur, errCode, lexFailed, failRes := m.scanIdxColdNumber(
				src, coldTokStart, valueStart, depth, bitWord0,
			)
			if lexFailed {
				return failRes
			}
			if errCode != ErrNone {
				return m.finishIdxError(src, phase, cur, valueStart, depth, bitWord0, errOff, nextCur, errCode)
			}
			*(*uint64)(unsafe.Add(dstPtr, uintptr(elen)*8)) = entry
			elen++
			cur = endPos
			if endPos >= blockEnd {
				goto advanceIdxMultiBlockScalar
			}
			if isScalarContByte[*(*byte)(unsafe.Add(srcPtr, endPos))] {
				ev |= uint64(1) << uint(endPos-blockBase)
			}
			if depth == 0 {
				goto finishIdxDone
			}
			curCount++
			if topArr {
				phase = PhaseArrayAfterVal
			} else {
				phase = PhaseObjectAfterVal
			}
			goto consumeIdxEvents
		}
	}

finishIdxDone:
	if b.active {
		b.clearActive()
	}
	g.BitStack[0] = bitWord0
	g.Phase = PhaseDone
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = 0
	m.diag.recordIndexComplete(cur, elen, len(m.Unesc))
	*outDoc = ValidatedDoc{
		src:        src,
		entries:    dst[:elen],
		unesc:      m.Unesc,
		consumed:   cur,
		valueStart: valueStart,
		validated:  true,
	}
	return ScanResult{
		Consumed:    cur,
		Produced:    elen,
		ValueStart:  valueStart,
		Reason:      StopDone,
		ErrorOffset: -1,
		Code:        ErrNone,
	}
}

//go:noinline
func (m *Machine) finishIdxEOF(src string, phase GrammarPhase, cur, valueStart, depth int, bitWord0 uint64) ScanResult {
	m.batch.clearActive()
	g := &m.Grammar
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	return m.failEOF(src, true)
}

//go:noinline
func (m *Machine) finishIdxError(
	src string,
	phase GrammarPhase,
	cur, valueStart, depth int,
	bitWord0 uint64,
	errOff, nextCur int,
	errCode ScanError,
) ScanResult {
	m.batch.clearActive()
	g := &m.Grammar
	g.BitStack[0] = bitWord0
	g.Phase = phase
	g.Cur = cur
	g.ValueStart = valueStart
	g.Depth = depth
	return m.failAt(src, true, errOff, nextCur, errCode)
}

//go:noinline
func (m *Machine) scanIdxSameBlockSpecialStr(
	src string,
	coldTokStart, coldFirstSpec, coldCloseAbs, valueStart, depth int,
	bitWord0 uint64,
	phase GrammarPhase,
	isKey bool,
) (entry uint64, errOff, nextCur int, errCode ScanError, delegated bool, delRes ScanResult) {
	if src[coldFirstSpec] != '\\' {
		return 0, coldFirstSpec, coldFirstSpec + 1, ErrControlChar, false, ScanResult{}
	}
	rejectControl := m.Grammar.Profile.RejectControl
	rejectLoneSurrogate := m.Grammar.Profile.RejectLoneSurrogate
	if rejectControl {
		nd, endPos, ok := m.scanPlainColdStringNode(src, coldTokStart, coldFirstSpec, true, rejectLoneSurrogate)
		if !ok || endPos != coldCloseAbs+1 {
			return 0, 0, 0, ErrNone, true, m.delegateIdxErrorAt(src, coldTokStart, valueStart, depth, bitWord0, phase, isKey)
		}
		uoff, ulen, okU := UnpackEscapedSpan(nd.Val)
		if !okU || uoff > math.MaxUint32 || ulen > maxIdxSpanOrNext {
			return 0, coldTokStart, coldCloseAbs + 1, ErrSpanOverflow, false, ScanResult{}
		}
		return (uint64(ulen) << 36) | (uint64(uoff) << 4) | IdxStringEsc, 0, 0, ErrNone, false, ScanResult{}
	}
	nd, _, errRel, uerr := m.decodeEscapedStringToUnesc(src, coldTokStart+1, coldFirstSpec, coldCloseAbs, rejectLoneSurrogate, false)
	if uerr != ErrNone {
		if uerr == ErrSpanOverflow {
			return 0, coldTokStart, coldCloseAbs + 1, ErrSpanOverflow, false, ScanResult{}
		}
		eoff := coldFirstSpec + errRel
		return 0, eoff, eoff + 1, uerr, false, ScanResult{}
	}
	uoff, ulen, _ := UnpackEscapedSpan(nd.Val)
	if uoff > math.MaxUint32 || ulen > maxIdxSpanOrNext {
		return 0, coldTokStart, coldCloseAbs + 1, ErrSpanOverflow, false, ScanResult{}
	}
	return (uint64(ulen) << 36) | (uint64(uoff) << 4) | IdxStringEsc, 0, 0, ErrNone, false, ScanResult{}
}

//go:noinline
func (m *Machine) scanIdxCrossBlockStr(
	src string,
	coldTokStart, blockBase, batchStart, batchEnd, blkIdx, nBlocks int,
	specMask uint64,
	valueStart, depth int,
	bitWord0 uint64,
	phase GrammarPhase,
	isKey bool,
) (entry uint64, endPos, errOff, nextCur int, errCode ScanError, delegated bool, delRes ScanResult) {
	b := &m.batch
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
		blk := &b.blocks[nb&15]
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

	if foundClose && !sawSpec {
		closeAbs := batchStart + closeBlk*64 + int(closeBit)
		rawStart := coldTokStart + 1
		rawLen := closeAbs - rawStart
		return (uint64(rawLen) << 36) | (uint64(rawStart) << 4) | IdxStringPlain, closeAbs + 1, 0, 0, ErrNone, false, ScanResult{}
	}

	scanFrom := coldTokStart + 1
	if sawSpec {
		scanFrom = firstSpecAbs
	} else if batchEnd > scanFrom {
		scanFrom = batchEnd
	}
	rejectControl := m.Grammar.Profile.RejectControl
	rejectLoneSurrogate := m.Grammar.Profile.RejectLoneSurrogate
	nd, ep, ok := m.scanPlainColdStringNode(src, coldTokStart, scanFrom, rejectControl, rejectLoneSurrogate)
	if !ok {
		return 0, 0, 0, 0, ErrNone, true, m.delegateIdxErrorAt(src, coldTokStart, valueStart, depth, bitWord0, phase, isKey)
	}
	if (nd.Typ & TypeMask) == KStringCommon {
		rawStart := int(nd.Typ >> PosBits)
		rawLen := int(nd.Val)
		return (uint64(rawLen) << 36) | (uint64(rawStart) << 4) | IdxStringPlain, ep, 0, 0, ErrNone, false, ScanResult{}
	}
	uoff, ulen, okU := UnpackEscapedSpan(nd.Val)
	if !okU || uoff > math.MaxUint32 || ulen > maxIdxSpanOrNext {
		return 0, 0, coldTokStart, ep, ErrSpanOverflow, false, ScanResult{}
	}
	return (uint64(ulen) << 36) | (uint64(uoff) << 4) | IdxStringEsc, ep, 0, 0, ErrNone, false, ScanResult{}
}

//go:noinline
func (m *Machine) scanIdxColdNumber(
	src string,
	coldTokStart, valueStart, depth int,
	bitWord0 uint64,
) (entry uint64, endPos, errOff, nextCur int, errCode ScanError, lexFailed bool, failRes ScanResult) {
	ep, perr := kernScanNumberPrefix(src, coldTokStart)
	if perr != 0 || ep-coldTokStart > maxIdxSpanOrNext {
		lexRes := LexNumberAt(src, coldTokStart, true)
		if !lexRes.Complete {
			m.batch.clearActive()
			g := &m.Grammar
			g.BitStack[0] = bitWord0
			g.Cur = coldTokStart
			g.ValueStart = valueStart
			g.Depth = depth
			return 0, 0, 0, 0, ErrNone, true, m.failNumberLex(src, true, lexRes.Fault)
		}
		return 0, 0, coldTokStart, ep, ErrSpanOverflow, false, ScanResult{}
	}
	return (uint64(ep-coldTokStart) << 36) | (uint64(coldTokStart) << 4) | IdxNumberSpan, ep, 0, 0, ErrNone, false, ScanResult{}
}

//go:noinline
func (m *Machine) delegateIdxErrorAt(
	src string,
	coldTokStart, valueStart, depth int,
	bitWord0 uint64,
	phase GrammarPhase,
	isKey bool,
) ScanResult {
	m.batch.clearActive()
	g := &m.Grammar
	g.Profile.ValidateEscapes = true
	g.Profile.FullDocument = false
	g.Profile.EscapeHTMLObserve = false
	g.Profile.ASTCompatLex = false
	g.RootMode = ModeEmit
	g.BitStack[0] = bitWord0
	g.Cur = coldTokStart
	g.ValueStart = valueStart
	g.Depth = depth
	g.ParentNodeIdx = -1
	g.ErrOffset = -1
	g.Phase = phase
	m.Lex.reset()
	m.pending.reset()
	m.sink = nil
	m.sinkErr = nil
	if isKey {
		m.Lex.beginString(coldTokStart)
		g.Cur = coldTokStart + 1
		g.Phase = PhaseKeyString
	}
	var scratch [8]Node
	m.Nodes = scratch[:]
	m.NodeLen = 0
	res := m.stepGeneral(src, true)
	m.Nodes = nil
	return res
}

// ResolveValidatedEntryPos returns the exact Node.Position() byte offset in d.Src()
// for entry index targetIdx on cold single-shot callers, delegating to the shared
// pure-position consumer BuildValidatedSourceSpans.
// Request-scoped binders with multiple lookups must build and reuse the sidecar
// once per top-level value rather than calling ResolveValidatedEntryPos in a loop.
func ResolveValidatedEntryPos(d ValidatedDoc, targetIdx int, startOffset int) int {
	if !d.validated || targetIdx < 0 || targetIdx >= len(d.entries) {
		return 0
	}
	e := d.entries[targetIdx]
	kind := e & IdxKindMask
	switch kind {
	case IdxNumberSpan, IdxStringPlain:
		return int(uint32(e >> 4))
	}
	if targetIdx == 0 && d.valueStart >= 0 {
		if kind == IdxStringEsc {
			return d.valueStart + 1
		}
		return d.valueStart
	}
	spans, err := BuildValidatedSourceSpans(d, startOffset, false, false, false, nil)
	if err != nil || targetIdx >= len(spans) {
		return 0
	}
	return spans[targetIdx].EntryPosition(kind)
}
