//go:build !sonic_compat && (sonic_witness || sonic_diag) && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package optdec

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/ident"
	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/kernels"
)

// TestF3_MandatoryProducerWitnessContract is compiled ONLY when producer
// instrumentation is active (-tags=sonic_witness or -tags=sonic_diag).
// Mandatory witness gates run this test explicitly so forgetting the build tag
// fails closed immediately rather than silently skipping (§9.4).
func TestF3_MandatoryProducerWitnessContract(t *testing.T) {
	if !jsonscan.WitnessEnabled || !kernels.LexWitnessEnabled {
		t.Fatalf("witness build invariant broken: jsonscan.WitnessEnabled=%v kernels.LexWitnessEnabled=%v",
			jsonscan.WitnessEnabled, kernels.LexWitnessEnabled)
	}
	cur := ident.Current()
	if !cur.Capability.WitnessEnabled {
		t.Fatalf("ident.Current().Capability.WitnessEnabled=false under witness build: %+v", cur)
	}
	if cur.Instrumentation != "witness" && cur.Instrumentation != "diag" && cur.Instrumentation != "witness+diag" {
		t.Fatalf("unexpected ident instrumentation %q under witness build", cur.Instrumentation)
	}

	expNat := expectedDispatchedFamilyFromIdent(false)
	expSca := expectedDispatchedFamilyFromIdent(true)
	if expNat == jsonscan.LexProducerNone || expNat == jsonscan.LexProducerDisabled {
		t.Fatalf("invalid expected outer family for non-forced path: %v (ident=%+v)", expNat, cur)
	}
	if expSca != jsonscan.LexProducerGoScalarForced {
		t.Fatalf("expected forced scalar family go_scalar_forced, got %v", expSca)
	}

	// Verify sonic_scalar / SONIC_MODE=scalar / -race NEVER claim a native_* family (§9.2).
	if cur.EffectiveDispatch == "scalar" {
		if strings.HasPrefix(expNat.String(), "native_") {
			t.Fatalf("scalar effective dispatch must never expect native family: %s (ident=%+v)", expNat, cur)
		}
	} else {
		if !strings.HasPrefix(expNat.String(), "native_") {
			t.Fatalf("native effective dispatch %q expected native_* family, got %s", cur.EffectiveDispatch, expNat)
		}
	}

	type probe struct {
		A int    `json:"a"`
		B string `json:"b"`
		D any    `json:"d"`
	}
	src := `{"a":1,"b":"` + strings.Repeat("x", 96) + `","d":{"k":2}}`
	fixedP := &Parser{
		Document: Document{
			nodes: make([]node, defaultNodesCount, defaultNodesCount),
		},
	}

	for _, forceSca := range []bool{false, true} {
		preMap := map[string]any{}
		dst := probe{D: &preMap}
		var w routeWitness
		if _, _, err := decodeOneWithControl(src, diffStdOpts, &dst, &decodeTestControl{
			forceRep:     repForceIndex,
			forceScalar:  forceSca,
			pinnedParser: fixedP,
			witness:      &w,
		}); err != nil {
			t.Fatalf("witness probe (forceScalar=%v) failed: %v", forceSca, err)
		}
		want := jsonscan.LexProducerBit(expNat)
		if forceSca {
			want = jsonscan.LexProducerBit(expSca)
		}
		if w.ObservedProducers != want || w.PositionProducers != want || w.SubtreeProducers != want {
			t.Fatalf("witness probe (forceScalar=%v) observed main=%v pos=%v sub=%v want=%v (ident=%+v)",
				forceSca, w.ObservedProducers.Names(), w.PositionProducers.Names(), w.SubtreeProducers.Names(), want.Names(), cur)
		}
		if cur.EffectiveDispatch == "scalar" && !forceSca {
			for _, name := range w.ObservedProducers.Names() {
				if strings.HasPrefix(name, "native_") {
					t.Fatalf("scalar build illegally reported native family %q", name)
				}
			}
		}
	}
	t.Logf("WITNESS_CONTRACT_OK instrumentation=%s build_variant=%s dispatch=%s global_family=%s forced_family=%s",
		cur.Instrumentation, cur.BuildVariant, cur.EffectiveDispatch, expNat.String(), expSca.String())
}
