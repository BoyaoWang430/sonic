//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

import "github.com/bytedance/sonic/internal/jsonscan"

// decodeRepChoice allows internal differential/regression tests to pin a specific
// representation under identical public options without mutating global state
// or abusing semantic flags like OptionCaseSensitive.
type decodeRepChoice uint8

const (
	repAuto decodeRepChoice = iota
	repForceIndex
	repForceTape
)

// routeWitness records exact per-request path execution counts for tests and
// fuzz workers with zero shared state and zero production hot-path overhead.
type routeWitness struct {
	IndexRouteSelected   uint32
	IndexScanStarted     uint32
	IndexBarrierPassed   uint32
	IndexBinderEntered   uint32
	IndexSubtreeFallback uint32
	IndexSubtreeBytes    uint64
	IndexFallbackToTape  uint32
	IndexCapacityGrows   uint32
	PositionBuilds       uint32
	TapeRouteSelected    uint32
	SchemaRouteSelected  uint32
	RequestedForceScalar bool
	ObservedProducers    jsonscan.LexProducerMask
	PositionProducers    jsonscan.LexProducerMask
	SubtreeProducers     jsonscan.LexProducerMask
}

// decodeTestControl carries optional request-scoped test parameters and witness
// hooks. Production DecodeOne and DecodeWithSession never allocate or pass it.
type decodeTestControl struct {
	maxIdxDocBytes   int // >0 overrides MaxValidatedDocBytes (clamped <= MaxValidatedDocBytes)
	initialIdxCap    int // >0 overrides initial idxEntries slice cap for growth boundary tests
	maxIdxEntryBound int // >0 overrides hard upper entry bound for bounded termination tests
	simulateNoGrow   bool
	forceRep         decodeRepChoice
	forceScalar      bool
	pinnedParser     *Parser // optional fixed Parser instance for deterministic reuse tests
	witness          *routeWitness
}

func (c *decodeTestControl) effectiveMaxIdxDocBytes() int {
	if c != nil && c.maxIdxDocBytes > 0 && c.maxIdxDocBytes < jsonscan.MaxValidatedDocBytes {
		return c.maxIdxDocBytes
	}
	return jsonscan.MaxValidatedDocBytes
}
