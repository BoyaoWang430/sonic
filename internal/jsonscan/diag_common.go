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

// DiagSchemaVersion identifies the diagnostic snapshot schema version (D2 / F4 / H12).
// diag-v3 enforces per-role batch/source attribution (main vs position sidecar vs
// subtree compatibility bridge), consumed source-span deltas (cur - startPos), and
// retires the uncalled dead IndexRestartCount counter.
const DiagSchemaVersion = "diag-v3"

// DiagRole classifies which scanner role produced lexical block work so position
// sidecar builds and dynamic subtree compatibility scans are never conflated
// with primary top-level scan passes.
const (
	DiagRoleMain     uint8 = 0
	DiagRolePosition uint8 = 1
	DiagRoleSubtree  uint8 = 2
)

// DiagSnapshot captures primary lexical batching, index/tape output, cold
// position sidecar construction, dynamic subtree compatibility scans, index
// capacity resumes, and route selection witnesses when built with -tags=sonic_diag.
// In default production builds (!sonic_diag), all hooks compile to empty
// inlined no-ops with zero runtime instructions and zero Machine size overhead.
//
// Note: BytesLexed / PositionScanBytes / SubtreeScanBytes measure bytes classified
// by the 64B LexBatch kernel for each role; they do not represent total CPU bytes
// read by scalar number parsing, unquoting, or target binding.
type DiagSnapshot struct {
	SchemaVersion string
	Enabled       bool

	// Main top-level scan counters (DiagRoleMain only):
	ScanCompletes   uint64
	ScanSourceBytes uint64 // sum of (cur - startPos) per completed/errored main logical scan
	BatchCalls      uint64
	TokensProduced  uint64
	BytesLexed      uint64
	DescriptorBytes uint64
	NodesWritten    uint64
	IndexEntries    uint64
	UnescBytes      uint64

	// Position sidecar scan counters (DiagRolePosition only):
	PositionBuildCount  uint64
	PositionBatchCalls  uint64
	PositionScanBytes   uint64
	PositionSourceBytes uint64
	PositionEntries     uint64

	// Subtree compatibility bridge counters (DiagRoleSubtree only):
	SubtreeScanCount   uint64
	SubtreeBatchCalls  uint64
	SubtreeScanBytes   uint64
	SubtreeSourceBytes uint64

	// Resumable index growth counter:
	IndexResumeCount uint64

	// Route witnesses:
	IndexRouteHits   uint64
	IndexScanHits    uint64
	IndexBarrierHits uint64
	IndexBindHits    uint64
	TapeRouteHits    uint64
	SchemaRouteHits  uint64

	// Fast-path handoff, slow-path consumption, and non-fullDoc ValidateOne overscan diagnostics:
	FastToGeneralHandoffs    uint64
	FastPathResumes          uint64
	GeneralPathBytes         uint64
	ValidateOneLexBytes      uint64
	ValidateOneConsumedBytes uint64
}
