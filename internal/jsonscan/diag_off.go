//go:build !sonic_diag

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

const DiagEnabled = false

// diagMachineState is zero-sized in production builds (!sonic_diag) and placed
// at a non-trailing offset in Machine so it adds zero bytes and zero tail padding.
type diagMachineState struct{}

func (d *diagMachineState) reset(_ int)                             {}
func (d *diagMachineState) setStartPos(_ int)                       {}
func (d *diagMachineState) recordTapeComplete(_, _, _ int, _ uint8) {}
func (d *diagMachineState) recordIndexComplete(_, _, _ int)         {}

func ResetDiag() {}

func ReadDiag() DiagSnapshot {
	return DiagSnapshot{SchemaVersion: DiagSchemaVersion, Enabled: false}
}

func diagRecordLexBlocks(_, _, _ uint64, _ uint8) {}

func diagRecordPositionBuild(_ int) {}

func DiagRecordSubtreeScan(_ int) {}

func DiagRecordIndexResume() {}

func DiagRecordIndexRoute() {}

func DiagRecordIndexScan() {}

func DiagRecordIndexBarrier() {}

func DiagRecordIndexBind() {}

func DiagRecordTapeRoute() {}

func DiagRecordSchemaRoute() {}

func diagRecordFastHandoff() {}

func diagRecordFastResume() {}

func diagRecordGeneralBytes(_ int) {}

func diagRecordValidateOne(_ uint64, _ int) {}
