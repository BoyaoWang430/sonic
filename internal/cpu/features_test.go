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

package cpu

import "testing"

func TestResolveDispatchRawVsEffectiveSeparation(t *testing.T) {
	// 1. Full x86_64 AVX2 host under noavx2 -> falls back to SSSE3, NOT AVX2 and NOT scalar.
	r := resolveDispatch(true, true, true, true, true, "noavx2", false)
	if r.requestedMode != "noavx2" || r.useAVX2 || !r.useSSSE3 || r.useScalar {
		t.Fatalf("unexpected noavx2 with SSSE3: %+v", r)
	}

	// 2. Host WITHOUT SSSE3 under noavx2 -> falls back to baseline (!useSSSE3, !useScalar).
	rNoSSSE3 := resolveDispatch(false, true, false, true, false, "noavx2", false)
	if rNoSSSE3.requestedMode != "noavx2" || rNoSSSE3.useAVX2 || rNoSSSE3.useSSSE3 || rNoSSSE3.useScalar {
		t.Fatalf("unexpected noavx2 on non-SSSE3 host: %+v", rNoSSSE3)
	}

	// 3. nossse3 vs scalar must remain distinct: nossse3 disables AVX2/SSSE3 without setting UseScalar.
	rBase := resolveDispatch(true, true, true, true, true, "nossse3", false)
	if rBase.requestedMode != "nossse3" || rBase.useAVX2 || rBase.useSSSE3 || rBase.useScalar {
		t.Fatalf("nossse3 must not alias scalar: %+v", rBase)
	}
	rScalar := resolveDispatch(true, true, true, true, true, "scalar", false)
	if rScalar.requestedMode != "scalar" || rScalar.useAVX2 || rScalar.useSSSE3 || !rScalar.useScalar {
		t.Fatalf("scalar mode mismatch: %+v", rScalar)
	}

	// 4. Compile-time forced scalar (sonic_scalar / race / go1.28) overrides auto to UseScalar=true.
	rForced := resolveDispatch(true, true, true, true, true, "", true)
	if rForced.requestedMode != "auto" || rForced.useAVX2 || rForced.useSSSE3 || !rForced.useScalar {
		t.Fatalf("forced scalar mismatch: %+v", rForced)
	}
}
