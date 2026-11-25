//go:build !sonic_compat && !sonic_scalar && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

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

import "github.com/bytedance/sonic/internal/native/types"

//go:noescape
func htmlEscape(src *byte, size int, dst *byte, capacity int) (consumed, written int)

//go:noescape
func nonSpaceMask(src *byte, size int) (mask uint64)

//go:noescape
func numberScan(src *byte, size int, mode uint64) (end int, err types.ParsingError)

//go:noescape
func pathMasks(src *byte, size int) (events, nonspace, boundaries uint64)

//go:noescape
func quote(src *byte, size int, dst *byte, capacity int, flags uint64) (consumed, written int)

//go:noescape
func quoteHTML(src *byte, size int, dst *byte, capacity int) (consumed, written int)

//go:noescape
func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)

//go:noescape
func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)

//go:noescape
func scanStringRelaxedBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)

//go:noescape
func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)

//go:noescape
func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError)

//go:noescape
func space(src *byte, size int) (n int)

//go:noescape
func stringDecode(src *byte, size int, dst *byte, mode uint64) (end, written, escaped, errorPos, code int)

//go:noescape
func traversalMasks(src *byte, size int, validate uint64) (events, nonspace uint64)

//go:noescape
func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool)

//go:noescape
func lexBatch(src *byte, size int, pos int, blocks *types.LexBlockMask, capBlocks int, flags uint64, carry *types.LexBlockCarry, out *types.LexBatchOut)
