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

package kernels

import (
	"runtime"
	"unsafe"

	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

func assertDisjointLexBatch(src *byte, srcBytes int, dst *types.LexBlockMask, dstBlocks int) {
	if srcBytes <= 0 || dstBlocks <= 0 || src == nil || dst == nil {
		return
	}
	s0 := uintptr(unsafe.Pointer(src))
	s1 := s0 + uintptr(srcBytes)
	d0 := uintptr(unsafe.Pointer(dst))
	d1 := d0 + uintptr(dstBlocks)*types.LexBlockMaskSize
	if s0 < d1 && d0 < s1 {
		panic("sonic: LexBatch input and output block buffer must not overlap")
	}
}

// LexBatch classifies up to len(dst) 64-byte JSON blocks starting at src[pos:]
// into pointer-free LexBlockMask records without tracking container grammar state.
func LexBatch(src string, pos int, dst []types.LexBlockMask, flags types.LexFlags, carry *types.LexBlockCarry) types.LexBatchOut {
	return LexBatchMode(src, pos, dst, flags, carry, false)
}

// LexBatchMode is the unobserved production LexBatch dispatch entrypoint.
// In default release builds (!sonic_witness && !sonic_diag), no producer family
// classification, witness return value, or build-info lookup is compiled or executed.
func LexBatchMode(src string, pos int, dst []types.LexBlockMask, flags types.LexFlags, carry *types.LexBlockCarry, forceScalar bool) types.LexBatchOut {
	size := len(src)
	if pos < 0 || pos > size {
		panic("sonic: invalid LexBatch bounds")
	}
	if pos == size || len(dst) == 0 {
		return types.LexBatchOut{}
	}
	srcPtr := rt.StringData(src)
	dstPtr := rt.SliceData(dst)
	assertDisjointLexBatch(srcPtr, size, dstPtr, len(dst))

	var out types.LexBatchOut
	if forceScalar || cpu.UseScalar {
		types.LexBatchScalarRaw(srcPtr, size, pos, dstPtr, len(dst), uint64(flags), carry, &out)
	} else {
		lexBatch(srcPtr, size, pos, dstPtr, len(dst), uint64(flags), carry, &out)
	}
	runtime.KeepAlive(src)
	runtime.KeepAlive(dst)
	return out
}
