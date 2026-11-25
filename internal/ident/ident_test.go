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

package ident

import (
	"path/filepath"
	"testing"
)

func TestCurrentTupleCoherenceAndExpectationMatching(t *testing.T) {
	cur := Current()
	if cur.SemanticBackend != "sonic" && cur.SemanticBackend != "encoding_json_compat" {
		t.Fatalf("unexpected semantic_backend: %+v", cur)
	}
	if cur.RequestedMode == "" || cur.EffectiveDispatch == "" || cur.ArchitectureLevel == "" {
		t.Fatalf("incomplete identity tuple: %+v", cur)
	}

	// Matching against own actual attributes must pass.
	wantWitness := cur.Capability.WitnessEnabled
	wantDiag := cur.Capability.DiagEnabled
	if err := MatchExpectation(cur, Expectation{
		SemanticBackend:       cur.SemanticBackend,
		BuildVariant:          cur.BuildVariant,
		Instrumentation:       cur.Instrumentation,
		RequireWitnessEnabled: &wantWitness,
		RequireDiagEnabled:    &wantDiag,
		RequestedMode:         cur.RequestedMode,
		EffectiveDispatch:     cur.EffectiveDispatch,
	}); err != nil {
		t.Fatalf("self match failed: %v", err)
	}

	// Instrumentation coherence & mismatch rejection.
	wantInstr := "none"
	switch {
	case cur.Capability.SonicWitnessTag && cur.Capability.SonicDiagTag:
		wantInstr = "witness+diag"
	case cur.Capability.SonicDiagTag:
		wantInstr = "diag"
	case cur.Capability.SonicWitnessTag:
		wantInstr = "witness"
	}
	if cur.Instrumentation != wantInstr {
		t.Fatalf("instrumentation coherence mismatch: got=%q want=%q cap=%+v", cur.Instrumentation, wantInstr, cur.Capability)
	}
	if cur.Capability.WitnessEnabled != (cur.Capability.SonicWitnessTag || cur.Capability.SonicDiagTag) {
		t.Fatalf("witness_enabled coherence mismatch: cap=%+v", cur.Capability)
	}
	if cur.Capability.DiagEnabled != cur.Capability.SonicDiagTag {
		t.Fatalf("diag_enabled coherence mismatch: cap=%+v", cur.Capability)
	}
	wrongInstr := "witness"
	if cur.Instrumentation == wrongInstr {
		wrongInstr = "none"
	}
	if err := MatchExpectation(cur, Expectation{Instrumentation: wrongInstr}); err == nil {
		t.Fatalf("expected mismatch error for instrumentation=%q", wrongInstr)
	}
	oppositeWitness := !cur.Capability.WitnessEnabled
	if err := MatchExpectation(cur, Expectation{RequireWitnessEnabled: &oppositeWitness}); err == nil {
		t.Fatalf("expected mismatch error for require_witness_enabled=%v", oppositeWitness)
	}

	// Deliberate mismatch must fail (proving MatchExpectation never rubber-stamps).
	wrongSemantic := "encoding_json_compat"
	if cur.SemanticBackend == wrongSemantic {
		wrongSemantic = "sonic"
	}
	if err := MatchExpectation(cur, Expectation{SemanticBackend: wrongSemantic}); err == nil {
		t.Fatalf("expected mismatch error for semantic_backend=%q", wrongSemantic)
	}

	// Synthetic noavx2 check on both SSSE3 and non-SSSE3 capabilities.
	withSSSE3 := Tuple{
		SemanticBackend:   "sonic",
		BuildVariant:      "normal",
		RequestedMode:     "noavx2",
		EffectiveDispatch: "ssse3",
		Capability:        Capability{RawSSSE3: true, EffectiveSSSE3: true, UseSSSE3: true},
	}
	if err := MatchExpectation(withSSSE3, Expectation{ForbidAVX2: true, RequireNoavx2TierByCap: true}); err != nil {
		t.Fatalf("expected SSSE3 host noavx2 to pass: %v", err)
	}
	withoutSSSE3 := Tuple{
		SemanticBackend:   "sonic",
		BuildVariant:      "normal",
		RequestedMode:     "noavx2",
		EffectiveDispatch: "baseline",
		Capability:        Capability{RawSSSE3: false},
	}
	if err := MatchExpectation(withoutSSSE3, Expectation{ForbidAVX2: true, RequireNoavx2TierByCap: true}); err != nil {
		t.Fatalf("expected non-SSSE3 host noavx2 baseline fallback to pass: %v", err)
	}
	badNoavx2 := Tuple{
		SemanticBackend:   "sonic",
		BuildVariant:      "normal",
		RequestedMode:     "noavx2",
		EffectiveDispatch: "avx2",
		Capability:        Capability{RawSSSE3: true, UseAVX2: true},
	}
	if err := MatchExpectation(badNoavx2, Expectation{ForbidAVX2: true, RequireNoavx2TierByCap: true}); err == nil {
		t.Fatalf("expected active AVX2 under noavx2 to fail")
	}

	// Verify proof file emission via VerifyFromEnv.
	proofFile := filepath.Join(t.TempDir(), "proof.json")
	t.Setenv(EnvNonce, "nonce-123")
	t.Setenv(EnvRecordPath, proofFile)
	t.Setenv(EnvExpectJSON, `{"semantic_backend":"`+cur.SemanticBackend+`"}`)
	if err := VerifyFromEnv(); err != nil {
		t.Fatalf("VerifyFromEnv failed: %v", err)
	}
}
