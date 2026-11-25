//go:build !sonic_compat && go1.26 && !go1.28 && riscv64.rva22u64

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

package unquote

import (
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRVVIntoBytes(t *testing.T) {
	src := `a\n\u4e2d\ud83d\ude00`
	dst := make([]byte, 3, len(src))
	copy(dst, "old")
	if err := IntoBytes(src, &dst); err != 0 || string(dst) != "a\n中😀" {
		t.Fatalf("IntoBytes=%q, %v", dst, err)
	}

	// The public contract requires capacity for the source length, even if
	// its decoded form would fit. Rejection must preserve the caller's slice.
	dst = make([]byte, 2, 3)
	copy(dst, "ok")
	if err := IntoBytes(`\u0041`, &dst); err != types.ERR_EOF || string(dst) != "ok" {
		t.Fatalf("short destination=%q, %v", dst, err)
	}

	dst = make([]byte, 1, 16)
	if err := IntoBytes(`abc\x`, &dst); err != types.ERR_INVALID_ESCAPE || len(dst) != 1 {
		t.Fatalf("invalid escape: len=%d, %v", len(dst), err)
	}
}
