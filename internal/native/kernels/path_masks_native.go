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

func PathMasks(src string, start int) (events, nonspace, boundaries uint64, width int) {
	tail := src[start:]
	width = minInt(len(tail), 64)
	if cpu.UseScalar {
		events, nonspace, boundaries = pathMasksScalar(rt.StringData(tail), width)
		return
	}
	events, nonspace, boundaries = pathMasks(rt.StringData(tail), width)
	return
}

func ScanPathStringBlock(tail string) (end, escape int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	if cpu.UseScalar {
		end, escape, _, err, tailEvents, tailWidth = scanStringRelaxedBlockScalar(rt.StringData(tail), len(tail))
		return
	}
	end, escape, _, err, tailEvents, tailWidth = scanStringRelaxedBlock(rt.StringData(tail), len(tail))
	return
}
