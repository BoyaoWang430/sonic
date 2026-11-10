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

package cpu

import (
	"fmt"
	"os"
)

var (
	HasAVX2   bool
	HasSSE    bool
	HasPOPCNT bool
	HasCLMUL  bool
)

func init() {
	HasAVX2, HasSSE, HasPOPCNT, HasCLMUL = detectCPU()
	switch v := os.Getenv("SONIC_MODE"); v {
	case "", "auto":
		break
	case "noavx", "noavx2":
		HasAVX2 = false
	default:
		panic(fmt.Sprintf("invalid mode: '%s', should be one of 'auto', 'noavx', 'noavx2'", v))
	}
}
