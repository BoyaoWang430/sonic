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
	"math"
	"strings"
	"testing"
)

func TestParseValidatedNumberContract(t *testing.T) {
	// 1. Valid JSON numbers: exact parity with ParsePrefix on full-token spans,
	// including 0, -0, -0.0, int64/uint64 boundaries, >800-digit mantissas, and overflow.
	validSpans := []string{
		"0",
		"-0",
		"0.0",
		"-0.0",
		"-0.0000000000000000000000000",
		"-0e100",
		"1",
		"-1",
		"9223372036854775807",
		"9223372036854775808",
		"-9223372036854775808",
		"-9223372036854775809",
		"18446744073709551615",
		"18446744073709551616",
		"999999999999999999999999999999",
		"1.25",
		"-12.345e-67",
		"1e+1",
		"1e1000",
		"-1e1000",
		"1e-1000",
		"-1e-1000",
		"1.7976931348623157e308",
		"1.7976931348623159e308",
		"0." + strings.Repeat("0", 850) + "1",
		"1." + strings.Repeat("0", 820) + "5e-10",
		strings.Repeat("9", 900),
	}

	for _, s := range validSpans {
		got, gotErr := ParseValidatedNumber(s)
		want, wantEnd, wantErr := ParsePrefix(s, 0)
		if wantEnd != len(s) {
			t.Fatalf("fixture %q did not span entire string in ParsePrefix: end=%d len=%d", s, wantEnd, len(s))
		}
		if gotErr != wantErr || got.Kind != want.Kind {
			t.Fatalf("ParseValidatedNumber(%q): got (%+v, %v), want (%+v, %v)", s, got, gotErr, want, wantErr)
		}
		switch got.Kind {
		case Unsigned:
			if got.Uint64 != want.Uint64 {
				t.Fatalf("Unsigned mismatch on %q: got %d want %d", s, got.Uint64, want.Uint64)
			}
		case Signed:
			if got.Int64 != want.Int64 {
				t.Fatalf("Signed mismatch on %q: got %d want %d", s, got.Int64, want.Int64)
			}
		case Float:
			if math.Float64bits(got.Float64) != math.Float64bits(want.Float64) {
				t.Fatalf("Float64 bits mismatch on %q: got 0x%x (%v) want 0x%x (%v)",
					s, math.Float64bits(got.Float64), got.Float64, math.Float64bits(want.Float64), want.Float64)
			}
		}
	}

	// Verify -0 is Signed 0 and -0.0 retains negative signbit.
	negZeroInt, err := ParseValidatedNumber("-0")
	if err != 0 || negZeroInt.Kind != Signed || negZeroInt.Int64 != 0 {
		t.Fatalf("-0 expected Signed(0), got (%+v, %v)", negZeroInt, err)
	}
	negZeroFlt, err := ParseValidatedNumber("-0.0")
	if err != 0 || negZeroFlt.Kind != Float || negZeroFlt.Float64 != 0 || !math.Signbit(negZeroFlt.Float64) {
		t.Fatalf("-0.0 expected negative float zero, got (%+v, %v)", negZeroFlt, err)
	}

	// 2. Non-JSON Go float forms and unconsumed trailing bytes MUST be rejected
	// (ParseValidatedNumber must NEVER re-decide a shorter end!).
	rejected := []string{
		"",
		"-",
		"+1",
		"+0",
		"01",
		"-01",
		"1.",
		"1e",
		"1e+",
		"1e-",
		"1_000",
		"1.0_5",
		"1e1_0",
		"0x1p2",
		"-0x1p2",
		"NaN",
		"Inf",
		"+Inf",
		"-Inf",
		"infinity",
		"1 ",
		"1,2",
		"1+2",
		"1true",
		"1.2.3",
	}
	for _, bad := range rejected {
		res, perr := ParseValidatedNumber(bad)
		if perr == 0 {
			t.Fatalf("ParseValidatedNumber(%q) unexpectedly succeeded with %+v", bad, res)
		}
	}
}
