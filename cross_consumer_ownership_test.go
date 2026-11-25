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

package sonic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic/ast"
	"github.com/bytedance/sonic/internal/cpu"
	decerrors "github.com/bytedance/sonic/internal/decoder/errors"
	"github.com/bytedance/sonic/internal/encoder/alg"
)

type g4CallbackBox struct {
	Payload string
}

var g4ActiveCallbacks []string

func (b *g4CallbackBox) UnmarshalJSON(data []byte) error {
	g4ActiveCallbacks = append(g4ActiveCallbacks, "cb:"+string(data))
	// Re-entrant Unmarshal + Valid + Compact + AST inside a materialization callback
	// must run as independent operations without corrupting caller state (G4 §8.5).
	var nested map[string]int
	if err := ConfigDefault.Unmarshal([]byte(`{"re":7}`), &nested); err != nil || nested["re"] != 7 {
		return errors.New("reentrant Unmarshal failed")
	}
	if !Valid([]byte(`["ok",true]`)) {
		return errors.New("reentrant Valid failed")
	}
	if cbuf, err := alg.AppendCompactedJSON(nil, []byte(` { "x" : 1 } `), 0); err != nil || string(cbuf) != `{"x":1}` {
		return errors.New("reentrant Compact failed")
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.Payload = s
	return nil
}

type g4CrossTarget struct {
	ID       int64           `json:"id"`
	Selected string          `json:"selected"`
	Raw      json.RawMessage `json:"raw"`
	CB       g4CallbackBox   `json:"cb"`
}

type g4CountVisitor struct {
	keys []string
	strs []string
	nums []string
}

func (v *g4CountVisitor) OnNull() error           { return nil }
func (v *g4CountVisitor) OnBool(bool) error       { return nil }
func (v *g4CountVisitor) OnString(s string) error { v.strs = append(v.strs, s); return nil }
func (v *g4CountVisitor) OnInt64(_ int64, n json.Number) error {
	v.nums = append(v.nums, string(n))
	return nil
}
func (v *g4CountVisitor) OnFloat64(_ float64, n json.Number) error {
	v.nums = append(v.nums, string(n))
	return nil
}
func (v *g4CountVisitor) OnObjectBegin(int) error    { return nil }
func (v *g4CountVisitor) OnObjectKey(k string) error { v.keys = append(v.keys, k); return nil }
func (v *g4CountVisitor) OnObjectEnd() error         { return nil }
func (v *g4CountVisitor) OnArrayBegin(int) error     { return nil }
func (v *g4CountVisitor) OnArrayEnd() error          { return nil }

// oddChunkReader exercises Reader n>0 alongside io.EOF, zero-byte empty reads,
// and small arbitrary chunk splits (G4 §8.4 item 15).
type oddChunkReader struct {
	data      []byte
	off       int
	chunkSize int
	step      int
}

func (r *oddChunkReader) Read(p []byte) (int, error) {
	r.step++
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	// Inject deterministic 0-byte, nil-error reads every 3rd call.
	if r.step%3 == 0 {
		return 0, nil
	}
	n := r.chunkSize
	if n > len(p) {
		n = len(p)
	}
	rem := len(r.data) - r.off
	if n >= rem {
		copy(p, r.data[r.off:])
		r.off = len(r.data)
		// Return n > 0 AND io.EOF simultaneously on the final chunk!
		return rem, io.EOF
	}
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

// TestG4EightWayCrossConsumerAndOwnershipMatrix verifies G4.5 / §8.4 / §8.5 across:
//  1. Full ModeEmit (interface{} Unmarshal)
//  2. Schema selected fields
//  3. Schema unknown-field validation (including DisallowUnknownFields)
//  4. RawSpan (json.RawMessage verbatim preservation)
//  5. Stream chunked (oddChunkReader n>0+EOF, empty reads, Buffered() remainder)
//  6. ValidateOnly (ConfigStd.Valid)
//  7. Compact profile (whitespace/HTML observation + declared 10000 vs 4096 depth difference)
//  8. Applicable AST consumers (Get, ast.Loads, ast.LoadsUseNumber, ast.Preorder)
func TestG4EightWayCrossConsumerAndOwnershipMatrix(t *testing.T) {
	origScalar := cpu.UseScalar
	defer func() { cpu.UseScalar = origScalar }()

	for _, forceScalar := range []bool{false, true} {
		cpu.UseScalar = forceScalar

		validCorpus := []string{
			`{"id":101,"unknown_esc":"skip\n\u4e2d","selected":"val\t1","raw":{"nested":[1,true, "raw\\keep" ]},"cb":"box\n1"}`,
			` { "unknown_deep" : [{"a":[1,2,3],"b":{"c":"d"}}] , "id" : -42 , "selected" : "plain_ascii_value_32_bytes_long!" , "raw" : [ -0 , 1.5e2 ] , "cb" : "hello" } `,
		}

		for idx, doc := range validCorpus {
			origBytes := []byte(doc)
			workBuf := append([]byte(nil), origBytes...)

			// (1) Full ModeEmit -> map[string]any
			var fullAny map[string]any
			if err := ConfigStd.Unmarshal(workBuf, &fullAny); err != nil {
				t.Fatalf("scalar=%v valid[%d] Full ModeEmit failed: %v", forceScalar, idx, err)
			}

			// (2) & (3) & (4) Schema selected + unknown skip + RawSpan + callback re-entrancy
			g4ActiveCallbacks = nil
			var schemaGot g4CrossTarget
			if err := ConfigStd.Unmarshal(workBuf, &schemaGot); err != nil {
				t.Fatalf("scalar=%v valid[%d] Schema/RawSpan failed: %v", forceScalar, idx, err)
			}
			if len(g4ActiveCallbacks) != 1 {
				t.Fatalf("scalar=%v valid[%d] expected 1 materialization callback, got %v", forceScalar, idx, g4ActiveCallbacks)
			}

			// Verify strict DisallowUnknownFields rejects unknown keys.
			var strictUnknown g4CrossTarget
			cfgDis := Config{DisallowUnknownFields: true, ValidateString: true}.Froze()
			if err := cfgDis.Unmarshal(workBuf, &strictUnknown); err == nil {
				t.Fatalf("scalar=%v valid[%d] expected DisallowUnknownFields error", forceScalar, idx)
			}

			// (5) Stream chunked (with 0-byte reads and n>0+EOF) + multi-value Buffered() remainder
			trailingStream := string(workBuf) + "\n  {\"tail\":99}"
			for _, chunkSz := range []int{1, 7, 31, 64} {
				rd := &oddChunkReader{data: []byte(trailingStream), chunkSize: chunkSz}
				dec := ConfigStd.NewDecoder(rd)
				var streamGot g4CrossTarget
				if err := dec.Decode(&streamGot); err != nil {
					t.Fatalf("scalar=%v valid[%d] stream chunk=%d failed: %v", forceScalar, idx, chunkSz, err)
				}
				if !reflect.DeepEqual(streamGot, schemaGot) {
					t.Fatalf("scalar=%v valid[%d] stream chunk=%d mismatch: got=%+v want=%+v", forceScalar, idx, chunkSz, streamGot, schemaGot)
				}
				remBytes, _ := io.ReadAll(io.MultiReader(dec.Buffered(), rd))
				if strings.TrimSpace(string(remBytes)) != `{"tail":99}` {
					t.Fatalf("scalar=%v valid[%d] stream chunk=%d Buffered remainder=%q", forceScalar, idx, chunkSz, string(remBytes))
				}
			}

			// (6) ValidateOnly
			if !ConfigStd.Valid(workBuf) {
				t.Fatalf("scalar=%v valid[%d] ConfigStd.Valid returned false", forceScalar, idx)
			}

			// (7) Compact profile
			compBytes, err := alg.AppendCompactedJSON(nil, workBuf, 0)
			if err != nil {
				t.Fatalf("scalar=%v valid[%d] AppendCompactedJSON failed: %v", forceScalar, idx, err)
			}
			var stdCompBuf bytes.Buffer
			if err := json.Compact(&stdCompBuf, origBytes); err != nil {
				t.Fatal(err)
			}
			if string(compBytes) != stdCompBuf.String() {
				t.Fatalf("scalar=%v valid[%d] Compact=%q want=%q", forceScalar, idx, string(compBytes), stdCompBuf.String())
			}

			// (8) Applicable AST consumers: Get, Loads, LoadsUseNumber, Preorder
			gotNode, err := Get(workBuf, "selected")
			if err != nil {
				t.Fatalf("scalar=%v valid[%d] sonic.Get failed: %v", forceScalar, idx, err)
			}
			gotSel, err := gotNode.String()
			if err != nil || gotSel != schemaGot.Selected {
				t.Fatalf("scalar=%v valid[%d] sonic.Get=%q want=%q (err=%v)", forceScalar, idx, gotSel, schemaGot.Selected, err)
			}
			cLoads, vLoads, err := ast.Loads(string(workBuf))
			if err != nil || cLoads > len(workBuf) || !reflect.DeepEqual(vLoads, fullAny) {
				t.Fatalf("scalar=%v valid[%d] ast.Loads mismatch: consumed=%d err=%v", forceScalar, idx, cLoads, err)
			}
			cUseNum, _, err := ast.LoadsUseNumber(string(workBuf))
			if err != nil || cUseNum != cLoads {
				t.Fatalf("scalar=%v valid[%d] ast.LoadsUseNumber mismatch: %d vs %d (%v)", forceScalar, idx, cUseNum, cLoads, err)
			}
			var vis g4CountVisitor
			if err := ast.Preorder(string(workBuf), &vis, nil); err != nil {
				t.Fatalf("scalar=%v valid[%d] ast.Preorder failed: %v", forceScalar, idx, err)
			}
			if len(vis.keys) < 5 {
				t.Fatalf("scalar=%v valid[%d] unexpected visitor keys: %v", forceScalar, idx, vis.keys)
			}

			// Verify source input was 100% read-only across all 8 consumers, AND
			// mutating workBuf afterwards does not alter CopyString/Raw outputs (§8.5 items 1-4).
			if !bytes.Equal(workBuf, origBytes) {
				t.Fatalf("scalar=%v valid[%d] input buffer mutated!", forceScalar, idx)
			}
			wantSel := schemaGot.Selected
			wantRaw := string(schemaGot.Raw)
			if !strings.Contains(wantRaw, "raw\\\\keep") && idx == 0 {
				t.Fatalf("RawSpan was decoded instead of preserving verbatim source: %q", wantRaw)
			}
			for i := range workBuf {
				workBuf[i] = 'X'
			}
			if schemaGot.Selected != wantSel || string(schemaGot.Raw) != wantRaw {
				t.Fatalf("scalar=%v valid[%d] returned struct/Raw aliased caller buffer!", forceScalar, idx)
			}
		}

		// Malformed corpus: verify accept/reject consistency, callback non-invocation on scan failure,
		// error coordinate agreement across Sonic consumers, and pool cleanliness after error (§8.4 / §8.5).
		malformed := []struct {
			name string
			src  string
			pos  int
		}{
			{"missing_colon_before_bad_escape", `{"id" "\q", "cb":"skip"}`, 6},
			{"mismatched_bracket", `{"id":[1,2}`, 10},
			{"bad_escape_in_unknown", `{"unknown":"abc\qdef","cb":"never"}`, 15},
			{"bad_number_in_raw", `{"raw":1e+,"cb":"never"}`, 10},
			{"trailing_garbage_full_doc", `{"id":1,"cb":"ok"} trailing`, 19},
		}
		for _, tc := range malformed {
			g4ActiveCallbacks = nil
			var anyDst any
			errFull := ConfigStd.Unmarshal([]byte(tc.src), &anyDst)
			var schemaDst g4CrossTarget
			errSchema := ConfigStd.Unmarshal([]byte(tc.src), &schemaDst)
			validOK := ConfigStd.Valid([]byte(tc.src))
			_, errComp := alg.AppendCompactedJSON(nil, []byte(tc.src), 0)
			streamDec := ConfigStd.NewDecoder(strings.NewReader(tc.src))
			var streamDst g4CrossTarget
			errStream := streamDec.Decode(&streamDst)
			if tc.name == "trailing_garbage_full_doc" {
				// Declared boundary difference (§7 G4.5): Stream Decode is multi-value prefix,
				// so the 1st value succeeds and the 2nd Decode rejects trailing garbage!
				if errStream != nil {
					t.Fatalf("scalar=%v stream 1st Decode unexpectedly failed on valid prefix: %v", forceScalar, errStream)
				}
				var secondDst any
				errStream = streamDec.Decode(&secondDst)
			}

			if errFull == nil || errSchema == nil || validOK || errComp == nil || errStream == nil {
				t.Fatalf("scalar=%v malformed[%s]: expected consumers to reject: full=%v schema=%v valid=%v comp=%v stream=%v",
					forceScalar, tc.name, errFull, errSchema, validOK, errComp, errStream)
			}
			// On scan-stage syntax failure, Unmarshaler callbacks must NOT run before a valid Document exists (except trailing_garbage in single-value Stream).
			if tc.name != "trailing_garbage_full_doc" && len(g4ActiveCallbacks) != 0 {
				t.Fatalf("scalar=%v malformed[%s]: callback ran despite scan syntax error: %v", forceScalar, tc.name, g4ActiveCallbacks)
			}
			if !testSonicCompatBuildTag {
				var seFull, seSchema decerrors.SyntaxError
				if errors.As(errFull, &seFull) && errors.As(errSchema, &seSchema) {
					if seFull.Pos != tc.pos || seSchema.Pos != tc.pos {
						t.Fatalf("scalar=%v malformed[%s]: SyntaxError.Pos full=%d schema=%d want=%d",
							forceScalar, tc.name, seFull.Pos, seSchema.Pos, tc.pos)
					}
				}
			}

			// Verify pool cleanliness right after error: immediate next valid decode must succeed cleanly (§8.5 item 10).
			var cleanCheck g4CrossTarget
			if err := ConfigStd.Unmarshal([]byte(`{"id":7,"selected":"ok","raw":true,"cb":"c"}`), &cleanCheck); err != nil || cleanCheck.ID != 7 || cleanCheck.Selected != "ok" {
				t.Fatalf("scalar=%v malformed[%s] polluted pooled parser: err=%v got=%+v", forceScalar, tc.name, err, cleanCheck)
			}
		}

		// Explicitly preserve declared profile differences (4096 Decode/Valid vs 10000 Compact; §7 G4.5 / §11):
		if !testSonicCompatBuildTag {
			depth5000 := strings.Repeat("[", 5000) + "0" + strings.Repeat("]", 5000)
			if ConfigStd.Valid([]byte(depth5000)) {
				t.Fatalf("scalar=%v depth 5000 must be rejected by 4096-depth Valid profile", forceScalar)
			}
			if _, err := alg.AppendCompactedJSON(nil, []byte(depth5000), 0); err != nil {
				t.Fatalf("scalar=%v depth 5000 must be accepted by 10000-depth Compact profile: %v", forceScalar, err)
			}
		}
	}
}
