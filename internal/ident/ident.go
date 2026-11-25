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
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/bytedance/sonic/internal/cpu"
)

const (
	EnvExpectJSON = "SONIC_EXPECT_IDENTITY_JSON"
	EnvRecordPath = "SONIC_IDENTITY_RECORD_PATH"
	EnvNonce      = "SONIC_IDENTITY_NONCE"
)

type Capability struct {
	RawAVX2           bool `json:"raw_avx2"`
	RawSSE            bool `json:"raw_sse"`
	RawSSSE3          bool `json:"raw_ssse3"`
	RawPOPCNT         bool `json:"raw_popcnt"`
	RawCLMUL          bool `json:"raw_clmul"`
	ArchHasNEON       bool `json:"arch_has_neon"`
	ArchHasRVV        bool `json:"arch_has_rvv"`
	EffectiveAVX2     bool `json:"effective_avx2"`
	EffectiveSSSE3    bool `json:"effective_ssse3"`
	UseAVX2           bool `json:"use_avx2"`
	UseSSSE3          bool `json:"use_ssse3"`
	UseScalar         bool `json:"use_scalar"`
	RaceEnabled       bool `json:"race_enabled"`
	SonicScalarTag    bool `json:"sonic_scalar_tag"`
	SonicCompatTag    bool `json:"sonic_compat_tag"`
	FutureGoScalarTag bool `json:"future_go_scalar_tag"`
	SonicWitnessTag   bool `json:"sonic_witness_tag"`
	SonicDiagTag      bool `json:"sonic_diag_tag"`
	WitnessEnabled    bool `json:"witness_enabled"`
	DiagEnabled       bool `json:"diag_enabled"`
}

type Tuple struct {
	GOOS              string     `json:"goos"`
	GOARCH            string     `json:"goarch"`
	GoVersion         string     `json:"go_version"`
	SemanticBackend   string     `json:"semantic_backend"`
	BuildVariant      string     `json:"build_variant"`
	Instrumentation   string     `json:"instrumentation"`
	RequestedMode     string     `json:"requested_mode"`
	EffectiveDispatch string     `json:"effective_dispatch"`
	ArchitectureLevel string     `json:"architecture_level"`
	AssemblyPresence  string     `json:"assembly_presence"`
	Capability        Capability `json:"capability"`
	GrammarBackend    string     `json:"grammar_backend"`
}

type Expectation struct {
	SemanticBackend         string   `json:"semantic_backend,omitempty"`
	BuildVariant            string   `json:"build_variant,omitempty"`
	Instrumentation         string   `json:"instrumentation,omitempty"`
	RequireWitnessEnabled   *bool    `json:"require_witness_enabled,omitempty"`
	RequireDiagEnabled      *bool    `json:"require_diag_enabled,omitempty"`
	RequestedMode           string   `json:"requested_mode,omitempty"`
	EffectiveDispatch       string   `json:"effective_dispatch,omitempty"`
	EffectiveDispatchIn     []string `json:"effective_dispatch_in,omitempty"`
	ArchitectureLevel       string   `json:"architecture_level,omitempty"`
	ArchitectureLevelPrefix string   `json:"architecture_level_prefix,omitempty"`
	AssemblyPresence        string   `json:"assembly_presence,omitempty"`
	ForbidAVX2              bool     `json:"forbid_avx2,omitempty"`
	ForbidSSSE3             bool     `json:"forbid_ssse3,omitempty"`
	RequireNoavx2TierByCap  bool     `json:"require_noavx2_tier_by_cap,omitempty"`
}

type ProofRecord struct {
	Nonce    string `json:"nonce"`
	PID      int    `json:"pid"`
	Identity Tuple  `json:"identity"`
}

// Current computes Sonic's actual compile-time + runtime dispatch identity
// strictly from compiled build-tag files and resolved cpu.* state (never echoing
// expected environment variables).
func Current() Tuple {
	variant := "normal"
	switch {
	case tagSonicScalar:
		variant = "sonic_scalar"
	case tagRace:
		variant = "race"
	case tagFutureGoScalar:
		variant = "future_go_scalar"
	}

	dispatch := "compat"
	if semanticBackend == "sonic" {
		if cpu.UseScalar {
			dispatch = "scalar"
		} else {
			dispatch = archNativeDispatch()
		}
	}

	asmPresence := "zero_sonic_asm"
	if semanticBackend == "sonic" && !tagSonicScalar && !tagFutureGoScalar {
		if tagRace {
			asmPresence = "sonic_asm_linked_unused_under_race"
		} else {
			asmPresence = "sonic_asm_compiled"
		}
	}

	instrumentation := "none"
	switch {
	case tagSonicWitness && tagSonicDiag:
		instrumentation = "witness+diag"
	case tagSonicDiag:
		instrumentation = "diag"
	case tagSonicWitness:
		instrumentation = "witness"
	}

	return Tuple{
		GOOS:              runtime.GOOS,
		GOARCH:            runtime.GOARCH,
		GoVersion:         runtime.Version(),
		SemanticBackend:   semanticBackend,
		BuildVariant:      variant,
		Instrumentation:   instrumentation,
		RequestedMode:     cpu.RequestedMode,
		EffectiveDispatch: dispatch,
		ArchitectureLevel: archLevel,
		AssemblyPresence:  asmPresence,
		Capability: Capability{
			RawAVX2:           cpu.RawHasAVX2,
			RawSSE:            cpu.RawHasSSE,
			RawSSSE3:          cpu.RawHasSSSE3,
			RawPOPCNT:         cpu.RawHasPOPCNT,
			RawCLMUL:          cpu.RawHasCLMUL,
			ArchHasNEON:       archHasNEON,
			ArchHasRVV:        archHasRVV,
			EffectiveAVX2:     cpu.HasAVX2,
			EffectiveSSSE3:    cpu.HasSSSE3,
			UseAVX2:           cpu.UseAVX2,
			UseSSSE3:          cpu.UseSSSE3,
			UseScalar:         cpu.UseScalar,
			RaceEnabled:       tagRace,
			SonicScalarTag:    tagSonicScalar,
			SonicCompatTag:    tagSonicCompat,
			FutureGoScalarTag: tagFutureGoScalar,
			SonicWitnessTag:   tagSonicWitness,
			SonicDiagTag:      tagSonicDiag,
			WitnessEnabled:    tagSonicWitness || tagSonicDiag,
			DiagEnabled:       tagSonicDiag,
		},
		GrammarBackend: grammarBackend,
	}
}

func MatchExpectation(actual Tuple, exp Expectation) error {
	if exp.SemanticBackend != "" && actual.SemanticBackend != exp.SemanticBackend {
		return fmt.Errorf("semantic_backend mismatch: actual=%q want=%q", actual.SemanticBackend, exp.SemanticBackend)
	}
	if exp.BuildVariant != "" && actual.BuildVariant != exp.BuildVariant {
		return fmt.Errorf("build_variant mismatch: actual=%q want=%q", actual.BuildVariant, exp.BuildVariant)
	}
	if exp.Instrumentation != "" && actual.Instrumentation != exp.Instrumentation {
		return fmt.Errorf("instrumentation mismatch: actual=%q want=%q", actual.Instrumentation, exp.Instrumentation)
	}
	if exp.RequireWitnessEnabled != nil && actual.Capability.WitnessEnabled != *exp.RequireWitnessEnabled {
		return fmt.Errorf("witness_enabled mismatch: actual=%v want=%v (instrumentation=%q)", actual.Capability.WitnessEnabled, *exp.RequireWitnessEnabled, actual.Instrumentation)
	}
	if exp.RequireDiagEnabled != nil && actual.Capability.DiagEnabled != *exp.RequireDiagEnabled {
		return fmt.Errorf("diag_enabled mismatch: actual=%v want=%v (instrumentation=%q)", actual.Capability.DiagEnabled, *exp.RequireDiagEnabled, actual.Instrumentation)
	}
	if exp.RequestedMode != "" && actual.RequestedMode != exp.RequestedMode {
		return fmt.Errorf("requested_mode mismatch: actual=%q want=%q", actual.RequestedMode, exp.RequestedMode)
	}
	if exp.EffectiveDispatch != "" && actual.EffectiveDispatch != exp.EffectiveDispatch {
		return fmt.Errorf("effective_dispatch mismatch: actual=%q want=%q", actual.EffectiveDispatch, exp.EffectiveDispatch)
	}
	if len(exp.EffectiveDispatchIn) > 0 {
		ok := false
		for _, cand := range exp.EffectiveDispatchIn {
			if actual.EffectiveDispatch == cand {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("effective_dispatch %q not in allowed %v", actual.EffectiveDispatch, exp.EffectiveDispatchIn)
		}
	}
	if exp.ArchitectureLevel != "" && actual.ArchitectureLevel != exp.ArchitectureLevel {
		return fmt.Errorf("architecture_level mismatch: actual=%q want=%q", actual.ArchitectureLevel, exp.ArchitectureLevel)
	}
	if exp.ArchitectureLevelPrefix != "" && !strings.HasPrefix(actual.ArchitectureLevel, exp.ArchitectureLevelPrefix) {
		return fmt.Errorf("architecture_level %q lacks prefix %q", actual.ArchitectureLevel, exp.ArchitectureLevelPrefix)
	}
	if exp.AssemblyPresence != "" && actual.AssemblyPresence != exp.AssemblyPresence {
		return fmt.Errorf("assembly_presence mismatch: actual=%q want=%q", actual.AssemblyPresence, exp.AssemblyPresence)
	}
	if exp.ForbidAVX2 && (actual.Capability.UseAVX2 || actual.EffectiveDispatch == "avx2") {
		return fmt.Errorf("AVX2 forbidden but active: %+v", actual)
	}
	if exp.ForbidSSSE3 && (actual.Capability.UseSSSE3 || actual.EffectiveDispatch == "ssse3") {
		return fmt.Errorf("SSSE3 forbidden but active: %+v", actual)
	}
	if exp.RequireNoavx2TierByCap {
		if actual.Capability.UseAVX2 || actual.Capability.UseScalar {
			return fmt.Errorf("noavx2 expected non-AVX2 non-scalar dispatch, got %+v", actual)
		}
		want := "baseline"
		if actual.Capability.RawSSSE3 {
			want = "ssse3"
		}
		if actual.EffectiveDispatch != want {
			return fmt.Errorf("noavx2 hardware-capability tier mismatch (raw_ssse3=%v): actual=%q want=%q",
				actual.Capability.RawSSSE3, actual.EffectiveDispatch, want)
		}
	}
	return nil
}

// VerifyFromEnv validates Current() against SONIC_EXPECT_IDENTITY_JSON (if set)
// and writes a nonce-bound proof record to SONIC_IDENTITY_RECORD_PATH (if set).
func VerifyFromEnv() error {
	actual := Current()
	rawExp := os.Getenv(EnvExpectJSON)
	if rawExp != "" {
		var exp Expectation
		if err := json.Unmarshal([]byte(rawExp), &exp); err != nil {
			return fmt.Errorf("invalid %s: %w", EnvExpectJSON, err)
		}
		if err := MatchExpectation(actual, exp); err != nil {
			return err
		}
	}
	recPath := os.Getenv(EnvRecordPath)
	if recPath != "" {
		rec := ProofRecord{
			Nonce:    os.Getenv(EnvNonce),
			PID:      os.Getpid(),
			Identity: actual,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		tmp := fmt.Sprintf("%s.tmp.%d", recPath, os.Getpid())
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return fmt.Errorf("write identity proof tmp %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, recPath); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("commit identity proof %s: %w", recPath, err)
		}
	}
	return nil
}
