//go:build sonic_compat || (!(amd64 && go1.20) && !(arm64 && go1.20) && !(riscv64 && go1.26 && riscv64.rva22u64) && !(sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package number

import "github.com/bytedance/sonic/internal/native/types"

// Scan scans one JSON number prefix starting at start, without skipping space.
// The caller must validate the following delimiter: "01" ends after "0" and
// "1+2" ends before '+'. A decimal point or exponent requires following digits.
// On error, end is the offending byte, or len(s) for ERR_EOF. An out-of-range
// start returns ERR_INVALID_CHAR and the unchanged start.
func Scan(s string, start int) (end int, err types.ParsingError) {
	return scanGeneric(s, start)
}
