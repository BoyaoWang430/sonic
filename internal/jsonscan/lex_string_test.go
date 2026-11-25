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

package jsonscan

import (
	"strings"
	"testing"
)

func runWindowedValidate(srcBody string, chunkSize int, pol StringPolicy) (int, ScanError) {
	var st StringValidateState
	st.reset()
	n := len(srcBody)
	if n == 0 {
		return st.advanceWindow(srcBody, 0, 0, true, pol, pol.RejectControl)
	}
	for end := chunkSize; end < n; end += chunkSize {
		errOff, code := st.advanceWindow(srcBody[:end], 0, end, false, pol, pol.RejectControl)
		if code != ErrNone {
			return errOff, code
		}
	}
	return st.advanceWindow(srcBody, 0, n, true, pol, pol.RejectControl)
}

func TestStringValidateStateChunkSplitAndPolicyParity(t *testing.T) {
	strictSurrPol := StringPolicy{
		ValidateEscapes:     true,
		RejectControl:       true,
		RejectLoneSurrogate: true,
	}
	relaxedSurrPol := StringPolicy{
		ValidateEscapes:     true,
		RejectControl:       true,
		RejectLoneSurrogate: false,
	}
	allowCtrlValEscPol := StringPolicy{
		ValidateEscapes:     true,
		RejectControl:       false,
		RejectLoneSurrogate: false,
	}

	cases := []struct {
		name string
		body string
		pol  StringPolicy
		want ScanError
		off  int
	}{
		{"plain_ascii", "hello world 1234567890", strictSurrPol, ErrNone, -1},
		{"all_basic_escapes", `\"\\\/\b\f\n\r\t`, strictSurrPol, ErrNone, -1},
		{"valid_bmp_unicode", `prefix\u0061\u4e2d\u20ac_suffix`, strictSurrPol, ErrNone, -1},
		{"valid_surrogate_pair", `emoji:\uD83D\uDE00!`, strictSurrPol, ErrNone, -1},
		{"chained_surrogate_allowed_when_relaxed", `\uD800\uD83D\uDE00`, relaxedSurrPol, ErrNone, -1},
		{"chained_surrogate_rejected_when_strict", `\uD800\uD83D\uDE00`, strictSurrPol, ErrInvalidEscapeUTF, 0},
		{"lone_high_surrogate_at_end", `abc\uD800`, strictSurrPol, ErrInvalidEscapeUTF, 3},
		{"lone_high_surrogate_followed_by_char", `abc\uD800xyz`, strictSurrPol, ErrInvalidEscapeUTF, 3},
		{"lone_high_surrogate_followed_by_newline_esc", `abc\uD800\n`, strictSurrPol, ErrInvalidEscapeUTF, 3},
		{"lone_low_surrogate", `abc\uDC00xyz`, strictSurrPol, ErrInvalidEscapeUTF, 3},
		{"lone_surrogate_allowed_in_rfc8259_mode", `abc\uD800xyz\uDC00`, relaxedSurrPol, ErrNone, -1},
		{"invalid_second_surrogate_hex_points_to_second_slash", `abc\uD800\u123G`, strictSurrPol, ErrInvalidEscapeUTF, 9},
		{"invalid_simple_escape", `0123456789\q`, strictSurrPol, ErrInvalidEscape, 10},
		{"invalid_hex_first_u", `0123\u00G0`, strictSurrPol, ErrInvalidEscapeUTF, 4},
		{"control_char_rejected_in_strict", "abc\x01def", strictSurrPol, ErrControlChar, 3},
		{"control_char_allowed_when_reject_ctrl_false", "abc\x01\\u0061", allowCtrlValEscPol, ErrNone, -1},
		{"control_char_allowed_but_bad_escape_caught", "abc\x01\\q", allowCtrlValEscPol, ErrInvalidEscape, 4},
		{"later_control_char_retains_scan_priority_over_lone_surr", "\\uD800abc\x01", strictSurrPol, ErrControlChar, 9},
		{"long_backslash_run_even", strings.Repeat(`\\`, 37) + `\uD83D\uDE00`, strictSurrPol, ErrNone, -1},
	}

	for _, tc := range cases {
		for _, chunk := range []int{1, 2, 3, 5, 7, 8, 16, 64, len(tc.body) + 1} {
			gotOff, gotErr := runWindowedValidate(tc.body, chunk, tc.pol)
			if gotErr != tc.want || (tc.want != ErrNone && gotOff != tc.off) {
				t.Fatalf("%s (chunk=%d): got (off=%d, err=%v), want (off=%d, err=%v)",
					tc.name, chunk, gotOff, gotErr, tc.off, tc.want)
			}
		}
	}

	// Zero-allocation proof over a large escaped input.
	bigBody := strings.Repeat(`\u0061\uD83D\uDE00\\`, 2048)
	allocs := testing.AllocsPerRun(20, func() {
		off, code := runWindowedValidate(bigBody, 64, strictSurrPol)
		if code != ErrNone || off != -1 {
			t.Fatalf("unexpected error: off=%d code=%v", off, code)
		}
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs/op in zero-output string validator, got %v", allocs)
	}
}
