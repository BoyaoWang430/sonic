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
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// SkipString scans content immediately after a JSON string's opening quote.
// It follows ScanString's syntax contract and returns a relative end offset,
// including the closing quote. On error end identifies the offending byte,
// or len(content) for EOF. All reads stay within content.
// Callers that know the mode should use SkipStringRelaxed or SkipStringStrict:
// each makes one assembly call and stays within the inlining budget.
func SkipString(content string, validate bool) (end int, err types.ParsingError) {
	if validate {
		return SkipStringStrict(content)
	}
	return SkipStringRelaxed(content)
}

// SkipStringRelaxed is SkipString without control or escape validation.
func SkipStringRelaxed(content string) (end int, err types.ParsingError) {
	if cpu.UseScalar {
		end, _, _, err = scanStringRelaxedScalar(rt.StringData(content), len(content))
		return
	}
	end, _, _, err = scanStringRelaxed(rt.StringData(content), len(content))
	return
}

// SkipStringStrict is SkipString with JSON control and escape validation.
func SkipStringStrict(content string) (end int, err types.ParsingError) {
	if cpu.UseScalar {
		end, _, _, err = scanStringStrictScalar(rt.StringData(content), len(content))
		return
	}
	end, _, _, err = scanStringStrict(rt.StringData(content), len(content))
	return
}
