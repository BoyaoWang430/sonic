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
	"math"
	"reflect"
	"strings"
	"testing"
)

type testFieldMatcher struct {
	names []string
}

func (f testFieldMatcher) FieldCount() int          { return len(f.names) }
func (f testFieldMatcher) FieldName(idx int) string { return f.names[idx] }
func (f testFieldMatcher) MatchField(key string, caseSensitive, _ bool) int {
	for i, n := range f.names {
		if n == key || (!caseSensitive && strings.EqualFold(n, key)) {
			return i
		}
	}
	return -1
}

func runSplitStream(t *testing.T, src string, chunkSize int, initCap int, growBy int, prof Profile, mode OutputMode, plan *Plan) (ScanResult, []Node, []byte, CompactObservation) {
	t.Helper()
	var m Machine
	m.Reset(prof, mode)
	if plan != nil {
		m.ConfigureSchema(plan, false, false, false)
	}
	if mode == ModeEmit {
		m.Nodes = make([]Node, initCap)
	}
	fed := 0
	var buf []byte
	for {
		if fed == 0 || m.Grammar.Phase != PhaseFlushPending {
			if fed < len(src) && (len(buf) == 0 || m.Grammar.Phase != PhaseFlushPending) {
				// Only feed more input on initial step or after StopNeedInput.
			}
		}
		if len(buf) == 0 && fed < len(src) {
			end := fed + chunkSize
			if end > len(src) {
				end = len(src)
			}
			buf = append(buf, src[fed:end]...)
			fed = end
		}
		final := fed >= len(src)
		res := m.Step(string(buf), final)
		switch res.Reason {
		case StopNeedInput:
			if final {
				t.Fatalf("unexpected StopNeedInput when final=true on %q", src)
			}
			end := fed + chunkSize
			if end > len(src) {
				end = len(src)
			}
			buf = append(buf, src[fed:end]...)
			fed = end
		case StopNeedOutput:
			nextLen := len(m.Nodes) + growBy
			if growBy <= 0 {
				if len(m.Nodes) == 0 {
					nextLen = 1
				} else {
					nextLen = len(m.Nodes) * 2
				}
			}
			grown := make([]Node, nextLen)
			copy(grown[:m.NodeLen], m.Nodes[:m.NodeLen])
			m.Nodes = grown
		default:
			return res, append([]Node(nil), m.Nodes[:m.NodeLen]...), append([]byte(nil), m.Unesc...), m.CompactObs
		}
	}
}

func TestProfileContainerDepth4096And10000(t *testing.T) {
	// 1. Strict 4096 profile: root scalar = 0, 4096 nested multi-element arrays & non-empty objects pass, 4097 rejects at open.
	strictProf := StrictProfile(true, true, false, false, true, true)
	var m Machine
	m.Reset(strictProf, ModeValidateOnly)
	resScalar := m.Step("42", true)
	if resScalar.Reason != StopDone || m.Stat.MaxDepth != 0 {
		t.Fatalf("root scalar expected depth 0, got reason=%v maxDepth=%d", resScalar.Reason, m.Stat.MaxDepth)
	}

	arr4096TwoElem := strings.Repeat("[", 4096) + "1,2" + strings.Repeat("]", 4096)
	m.Reset(strictProf, ModeValidateOnly)
	res4096 := m.Step(arr4096TwoElem, true)
	if res4096.Reason != StopDone || m.Stat.MaxDepth != 4096 {
		t.Fatalf("4096 two-element array failed: res=%+v maxDepth=%d", res4096, m.Stat.MaxDepth)
	}

	obj4096 := strings.Repeat(`{"k":`, 4096) + "1" + strings.Repeat("}", 4096)
	m.Reset(strictProf, ModeValidateOnly)
	resObj4096 := m.Step(obj4096, true)
	if resObj4096.Reason != StopDone || m.Stat.MaxDepth != 4096 {
		t.Fatalf("4096 non-empty object failed: res=%+v maxDepth=%d", resObj4096, m.Stat.MaxDepth)
	}

	arr4097 := strings.Repeat("[", 4097) + "1" + strings.Repeat("]", 4097)
	m.Reset(strictProf, ModeValidateOnly)
	res4097 := m.Step(arr4097, true)
	if res4097.Reason != StopError || res4097.Code != ErrDepthOverflow || res4097.ErrorOffset != 4096 {
		t.Fatalf("4097 expected ErrDepthOverflow at 4096, got %+v", res4097)
	}

	// Non-zero BaseDepth checks bounds immediately.
	baseProf := strictProf
	baseProf.BaseDepth = 4095
	m.Reset(baseProf, ModeValidateOnly)
	if r := m.Step(`[1]`, true); r.Reason != StopDone {
		t.Fatalf("BaseDepth=4095 + [1] expected StopDone, got %+v", r)
	}
	m.Reset(baseProf, ModeValidateOnly)
	if r := m.Step(`[[1]]`, true); r.Reason != StopError || r.Code != ErrDepthOverflow || r.ErrorOffset != 1 {
		t.Fatalf("BaseDepth=4095 + [[1]] expected ErrDepthOverflow at 1, got %+v", r)
	}

	// 2. Compact 10000 profile: 9999 and 10000 pass, 10001 rejects at open (pos=10000).
	compProf := CompactProfile(true, true)
	arr10000 := strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000)
	m.Reset(compProf, ModeCompactObserve)
	if r := m.Step(arr10000, true); r.Reason != StopDone {
		t.Fatalf("Compact 10000 failed: %+v", r)
	}
	arr10001 := strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001)
	m.Reset(compProf, ModeCompactObserve)
	if r := m.Step(arr10001, true); r.Reason != StopError || r.Code != ErrDepthOverflow || r.ErrorOffset != 10000 {
		t.Fatalf("Compact 10001 expected ErrDepthOverflow at 10000, got %+v", r)
	}
}

func TestAllSplitPositionsOutputFullAndModeEquivalence(t *testing.T) {
	prof := StrictProfile(true, true, true, false, true, true)
	validCases := []string{
		`{"a":1,"b":[true,false,null,-0.0,1.25e+10],"esc":"hello\n\u4e2d\u6587\uD83D\uDE00"}`,
		`[{"k":""}, [], {}, "plain", -9223372036854775808, 18446744073709551615]`,
	}
	for _, src := range validCases {
		refRes, refNodes, refUnesc, _ := runSplitStream(t, src, len(src)+1, 64, 0, prof, ModeEmit, nil)
		if refRes.Reason != StopDone {
			t.Fatalf("ref failed on %q: %+v", src, refRes)
		}
		// Exhaustively split at EVERY 1-byte and boundary chunk size AND cap=0/1 (+1 node growth).
		for _, chunk := range []int{1, 2, 3, 7, 15, 16, 17, 31, 32, 33, 63, 64, 65} {
			for _, capInit := range []int{0, 1, 2, 5} {
				gotRes, gotNodes, gotUnesc, _ := runSplitStream(t, src, chunk, capInit, 1, prof, ModeEmit, nil)
				if gotRes != refRes || !reflect.DeepEqual(gotNodes, refNodes) || string(gotUnesc) != string(refUnesc) {
					t.Fatalf("mismatch chunk=%d capInit=%d on %q:\n  gotRes=%+v wantRes=%+v", chunk, capInit, src, gotRes, refRes)
				}
			}
		}
		// Mode equivalence: ValidateOnly, RawSpan, CompactObserve all accept identical valid set.
		for _, mode := range []OutputMode{ModeValidateOnly, ModeRawSpan, ModeCompactObserve} {
			mRes, _, _, _ := runSplitStream(t, src, 3, 0, 1, prof, mode, nil)
			if mRes.Reason != StopDone || mRes.Consumed != len(src) {
				t.Fatalf("mode=%v rejected valid input %q: %+v", mode, src, mRes)
			}
		}
	}

	invalidCases := []struct {
		src     string
		wantOff int
		wantErr ScanError
	}{
		{`{"a":1,"b":[truX]}`, 15, ErrInvalidLiteral},
		{`{"a":"\uD800\u0041"}`, 6, ErrInvalidEscapeUTF},
		{`{"a":"\q"}`, 6, ErrInvalidEscape},
		{`{"a":1e+}`, 8, ErrInvalidNumber},
		{`{"a":1`, 6, ErrEOF},
		{`{"a":1} trailing`, 8, ErrInvalidChar},
	}
	for _, tc := range invalidCases {
		for _, chunk := range []int{1, 2, 5, 16, 64} {
			for _, capInit := range []int{0, 1, 8} {
				resEmit, _, _, _ := runSplitStream(t, tc.src, chunk, capInit, 1, prof, ModeEmit, nil)
				resVal, _, _, _ := runSplitStream(t, tc.src, chunk, 0, 1, prof, ModeValidateOnly, nil)
				resRaw, _, _, _ := runSplitStream(t, tc.src, chunk, 0, 1, prof, ModeRawSpan, nil)
				if resEmit.Reason != StopError || resEmit.ErrorOffset != tc.wantOff || resEmit.Code != tc.wantErr {
					t.Fatalf("Emit chunk=%d cap=%d on %q: got %+v, want off=%d code=%v",
						chunk, capInit, tc.src, resEmit, tc.wantOff, tc.wantErr)
				}
				if resVal.ErrorOffset != tc.wantOff || resVal.Code != tc.wantErr ||
					resRaw.ErrorOffset != tc.wantOff || resRaw.Code != tc.wantErr {
					t.Fatalf("mode error parity mismatch on %q: val=%+v raw=%+v", tc.src, resVal, resRaw)
				}
			}
		}
	}
}

func TestLinearLongTokenResumptionAndSchemaDeepUnknown(t *testing.T) {
	// 1. 150 KiB string and 150 KiB raw number fed in 128-byte chunks: verifies linear O(N) LexState progress.
	longStrJSON := `"` + strings.Repeat("abcdef\\n", 20000) + `"`
	prof := StrictProfile(true, true, false, true, true, true)
	resStr, nodesStr, unescStr, _ := runSplitStream(t, longStrJSON, 128, 4, 0, prof, ModeEmit, nil)
	if resStr.Reason != StopDone || len(nodesStr) != 1 || len(unescStr) != 140000 {
		t.Fatalf("long string linear resume failed: res=%+v nodes=%d unesc=%d", resStr, len(nodesStr), len(unescStr))
	}

	longNumJSON := `[` + "1" + strings.Repeat("234567890", 17000) + `]`
	var m Machine
	m.Reset(prof, ModeEmit)
	m.Nodes = make([]Node, 8)
	prevNumScanOff := 0
	var buf []byte
	for off := 0; off < len(longNumJSON); {
		end := off + 257
		if end > len(longNumJSON) {
			end = len(longNumJSON)
		}
		buf = append(buf, longNumJSON[off:end]...)
		off = end
		final := off >= len(longNumJSON)
		r := m.Step(string(buf), final)
		if !final {
			if r.Reason != StopNeedInput {
				t.Fatalf("expected StopNeedInput at %d, got %+v", off, r)
			}
			if m.Lex.NumScanOff < prevNumScanOff {
				t.Fatalf("NumScanOff regressed: %d < %d", m.Lex.NumScanOff, prevNumScanOff)
			}
			prevNumScanOff = m.Lex.NumScanOff
		} else if r.Reason != StopDone {
			t.Fatalf("final long number step failed: %+v", r)
		}
	}

	// 2. Schema plan with >100-deep unknown subtree AND >100-deep RawMessage (no 64-level bailout!).
	deep120 := strings.Repeat("[", 120) + "1e9999" + strings.Repeat("]", 120)
	plan := &Plan{
		Kind:   PlanStruct,
		Struct: testFieldMatcher{names: []string{"id", "raw"}},
		Fields: []*Plan{
			{Kind: PlanScalar},
			{Kind: PlanRaw},
		},
	}
	schemaDoc := `{"unknown_deep":` + deep120 + `,"id":99,"raw":` + deep120 + `}`
	sRes, sNodes, _, _ := runSplitStream(t, schemaDoc, 31, 1, 1, StrictProfile(true, true, false, false, true, true), ModeEmit, plan)
	if sRes.Reason != StopDone || len(sNodes) != 5 {
		t.Fatalf("schema deep120 expected 5 nodes (obj + 2*(key+val)), got reason=%v len=%d", sRes.Reason, len(sNodes))
	}
	if (sNodes[4].Typ & TypeMask) != KRaw {
		t.Fatalf("expected KRaw for raw field, got typ=%#x", sNodes[4].Typ)
	}
}

func TestEscapedSpanBoundsChecking(t *testing.T) {
	if _, ok := PackEscapedSpan(-1, 5); ok {
		t.Fatal("expected negative uoff to fail")
	}
	if _, ok := PackEscapedSpan(MaxUnescOffset+1, 5); ok {
		t.Fatal("expected uoff > MaxUnescOffset to fail")
	}
	if _, ok := PackEscapedSpan(10, -1); ok {
		t.Fatal("expected negative ulen to fail")
	}
	packed, ok := PackEscapedSpan(MaxUnescOffset, math.MaxInt32)
	if !ok {
		t.Fatal("expected max valid uoff/ulen to succeed")
	}
	uoff, ulen, inUnesc := UnpackEscapedSpan(packed)
	if !inUnesc || uoff != MaxUnescOffset || ulen != math.MaxInt32 {
		t.Fatalf("unpack mismatch: inUnesc=%v uoff=%d ulen=%d", inUnesc, uoff, ulen)
	}
}
