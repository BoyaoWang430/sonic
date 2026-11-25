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

package native

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestRISCVStringProbeProtectedTail(t *testing.T) {
	page := os.Getpagesize()
	memory, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(memory)
	for _, guard := range [][]byte{memory[:page], memory[2*page:]} {
		if err := syscall.Mprotect(guard, syscall.PROT_NONE); err != nil {
			t.Fatal(err)
		}
	}
	for offset := 0; offset < 16; offset++ {
		prefix := strings.Repeat("\"\\#]", (offset+3)/4)[:offset]
		for n := 0; n < 41; n++ {
			for _, tail := range []string{"", `"`, `\`, `\"`, `\""`, `\\"`, `"\`, `#]"`} {
				input := prefix + strings.Repeat("a", n) + tail
				for _, begin := range []int{page, 2*page - len(input)} {
					copy(memory[begin:begin+len(input)], input)
					src := rt.String(&memory[begin], len(input))
					assertRISCVStringProbeTail(t, src, offset)
				}
			}
		}
	}
}
