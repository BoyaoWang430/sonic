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

package kernels

import (
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// LexWitnessEnabled is true when compiled with -tags=sonic_witness or -tags=sonic_diag.
const LexWitnessEnabled = true

var riscv64NativeLexFamily = detectRISCV64NativeLexFamily()

func detectRISCV64NativeLexFamily() types.LexProducer {
	if runtime.GOARCH != "riscv64" {
		return types.LexProducerNone
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "GORISCV64" && (s.Value == "rva23u64" || strings.HasPrefix(s.Value, "rva23u64,")) {
				return types.LexProducerAsmRISCV64RVV
			}
		}
	}
	return types.LexProducerAsmRISCV64Zbb
}

func nativeLexBatchFamily() types.LexProducer {
	switch runtime.GOARCH {
	case "amd64":
		if cpu.UseAVX2 {
			return types.LexProducerAsmAMD64AVX2
		}
		if cpu.UseSSSE3 {
			return types.LexProducerAsmAMD64SSSE3
		}
		return types.LexProducerAsmAMD64SSE2
	case "arm64":
		return types.LexProducerAsmARM64NEON
	case "riscv64":
		return riscv64NativeLexFamily
	default:
		return types.LexProducerNone
	}
}

// ExpectedDispatchedLexFamily reports the outer implementation family expected
// for forceScalar under current build tags and CPU dispatch state.
func ExpectedDispatchedLexFamily(forceScalar bool) types.LexProducer {
	if forceScalar {
		return types.LexProducerGoScalarForced
	}
	if cpu.UseScalar {
		if cpu.ForcedScalarBuild {
			return types.LexProducerGoScalarBuild
		}
		return types.LexProducerGoScalarGlobal
	}
	return nativeLexBatchFamily()
}

// LexBatchModeObserved executes the exact same LexBatch dispatch algorithm as
// LexBatchMode and reports the outer dispatched implementation family at the
// Go/kernel boundary.
func LexBatchModeObserved(src string, pos int, dst []types.LexBlockMask, flags types.LexFlags, carry *types.LexBlockCarry, forceScalar bool) (types.LexBatchOut, types.LexProducer) {
	size := len(src)
	if pos < 0 || pos > size {
		panic("sonic: invalid LexBatch bounds")
	}
	if pos == size || len(dst) == 0 {
		return types.LexBatchOut{}, types.LexProducerNone
	}
	srcPtr := rt.StringData(src)
	dstPtr := rt.SliceData(dst)
	assertDisjointLexBatch(srcPtr, size, dstPtr, len(dst))

	var out types.LexBatchOut
	var family types.LexProducer
	if forceScalar {
		types.LexBatchScalarRaw(srcPtr, size, pos, dstPtr, len(dst), uint64(flags), carry, &out)
		family = types.LexProducerGoScalarForced
	} else if cpu.UseScalar {
		types.LexBatchScalarRaw(srcPtr, size, pos, dstPtr, len(dst), uint64(flags), carry, &out)
		if cpu.ForcedScalarBuild {
			family = types.LexProducerGoScalarBuild
		} else {
			family = types.LexProducerGoScalarGlobal
		}
	} else {
		lexBatch(srcPtr, size, pos, dstPtr, len(dst), uint64(flags), carry, &out)
		family = nativeLexBatchFamily()
	}
	runtime.KeepAlive(src)
	runtime.KeepAlive(dst)
	return out, family
}
