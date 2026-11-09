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

package ast

import (
	"math/rand"
	"strings"
	"testing"
)

func TestSkipPairQuoteMasks(t *testing.T) {
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		// '#' following a quote can inherit a bit from a subtract-based zero
		// detector. The analogous delimiter neighbors must not change depth.
		fillers := [][]byte{{'#'}, {pair[1]}, {pair[0], pair[1]}, {pair[0], pair[0] ^ 1, pair[1], pair[1] ^ 1}}
		for quotes := 0; quotes < 256; quotes++ {
			for _, filler := range fillers {
				var word [8]byte
				for i := range word {
					word[i] = filler[i%len(filler)]
					if quotes&(1<<uint(i)) != 0 {
						word[i] = '"'
					}
				}
				for _, leading := range []string{"", `"aaaaaaa`} {
					// The second prefix enters the tested word inside a string.
					// Enough closing delimiters follow to expose the exit state
					// even when all unquoted bytes in the word open containers.
					suffix := strings.Repeat(string(pair[1]), 9) + `"` + strings.Repeat(string(pair[1]), 9)
					body := string(pair[0]) + leading + string(word[:]) + suffix
					for offset := 0; offset < 8; offset++ {
						src := strings.Repeat("p", offset) + body
						checkSkipPair(t, src, offset, pair[0], pair[1])
					}
				}
			}
		}
	}
}

func TestSkipPairFirstCloseAndEscapes(t *testing.T) {
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		for offset := 0; offset < 8; offset++ {
			prefix := strings.Repeat("p", offset)
			for padding := 0; padding < 16; padding++ {
				plain := strings.Repeat("a", padding)
				closed := prefix + string(pair[0]) + plain + string(pair[1])
				// Events after the first closing delimiter may share its word.
				// Neither later quote parity nor later errors can alter the end.
				for _, suffix := range []string{`"#"`, `"}]{[`, `\"`, `\\`, "\x00\xff", strings.Repeat(`"#`, 8)} {
					src := closed + suffix
					if got := skipPair(src, offset, pair[0], pair[1]); got != len(closed) {
						t.Fatalf("first close in %q at %d: got %d, want %d", src, offset, got, len(closed))
					}
				}
				for _, body := range []string{
					`"` + plain + `"#"{}[]"`,
					`"` + plain + `\"#{}[]"`,
					`"` + plain + `\\"#"{}[]"`,
					`"` + plain + `\n","` + strings.Repeat("b", 24) + `"`,
					plain + `\` + string(pair[1]), // Escaped delimiter outside a string.
					plain + `"` + string(pair[1]), // Unclosed string hides the delimiter.
					plain + `\`,                   // Trailing backslash, including at a word boundary.
				} {
					src := prefix + string(pair[0]) + body + string(pair[1])
					checkSkipPair(t, src, offset, pair[0], pair[1])
					// Exercise a short tail and every truncation near the end.
					for trim := 1; trim <= 8 && len(src)-trim >= offset; trim++ {
						checkSkipPair(t, src[:len(src)-trim], offset, pair[0], pair[1])
					}
				}
			}
		}
	}
}

func TestSkipPairMaskRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(0x71756f7465))
	alphabet := []byte{'"', '#', '{', 'z', '}', '|', '[', 'Z', ']', '\\', 'a', 0, 0x80, 0xff}
	for trial := 0; trial < 10000; trial++ {
		data := make([]byte, rng.Intn(128))
		for i := range data {
			if trial&1 == 0 {
				data[i] = alphabet[rng.Intn(len(alphabet))]
			} else {
				data[i] = byte(rng.Intn(256))
			}
		}
		pos := rng.Intn(len(data) + 1)
		for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
			checkSkipPair(t, string(data), pos, pair[0], pair[1])
			// Always exercise the body too; arbitrary input rarely starts with
			// the selected opening delimiter.
			wrapped := string(data[:pos]) + string(pair[0]) + string(data[pos:]) + string(pair[1])
			checkSkipPair(t, wrapped, pos, pair[0], pair[1])
		}
	}
}
