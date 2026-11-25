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
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type listFieldMatcher struct {
	names []string
}

func (m listFieldMatcher) FieldCount() int        { return len(m.names) }
func (m listFieldMatcher) FieldName(i int) string { return m.names[i] }
func (m listFieldMatcher) MatchField(key string, caseSensitive, _ bool) int {
	for i, n := range m.names {
		if n == key || (!caseSensitive && strings.EqualFold(n, key)) {
			return i
		}
	}
	return -1
}

// TestSchemaFastPathResumeParity verifies that resuming stepSchemaFullEmit after
// stepGeneral detours (unknown fields, PlanRaw, escaped keys, cross-64B/1024B keys,
// depth>=32, StopNeedOutput, and malformed unknown values) produces bit-for-bit
// identical ScanResult, Nodes, Unesc, and Stat as stepUnbatched (SetBatchCapForTest(0)).
func TestSchemaFastPathResumeParity(t *testing.T) {
	childMatcher := listFieldMatcher{names: []string{"k", "v", "raw"}}
	childPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     childMatcher,
		FieldNames: childMatcher.names,
		Fields:     []*Plan{nil, nil, {Kind: PlanRaw}},
	}
	rootMatcher := listFieldMatcher{names: []string{"a", "b", "raw", "child", "arr", "deep", "tail"}}
	rootPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     rootMatcher,
		FieldNames: rootMatcher.names,
		Fields: []*Plan{
			nil,
			nil,
			{Kind: PlanRaw},
			childPlan,
			{Kind: PlanSlice, Elem: childPlan},
			nil,
			nil,
		},
	}

	pad55 := strings.Repeat("x", 55)
	pad1015 := strings.Repeat("y", 1015)
	deep34 := strings.Repeat(`{"n":[`, 34) + `1` + strings.Repeat(`]}`, 34)

	cases := []struct {
		name            string
		src             string
		disallowUnknown bool
		relaxedUnknown  bool
	}{
		{
			name: "early_and_scattered_unknowns",
			src:  `{"a":1,"_u1":{"x":[1,2,3],"y":"skip"},"b":"` + pad55 + `","_u2":[true,false,null],"child":{"_cu":99,"k":7,"raw":{"r":[1,2]},"v":"ok"},"arr":[{"_au":{"z":1},"k":1,"v":"a"},{"k":2,"_au2":"s","v":"b"}],"tail":42}`,
		},
		{
			name: "cross_64B_and_1024B_keys",
			src:  `{"a":"` + pad55 + `","b":"` + pad1015 + `","child":{"k":1,"v":"two"},"_unk_1024":{"p":"` + pad55 + `"},"tail":99}`,
		},
		{
			name: "escaped_matched_and_unknown_keys",
			src:  `{"\u0061":1,"\u005fu":{"x":1},"b":"` + pad55 + `","ta\u0069l":77}`,
		},
		{
			name: "plan_raw_then_more_fields",
			src:  `{"a":1,"raw":{"big":"` + pad55 + `","arr":[1,2,3]},"b":"after_raw","_u":{"s":"skip"},"tail":5}`,
		},
		{
			name: "deep_gt32_handoff_and_resume",
			src:  `{"a":1,"deep":` + deep34 + `,"b":"` + pad55 + `","_u":123,"tail":9}`,
		},
		{
			name: "many_unknowns_non_recursive",
			src:  `{"a":1,` + strings.Repeat(`"_u":{"x":1},`, 300) + `"tail":7}`,
		},
		{
			name:            "disallow_unknown_resume",
			src:             `{"a":1,"_dis":{"k":"v"},"b":"` + pad55 + `","tail":2}`,
			disallowUnknown: true,
		},
		{
			name:           "relaxed_unknown_with_cross_block_key",
			src:            `{"a":"` + pad55 + `","_unk_cross":{"k":[1,2,3]},"b":"ok","tail":3}`,
			relaxedUnknown: true,
		},
		{
			name: "syntax_error_inside_unknown",
			src:  `{"a":1,"_bad":{"x":[1,bad]},"b":"` + pad55 + `","tail":2}`,
		},
		{
			name: "eof_inside_unknown",
			src:  `{"a":1,"_eof":{"x":[1,2`,
		},
		{
			name: "syntax_error_after_resume",
			src:  `{"a":1,"_u":{"x":1},"b":"` + pad55 + `","tail":bad}`,
		},
	}

	run := func(src string, batchCap int, initCap int, disUnk, relaxed bool) (ScanResult, []Node, []byte, Stat) {
		var m Machine
		prof := StrictProfile(true, true, false, false, true, false)
		m.Reset(prof, ModeEmit)
		m.ConfigureSchema(rootPlan, false, disUnk, relaxed)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		buf := make([]Node, initCap)
		for {
			m.Nodes = buf
			res := m.Step(src, true)
			if res.Reason == StopNeedOutput {
				grown := make([]Node, len(buf)*2)
				copy(grown[:res.Produced], buf[:res.Produced])
				buf = grown
				continue
			}
			var outNodes []Node
			if res.Reason == StopDone {
				outNodes = append([]Node(nil), buf[:res.Produced]...)
			}
			return res, outNodes, append([]byte(nil), m.Unesc...), m.Stat
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refRes, refNodes, refUnesc, refStat := run(tc.src, 0, 64, tc.disallowUnknown, tc.relaxedUnknown)
			for _, initCap := range []int{8, 12, 64, 512} {
				gotRes, gotNodes, gotUnesc, gotStat := run(tc.src, -1, initCap, tc.disallowUnknown, tc.relaxedUnknown)
				if gotRes != refRes {
					t.Fatalf("initCap=%d res mismatch:\n  got  = %+v\n  want = %+v", initCap, gotRes, refRes)
				}
				if refRes.Reason == StopDone {
					if len(gotNodes) != len(refNodes) {
						t.Fatalf("initCap=%d nodes len=%d want %d", initCap, len(gotNodes), len(refNodes))
					}
					for i := range refNodes {
						if gotNodes[i] != refNodes[i] {
							t.Fatalf("initCap=%d node[%d]=%+v want %+v", initCap, i, gotNodes[i], refNodes[i])
						}
					}
					if !bytes.Equal(gotUnesc, refUnesc) {
						t.Fatalf("initCap=%d unesc mismatch", initCap)
					}
					if gotStat != refStat {
						t.Fatalf("initCap=%d stat=%+v want %+v", initCap, gotStat, refStat)
					}
				}
			}
		})
	}
}

// TestEmitBlockTailDelimiterBoundaries shifts rich object/array payloads across
// all 64 byte alignments so every structural delimiter (':', ',', '{', '}',
// '[', ']', '"') lands on byte 63 of a 64-byte block, verifying bit-for-bit
// parity between stepPlainFullEmit / stepSchemaFullEmit and stepUnbatched.
func TestEmitBlockTailDelimiterBoundaries(t *testing.T) {
	matcher := listFieldMatcher{names: []string{"pad", "a", "b", "c", "arr", "obj", "tail"}}
	plan := &Plan{
		Kind:       PlanStruct,
		Struct:     matcher,
		FieldNames: matcher.names,
		Fields: []*Plan{
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			nil,
			{Kind: PlanSlice, Elem: &Plan{Kind: PlanScalar, TypedNumber: true}},
			nil,
			{Kind: PlanScalar, TypedNumber: true},
		},
	}

	runMode := func(src string, withSchema, useNum bool, batchCap int) (ScanResult, []Node, Stat) {
		var m Machine
		prof := StrictProfile(false, false, false, useNum, !useNum, true)
		m.Reset(prof, ModeEmit)
		if withSchema {
			m.ConfigureSchema(plan, false, false, false)
		}
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		buf := make([]Node, 512)
		m.Nodes = buf
		res := m.Step(src, true)
		var out []Node
		if res.Reason == StopDone {
			out = append([]Node(nil), buf[:res.Produced]...)
		}
		return res, out, m.Stat
	}

	for padLen := 0; padLen < 64; padLen++ {
		pad := strings.Repeat("p", padLen)
		valid := `{"pad":"` + pad + `","a":12345,"b":0.95,"c":"str","arr":[1,2,3,true,false,null],"obj":{"x":7,"y":[8,9]},"tail":-42}`
		invalidColon := `{"pad":"` + pad + `","a" 1}`
		invalidComma := `{"pad":"` + pad + `","a":1 "b":2}`
		invalidArr := `{"pad":"` + pad + `","arr":[1 2]}`

		for _, src := range []string{valid, invalidColon, invalidComma, invalidArr} {
			for _, withSchema := range []bool{false, true} {
				for _, useNum := range []bool{false, true} {
					if withSchema && useNum {
						// Under UseNumber+TypedNumber, schema fast path intentionally
						// emits typed KUint/KSint/KReal instead of KRawNumber; verify
						// syntax/error parity and node counts separately.
						gotRes, gotNodes, gotStat := runMode(src, true, true, -1)
						refRes, refNodes, refStat := runMode(src, true, true, 0)
						if gotRes != refRes {
							t.Fatalf("pad=%d schema+useNum res mismatch src=%q:\n  got  =%+v\n  want =%+v", padLen, src, gotRes, refRes)
						}
						if refRes.Reason == StopDone && (len(gotNodes) != len(refNodes) || gotStat != refStat) {
							t.Fatalf("pad=%d schema+useNum len/stat mismatch: got=(%d,%+v) want=(%d,%+v)",
								padLen, len(gotNodes), gotStat, len(refNodes), refStat)
						}
						continue
					}
					gotRes, gotNodes, gotStat := runMode(src, withSchema, useNum, -1)
					refRes, refNodes, refStat := runMode(src, withSchema, useNum, 0)
					if gotRes != refRes {
						t.Fatalf("pad=%d schema=%v useNum=%v res mismatch src=%q:\n  got  =%+v\n  want =%+v",
							padLen, withSchema, useNum, src, gotRes, refRes)
					}
					if refRes.Reason == StopDone {
						if len(gotNodes) != len(refNodes) || gotStat != refStat {
							t.Fatalf("pad=%d schema=%v useNum=%v len/stat mismatch", padLen, withSchema, useNum)
						}
						for i := range refNodes {
							if gotNodes[i] != refNodes[i] {
								t.Fatalf("pad=%d schema=%v useNum=%v node[%d]=%+v want %+v",
									padLen, withSchema, useNum, i, gotNodes[i], refNodes[i])
							}
						}
					}
				}
			}
		}
	}
}

// TestSchemaTypedNumberErrorCodeAndCursorParity verifies that Schema TypedNumber
// projection only selects node storage representation and never alters JSON
// number boundaries, ScanError codes, ErrorOffset, or Consumed cursors relative
// to stepPlainFullEmit and stepUnbatched (especially under UseNumber).
func TestSchemaTypedNumberErrorCodeAndCursorParity(t *testing.T) {
	matcher := listFieldMatcher{names: []string{"pad", "n"}}
	plan := &Plan{
		Kind:       PlanStruct,
		Struct:     matcher,
		FieldNames: matcher.names,
		Fields: []*Plan{
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
		},
	}

	run := func(src string, withSchema, useNum bool, batchCap int) ScanResult {
		var m Machine
		prof := StrictProfile(false, false, false, useNum, !useNum, true)
		m.Reset(prof, ModeEmit)
		if withSchema {
			m.ConfigureSchema(plan, false, false, false)
		}
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		m.Nodes = make([]Node, 256)
		return m.Step(src, true)
	}

	badNums := []string{
		`123+4`, `123-4`, `1.5+4`, `1.5-4`, `0.5+4`, `-0.5-4`,
		`0+1`, `-0+1`, `01`, `-01`, `123a`, `0a`,
		`1.1234567890123456789+4`, `0.1234567890123456789+4`,
		`1e5+4`, `1e400+1`, `18446744073709551615+4`,
		`1.`, `0.`, `1.e2`, `0.e2`, `1.2.3`, `0.5.1`, `1e`, `1e+`, `1e+2.3`, `-`, `-a`,
	}
	pad := strings.Repeat("p", 80)
	for _, bad := range badNums {
		src := `{"pad":"` + pad + `","n":` + bad + `}`
		for _, useNum := range []bool{false, true} {
			ref := run(src, false, useNum, 0)
			gotPlain := run(src, false, useNum, -1)
			gotSchema := run(src, true, useNum, -1)
			if ref.Reason != StopError {
				t.Fatalf("bad=%q useNum=%v: expected StopError, got %+v", bad, useNum, ref)
			}
			if gotPlain != ref {
				t.Fatalf("bad=%q useNum=%v plain != unbatched:\n  got  =%+v\n  want =%+v", bad, useNum, gotPlain, ref)
			}
			if gotSchema != ref {
				t.Fatalf("bad=%q useNum=%v schema != unbatched:\n  got  =%+v\n  want =%+v", bad, useNum, gotSchema, ref)
			}
		}
	}
	// Explicit witness: under UseNumber=true, 123+4 must report ErrInvalidNumber
	// (not ErrExpectObjCommaOrEnd) on all paths.
	srcPlus := `{"pad":"` + pad + `","n":123+4}`
	if res := run(srcPlus, true, true, -1); res.Code != ErrInvalidNumber {
		t.Fatalf("schema UseNumber 123+4: got Code=%v want ErrInvalidNumber (res=%+v)", res.Code, res)
	}
}

// TestShouldYieldToFastEmitMatchesSchemaEntryThreshold verifies that
// shouldYieldToFastEmit and canEnterSchemaFullEmit enforce the exact same
// plainEmitBlockMaxNodes headroom threshold so stepGeneral never yields back
// when remaining capacity is 8..63 slots.
func TestShouldYieldToFastEmitMatchesSchemaEntryThreshold(t *testing.T) {
	matcher := listFieldMatcher{names: []string{"a"}}
	plan := &Plan{Kind: PlanStruct, Struct: matcher, FieldNames: matcher.names, Fields: []*Plan{nil}}
	src := strings.Repeat(" ", 200)

	for _, rem := range []int{0, 7, 8, 32, plainEmitBlockMaxNodes - 1, plainEmitBlockMaxNodes, plainEmitBlockMaxNodes + 1} {
		var m Machine
		m.Reset(StrictProfile(false, false, false, false, true, true), ModeEmit)
		m.ConfigureSchema(plan, false, false, false)
		m.Nodes = make([]Node, rem)
		m.NodeLen = 0
		want := rem >= plainEmitBlockMaxNodes
		if got := m.shouldYieldToFastEmit(src, true); got != want {
			t.Fatalf("rem=%d: shouldYieldToFastEmit=%v want %v", rem, got, want)
		}
		if got := m.canEnterSchemaFullEmit(src, true); got != want {
			t.Fatalf("rem=%d: canEnterSchemaFullEmit=%v want %v", rem, got, want)
		}
	}
}

// TestSchemaFastPathResumeDiagBounds verifies under -tags=sonic_diag that
// stepSchemaFullEmit incurs zero handoffs on clean payloads and immediately
// resumes after unknown-field detours so slow-path byte consumption stays
// strictly bounded on large structured documents.
func TestSchemaFastPathResumeDiagBounds(t *testing.T) {
	if !DiagEnabled {
		t.Skip("requires -tags=sonic_diag")
	}
	elemMatcher := listFieldMatcher{names: []string{"id", "code", "label", "weight", "rank", "flags"}}
	elemPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     elemMatcher,
		FieldNames: elemMatcher.names,
		Fields: []*Plan{
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
		},
	}
	rootMatcher := listFieldMatcher{names: []string{"id", "region", "lang", "title", "desc", "status", "score", "tags", "meta"}}
	rootPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     rootMatcher,
		FieldNames: rootMatcher.names,
		Fields: []*Plan{
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanSlice, Elem: elemPlan},
			nil,
		},
	}

	buildDoc := func(unknownMode int) string {
		var sb strings.Builder
		sb.Grow(8 << 10)
		sb.WriteString(`{"id":7349812345678901234`)
		if unknownMode >= 1 {
			sb.WriteString(`,"_unk_early":{"trace":"t01","buckets":[1,2,3,4,5],"nested":{"x":true,"y":"skip_me"}}`)
		}
		sb.WriteString(`,"region":"us-west","lang":"en-US","title":"structured payload for schema resume diagnostics","desc":"`)
		sb.WriteString(strings.Repeat("description_segment_0123456789 ", 12))
		sb.WriteString(`"`)
		if unknownMode >= 8 {
			sb.WriteString(`,"_unk_2":[10,20,30,"ignored",{"a":1,"b":2}]`)
		}
		sb.WriteString(`,"status":1,"score":97.625`)
		if unknownMode >= 8 {
			sb.WriteString(`,"_unk_3":{"k1":123,"k2":456,"k3":789},"_unk_4":true`)
		}
		sb.WriteString(`,"tags":[`)
		for i := 0; i < 64; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			if unknownMode >= 8 && i%16 == 4 {
				fmt.Fprintf(&sb, `{"id":%d,"_unk_elem":{"m":%d,"n":"skip"},"code":"tag_%02d","label":"label_%02d","weight":%.3f,"rank":%d,"flags":%d}`,
					1000+i, i, i%16, i, float64(i)*0.125, i, uint64(i)*31+7)
				continue
			}
			fmt.Fprintf(&sb, `{"id":%d,"code":"tag_%02d","label":"label_%02d","weight":%.3f,"rank":%d,"flags":%d}`,
				1000+i, i%16, i, float64(i)*0.125, i, uint64(i)*31+7)
		}
		sb.WriteString(`],"meta":{"src":"test","ver":12,"flags":[0,1,2,3],"enabled":true}`)
		if unknownMode >= 8 {
			sb.WriteString(`,"_unk_tail":{"z":[1,2,3],"s":"tail_skip"}`)
		}
		sb.WriteByte('}')
		return sb.String()
	}

	run := func(src string) DiagSnapshot {
		ResetDiag()
		var m Machine
		m.Reset(StrictProfile(false, false, false, true, false, true), ModeEmit)
		m.ConfigureSchema(rootPlan, false, false, false)
		buf := make([]Node, 1024)
		for {
			m.Nodes = buf
			res := m.Step(src, true)
			if res.Reason == StopNeedOutput {
				grown := make([]Node, len(buf)*2)
				copy(grown[:res.Produced], buf[:res.Produced])
				buf = grown
				continue
			}
			if res.Reason != StopDone {
				t.Fatalf("unexpected scan result: %+v", res)
			}
			return ReadDiag()
		}
	}

	s0 := run(buildDoc(0))
	if s0.FastToGeneralHandoffs != 0 || s0.GeneralPathBytes != 0 {
		t.Fatalf("0 unknowns: expected zero handoffs/general bytes, got handoffs=%d genBytes=%d",
			s0.FastToGeneralHandoffs, s0.GeneralPathBytes)
	}

	s1 := run(buildDoc(1))
	if s1.FastPathResumes == 0 || s1.GeneralPathBytes > 256 {
		t.Fatalf("1 early unknown: expected fast-path resume with bounded general bytes, got resumes=%d genBytes=%d",
			s1.FastPathResumes, s1.GeneralPathBytes)
	}

	docMany := buildDoc(8)
	sMany := run(docMany)
	if sMany.FastPathResumes < 6 || sMany.GeneralPathBytes > uint64(len(docMany)/5) {
		t.Fatalf("many unknowns: expected repeated resumes with bounded general bytes, got resumes=%d genBytes=%d (docLen=%d)",
			sMany.FastPathResumes, sMany.GeneralPathBytes, len(docMany))
	}
}

// TestSchemaWindowHandoffParity verifies bit-for-bit parity (ScanResult
// including Reason/Code/ErrorOffset/Consumed, Nodes, Unesc, Stat) between
// bidirectional verified window handoff + post-unknown value cooldown and
// stepUnbatched (SetBatchCapForTest(0)) across 63/64 and 1023/1024 byte
// boundaries, cross-batch keys, carry-carrying strings, unknown-followed-by-
// nested containers, PlanRaw, DisallowUnknown, escaped keys, malformed unknown
// values, and StopNeedOutput capacity growth.
func TestSchemaWindowHandoffParity(t *testing.T) {
	childMatcher := listFieldMatcher{names: []string{"k", "v", "raw"}}
	childPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     childMatcher,
		FieldNames: childMatcher.names,
		Fields:     []*Plan{nil, nil, {Kind: PlanRaw}},
	}
	rootNames := []string{
		"id", "uid", "status", "score", "enabled",
		"region", "lang", "title", "desc", "tag",
		"child", "arr", "raw", "extra", "tail",
	}
	rootMatcher := listFieldMatcher{names: rootNames}
	rootPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     rootMatcher,
		FieldNames: rootNames,
		Fields: []*Plan{
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar, TypedNumber: true},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			childPlan,
			{Kind: PlanSlice, Elem: childPlan},
			{Kind: PlanRaw},
			{Kind: PlanScalar},
			{Kind: PlanScalar, TypedNumber: true},
		},
	}

	run := func(src string, batchCap, initCap int, useNum, disUnk bool) (ScanResult, []Node, []byte, Stat) {
		var m Machine
		prof := StrictProfile(true, true, false, useNum, !useNum, true)
		m.Reset(prof, ModeEmit)
		m.ConfigureSchema(rootPlan, false, disUnk, false)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		buf := make([]Node, initCap)
		for {
			m.Nodes = buf
			res := m.Step(src, true)
			if res.Reason == StopNeedOutput {
				grown := make([]Node, len(buf)*2)
				copy(grown[:res.Produced], buf[:res.Produced])
				buf = grown
				continue
			}
			var outNodes []Node
			if res.Reason == StopDone {
				outNodes = append([]Node(nil), buf[:res.Produced]...)
			}
			return res, outNodes, append([]byte(nil), m.Unesc...), m.Stat
		}
	}

	// Build interleaved and clustered 15 known + 80 unknown payloads.
	var interParts, clustParts []string
	knownPairs := []string{
		`"id":101`, `"uid":202`, `"status":1`, `"score":98.5`, `"enabled":true`,
		`"region":"cn"`, `"lang":"zh"`, `"title":"t"`, `"desc":"d"`, `"tag":"g"`,
		`"child":{"k":1,"v":"cv"}`, `"arr":[{"k":2,"v":"a1"},{"k":3,"v":"a2"}]`,
		`"raw":{"r":[1,2]}`, `"extra":"e"`, `"tail":99`,
	}
	uIdx := 0
	for k := 0; k < len(knownPairs); k++ {
		interParts = append(interParts, knownPairs[k])
		n := 5
		if k < 5 {
			n = 6
		}
		for j := 0; j < n; j++ {
			interParts = append(interParts, fmt.Sprintf(`"_u%02d":{"x":%d,"s":"v%d"}`, uIdx, uIdx, uIdx%7))
			uIdx++
		}
	}
	clustParts = append(clustParts, knownPairs[:5]...)
	for i := 0; i < 80; i++ {
		clustParts = append(clustParts, fmt.Sprintf(`"_u%02d":%d`, i, i*3+1))
	}
	clustParts = append(clustParts, knownPairs[5:]...)

	cases := []struct {
		name   string
		src    string
		disUnk bool
	}{
		{
			name: "interleaved_15k_80u",
			src:  "{" + strings.Join(interParts, ",") + "}",
		},
		{
			name: "clustered_15k_80u",
			src:  "{" + strings.Join(clustParts, ",") + "}",
		},
		{
			name: "unknown_immediately_followed_by_known_nested_containers",
			src:  `{"id":1,"_u_pre":{"skip":[1,2,3]},"child":{"k":10,"v":"nested_ok","raw":{"z":1}},"_u_mid":42,"arr":[{"k":1,"v":"a"},{"k":2,"v":"b"},{"k":3,"v":"c"}],"region":"us","lang":"en","title":"after_nested","tail":7}`,
		},
		{
			name: "escaped_keys_and_raw_mixed_with_unknowns",
			src:  `{"\u0069d":1,"_u1":10,"raw":{"x":[1,2,3]},"_u\u0032":{"y":"esc"},"\u0074itle":"ok","desc":"` + strings.Repeat("d", 120) + `","_u3":false,"tail":8}`,
		},
		{
			name:   "disallow_unknown_interleaved",
			src:    `{"id":1,"_u_dis":{"a":1,"b":[2,3]},"region":"us","_u_dis2":99,"tail":5}`,
			disUnk: true,
		},
	}

	// Boundary sweep across 63/64 and 1023/1024 byte alignments:
	//  - unknown key spanning byte 64 and byte 1024 (cur < batchStart fallback)
	//  - long string crossing byte 1024 immediately after unknown field (carry preservation)
	//  - malformed unknown values at exact 63/64 and 1023/1024 boundaries
	for _, padLen := range []int{40, 46, 47, 48, 49, 995, 1004, 1005, 1006, 1007, 1008, 1009} {
		pad := strings.Repeat("p", padLen)
		cases = append(cases,
			struct {
				name   string
				src    string
				disUnk bool
			}{
				name: fmt.Sprintf("boundary_valid_pad%d", padLen),
				src:  `{"desc":"` + pad + `","_u_cross_key_boundary":{"a":1,"b":"` + strings.Repeat("s", 70) + `"},"title":"` + strings.Repeat("t", 120) + `","_u2":123,"tail":9}`,
			},
			struct {
				name   string
				src    string
				disUnk bool
			}{
				name: fmt.Sprintf("boundary_carry_string_pad%d", padLen),
				src:  `{"id":1,"_u_early":7,"desc":"` + pad + `","title":"` + strings.Repeat("w", 96) + `","_u_after":8,"tail":11}`,
			},
		)
	}

	badUnknowns := []struct {
		name string
		frag string
	}{
		{"num_suffix", `"_bad":123+4`},
		{"missing_comma", `"_bad":{"a":1 "b":2}`},
		{"trailing_comma", `"_bad":[1,2,]`},
		{"bad_literal", `"_bad":tru`},
		{"bad_escape", `"_bad":"abc\qdef"`},
		{"raw_ctrl", "\"_bad\":\"abc\x01def\""},
		{"truncated_eof", `"_bad":{"nested":[1,2`},
	}
	for _, padLen := range []int{45, 48, 1005, 1008} {
		pad := strings.Repeat("x", padLen)
		for _, bu := range badUnknowns {
			cases = append(cases, struct {
				name   string
				src    string
				disUnk bool
			}{
				name: fmt.Sprintf("bad_%s_pad%d", bu.name, padLen),
				src:  `{"desc":"` + pad + `",` + bu.frag + `,"tail":1}`,
			})
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, useNum := range []bool{false, true} {
				refRes, refNodes, refUnesc, refStat := run(tc.src, 0, 64, useNum, tc.disUnk)
				for _, initCap := range []int{8, 12, 64, 512} {
					gotRes, gotNodes, gotUnesc, gotStat := run(tc.src, -1, initCap, useNum, tc.disUnk)
					if gotRes != refRes {
						t.Fatalf("useNum=%v initCap=%d ScanResult mismatch:\n  got  =%+v\n  want =%+v", useNum, initCap, gotRes, refRes)
					}
					if refRes.Reason == StopDone {
						if len(gotNodes) != len(refNodes) || gotStat != refStat {
							t.Fatalf("useNum=%v initCap=%d len/stat mismatch: got=(%d,%+v) want=(%d,%+v)",
								useNum, initCap, len(gotNodes), gotStat, len(refNodes), refStat)
						}
						if !useNum {
							for i := range refNodes {
								if gotNodes[i] != refNodes[i] {
									t.Fatalf("initCap=%d node[%d]=%+v want %+v", initCap, i, gotNodes[i], refNodes[i])
								}
							}
						}
						if !bytes.Equal(gotUnesc, refUnesc) {
							t.Fatalf("useNum=%v initCap=%d unesc mismatch", useNum, initCap)
						}
					}
				}
			}
		})
	}
}

// TestSchemaWindowHandoffDiagBounds guards against regression to repeated
// LexBatch re-classification on dense strict-unknown schema payloads and
// verifies that non-window cold handoffs (PlanRaw) retain baseline ramping.
// Note: BytesLexed / BatchCalls measure bytes classified by observed LexBatch
// windows, not total bytes read by scalar/string scanners.
func TestSchemaWindowHandoffDiagBounds(t *testing.T) {
	if !DiagEnabled {
		t.Skip("requires -tags=sonic_diag")
	}
	names := []string{
		"id", "uid", "author_id", "status", "score", "rate", "enabled", "verified",
		"region", "lang", "title", "desc", "tag", "trace_id", "category", "raw",
	}
	matcher := listFieldMatcher{names: names}
	fields := make([]*Plan, len(names))
	for i := range names {
		if names[i] == "raw" {
			fields[i] = &Plan{Kind: PlanRaw}
		} else {
			fields[i] = &Plan{Kind: PlanScalar, TypedNumber: i < 6}
		}
	}
	plan := &Plan{Kind: PlanStruct, Struct: matcher, FieldNames: names, Fields: fields}

	runDiag := func(src string) DiagSnapshot {
		ResetDiag()
		var m Machine
		m.Reset(StrictProfile(false, false, false, true, false, true), ModeEmit)
		m.ConfigureSchema(plan, false, false, false)
		buf := make([]Node, 1024)
		for {
			m.Nodes = buf
			res := m.Step(src, true)
			if res.Reason == StopNeedOutput {
				grown := make([]Node, len(buf)*2)
				copy(grown[:res.Produced], buf[:res.Produced])
				buf = grown
				continue
			}
			if res.Reason != StopDone {
				t.Fatalf("unexpected scan result: %+v", res)
			}
			return ReadDiag()
		}
	}

	known := []string{
		`"id":1001`, `"uid":2002`, `"author_id":3003`, `"status":1`, `"score":98.5`,
		`"rate":0.75`, `"enabled":true`, `"verified":false`, `"region":"cn"`, `"lang":"zh"`,
		`"title":"title_v1"`, `"desc":"desc_v1"`, `"tag":"tag_v1"`, `"trace_id":"tr_1"`, `"category":"cat_1"`,
	}
	var inter, clust []string
	u := 0
	for k := 0; k < len(known); k++ {
		inter = append(inter, known[k])
		n := 5
		if k < 5 {
			n = 6
		}
		for j := 0; j < n; j++ {
			inter = append(inter, fmt.Sprintf(`"_u%02d":{"a":%d,"b":"v%d"}`, u, u, u%10))
			u++
		}
	}
	clust = append(clust, known[:7]...)
	for i := 0; i < 80; i++ {
		clust = append(clust, fmt.Sprintf(`"_u%02d":%d`, i, 1000+i))
	}
	clust = append(clust, known[7:]...)

	interSrc := "{" + strings.Join(inter, ",") + "}"
	sInter := runDiag(interSrc)
	if sInter.BytesLexed > uint64(len(interSrc)+128) || sInter.BatchCalls > 6 {
		t.Fatalf("interleaved 15K+80U re-lex regression: len=%d BytesLexed=%d BatchCalls=%d Handoffs=%d",
			len(interSrc), sInter.BytesLexed, sInter.BatchCalls, sInter.FastToGeneralHandoffs)
	}

	clustSrc := "{" + strings.Join(clust, ",") + "}"
	sClust := runDiag(clustSrc)
	if sClust.BytesLexed > uint64(len(clustSrc)+128) || sClust.BatchCalls > 6 {
		t.Fatalf("clustered 15K+80U re-lex regression: len=%d BytesLexed=%d BatchCalls=%d Handoffs=%d",
			len(clustSrc), sClust.BytesLexed, sClust.BatchCalls, sClust.FastToGeneralHandoffs)
	}

	// Verify narrowed full-batch guard: a PlanRaw handoff does NOT preserve a
	// reusable window (b.active==false on handoff), so stepGeneralMode keeps
	// baseline 1-block initial ramp instead of lexing a full 1 KiB block for a
	// short Raw value followed by a long tail.
	rawDoc := `{"id":1,"raw":{"a":1},"desc":"` + strings.Repeat("z", 1500) + `","status":2}`
	sRaw := runDiag(rawDoc)
	// Initial Schema batch classifies [0, 1024), PlanRaw clears window and
	// general classifies only 1 block (64 B) for {"a":1}, then resumes Schema
	// passing its tiny tail; total BytesLexed must stay well below 2*1024 B.
	if sRaw.FastToGeneralHandoffs != 1 || sRaw.BytesLexed > 1200 {
		t.Fatalf("PlanRaw handoff should preserve baseline small-batch ramp: handoffs=%d BytesLexed=%d BatchCalls=%d",
			sRaw.FastToGeneralHandoffs, sRaw.BytesLexed, sRaw.BatchCalls)
	}
}
