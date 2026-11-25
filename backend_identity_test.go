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

package sonic

import (
	"testing"

	"github.com/bytedance/sonic/internal/ident"
)

// TestBackendIdentityContract runs under all build configurations (normal,
// sonic_scalar, sonic_compat, -race, checkptr, noavx2, nossse3) so the gate
// runner can verify the compiled binary's actual backend identity and confirm
// via nonce proof that test filtering did not skip identity verification.
func TestBackendIdentityContract(t *testing.T) {
	actual := ident.Current()
	if testSonicCompatBuildTag && actual.SemanticBackend != "encoding_json_compat" {
		t.Fatalf("sonic_compat tag active but semantic_backend=%q", actual.SemanticBackend)
	}
	if testSonicScalarBuildTag && actual.BuildVariant != "sonic_scalar" {
		t.Fatalf("sonic_scalar tag active but build_variant=%q", actual.BuildVariant)
	}
	if err := ident.VerifyFromEnv(); err != nil {
		t.Fatalf("backend identity verification failed: %v (actual=%+v)", err, actual)
	}
}
