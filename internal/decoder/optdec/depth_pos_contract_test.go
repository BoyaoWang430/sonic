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
	"errors"
	"strings"
	"testing"
)

func extractSonicSyntaxPos(t *testing.T, label string, err error) int {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected SyntaxError, got nil", label)
	}
	var sv SyntaxError
	if errors.As(err, &sv) {
		return sv.Pos
	}
	var sp *SyntaxError
	if errors.As(err, &sp) && sp != nil {
		return sp.Pos
	}
	t.Fatalf("%s: expected SyntaxError, got %T (%v)", label, err, err)
	return -1
}

// TestContainerDepthAndPosContract verifies:
//   - maxDepth counts ONLY open containers (root scalar=0, root container=1).
//   - Two-element arrays ([0,0]), nonempty objects ({"a":0}), and mixed containers
//     at depth 4095 and 4096 succeed across all Sonic scanners.
//   - Opening the 4097th container fails immediately at its opening '[' or '{'
//     (SyntaxError.Pos == tokPos), whether closed or truncated, invariant to
//     initial tape capacity, ISA/scalar mode, chunk size, and pool reuse.
//   - Earlier invalid bytes fail before a later >4096 run; EOF reports len(src).
func TestContainerDepthAndPosContract(t *testing.T) {
	buildMixed := func(depth int, leaf string, closeAll bool) (src string, open4097Pos int) {
		var b strings.Builder
		for i := 0; i < depth; i++ {
			if i == 4096 {
				open4097Pos = b.Len()
			}
			if i%2 == 0 {
				b.WriteByte('[')
			} else {
				b.WriteString(`{"a":`)
			}
		}
		b.WriteString(leaf)
		if closeAll {
			for i := depth - 1; i >= 0; i-- {
				if i%2 == 0 {
					b.WriteByte(']')
				} else {
					b.WriteByte('}')
				}
			}
		}
		return b.String(), open4097Pos
	}

	// 1. Valid depths 4095 and 4096 across empty, single, two-element, nonempty object, and mixed shapes.
	for _, depth := range []int{4095, 4096} {
		validShapes := []struct {
			name string
			src  string
		}{
			{"empty_array_leaf", strings.Repeat("[", depth-1) + "[]" + strings.Repeat("]", depth-1)},
			{"two_elem_array_leaf", strings.Repeat("[", depth) + "0,0" + strings.Repeat("]", depth)},
			{"empty_obj_leaf", strings.Repeat(`{"a":`, depth-1) + "{}" + strings.Repeat("}", depth-1)},
			{"nonempty_obj_leaf", strings.Repeat(`{"a":`, depth-1) + `{"a":0,"b":1}` + strings.Repeat("}", depth-1)},
		}
		mixedSrc, _ := buildMixed(depth, "0", true)
		validShapes = append(validShapes, struct{ name, src string }{"mixed_containers", mixedSrc})

		for _, sh := range validShapes {
			if _, err := decodeViaRunsWithInit[any](sh.src, diffStdOpts, nil); err != nil {
				t.Fatalf("depth=%d %s runs failed: %v", depth, sh.name, err)
			}
			if _, err := decodeViaScalarRunsWithInit[any](sh.src, diffStdOpts, nil); err != nil {
				t.Fatalf("depth=%d %s scalar failed: %v", depth, sh.name, err)
			}
			if _, err := decodeViaGiantWithInit[any](sh.src, diffStdOpts, nil); err != nil {
				t.Fatalf("depth=%d %s giant failed: %v", depth, sh.name, err)
			}
			if _, err := decodeViaChunkedStreamWithInit[any](sh.src, diffStdOpts, 37, nil); err != nil {
				t.Fatalf("depth=%d %s stream failed: %v", depth, sh.name, err)
			}
		}
	}

	// 2. Depth 4097 overflow across closed, unclosed, mixed, non-zero baseDepth Schema/Raw, capacities, and chunk sizes.
	mixed4097Closed, mixedPos := buildMixed(4097, "0", true)
	mixed4097Open, _ := buildMixed(4097, "", false)
	overflowCases := []struct {
		name    string
		src     string
		wantPos int
	}{
		{"array_closed_4097", strings.Repeat("[", 4097) + "0" + strings.Repeat("]", 4097), 4096},
		{"array_unclosed_4097", strings.Repeat("[", 4097), 4096},
		{"object_closed_4097", strings.Repeat(`{"a":`, 4096) + `{}` + strings.Repeat("}", 4096), 5 * 4096},
		{"object_unclosed_4097", strings.Repeat(`{"a":`, 4096) + `{`, 5 * 4096},
		{"mixed_closed_4097", mixed4097Closed, mixedPos},
		{"mixed_unclosed_4097", mixed4097Open, mixedPos},
	}

	for _, tc := range overflowCases {
		t.Run(tc.name, func(t *testing.T) {
			var dummy any
			_, _, errRouter := DecodeOne(tc.src, diffStdOpts, &dummy)
			_, errRuns := decodeViaRunsWithInit[any](tc.src, diffStdOpts, nil)
			_, errScalar := decodeViaScalarRunsWithInit[any](tc.src, diffStdOpts, nil)
			_, errGiant := decodeViaGiantWithInit[any](tc.src, diffStdOpts, nil)
			_, errStream1 := decodeViaChunkedStreamWithInit[any](tc.src, diffStdOpts, 1, nil)
			_, errStream64 := decodeViaChunkedStreamWithInit[any](tc.src, diffStdOpts, 64, nil)

			for name, err := range map[string]error{
				"router":   errRouter,
				"runs":     errRuns,
				"scalar":   errScalar,
				"giant":    errGiant,
				"stream1":  errStream1,
				"stream64": errStream64,
			} {
				if pos := extractSonicSyntaxPos(t, name, err); pos != tc.wantPos {
					t.Fatalf("%s Pos=%d, want %d (err=%v)", name, pos, tc.wantPos, err)
				}
			}

			// Capacity invariance across OutputFull boundaries (1, 2, 8, 24, 5000).
			for _, capInit := range []int{1, 2, 8, 24, 5000} {
				for _, scalar := range []bool{false, true} {
					p := cParityRunsTuneParser(tc.src, 0, capInit, diffStdOpts)
					code := parseRunsGrowingMode(p, scalar)
					if code != SONIC_STACK_OVERFLOW {
						t.Fatalf("cap=%d scalar=%v code=%v, want SONIC_STACK_OVERFLOW", capInit, scalar, code)
					}
					if pos := extractSonicSyntaxPos(t, "capCheck", p.fixError(code)); pos != tc.wantPos {
						t.Fatalf("cap=%d scalar=%v Pos=%d, want %d", capInit, scalar, pos, tc.wantPos)
					}
				}
			}
		})
	}

	// 3. Schema non-zero baseDepth (RawMessage and unknown field at outer depth 1 -> inner 4095 ok, 4096 overflow).
	type schemaBaseEnv struct {
		ID  int             `json:"id"`
		Raw json.RawMessage `json:"raw"`
	}
	okNested4095 := `{"id":7,"skip":` + strings.Repeat("[", 4095) + "1" + strings.Repeat("]", 4095) + `,"raw":` + strings.Repeat("[", 4095) + "2" + strings.Repeat("]", 4095) + `}`
	if got, _, err := decodeViaSchemaOrFullWithInit[schemaBaseEnv](okNested4095, diffStdOpts, nil); err != nil || got.ID != 7 || len(got.Raw) == 0 {
		t.Fatalf("depth 1+4095=4096 schema/raw failed: err=%v id=%d", err, got.ID)
	}
	overNested4096 := `{"id":7,"raw":` + strings.Repeat("[", 4096) + "2" + strings.Repeat("]", 4096) + `}`
	wantRawOverPos := len(`{"id":7,"raw":`) + 4095
	_, _, errSchOver := decodeViaSchemaOrFullWithInit[schemaBaseEnv](overNested4096, diffStdOpts, nil)
	_, errRunsOver := decodeViaRunsWithInit[schemaBaseEnv](overNested4096, diffStdOpts, nil)
	_, errStrmOver := decodeViaChunkedStreamWithInit[schemaBaseEnv](overNested4096, diffStdOpts, 31, nil)
	if extractSonicSyntaxPos(t, "schemaOver", errSchOver) != wantRawOverPos ||
		extractSonicSyntaxPos(t, "runsOver", errRunsOver) != wantRawOverPos ||
		extractSonicSyntaxPos(t, "streamOver", errStrmOver) != wantRawOverPos {
		t.Fatalf("non-zero baseDepth overflow Pos mismatch: schema=%v runs=%v stream=%v want=%d", errSchOver, errRunsOver, errStrmOver, wantRawOverPos)
	}

	// 4. Explicit reuse of the exact same Parser & StreamSession after 4097 overflow.
	reusedP := newParserImpl(overflowCases[0].src, 0, diffStdOpts)
	if code := reusedP.parse(); code != SONIC_STACK_OVERFLOW {
		t.Fatalf("first parse on reusedP got %v", code)
	}
	reusedP.reset()
	reusedP.Json = `{"id":42,"raw":[1,2,3]}`
	reusedP.end = len(reusedP.Json)
	reusedP.options = diffStdOpts
	reusedP.nbuf.init(reusedP.nodes)
	if code := reusedP.parse(); code != SONIC_OK || reusedP.nbuf.depth != 0 || reusedP.nbuf.stat.max_depth != 2 {
		t.Fatalf("reusedP after reset corrupted: code=%v depth=%d max_depth=%d", code, reusedP.nbuf.depth, reusedP.nbuf.stat.max_depth)
	}
	reusedP.free()

	reusedSess := getStreamSession()
	reusedSess.beginComplete(diffStdOpts, true, overflowCases[0].src, true)
	if _, code := reusedSess.Step(overflowCases[0].src, true); code != SONIC_STACK_OVERFLOW {
		t.Fatalf("first Step on reusedSess got %v", code)
	}
	reusedSess.Reset()
	validNext := `[10,20]`
	reusedSess.beginComplete(diffStdOpts, true, validNext, true)
	if reason, code := reusedSess.Step(validNext, true); reason != scanStopDone || code != SONIC_OK || reusedSess.p.nbuf.depth != 0 {
		t.Fatalf("reusedSess after Reset failed: reason=%v code=%v depth=%d", reason, code, reusedSess.p.nbuf.depth)
	}
	FreeStreamSession(reusedSess)

	// 5. Earlier invalid byte priority & true-end EOF Pos.
	earlyBad := `[[?` + strings.Repeat("[", 4100)
	for name, fn := range map[string]func() error{
		"runs":   func() error { _, e := decodeViaRunsWithInit[any](earlyBad, diffStdOpts, nil); return e },
		"scalar": func() error { _, e := decodeViaScalarRunsWithInit[any](earlyBad, diffStdOpts, nil); return e },
		"stream": func() error { _, e := decodeViaChunkedStreamWithInit[any](earlyBad, diffStdOpts, 5, nil); return e },
	} {
		if pos := extractSonicSyntaxPos(t, name+"/earlyBad", fn()); pos != 2 {
			t.Fatalf("%s failed to prioritize earlier invalid byte at 2: got Pos=%d", name, pos)
		}
	}

	for _, eofInput := range []string{"", "   ", "[1, 2", `"unterminated`, `{"a":`} {
		_, eRuns := decodeViaRunsWithInit[any](eofInput, diffStdOpts, nil)
		_, eScalar := decodeViaScalarRunsWithInit[any](eofInput, diffStdOpts, nil)
		_, eStream := decodeViaChunkedStreamWithInit[any](eofInput, diffStdOpts, 3, nil)
		for name, err := range map[string]error{"runs": eRuns, "scalar": eScalar, "stream": eStream} {
			if pos := extractSonicSyntaxPos(t, name+"/eof", err); pos != len(eofInput) {
				t.Fatalf("%s on %q reported Pos=%d, want len=%d (err=%v)", name, eofInput, pos, len(eofInput), err)
			}
		}
	}
}
