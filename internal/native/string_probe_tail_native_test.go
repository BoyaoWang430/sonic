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

package native

import (
	"encoding/binary"

	"math/bits"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

func tokenString(src string, pos int) int {
	base := unsafe.Pointer(rt.StringData(src))
	sp, se := pos, len(src)
	stop := minNative(se, sp+32)
	for sp < stop && se-sp >= 8 {
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, sp))[:])
		q := x ^ ('"' * tokenByteOnes)
		quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
		b := x ^ ('\\' * tokenByteOnes)
		backslashes := (b - tokenByteOnes) &^ b & tokenByteHighBits
		if quotes != 0 {
			tz := bits.TrailingZeros64(quotes)
			if backslashes == 0 || tz < bits.TrailingZeros64(backslashes) {
				return sp + (tz >> 3) + 1
			}
			sp += bits.TrailingZeros64(backslashes) >> 3
			goto scanNative
		}
		if backslashes != 0 {
			sp += bits.TrailingZeros64(backslashes) >> 3
			goto scanNative
		}
		sp += 8
	}
	if sp < stop && sp >= 8 {
		if src[sp] == '\\' {
			goto scanNative
		}
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, se-8))[:])
		x >>= (8 - (se - sp)) * 8
		b := x ^ ('\\' * tokenByteOnes)
		backslashes := (b - tokenByteOnes) &^ b & tokenByteHighBits
		q := x ^ ('"' * tokenByteOnes)
		quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
		if quotes != 0 && (backslashes == 0 || bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
			return sp + (bits.TrailingZeros64(quotes) >> 3) + 1
		}
		if backslashes == 0 {
			return -1
		}
		sp += bits.TrailingZeros64(backslashes) >> 3
	}
scanNative:
	pos = sp
	end, err := kernels.SkipStringRelaxed(src[pos:])
	if err != 0 {
		return -1
	}
	return pos + end
}

// The byte oracle is independent of word classification and accepts the same
// relaxed escape syntax as the short probes and their native continuation.
func assertRISCVStringProbeTail(t *testing.T, src string, pos int) {
	t.Helper()
	wantEnd, wantEscape, wantErr := len(src), -1, types.ERR_EOF
	for i := pos; i < len(src); i++ {
		if src[i] == '"' {
			wantEnd, wantErr = i+1, 0
			break
		}
		if src[i] == '\\' {
			if wantEscape < 0 {
				wantEscape = i
			}
			i++
		}
	}
	wantToken := wantEnd
	if wantErr != 0 {
		wantToken = -1
	}
	if got := tokenString(src, pos); got != wantToken {
		t.Fatalf("token src=%q pos=%d: end=%d, want %d", src, pos, got, wantToken)
	}
	var block pathBlock
	end, escape, err := block.stringTail(src, pos)
	if end != wantEnd || escape != wantEscape || err != wantErr {
		t.Fatalf("path src=%q pos=%d: (%d,%d,%d), want (%d,%d,%d)", src, pos, end, escape, err, wantEnd, wantEscape, wantErr)
	}
}

func TestRISCVStringProbeTail(t *testing.T) {
	// Cover every word remainder, the 32-byte Go probe limit, incomplete
	// escapes, and a quote before or after a backslash in the same last word.
	for offset := 0; offset < 16; offset++ {
		prefix := strings.Repeat("\"\\#]", (offset+3)/4)[:offset]
		for n := 0; n < 41; n++ {
			for _, tail := range []string{"", `"`, `\`, `\"`, `\""`, `\\"`, `\n"`, `"\`, `#]"`, "\x00\xff\""} {
				src := prefix + strings.Repeat("a", n) + tail
				assertRISCVStringProbeTail(t, src, offset)
			}
		}
	}
	// Every byte can occupy the first event in a tail, including the bytes
	// adjacent to quotes and slashes that expose subtraction-borrow mistakes.
	for offset := 0; offset < 8; offset++ {
		prefix := strings.Repeat("\"", offset)
		for n := 0; n < 32; n++ {
			for b := 0; b < 256; b++ {
				body := strings.Repeat("a", n) + string([]byte{byte(b)}) + `#]"`
				assertRISCVStringProbeTail(t, prefix+body, offset)
			}
		}
	}
}
