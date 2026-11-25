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

package number

import (
	"math/rand"
	"testing"
)

// Compare the RISC-V scanner with the production Go fallback in the same build.
func TestNumberScanMatchesExistingGo(t *testing.T) {
	rng := rand.New(rand.NewSource(882613))
	alphabet := "00011123456789.eE++--\x00\xffxyz,} \n"
	for i := 0; i < 50000; i++ {
		buf := make([]byte, rng.Intn(160))
		for j := range buf {
			buf[j] = alphabet[rng.Intn(len(alphabet))]
		}
		src := string(buf)
		for _, start := range []int{-1, 0, len(src) / 2, len(src), len(src) + 1} {
			end, err := Scan(src, start)
			wantEnd, wantErr := scanGeneric(src, start)
			if end != wantEnd || err != wantErr {
				t.Fatalf("ScanNumber(%q,%d)=(%d,%v), Go=(%d,%v)", src, start, end, err, wantEnd, wantErr)
			}
		}
	}
}
