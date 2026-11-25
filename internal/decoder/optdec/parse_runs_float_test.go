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
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

// runsNumberCorpus mixes boundary tokens with random decimals around every
// limit of the fast paths: 19 integer digits, 17 significant digits,
// 2^52 significands, 1e15 for positive exponents, |exponent| <= 22, and at
// most three exponent digits. Malformed tokens must keep exact error parity.
func runsNumberCorpus() []string {
	corpus := []string{
		"0", "-0", "0.0", "-0.0", "0.000", "0e5", "-0e-5", "0.000e7", "0.0001", "0.000123e3",
		"1", "1.5", "-2.718281828e+10", "1e22", "1e23", "1e-22", "1e-23", "-1e22",
		"4503599627370495", "4503599627370495.5", "4503599627370496.5", "450359962737049.55",
		"1000000000000000e1", "1000000000000001e1", "999999999999999.9e7", "1000000000000000e22",
		"9007199254740993", "123456789012345678.9", "12345678901234567.89", "1234567890123456.789",
		"0.12345678901234567890123", "0.00000000000000000000001", "1.00000000000000011102230246251565404236316680908203125",
		"1e0", "1e00", "1e000", "1e0000", "1e+022", "1e-022", "1e400", "1e-400", "2.5E3", "2.5E-3",
		"1.", "1.e5", "1e", "1e+", "1e-", "-", "-x", "1.x", "0.", "0.e1", "01", "01.5", "1ee5", "1.5.5",
		"18446744073709551615", "18446744073709551616", "-9223372036854775808", "-9223372036854775809",
		"99999999999999999999.5", "0.99999999999999999999",
	}
	rng := rand.New(rand.NewSource(20261105))
	digits := func(n int, leading bool) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			d := byte('0' + rng.Intn(10))
			if i == 0 && leading && d == '0' {
				d = '1' + byte(rng.Intn(9))
			}
			b.WriteByte(d)
		}
		return b.String()
	}
	for i := 0; i < 40000; i++ {
		var b strings.Builder
		if rng.Intn(3) == 0 {
			b.WriteByte('-')
		}
		if rng.Intn(6) == 0 {
			b.WriteByte('0')
		} else {
			b.WriteString(digits(1+rng.Intn(20), true))
		}
		if rng.Intn(4) != 0 {
			b.WriteByte('.')
			if rng.Intn(5) == 0 {
				b.WriteString(strings.Repeat("0", rng.Intn(25)))
			}
			b.WriteString(digits(rng.Intn(26), false))
		}
		if rng.Intn(3) == 0 {
			b.WriteByte("eE"[rng.Intn(2)])
			switch rng.Intn(3) {
			case 0:
				b.WriteByte('-')
			case 1:
				b.WriteByte('+')
			}
			b.WriteString(digits(rng.Intn(5), false))
		}
		corpus = append(corpus, b.String())
	}
	for e := -30; e <= 30; e++ {
		for _, m := range []string{"1", "45", "4503599627370495", "1000000000000000", "1000000000000001", "123.456"} {
			corpus = append(corpus, m+"e"+strconv.Itoa(e), "-"+m+"e"+strconv.Itoa(e))
		}
	}
	return corpus
}

// Every token in the 40,000+ corpus must produce identical node bits, error code,
// and cursor across hardware LexBatch, scalar LexBatch, and independent
// jsonscan.LexNumberAt + number.ParseValidatedNumber reference evaluation.
func TestRunsNumberMatchesGoReference(t *testing.T) {
	for _, s := range runsNumberCorpus() {
		framed := "[" + s + "]"

		pAsm := newParser(framed, 0, 0)
		pAsm.isEface = true
		gotAsm := pAsm.parse()

		pSca := newParser(framed, 0, 0)
		pSca.isEface = true
		gotSca := parseRunsGrowingMode(pSca, true)

		if gotAsm != gotSca || pAsm.Pos() != pSca.Pos() || pAsm.nbuf.nlen != pSca.nbuf.nlen {
			t.Fatalf("%q: asm=(%d,%d,%d) != scalar=(%d,%d,%d)",
				s, gotAsm, pAsm.Pos(), pAsm.nbuf.nlen, gotSca, pSca.Pos(), pSca.nbuf.nlen)
		}

		// Independent reference check via authoritative LexNumberAt + ParseValidatedNumber.
		lexRes := jsonscan.LexNumberAt(framed, 1, true)
		if !lexRes.Complete {
			if gotAsm != SONIC_INVALID_NUM || pAsm.Pos() != lexRes.Fault.FaultPos {
				t.Fatalf("%q: parse=(%d,%d), lexFault=(%d,%d)", s, gotAsm, pAsm.Pos(), SONIC_INVALID_NUM, lexRes.Fault.FaultPos)
			}
		} else if lexRes.End != len(framed)-1 {
			// Trailing bytes after a valid prefix (e.g. "01" -> '0' then '1' inside '[01]') fail in array grammar.
			if gotAsm == SONIC_OK {
				t.Fatalf("%q: expected array trailing error at %d, got SONIC_OK", s, lexRes.End)
			}
		} else {
			numRes, perr := number.ParseValidatedNumber(framed[lexRes.Start:lexRes.End])
			if perr == types.ERR_FLOAT_INFINITY {
				if gotAsm != SONIC_FLOAT_INF {
					t.Fatalf("%q: expected SONIC_FLOAT_INF, got %d", s, gotAsm)
				}
			} else if perr != 0 {
				t.Fatalf("%q: unexpected ParseValidatedNumber err %v", s, perr)
			} else {
				var wantTyp, wantVal uint64
				switch numRes.Kind {
				case number.Unsigned:
					wantTyp, wantVal = KUint, numRes.Uint64
				case number.Signed:
					wantTyp, wantVal = KSint, uint64(numRes.Int64)
				default:
					wantTyp, wantVal = KReal, math.Float64bits(numRes.Float64)
				}
				nd := pAsm.nodes[1]
				if gotAsm != SONIC_OK || (nd.typ&TypeMask) != wantTyp || nd.val != wantVal || pSca.nodes[1] != nd {
					t.Fatalf("%q: parse=%d node=(%#x,%#x), want=(%#x,%#x)",
						s, gotAsm, nd.typ&TypeMask, nd.val, wantTyp, wantVal)
				}
			}
		}

		pAsm.free()
		pSca.free()
	}
}

func TestSkipNumberFastAllBytes(t *testing.T) {
	isNumByte := func(b byte) bool {
		return b >= '0' && b <= '9' || b == '.' || b == '-' || b == '+' || b == 'e' || b == 'E'
	}
	for start := 0; start <= 7; start++ {
		for run := 0; run <= 24; run++ {
			for value := 0; value < 256; value++ {
				if isNumByte(byte(value)) {
					continue
				}
				var b strings.Builder
				b.WriteString(strings.Repeat(" ", start))
				for i := 0; i < run; i++ {
					b.WriteByte('0' + byte((i*7)%10))
				}
				b.WriteByte(byte(value))
				b.WriteString("99999999")
				end, ok := SkipNumberFast(b.String(), start)
				if wantEnd, wantOK := start+run, run > 0; end != wantEnd || ok != wantOK {
					t.Fatalf("start=%d run=%d value=%#x: got (%d,%v), want (%d,%v)", start, run, value, end, ok, wantEnd, wantOK)
				}
			}
		}
	}
}
