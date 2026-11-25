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
	"strings"
	"testing"
)

// TestF3_MachinePolicyLifecycleAndClearActivePreservation deterministically verifies
// on the exact same Machine instance that:
//  1. New request resets clear prior forceScalar / diagRole / capOverride / witness state.
//  2. Mid-scan clearActive() (cross-block strings, cold numbers, capacity resume) NEVER
//     clears request-scoped forceScalar or diagRole (§7.1–§7.4).
//  3. ReleasePoolRefs clears request policy before returning to a pool.
//  4. Under !sonic_witness && !sonic_diag, WitnessEnabled==false and observations stay
//     DISABLED (0 / LexProducerDisabled) while ALL functional lifecycle checks still run.
//     Under sonic_witness || sonic_diag, observed outer families match exact requested policy.
func TestF3_MachinePolicyLifecycleAndClearActivePreservation(t *testing.T) {
	if !WitnessEnabled {
		if got := ExpectedDispatchedLexFamily(false); got != LexProducerDisabled {
			t.Fatalf("default build ExpectedDispatchedLexFamily(false) got %v want %v", got, LexProducerDisabled)
		}
		if got := ExpectedDispatchedLexFamily(true); got != LexProducerDisabled {
			t.Fatalf("default build ExpectedDispatchedLexFamily(true) got %v want %v", got, LexProducerDisabled)
		}
	}
	wantNat := LexProducerBit(ExpectedDispatchedLexFamily(false))
	wantSca := LexProducerBit(LexProducerGoScalarForced)

	// Payload with cross-block escaped string (>128 bytes), cold multi-digit decimal/exponent,
	// and >200 array elements to trigger both clearActive() and multiple R2-G capacity resumes.
	var sb strings.Builder
	sb.WriteString(`{"long":"`)
	sb.WriteString(strings.Repeat(`abc\n`, 48)) // 240 bytes -> crosses multiple 64B blocks with escapes
	sb.WriteString(`","num":1.2345678901234567890123456789e+120,"arr":[`)
	for i := 0; i < 240; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("12345")
	}
	sb.WriteString(`]}`)
	payload := sb.String()

	var m Machine

	// 1. Same Machine: true -> false -> true -> true -> false on ResetValidatedDoc + ScanValidatedDocResumable.
	seq := []bool{true, false, true, true, false}
	for step, reqScalar := range seq {
		m.ResetValidatedDoc(true, false, 0)
		// ResetValidatedDoc must clear any sticky forceScalar from the previous request!
		if m.ForceScalar() {
			t.Fatalf("step %d: ResetValidatedDoc left sticky forceScalar=true", step)
		}
		m.SetForceScalar(reqScalar)

		dst := make([]uint64, 64) // forces multiple R2-G capacity resumes
		var ckpt ValidatedScanCheckpoint
		var vdoc ValidatedDoc
		resumes := 0
		for {
			out := m.ScanValidatedDocResumable(payload, dst, MaxValidatedDocBytes, &ckpt, &vdoc)
			if out.Status == ValidatedScanNeedsCapacity {
				grown := make([]uint64, len(dst)*2)
				copy(grown, dst[:out.Result.Produced])
				dst = grown
				resumes++
				if m.ForceScalar() != reqScalar {
					t.Fatalf("step %d resume %d: forceScalar drifted to %v (want %v)", step, resumes, m.ForceScalar(), reqScalar)
				}
				continue
			}
			if out.Status != ValidatedScanComplete || !vdoc.IsValid() {
				t.Fatalf("step %d unexpected scan status %v res=%+v", step, out.Status, out.Result)
			}
			break
		}
		if resumes == 0 {
			t.Fatalf("step %d expected >=1 capacity resume on initial cap=64", step)
		}
		gotMask := m.ObservedLexProducers()
		if WitnessEnabled {
			wantMask := wantNat
			if reqScalar {
				wantMask = wantSca
			}
			if gotMask != wantMask {
				t.Fatalf("step %d (forceScalar=%v) outer family mismatch: got=%v want=%v (calls=%d resumes=%d)",
					step, reqScalar, gotMask.Names(), wantMask.Names(), m.LexBatchCalls(), resumes)
			}
		} else if gotMask != 0 {
			t.Fatalf("step %d default build (!sonic_witness && !sonic_diag) unexpectedly recorded mask=%v", step, gotMask.Names())
		}

		// Verify BuildValidatedSourceSpansObserved matches functional spans AND witness mode!
		spans, posMask, err := BuildValidatedSourceSpansObserved(vdoc, 0, true, false, reqScalar, nil)
		if err != nil || len(spans) != len(vdoc.Entries()) {
			t.Fatalf("step %d sidecar build failed: err=%v len=%d/%d", step, err, len(spans), len(vdoc.Entries()))
		}
		if WitnessEnabled {
			wantMask := wantNat
			if reqScalar {
				wantMask = wantSca
			}
			if posMask != wantMask {
				t.Fatalf("step %d sidecar family mismatch (forceScalar=%v): got=%v want=%v",
					step, reqScalar, posMask.Names(), wantMask.Names())
			}
		} else if posMask != 0 {
			t.Fatalf("step %d default build sidecar unexpectedly recorded mask=%v", step, posMask.Names())
		}
	}

	// 2. Direct clearActive() during active scan must NOT clear forceScalar or diagRole.
	m.ResetFastEmit(false, true, false, true, true, 0)
	m.SetDiagRole(DiagRoleSubtree)
	m.SetBatchCapForTest(2)
	m.batch.clearActive()
	if !m.batch.forceScalar || m.batch.diagRole != DiagRoleSubtree || m.batch.capOverride != 2 {
		t.Fatalf("clearActive() corrupted request policy: forceScalar=%v diagRole=%d capOverride=%d",
			m.batch.forceScalar, m.batch.diagRole, m.batch.capOverride)
	}
	m.Nodes = make([]Node, 4)
	for {
		res := m.Step(payload, true)
		if res.Reason == StopNeedOutput {
			grown := make([]Node, len(m.Nodes)*4)
			copy(grown, m.Nodes[:res.Produced])
			m.Nodes = grown
			continue
		}
		if res.Reason != StopDone {
			t.Fatalf("Step with capOverride=2 failed: %+v", res)
		}
		break
	}
	if WitnessEnabled {
		if got := m.ObservedLexProducers(); got != wantSca {
			t.Fatalf("mid-scan clearActive/handoff drifted family under forceScalar=true: got=%v want=%v",
				got.Names(), wantSca.Names())
		}
	} else if got := m.ObservedLexProducers(); got != 0 {
		t.Fatalf("default build unexpectedly recorded mask=%v", got.Names())
	}

	// 3. ReleasePoolRefs MUST clear request-scoped policy (forceScalar, diagRole, capOverride, witness).
	m.ReleasePoolRefs()
	if m.batch.forceScalar || m.batch.diagRole != DiagRoleMain || m.batch.capOverride != 0 || m.ObservedLexProducers() != 0 {
		t.Fatalf("ReleasePoolRefs leaked policy state: forceScalar=%v diagRole=%d capOverride=%d producers=%v",
			m.batch.forceScalar, m.batch.diagRole, m.batch.capOverride, m.ObservedLexProducers().Names())
	}

	// 4. Subsequent ResetFastEmit(forceScalar=false) on the exact same Machine runs default family.
	m.ResetFastEmit(false, true, false, true, false, 0)
	m.Nodes = make([]Node, 1024)
	res := m.Step(payload, true)
	if res.Reason != StopDone {
		t.Fatalf("ResetFastEmit(false) failed: %+v", res)
	}
	if WitnessEnabled {
		if got := m.ObservedLexProducers(); got != wantNat {
			t.Fatalf("post-ReleasePoolRefs native scan family mismatch: got=%v want=%v",
				got.Names(), wantNat.Names())
		}
	} else if got := m.ObservedLexProducers(); got != 0 {
		t.Fatalf("default build unexpectedly recorded mask=%v", got.Names())
	}
}
