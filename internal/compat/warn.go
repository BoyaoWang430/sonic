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

package compat

import (
	"fmt"
	"os"
)

func Warn(prefix string) {
	fmt.Fprintf(os.Stderr, "WARNING: %s is running on the explicit encoding/json compatibility adapter (-tags=sonic_compat or non-64-bit/rva20u64 fallback target); Sonic native acceleration requires amd64/arm64 (go1.20+) or riscv64 rva22u64+ (go1.26+), or -tags=sonic_scalar on 64-bit targets\n", prefix)
}
