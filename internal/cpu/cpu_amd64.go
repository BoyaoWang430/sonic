//go:build amd64 && !sonic_scalar && !sonic_compat && !go1.28

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

package cpu

func cpuid(leaf, subleaf uint32) (eax, ebx, ecx, edx uint32)
func xgetbv() uint64

func detectCPU() (hasAVX2, hasSSE, hasSSSE3, hasPOPCNT, hasCLMUL bool) {
	maxLeaf, _, _, _ := cpuid(0, 0)
	if maxLeaf < 1 {
		return false, false, false, false, false
	}
	_, _, ecx1, edx1 := cpuid(1, 0)
	hasSSE = (edx1 & (1 << 25)) != 0
	hasSSSE3 = (ecx1 & (1 << 9)) != 0
	hasCLMUL = (ecx1 & (1 << 1)) != 0
	hasPOPCNT = (ecx1 & (1 << 23)) != 0
	osxsave := (ecx1 & (1 << 27)) != 0
	hasAVX := (ecx1 & (1 << 28)) != 0
	if maxLeaf >= 7 && osxsave && hasAVX {
		if (xgetbv() & 0x6) == 0x6 {
			_, ebx7, _, _ := cpuid(7, 0)
			hasAVX2 = (ebx7 & (1 << 5)) != 0
		}
	}
	return
}
