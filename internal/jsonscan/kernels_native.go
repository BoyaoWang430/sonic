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

package jsonscan

import (
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
)

func kernTraversalMasks(src string, start int, validate bool) (events, nonspace uint64, width int) {
	return kernels.TraversalMasks(src, start, validate)
}

func kernScanString(src string, quotePos int, rejectCtrl bool) (end, escape, errPos int, err types.ParsingError) {
	return kernels.ScanString(src, quotePos, rejectCtrl)
}

// kernUnquoteRaw preserves the kernel's raw errorPos and ParsingError so
// error_adapter.go can normalize backslash offsets in O(1) without rescanning
// the entire string prefix from scratch.
func kernUnquoteRaw(src string, dst []byte, replaceUnicode bool) (written, rawErrPos int, perr types.ParsingError) {
	var flags uint64
	if replaceUnicode {
		flags = types.F_UNICODE_REPLACE
	}
	return kernels.Unquote(src, dst, flags)
}

func kernScanNumberPrefix(src string, start int) (end int, err types.ParsingError) {
	return kernels.ScanNumber(src, start)
}

func kernScanNumberStrict(src string, start int) (end int, err types.ParsingError) {
	return kernels.ScanNumberStrict(src, start)
}

func kernSkipContainerRelaxed(src string, openPos int) (end int, err types.ParsingError) {
	return kernels.SkipContainer(src, openPos)
}

func kernSkipStringRelaxed(content string) (end int, err types.ParsingError) {
	return kernels.SkipStringRelaxed(content)
}

func kernSkipNumberRelaxed(src string, start int) int {
	return kernels.SkipNumber(src, start)
}

func kernValidateUTF8(src string) bool {
	if src == "" {
		return true
	}
	_, _, full := kernels.InvalidUTF8(src, 0, nil)
	return !full
}

func kernLexBatchUnobserved(src string, pos int, dst []types.LexBlockMask, flags types.LexFlags, carry *types.LexBlockCarry, forceScalar bool) types.LexBatchOut {
	return kernels.LexBatchMode(src, pos, dst, flags, carry, forceScalar)
}
