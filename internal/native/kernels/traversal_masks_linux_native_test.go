//go:build !sonic_compat && linux && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

import (
	"os"
	"syscall"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestTraversalMasksGuardPages(t *testing.T) {
	page := os.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Munmap(mapping); err != nil {
			t.Error(err)
		}
	}()
	mid := mapping[page : 2*page]
	if err := syscall.Mprotect(mid, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	for i := range mid {
		mid[i] = 'x'
	}
	if err := syscall.Mprotect(mid, syscall.PROT_READ); err != nil {
		t.Fatal(err)
	}
	for n := 0; n <= 70; n++ {
		for _, base := range []int{0, page - n} {
			src := rt.String(&mapping[page+base], n)
			events, got, width := TraversalMasks(src, 0, true)
			wantWidth := minInt(n, 64)
			want := ^uint64(0) >> uint(64-wantWidth)
			if events != 0 || got != want || width != wantWidth {
				t.Fatalf("base=%d len=%d: (%x,%d)", base, n, got, width)
			}
		}
	}
	events, n := traversalMasks(&mapping[2*page], 0, 1)
	if events|n != 0 {
		t.Fatal(events, n)
	}
}
