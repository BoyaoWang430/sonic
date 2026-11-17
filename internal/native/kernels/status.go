//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

// Copyright 2026 ByteDance Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kernels

import "github.com/bytedance/sonic/internal/native/types"

// Bounded Kernel Memory & Aliasing Support Matrix (Architecture Contract C2/C3):
//
//  1. Read Bounds: Exported kernels operate on explicit (ptr, logicalLen, outCap)
//     slices/spans and read strictly within src[:logicalLen]. Callers never
//     pad inputs or couple to AVX2/NEON/RVV vector widths.
//  2. Zero-Length & Nil Pointers: Every exported kernel accepts size == 0
//     (including nil data pointers when size == 0) without dereferencing memory.
//  3. In-Place & Overlap:
//     - Unquote / UnquoteBuffer / UnquoteStatus and ParseString / DecodeQuoted
//     support exact in-place aliasing (dst == src) and disjoint buffers;
//     arbitrary partial overlap (dst != src with overlapping byte ranges) is
//     unsupported.
//     - Quote* and HTMLEscape* require disjoint src and dst buffers.
//  4. Neutral Status Protocol: Kernels report neutral KernelStatus codes and
//     local byte offsets; outer Go scanners/decoders translate them into public
//     SyntaxError, MismatchTypeError, or AST error values.
type KernelStatus uint8

const (
	StatusOK KernelStatus = iota
	StatusEOF
	StatusControlChar
	StatusInvalidEscape
	StatusInvalidUnicode
	StatusInvalidChar
	StatusInvalidNumber
	StatusInvalidLiteral
	StatusDepthOverflow
	StatusOutputFull
	StatusNeedMore
)

// FromParsingError maps legacy native/types.ParsingError codes to neutral KernelStatus.
func FromParsingError(err types.ParsingError) KernelStatus {
	switch err {
	case 0:
		return StatusOK
	case types.ERR_EOF:
		return StatusEOF
	case types.ERR_INVALID_CHAR:
		return StatusInvalidChar
	case types.ERR_INVALID_ESCAPE:
		return StatusInvalidEscape
	case types.ERR_INVALID_UNICODE:
		return StatusInvalidUnicode
	case types.ERR_INVALID_NUMBER_FMT, types.ERR_INTEGER_OVERFLOW, types.ERR_FLOAT_INFINITY:
		return StatusInvalidNumber
	case types.ERR_RECURSE_EXCEED_MAX:
		return StatusDepthOverflow
	default:
		return StatusInvalidChar
	}
}

func decodeQuotedRawCodeToStatus(code int) KernelStatus {
	switch code {
	case 0:
		return StatusOK
	case 1:
		return StatusControlChar
	case 2:
		return StatusInvalidEscape
	case 5:
		return StatusEOF
	case 12:
		return StatusInvalidUnicode
	default:
		return StatusInvalidChar
	}
}
