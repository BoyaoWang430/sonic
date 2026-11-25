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

package optdec

import (
	"strings"
	"testing"
)

// Both parsers must preserve every decoded value across tape growth. Literal
// node payloads are not part of that contract: C-compatible nodes encode the
// bool value in their type and may leave the separate payload unspecified.
func TestNativeParserLargeNodeGrowthValues(t *testing.T) {
	for _, count := range []int{65535, 65536, 131073} {
		input := rvvBooleanArray(count)
		pos := 0
		var got []bool
		if err := Decode(&input, &pos, 0, &got); err != nil {
			t.Fatalf("count=%d: %v", count, err)
		}
		if pos != len(input) || len(got) != count {
			t.Fatalf("count=%d: position=%d length=%d", count, pos, len(got))
		}
		for i, value := range got {
			if value != (i != count-1) {
				t.Fatalf("count=%d: value[%d]=%v", count, i, value)
			}
		}
	}
}

func rvvBooleanArray(count int) string {
	return "[" + strings.Repeat("true,", count-1) + "false]"
}
