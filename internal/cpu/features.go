/*
 * Copyright 2021 ByteDance Inc.
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

import "os"

var (
	// RawHas* preserve the unmodified host CPU capabilities observed before
	// applying SONIC_MODE or compile-time scalar build overrides.
	RawHasAVX2   bool
	RawHasSSE    bool
	RawHasSSSE3  bool
	RawHasPOPCNT bool
	RawHasCLMUL  bool

	// Has* reflect effective feature availability after SONIC_MODE / build overrides.
	HasAVX2   bool
	HasSSE    bool
	HasSSSE3  bool
	HasPOPCNT bool
	HasCLMUL  bool

	// RequestedMode records the normalized SONIC_MODE ("auto", "noavx2", "nossse3", "scalar").
	RequestedMode     string
	ForcedScalarBuild = forceScalarBuild

	// UseAVX2, UseSSSE3, and UseScalar record the resolved kernel dispatch tier
	// selected once at process init (after applying SONIC_MODE overrides or
	// pure-Go scalar build constraints). Direct //go:noescape assembly entrypoints
	// read package-local mirrors of these booleans rather than indirect Go func
	// pointers so pointer arguments remain non-escaping and avoid ABI0 closure
	// trampolines.
	UseAVX2   bool
	UseSSSE3  bool
	UseScalar bool
)

type resolvedFeatures struct {
	requestedMode string
	hasAVX2       bool
	hasSSE        bool
	hasSSSE3      bool
	hasPOPCNT     bool
	hasCLMUL      bool
	useAVX2       bool
	useSSSE3      bool
	useScalar     bool
}

func resolveDispatch(rawAVX2, rawSSE, rawSSSE3, rawPOPCNT, rawCLMUL bool, modeEnv string, forcedScalar bool) resolvedFeatures {
	r := resolvedFeatures{
		requestedMode: "auto",
		hasAVX2:       rawAVX2,
		hasSSE:        rawSSE,
		hasSSSE3:      rawSSSE3,
		hasPOPCNT:     rawPOPCNT,
		hasCLMUL:      rawCLMUL,
	}
	switch modeEnv {
	case "", "auto":
		r.requestedMode = "auto"
	case "noavx", "noavx2":
		r.requestedMode = "noavx2"
		r.hasAVX2 = false
	case "nossse3":
		r.requestedMode = "nossse3"
		r.hasAVX2 = false
		r.hasSSSE3 = false
	case "scalar":
		r.requestedMode = "scalar"
		r.useScalar = true
		r.hasAVX2 = false
		r.hasSSSE3 = false
	default:
		panic("invalid mode: '" + modeEnv + "', should be one of 'auto', 'noavx', 'noavx2', 'nossse3', 'scalar'")
	}
	if forcedScalar {
		r.useScalar = true
		r.hasAVX2 = false
		r.hasSSSE3 = false
	}
	r.useAVX2 = r.hasAVX2 && r.hasPOPCNT && r.hasCLMUL
	r.useSSSE3 = r.hasSSSE3
	return r
}

func init() {
	RawHasAVX2, RawHasSSE, RawHasSSSE3, RawHasPOPCNT, RawHasCLMUL = detectCPU()
	res := resolveDispatch(RawHasAVX2, RawHasSSE, RawHasSSSE3, RawHasPOPCNT, RawHasCLMUL, os.Getenv("SONIC_MODE"), forceScalarBuild)
	RequestedMode = res.requestedMode
	HasAVX2 = res.hasAVX2
	HasSSE = res.hasSSE
	HasSSSE3 = res.hasSSSE3
	HasPOPCNT = res.hasPOPCNT
	HasCLMUL = res.hasCLMUL
	UseAVX2 = res.useAVX2
	UseSSSE3 = res.useSSSE3
	UseScalar = res.useScalar
}
