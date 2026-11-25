//go:build goexperiment.jsonv2
// +build goexperiment.jsonv2

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

package compatibility_test

import (
	"go/version"
	"runtime"
	"strings"
)

// Go 1.27 uses the jsonv2 backend for encoding/json by default.
const stdUsesJSONV2 = true

// Go 1.27.3 changed seven DefaultOptionsV1 cases in the jsonv2-backed
// encoding/json facade: six restored legacy v1 behavior (F32-OVF, F64-OVF,
// STR-NULL, TM-KEY-QUOTE, MAP-STRING-TEXT, UNSUPPORTED-VALUE-PAYLOAD), while
// F64-LONG-OVF stopped saturating the interface{} slot to +Inf and leaves the
// allocated float64 zero value (float64(0), whereas legacy v1 leaves nil).
var stdJSONV2Pre1273 = isPreGo1273(runtime.Version())

func isPreGo1273(v string) bool {
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
