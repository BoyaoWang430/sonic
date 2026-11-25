//go:build !sonic_witness && !sonic_diag

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

import "github.com/bytedance/sonic/internal/native/types"

// WitnessEnabled is false in default release builds (!sonic_witness && !sonic_diag).
// Empty observations under!WitnessEnabled represent DISABLED/NOT_COLLECTED.
const WitnessEnabled = false

// lexWitnessState is zero-sized in release builds and placed at a non-trailing
// offset in blockBatchState so it adds zero bytes and zero tail padding.
type lexWitnessState struct{}

func (w *lexWitnessState) reset() {}

func (w *lexWitnessState) observedMask() LexProducerMask { return 0 }

func (b *blockBatchState) fillLexBatch(src string, pos int, dst []types.LexBlockMask, flags types.LexFlags, carry *types.LexBlockCarry, forceScalar bool) types.LexBatchOut {
	return kernLexBatchUnobserved(src, pos, dst, flags, carry, forceScalar)
}

func expectedDispatchedLexFamily(_ bool) types.LexProducer {
	return types.LexProducerDisabled
}
