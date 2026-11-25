//go:build sonic_diag

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

import "sync/atomic"

const DiagEnabled = true

// diagMachineState tracks the per-logical-scan source origin and single-completion
// guard exclusively in -tags=sonic_diag builds.
type diagMachineState struct {
	startPos  int
	completed bool
}

func (d *diagMachineState) reset(startPos int) {
	d.startPos = startPos
	d.completed = false
}

func (d *diagMachineState) setStartPos(startPos int) {
	d.startPos = startPos
}

func (d *diagMachineState) span(cur int) int {
	if cur > d.startPos {
		return cur - d.startPos
	}
	return 0
}

func (d *diagMachineState) recordTapeComplete(cur, nodesWritten, unescBytes int, role uint8) {
	if d.completed {
		return
	}
	d.completed = true
	span := d.span(cur)
	switch role {
	case DiagRolePosition:
		if span > 0 {
			diagPositionSourceBytes.Add(uint64(span))
		}
	case DiagRoleSubtree:
		if span > 0 {
			diagSubtreeSourceBytes.Add(uint64(span))
		}
	default:
		diagScanCompletes.Add(1)
		if span > 0 {
			diagScanSourceBytes.Add(uint64(span))
		}
		if nodesWritten > 0 {
			diagNodesWritten.Add(uint64(nodesWritten))
		}
		if unescBytes > 0 {
			diagUnescBytes.Add(uint64(unescBytes))
		}
	}
}

func (d *diagMachineState) recordIndexComplete(cur, entriesWritten, unescBytes int) {
	if d.completed {
		return
	}
	d.completed = true
	span := d.span(cur)
	diagScanCompletes.Add(1)
	if span > 0 {
		diagScanSourceBytes.Add(uint64(span))
	}
	if entriesWritten > 0 {
		diagIndexEntries.Add(uint64(entriesWritten))
	}
	if unescBytes > 0 {
		diagUnescBytes.Add(uint64(unescBytes))
	}
}

var (
	diagScanCompletes   atomic.Uint64
	diagScanSourceBytes atomic.Uint64
	diagBatchCalls      atomic.Uint64
	diagTokensProduced  atomic.Uint64
	diagBytesLexed      atomic.Uint64
	diagDescriptorBytes atomic.Uint64
	diagNodesWritten    atomic.Uint64
	diagIndexEntries    atomic.Uint64
	diagUnescBytes      atomic.Uint64

	diagPositionBuildCount  atomic.Uint64
	diagPositionBatchCalls  atomic.Uint64
	diagPositionScanBytes   atomic.Uint64
	diagPositionSourceBytes atomic.Uint64
	diagPositionEntries     atomic.Uint64

	diagSubtreeScanCount   atomic.Uint64
	diagSubtreeBatchCalls  atomic.Uint64
	diagSubtreeScanBytes   atomic.Uint64
	diagSubtreeSourceBytes atomic.Uint64

	diagIndexResumeCount atomic.Uint64

	diagIndexRouteHits   atomic.Uint64
	diagIndexScanHits    atomic.Uint64
	diagIndexBarrierHits atomic.Uint64
	diagIndexBindHits    atomic.Uint64
	diagTapeRouteHits    atomic.Uint64
	diagSchemaRouteHits  atomic.Uint64

	diagFastToGeneralHandoffs    atomic.Uint64
	diagFastPathResumes          atomic.Uint64
	diagGeneralPathBytes         atomic.Uint64
	diagValidateOneLexBytes      atomic.Uint64
	diagValidateOneConsumedBytes atomic.Uint64
)

func ResetDiag() {
	diagScanCompletes.Store(0)
	diagScanSourceBytes.Store(0)
	diagBatchCalls.Store(0)
	diagTokensProduced.Store(0)
	diagBytesLexed.Store(0)
	diagDescriptorBytes.Store(0)
	diagNodesWritten.Store(0)
	diagIndexEntries.Store(0)
	diagUnescBytes.Store(0)

	diagPositionBuildCount.Store(0)
	diagPositionBatchCalls.Store(0)
	diagPositionScanBytes.Store(0)
	diagPositionSourceBytes.Store(0)
	diagPositionEntries.Store(0)

	diagSubtreeScanCount.Store(0)
	diagSubtreeBatchCalls.Store(0)
	diagSubtreeScanBytes.Store(0)
	diagSubtreeSourceBytes.Store(0)

	diagIndexResumeCount.Store(0)

	diagIndexRouteHits.Store(0)
	diagIndexScanHits.Store(0)
	diagIndexBarrierHits.Store(0)
	diagIndexBindHits.Store(0)
	diagTapeRouteHits.Store(0)
	diagSchemaRouteHits.Store(0)

	diagFastToGeneralHandoffs.Store(0)
	diagFastPathResumes.Store(0)
	diagGeneralPathBytes.Store(0)
	diagValidateOneLexBytes.Store(0)
	diagValidateOneConsumedBytes.Store(0)
}

func ReadDiag() DiagSnapshot {
	return DiagSnapshot{
		SchemaVersion:            DiagSchemaVersion,
		Enabled:                  true,
		ScanCompletes:            diagScanCompletes.Load(),
		ScanSourceBytes:          diagScanSourceBytes.Load(),
		BatchCalls:               diagBatchCalls.Load(),
		TokensProduced:           diagTokensProduced.Load(),
		BytesLexed:               diagBytesLexed.Load(),
		DescriptorBytes:          diagDescriptorBytes.Load(),
		NodesWritten:             diagNodesWritten.Load(),
		IndexEntries:             diagIndexEntries.Load(),
		UnescBytes:               diagUnescBytes.Load(),
		PositionBuildCount:       diagPositionBuildCount.Load(),
		PositionBatchCalls:       diagPositionBatchCalls.Load(),
		PositionScanBytes:        diagPositionScanBytes.Load(),
		PositionSourceBytes:      diagPositionSourceBytes.Load(),
		PositionEntries:          diagPositionEntries.Load(),
		SubtreeScanCount:         diagSubtreeScanCount.Load(),
		SubtreeBatchCalls:        diagSubtreeBatchCalls.Load(),
		SubtreeScanBytes:         diagSubtreeScanBytes.Load(),
		SubtreeSourceBytes:       diagSubtreeSourceBytes.Load(),
		IndexResumeCount:         diagIndexResumeCount.Load(),
		IndexRouteHits:           diagIndexRouteHits.Load(),
		IndexScanHits:            diagIndexScanHits.Load(),
		IndexBarrierHits:         diagIndexBarrierHits.Load(),
		IndexBindHits:            diagIndexBindHits.Load(),
		TapeRouteHits:            diagTapeRouteHits.Load(),
		SchemaRouteHits:          diagSchemaRouteHits.Load(),
		FastToGeneralHandoffs:    diagFastToGeneralHandoffs.Load(),
		FastPathResumes:          diagFastPathResumes.Load(),
		GeneralPathBytes:         diagGeneralPathBytes.Load(),
		ValidateOneLexBytes:      diagValidateOneLexBytes.Load(),
		ValidateOneConsumedBytes: diagValidateOneConsumedBytes.Load(),
	}
}

func diagRecordLexBlocks(blocksProduced, bytesScanned, eventsConsumed uint64, role uint8) {
	switch role {
	case DiagRolePosition:
		diagPositionBatchCalls.Add(1)
		diagPositionScanBytes.Add(bytesScanned)
	case DiagRoleSubtree:
		diagSubtreeBatchCalls.Add(1)
		diagSubtreeScanBytes.Add(bytesScanned)
	default:
		diagBatchCalls.Add(1)
		diagTokensProduced.Add(eventsConsumed)
		diagBytesLexed.Add(bytesScanned)
		diagDescriptorBytes.Add(blocksProduced * 24)
	}
}

func diagRecordPositionBuild(entries int) {
	diagPositionBuildCount.Add(1)
	if entries > 0 {
		diagPositionEntries.Add(uint64(entries))
	}
}

func DiagRecordSubtreeScan(_ int) {
	diagSubtreeScanCount.Add(1)
}

func DiagRecordIndexResume()  { diagIndexResumeCount.Add(1) }
func DiagRecordIndexRoute()   { diagIndexRouteHits.Add(1) }
func DiagRecordIndexScan()    { diagIndexScanHits.Add(1) }
func DiagRecordIndexBarrier() { diagIndexBarrierHits.Add(1) }
func DiagRecordIndexBind()    { diagIndexBindHits.Add(1) }
func DiagRecordTapeRoute()    { diagTapeRouteHits.Add(1) }
func DiagRecordSchemaRoute()  { diagSchemaRouteHits.Add(1) }

func diagRecordFastHandoff() { diagFastToGeneralHandoffs.Add(1) }
func diagRecordFastResume()  { diagFastPathResumes.Add(1) }

func diagRecordGeneralBytes(n int) {
	if n > 0 {
		diagGeneralPathBytes.Add(uint64(n))
	}
}

func diagRecordValidateOne(lexBytes uint64, consumedBytes int) {
	if lexBytes > 0 {
		diagValidateOneLexBytes.Add(lexBytes)
	}
	if consumedBytes > 0 {
		diagValidateOneConsumedBytes.Add(uint64(consumedBytes))
	}
}
