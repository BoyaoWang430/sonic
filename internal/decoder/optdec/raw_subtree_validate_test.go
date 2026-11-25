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
	"encoding/json"
	"strings"
	"testing"
)

// TestNoValidateJSONStillValidatesRawSubtrees checks that NoValidateJSONSkip
// only relaxes values that are skipped. A json.RawMessage is consumed, so an
// invalid subtree must be rejected whether the decode runs through the full
// parser (small input) or the schema projection (input of 64 bytes or more).
func TestNoValidateJSONStillValidatesRawSubtrees(t *testing.T) {
	type target struct {
		ID   int             `json:"id"`
		Data json.RawMessage `json:"data"`
		Pad  string          `json:"pad"`
	}
	noValidate := uint64(1 << _F_no_validate_json)
	pad := strings.Repeat("p", 80)

	invalid := []struct {
		raw  string
		opts uint64
	}{
		{`{"a" 1,"b":[1,,2]}`, noValidate},
		{`[1,,2]`, noValidate},
		{`{"a":[}`, noValidate},
		{"\"x\x01y\"", noValidate | 1<<_F_validate_string},
	}
	for _, tc := range invalid {
		for _, doc := range []string{
			`{"id":1,"data":` + tc.raw + `}`,
			`{"id":1,"data":` + tc.raw + `,"pad":"` + pad + `"}`,
		} {
			var got target
			if _, _, err := DecodeOne(doc, tc.opts, &got); err == nil {
				t.Fatalf("invalid raw subtree accepted for %q: Data=%q", doc, got.Data)
			}
		}
	}

	const raw = `{"a":[1,2,{"b":"c"}]}`
	var got target
	doc := `{"id":2,"data":` + raw + `,"pad":"` + pad + `"}`
	if _, _, err := DecodeOne(doc, noValidate, &got); err != nil {
		t.Fatalf("valid raw subtree rejected: %v", err)
	}
	if got.ID != 2 || string(got.Data) != raw || got.Pad != pad {
		t.Fatalf("unexpected result: %+v", got)
	}
}
