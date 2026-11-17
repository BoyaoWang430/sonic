//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

// Copyright 2021 ByteDance Inc.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// https://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package optdec

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/rt"
)

// cParityAtofFallback converts a validated decimal float token [src, src+size)
// to IEEE-754 float64. Short tokens (<= 800 bytes) use strconv; longer tokens
// use the shared bounded decimal engine in internal/native/number, avoiding
// older Go toolchains' >800-digit decimal-point truncation bug.
func cParityAtofFallback(src unsafe.Pointer, size int, buf *[]byte) float64 {
	raw := rt.Mem2Str(unsafe.Slice((*byte)(src), size))
	if size <= 800 {
		v, _ := number.ParseValidJSONFloat64(raw)
		return v
	}
	var scratch []byte
	if buf != nil {
		scratch = *buf
	}
	return number.ParseDecimalSliceFloat64(raw, scratch)
}

func cParityAtofNative(src unsafe.Pointer, size int, buf *[]byte) float64 {
	raw := rt.Mem2Str(unsafe.Slice((*byte)(src), size))
	var scratch []byte
	if buf != nil {
		scratch = *buf
	}
	return number.ParseDecimalSliceFloat64(raw, scratch)
}
