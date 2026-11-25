//go:build goexperiment.jsonv2 && !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package optdec

import (
	"go/version"
	"runtime"
	"strings"
)

const diffStdUsesJSONV2 = true

var diffStdJSONV2Pre1273 = isDiffPreGo1273(runtime.Version())

func isDiffPreGo1273(v string) bool {
	if i := strings.Index(v, "go1."); i >= 0 {
		v = v[i:]
		if sp := strings.IndexByte(v, ' '); sp >= 0 {
			v = v[:sp]
		}
		if dash := strings.IndexByte(v, '-'); dash >= 0 {
			v = v[:dash]
		}
		return version.Compare(v, "go1.27.3") < 0
	}
	return false
}
