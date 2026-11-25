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
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// Error exits expose the current grammar phase as well as the cursor. In
// particular, reading a token may change the active stack before it fails.
func TestRISCVValidateOneErrorState(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		err   types.ParsingError
		stack []int
	}{
		{"empty", "", types.ERR_EOF, []int{skipValueState}},
		{"first_array_eof", "[", types.ERR_EOF, []int{skipFirstArrayState}},
		{"first_array_nul", "[\x00", types.ERR_EOF, []int{skipFirstArrayState}},
		{"first_array_invalid", "[?", types.ERR_INVALID_CHAR, []int{skipArrayState}},
		{"array_tail_eof", "[1", types.ERR_EOF, []int{skipArrayState}},
		{"array_tail_nul", "[1\x00", types.ERR_EOF, []int{skipArrayState}},
		{"array_value_eof", "[1,", types.ERR_EOF, []int{skipArrayState, skipValueState}},
		{"array_value_nul", "[1,\x00", types.ERR_EOF, []int{skipArrayState, skipValueState}},
		{"array_value_invalid", "[1,?", types.ERR_INVALID_CHAR, []int{skipArrayState}},
		{"first_object_eof", "{", types.ERR_EOF, []int{skipFirstObjectState}},
		{"first_object_nul", "{\x00", types.ERR_EOF, []int{skipFirstObjectState}},
		{"first_object_invalid", "{]", types.ERR_INVALID_CHAR, []int{skipFirstObjectState}},
		{"first_key_eof", `{"a`, types.ERR_EOF, []int{skipObjectState}},
		{"colon_eof", `{"a"`, types.ERR_EOF, []int{skipObjectState, skipElementState}},
		{"colon_nul", "{\"a\"\x00", types.ERR_EOF, []int{skipObjectState, skipElementState}},
		{"colon_invalid", `{"a"?`, types.ERR_INVALID_CHAR, []int{skipObjectState, skipElementState}},
		{"object_value_eof", `{"a":`, types.ERR_EOF, []int{skipObjectState, skipValueState}},
		{"object_value_nul", "{\"a\":\x00", types.ERR_EOF, []int{skipObjectState, skipValueState}},
		{"object_value_invalid", `{"a":?`, types.ERR_INVALID_CHAR, []int{skipObjectState}},
		{"object_tail_nul", "{\"a\":1\x00", types.ERR_EOF, []int{skipObjectState}},
		{"next_key_eof", `{"a":1,`, types.ERR_EOF, []int{skipObjectState, skipKeyState}},
		{"next_key_nul", "{\"a\":1,\x00", types.ERR_EOF, []int{skipObjectState, skipKeyState}},
		{"next_key_body_eof", `{"a":1,"`, types.ERR_EOF, []int{skipObjectState, skipElementState}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, flags := range []uint64{0, types.F_VALIDATE_STRING} {
				var m types.StateMachine
				m.Sp, m.Vt[0] = 1, -1 // Reuse must not preserve the prior active state.
				p := 0
				ret := ValidateOne(&tc.src, &p, &m, flags)
				if ret != -int(tc.err) || p != len(tc.src) || m.Sp != len(tc.stack) {
					t.Fatalf("flags=%d: got ret=%d cursor=%d sp=%d; want %d/%d/%d", flags, ret, p, m.Sp, -int(tc.err), len(tc.src), len(tc.stack))
				}
				if !equalState(m.Vt[:m.Sp], tc.stack) {
					t.Fatalf("flags=%d: active states=%v, want %v", flags, m.Vt[:m.Sp], tc.stack)
				}
			}
		})
	}
}

func TestRISCVValidateOneStateSlotLimit(t *testing.T) {
	const limit = types.MAX_RECURSE
	// depth counts ONLY open containers (root scalar=0,
	// root container=1). Subsequent array elements (",0") and nonempty object keys
	// ("{\"a\":0}") at depth==MAX_RECURSE do not consume extra container depth;
	// only opening the (MAX_RECURSE+1)-th container fails immediately at its '['/'{'.
	arrayPrefix, arraySuffix := strings.Repeat("[", limit), strings.Repeat("]", limit)
	objectPrefix := strings.Repeat(`{"a":`, limit-1)
	cases := []struct {
		name   string
		src    string
		cursor int // A negative cursor denotes successful traversal.
		state  int
	}{
		{"array_single_value", arrayPrefix + "0" + arraySuffix, -1, 0},
		{"array_second_value", arrayPrefix + "0,0" + arraySuffix, -1, 0},
		{"array_one_too_deep", arrayPrefix + "[0]" + arraySuffix, limit + 1, skipArrayState},
		{"object_empty_leaf", objectPrefix + "{}" + strings.Repeat("}", limit-1), -1, 0},
		{"object_nonempty_leaf", objectPrefix + `{"a":0}` + strings.Repeat("}", limit-1), -1, 0},
		{"object_one_too_deep", objectPrefix + `{"a":{}}` + strings.Repeat("}", limit-1), 5*limit + 1, skipObjectState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m types.StateMachine
			p := 0
			ret := ValidateOne(&tc.src, &p, &m, 0)
			if tc.cursor < 0 {
				if ret != 0 || p != len(tc.src) || m.Sp != 0 {
					t.Fatalf("got ret=%d cursor=%d sp=%d; want 0/%d/0", ret, p, m.Sp, len(tc.src))
				}
				return
			}
			if ret != -int(types.ERR_RECURSE_EXCEED_MAX) || p != tc.cursor || m.Sp != limit {
				t.Fatalf("got ret=%d cursor=%d sp=%d; want %d/%d/%d", ret, p, m.Sp, -int(types.ERR_RECURSE_EXCEED_MAX), tc.cursor, limit)
			}
			for i, state := range m.Vt[:m.Sp] {
				if state != tc.state {
					t.Fatalf("state[%d]=%d, want %d", i, state, tc.state)
				}
			}
		})
	}

	// Non-zero baseDepth and out-of-range baseDepth checks.
	t.Run("nonzero_and_invalid_base_depth", func(t *testing.T) {
		ok2 := `[{"k":1}]` // local container depth = 2
		p := 0
		if ret := ValidateOneAtDepth(&ok2, &p, nil, 0, limit-2); ret != 0 || p != len(ok2) {
			t.Fatalf("baseDepth=limit-2 on depth-2 value: ret=%d p=%d", ret, p)
		}
		p = 0
		if ret := ValidateOneAtDepth(&ok2, &p, nil, 0, limit-1); ret != -int(types.ERR_RECURSE_EXCEED_MAX) || p != 2 {
			t.Fatalf("baseDepth=limit-1 on depth-2 value: ret=%d p=%d, want %d/2", ret, p, -int(types.ERR_RECURSE_EXCEED_MAX))
		}
		scalar := `"ok"` // root scalar depth = 0
		p = 0
		if ret := ValidateOneAtDepth(&scalar, &p, nil, 0, limit); ret != 0 || p != len(scalar) {
			t.Fatalf("baseDepth=limit on scalar: ret=%d p=%d", ret, p)
		}
		p = 0
		if ret := ValidateOneAtDepth(&scalar, &p, nil, 0, -1); ret != -int(types.ERR_RECURSE_EXCEED_MAX) {
			t.Fatalf("negative baseDepth must fail: ret=%d", ret)
		}
		p = 0
		if ret := ValidateOneAtDepth(&scalar, &p, nil, 0, limit+1); ret != -int(types.ERR_RECURSE_EXCEED_MAX) {
			t.Fatalf("baseDepth>limit must fail: ret=%d", ret)
		}
	})
}
