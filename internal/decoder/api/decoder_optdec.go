//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.17 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)
// +build amd64,go1.17,!go1.28 arm64,go1.17,!go1.28 riscv64,go1.26,!go1.28,riscv64.rva22u64

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

package api

import (
	"github.com/bytedance/sonic/internal/decoder/optdec"
	"github.com/bytedance/sonic/internal/envs"
)

var (
	pretouchImpl                 = optdec.Pretouch
	pretouchManyImpl             = optdec.PretouchMany
	decodeImpl                   = optdec.Decode
	decodeStreamImpl             = optdec.DecodeStream
	freeStreamSessionImpl        = optdec.FreeStreamSession
	RegisterNoCopyRawMessageType = optdec.RegisterNoCopyRawMessageType
)

func decodeSessionImpl(s *string, i *int, f uint64, val interface{}, sessPtr **inputSession) error {
	return optdec.DecodeWithSession(s, i, f, val, sessPtr)
}

func decodeOneImpl(s string, f uint64, val interface{}) (string, int, error) {
	return optdec.DecodeOne(s, f, val)
}

func init() {
	// ARM64 and native RISC-V share optdec and enable all its optimizations.
	envs.EnableOptDec()
	envs.EnableFastMap()
}

type inputSession = optdec.InputSession
type streamSession = optdec.StreamSession
