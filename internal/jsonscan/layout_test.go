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
	"runtime"
	"testing"
	"unsafe"
)

// TestMachineLayoutAndEmptyStatePlacement verifies zero-sized field placement and Machine layout:
//   - In !sonic_diag builds, diagMachineState is zero-sized AND placed at a
//     non-trailing offset in Machine (identical offset to the immediately
//     following non-empty field Grammar), eliminating Go's trailing zero-sized
//     field padding.
//   - In !sonic_witness && !sonic_diag builds, lexWitnessState is zero-sized
//     AND placed at a non-trailing offset in blockBatchState (before calls),
//     preventing trailing padding.
//   - Logs concrete per-toolchain / per-GOARCH sizes for every build tag combo.
func TestMachineLayoutAndEmptyStatePlacement(t *testing.T) {
	szMachine := unsafe.Sizeof(Machine{})
	alignMachine := unsafe.Alignof(Machine{})
	offDiag := unsafe.Offsetof(Machine{}.diag)
	offGrammar := unsafe.Offsetof(Machine{}.Grammar)
	szDiag := unsafe.Sizeof(diagMachineState{})

	szBatch := unsafe.Sizeof(blockBatchState{})
	offWitness := unsafe.Offsetof(blockBatchState{}.witness)
	offCalls := unsafe.Offsetof(blockBatchState{}.calls)
	szWitness := unsafe.Sizeof(lexWitnessState{})

	t.Logf("MACHINE_LAYOUT go=%s goarch=%s diag=%v witness=%v sizeof(Machine)=%d align(Machine)=%d off(diag)=%d off(Grammar)=%d sizeof(diag)=%d sizeof(batch)=%d off(witness)=%d off(calls)=%d sizeof(witness)=%d",
		runtime.Version(), runtime.GOARCH, DiagEnabled, WitnessEnabled,
		szMachine, alignMachine, offDiag, offGrammar, szDiag,
		szBatch, offWitness, offCalls, szWitness)

	if !DiagEnabled {
		if szDiag != 0 {
			t.Fatalf("expected sizeof(diagMachineState)==0 under !sonic_diag, got %d", szDiag)
		}
		if offDiag != offGrammar {
			t.Fatalf("diagMachineState must sit immediately before Grammar with zero gap in !sonic_diag: off(diag)=%d off(Grammar)=%d", offDiag, offGrammar)
		}
		if offDiag >= szMachine {
			t.Fatalf("diagMachineState placed at trailing offset %d >= sizeof(Machine) %d", offDiag, szMachine)
		}
	} else {
		if szDiag == 0 {
			t.Fatalf("expected non-zero diagMachineState under sonic_diag")
		}
	}

	if !WitnessEnabled {
		if szWitness != 0 {
			t.Fatalf("expected sizeof(lexWitnessState)==0 under !sonic_witness && !sonic_diag, got %d", szWitness)
		}
		offDiagRole := unsafe.Offsetof(blockBatchState{}.diagRole)
		szDiagRole := unsafe.Sizeof(blockBatchState{}.diagRole)
		if offWitness != offDiagRole+szDiagRole {
			t.Fatalf("lexWitnessState must sit immediately after diagRole with zero added gap: off(witness)=%d want=%d",
				offWitness, offDiagRole+szDiagRole)
		}
		if offWitness >= szBatch || offWitness >= offCalls {
			t.Fatalf("lexWitnessState must sit strictly before trailing field calls: off(witness)=%d off(calls)=%d sizeof(batch)=%d",
				offWitness, offCalls, szBatch)
		}
	} else {
		if szWitness == 0 {
			t.Fatalf("expected non-zero lexWitnessState under sonic_witness/sonic_diag")
		}
	}
}
