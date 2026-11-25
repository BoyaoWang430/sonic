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
	"strings"
	"testing"
)

// TestSingleStructuralGrammarAuthority verifies the canonical (GrammarPhase, TokenKind)
// transition and error tables and proves bit-for-bit equivalence between
// stepPlainFullEmit (default cap) and stepGeneral (SetBatchCapForTest(1) / small Node caps)
// across valid, malformed, deep-nesting, escaped, numeric, and prefix-trailing inputs.
func TestSingleStructuralGrammarAuthority(t *testing.T) {
	phases := []GrammarPhase{
		PhaseValue,
		PhaseObjectOpen,
		PhaseObjectKey,
		PhaseObjectColon,
		PhaseObjectAfterVal,
		PhaseArrayOpen,
		PhaseArrayAfterVal,
		PhaseTrailingSpace,
	}
	expectedErr := map[GrammarPhase]ScanError{
		PhaseValue:          ErrInvalidChar,
		PhaseObjectOpen:     ErrExpectKey,
		PhaseObjectKey:      ErrExpectKey,
		PhaseObjectColon:    ErrExpectColon,
		PhaseObjectAfterVal: ErrExpectObjCommaOrEnd,
		PhaseArrayOpen:      ErrInvalidChar,
		PhaseArrayAfterVal:  ErrExpectArrCommaOrEnd,
		PhaseTrailingSpace:  ErrInvalidChar,
	}
	for _, ph := range phases {
		if got := structPhaseError(ph); got != expectedErr[ph] {
			t.Fatalf("phase %d error = %v, want %v", ph, got, expectedErr[ph])
		}
		if got := lookupStructTransition(ph, TokInvalid); got != actError {
			t.Fatalf("phase %d TokInvalid action = %v, want actError", ph, got)
		}
	}
	if got := uniqueTransitionByte(PhaseValue, actValNumber); got != -1 {
		t.Fatalf("uniqueTransitionByte(PhaseValue, actValNumber) = %d, want -1 for multi-byte action", got)
	}
	if got := uniqueTransitionByte(PhaseValue, actObjColon); got != -1 {
		t.Fatalf("uniqueTransitionByte(PhaseValue, actObjColon) = %d, want -1 for absent action", got)
	}
	for _, w := range []struct {
		phase GrammarPhase
		act   structAction
		got   int
	}{
		{PhaseObjectOpen, actObjKey, uniqueObjKeyOpenByte},
		{PhaseObjectKey, actObjKey, uniqueObjKeyNextByte},
		{PhaseObjectColon, actObjColon, uniqueObjColonByte},
		{PhaseObjectAfterVal, actObjComma, uniqueObjCommaByte},
		{PhaseObjectAfterVal, actCloseObj, uniqueObjCloseByte},
		{PhaseArrayAfterVal, actArrComma, uniqueArrCommaByte},
		{PhaseArrayAfterVal, actCloseArr, uniqueArrCloseByte},
	} {
		if w.got < 0 || lookupStructByteTransition(w.phase, byte(w.got)) != w.act {
			t.Fatalf("derived witness phase=%d act=%v got=%d inconsistent with structByteTransitionTable", w.phase, w.act, w.got)
		}
	}

	profiles := []Profile{
		StrictProfile(true, true, true, false, true, false),
		StrictProfile(true, false, false, false, true, true),
		StrictProfile(false, false, false, true, false, false),
		StrictProfile(true, false, true, true, false, true),
	}

	deep66 := strings.Repeat("[", 66) + `{"k":-0.25,"s":"a\nb"}` + strings.Repeat("]", 66)
	deepOverflow := strings.Repeat("[", StrictMaxContainerDepth+1) + strings.Repeat("]", StrictMaxContainerDepth+1)

	inputs := []string{
		`{}`,
		`[]`,
		`{"id":12345,"name":"alice","active":true,"score":98,"tags":["go","json","simd"],"z":-0,"f":0.125,"nf":-0.0,"big":-9999999999999999999}`,
		`{"esc":"line1\nline2\t\"quoted\"\u4e2d\u6587\uD83D\uDE00","e\nkey":[0,-0,1.0000000596046448,1e20,1e400]}`,
		`[1, 2, 3] trailing_garbage`,
		`123+456`,
		`0+1`,
		`0.5+1`,
		``,
		`   `,
		`}`,
		`]`,
		`{]`,
		`[}`,
		`{"a" 1}`,
		`{"a":1 "b":2}`,
		`[1 "bad\q"]`,
		`{1:2}`,
		`{"a":"unterminated`,
		"{\"a\":\"raw\x00ctrl\"}",
		"\"\\u\\x80\\x11",
		`{"a":1e+}`,
		`{"a":01}`,
		deep66,
		deepOverflow,
	}

	runScan := func(src string, prof Profile, batchCap int, initNodeCap int, forceScalar bool) (ScanResult, []Node, []byte, Stat) {
		var m Machine
		m.Reset(prof, ModeEmit)
		m.SetForceScalar(forceScalar)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		buf := make([]Node, initNodeCap)
		for {
			m.Nodes = buf
			res := m.Step(src, true)
			if res.Reason == StopNeedOutput {
				next := len(buf) * 2
				if next < 2 {
					next = 2
				}
				grown := make([]Node, next)
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

	for pIdx, prof := range profiles {
		for _, src := range inputs {
			refRes, refNodes, refUnesc, refStat := runScan(src, prof, 1, 64, true)

			for _, tc := range []struct {
				name        string
				batchCap    int
				initNodeCap int
				scalar      bool
			}{
				{"plain_native_64", -1, 64, false},
				{"plain_scalar_64", -1, 64, true},
				{"plain_native_cap0", -1, 0, false},
				{"plain_native_cap1", -1, 1, false},
				{"plain_native_cap2", -1, 2, false},
				{"plain_native_cap5", -1, 5, false},
				{"general_unbatched", 0, 64, false},
			} {
				gotRes, gotNodes, gotUnesc, gotStat := runScan(src, prof, tc.batchCap, tc.initNodeCap, tc.scalar)
				if gotRes != refRes {
					t.Fatalf("prof=%d case=%s input=%q:\n  got  res=%+v\n  want res=%+v", pIdx, tc.name, src, gotRes, refRes)
				}
				if refRes.Reason == StopDone {
					if len(gotNodes) != len(refNodes) {
						t.Fatalf("prof=%d case=%s input=%q node len=%d, want %d", pIdx, tc.name, src, len(gotNodes), len(refNodes))
					}
					for i := range refNodes {
						if gotNodes[i] != refNodes[i] {
							t.Fatalf("prof=%d case=%s input=%q node[%d]=%+v, want %+v", pIdx, tc.name, src, i, gotNodes[i], refNodes[i])
						}
					}
					if !bytes.Equal(gotUnesc, refUnesc) {
						t.Fatalf("prof=%d case=%s input=%q unesc=%q, want %q", pIdx, tc.name, src, gotUnesc, refUnesc)
					}
					if gotStat != refStat {
						t.Fatalf("prof=%d case=%s input=%q stat=%+v, want %+v", pIdx, tc.name, src, gotStat, refStat)
					}
				}
			}
		}
	}
}

// TestDefaultValidateOnlyAuthorityParity verifies that stepDefaultValidateOnly
// is gated exclusively to default non-strict full-input ModeValidateOnly scans
// and matches stepGeneral bit-for-bit on ScanResult, Grammar state (Phase, Depth,
// WasFirstKey, BitStack), Stat.MaxDepth, and zero tape/unesc writes across
// boundary, escaped-string, numeric, short-tail, deep-nesting, and error cases.
func TestDefaultValidateOnlyAuthorityParity(t *testing.T) {
	// 1. Verify gating rejects ConfigStd.Valid, Compact, Schema, AST, strict ValidateOne,
	// baseDepth>0, streaming, and non-default batch caps.
	gateCases := []struct {
		name  string
		setup func(*Machine)
		src   string
		final bool
		want  bool
	}{
		{
			name: "default_validate_ok",
			setup: func(m *Machine) {
				m.Reset(ASTValidateProfile(false, 0), ModeValidateOnly)
			},
			src:   `{"a":1}`,
			final: true,
			want:  true,
		},
		{
			name: "default_validate_streaming_blocked",
			setup: func(m *Machine) {
				m.Reset(ASTValidateProfile(false, 0), ModeValidateOnly)
			},
			src:   `{"a":1}`,
			final: false,
			want:  false,
		},
		{
			name: "config_std_valid_blocked",
			setup: func(m *Machine) {
				m.Reset(StrictProfile(true, true, false, false, false, true), ModeValidateOnly)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
		{
			name: "strict_validate_one_blocked",
			setup: func(m *Machine) {
				m.Reset(ASTValidateProfile(true, 0), ModeValidateOnly)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
		{
			name: "base_depth_nonzero_blocked",
			setup: func(m *Machine) {
				m.Reset(ASTValidateProfile(false, 1), ModeValidateOnly)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
		{
			name: "compact_blocked",
			setup: func(m *Machine) {
				m.Reset(CompactProfile(true, true), ModeCompactObserve)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
		{
			name: "schema_emit_blocked",
			setup: func(m *Machine) {
				m.Reset(StrictProfile(false, false, false, false, true, true), ModeEmit)
				m.ConfigureSchema(&Plan{Kind: PlanStruct, Struct: singleKeepMatcher{name: "a"}}, false, false, false)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
		{
			name: "ast_event_sink_blocked",
			setup: func(m *Machine) {
				m.Reset(ASTEventProfile(false), ModeEventSink)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
		{
			name: "batch_cap_override_blocked",
			setup: func(m *Machine) {
				m.Reset(ASTValidateProfile(false, 0), ModeValidateOnly)
				m.SetBatchCapForTest(1)
			},
			src:   `{"a":1}`,
			final: true,
			want:  false,
		},
	}
	for _, gc := range gateCases {
		var m Machine
		gc.setup(&m)
		if got := m.canEnterDefaultValidateOnly(gc.src, gc.final); got != gc.want {
			t.Fatalf("gate %s: canEnterDefaultValidateOnly=%v, want %v", gc.name, got, gc.want)
		}
	}

	// 2. Build parity corpus covering 64B block boundaries, 1024B batch boundaries,
	// escaped/raw-control strings, numbers, literals, short tails, NUL bytes, and deep nesting.
	pad58 := strings.Repeat("a", 58)
	pad62 := strings.Repeat("a", 62)
	pad1018 := strings.Repeat("a", 1018)
	pad1020 := strings.Repeat("a", 1020)
	longStr2500 := `{"k":"` + strings.Repeat(`x\"y\\z\u003c`, 200) + `","n":12345}`
	cross64EscQuote := `{"` + pad58 + `\"end":"` + pad62 + `\\\"v\\","n":1}`
	cross1024EscQuote := `{"k":"` + pad1018 + `\"still_in_str` + pad58 + `\\","after":true}`
	cross1024EvenSlash := `{"k":"` + pad1020 + `\\","after":[1,2,3]}`
	cross64Num := `{"` + pad58 + `":1234567890.12345e-10,"b":true}`
	cross64BadNum := `{"` + pad58 + `":12345abc,"b":true}`
	cross1024Num := `{"k":"` + pad1018 + `","n":98765432109876543210,"b":false}`
	cross1024BadNum := `{"k":"` + pad1018 + `","n":9876543210abc}`
	cross64Lit := `{"` + pad58 + `":false,"t":true,"n":null}`
	cross1024Lit := `{"k":"` + pad1018 + `","f":false,"t":true}`
	// Block-end colon regressions: ':' lands as the last event of a 64-byte block
	// (both directly at byte 63 and with trailing spaces crossing into block 1),
	// exercising case actObjColon with ev == 0 before the value arrives in the next block.
	blockEndColonVal := `{"` + strings.Repeat("k", 60) + `":` + strings.Repeat(" ", 8) + `0,"tail":"x"}`
	blockEndColonSpace := `{"` + strings.Repeat("k", 55) + `":` + strings.Repeat(" ", 64) + `true,"tail":null}`
	blockEndColonBad := `{"` + strings.Repeat("k", 60) + `":` + strings.Repeat(" ", 8) + `}`
	blockEndColonEOF := `{"` + strings.Repeat("k", 60) + `":` + strings.Repeat(" ", 8)
	deep66 := strings.Repeat("[", 66) + `{"k":"v\"x","n":-0.25}` + strings.Repeat("]", 66)
	deepMax := strings.Repeat("[", StrictMaxContainerDepth) + "0" + strings.Repeat("]", StrictMaxContainerDepth)
	deepOverflow := strings.Repeat("[", StrictMaxContainerDepth+1) + "0" + strings.Repeat("]", StrictMaxContainerDepth+1)

	inputs := []string{
		`{}`,
		`[]`,
		`""`,
		`"a"`,
		`"\""`,
		`"\\"`,
		`"\\\""`,
		`"\\\\"`,
		"\"raw\x00\x01\x1fctrl\\q\\uZZZZ\"",
		`{"id":12345,"name":"alice\"bob\\","active":true,"off":false,"nil":null,"z":0,"nz":-0,"f":0.125,"nf":-0.0,"exp":1e+20,"huge":1e9999,"tags":["go","json"]}`,
		`   {"a": [1, 2, 3]}   `,
		`[1, 2, 3] trailing_garbage`,
		`"prefix_str" trailing`,
		`123 trailing`,
		`true trailing`,
		`false trailing`,
		`null trailing`,
		`0`,
		`-0`,
		`01`,
		`-01`,
		`0+1`,
		`-0+1`,
		`123+456`,
		`0.5+1`,
		`1.2.3`,
		`1e2+3`,
		`1e2e3`,
		`[0+1]`,
		`[01]`,
		`[123+456]`,
		`[123abc]`,
		`[truefalse]`,
		`[1\"x"]`,
		`{"a":\"x"}`,
		``,
		`   `,
		"\x00",
		"  \x00",
		"{\"a\":\x00}",
		`}`,
		`]`,
		`{]`,
		`[}`,
		`{"a" 1}`,
		`{"a":1 "b":2}`,
		`[1,]`,
		`{,}`,
		`{1:2}`,
		`"`,
		`"unterminated`,
		`"unterminated\`,
		`{"unterminated_first_key`,
		`{"a":1,"unterminated_second_key`,
		`{"a":"unterminated_val`,
		`{"a":"` + pad1020 + `unterminated_across_batch`,
		`{"a":1e+}`,
		`{"a":1.}`,
		`{"a":0.}`,
		`{"a":0e}`,
		`{"a":-}`,
		`{"a":-a}`,
		`{"a":tru}`,
		`{"a":fals}`,
		`{"a":nul}`,
		`[1e+]`,
		`[tru]`,
		`["unterminated`,
		longStr2500,
		cross64EscQuote,
		cross1024EscQuote,
		cross1024EvenSlash,
		cross64Num,
		cross64BadNum,
		cross1024Num,
		cross1024BadNum,
		cross64Lit,
		cross1024Lit,
		blockEndColonVal,
		blockEndColonSpace,
		blockEndColonBad,
		blockEndColonEOF,
		deep66,
		deepMax,
		deepOverflow,
	}

	profiles := []Profile{
		ASTValidateProfile(false, 0),
		{MaxContainerDepth: StrictMaxContainerDepth, FullDocument: true, ASTCompatLex: true},
		{MaxContainerDepth: StrictMaxContainerDepth, FullDocument: true, ASTCompatLex: false},
		{MaxContainerDepth: StrictMaxContainerDepth, FullDocument: false, ASTCompatLex: false},
		{MaxContainerDepth: 0, FullDocument: false, ASTCompatLex: true},
	}

	type stateSnap struct {
		res         ScanResult
		phase       GrammarPhase
		depth       int
		wasFirstKey bool
		bit0        uint64
		bit1        uint64
		maxDepth    uint32
		nodeLen     int
		unescCap    int
	}
	runOne := func(src string, startCur int, prof Profile, batchCap int, forceScalar bool) stateSnap {
		var m Machine
		m.Reset(prof, ModeValidateOnly)
		m.Grammar.Cur = startCur
		m.SetForceScalar(forceScalar)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		res := m.Step(src, true)
		wasFirstKey := false
		if res.Reason == StopError && m.Grammar.Phase == PhaseKeyString {
			wasFirstKey = m.Grammar.WasFirstKey
		}
		return stateSnap{
			res:         res,
			phase:       m.Grammar.Phase,
			depth:       m.Grammar.Depth,
			wasFirstKey: wasFirstKey,
			bit0:        m.Grammar.BitStack[0],
			bit1:        m.Grammar.BitStack[1],
			maxDepth:    m.Stat.MaxDepth,
			nodeLen:     m.NodeLen,
			unescCap:    cap(m.Unesc),
		}
	}

	for pIdx, prof := range profiles {
		for _, src := range inputs {
			ref := runOne(src, 0, prof, 1, true)
			for _, tc := range []struct {
				name     string
				batchCap int
				scalar   bool
			}{
				{"fast_native", -1, false},
				{"fast_scalar", -1, true},
				{"general_unbatched", 0, false},
			} {
				got := runOne(src, 0, prof, tc.batchCap, tc.scalar)
				if got != ref {
					t.Fatalf("prof=%d case=%s input=%q:\n  got =%+v\n  want=%+v", pIdx, tc.name, src, got, ref)
				}
				if got.nodeLen != 0 || got.unescCap != 0 {
					t.Fatalf("prof=%d case=%s input=%q wrote tape/unesc: nodeLen=%d unescCap=%d", pIdx, tc.name, src, got.nodeLen, got.unescCap)
				}
			}
		}
	}

	// 3. Non-zero starting cursor g.Cur > 0 parity (e.g. ValidateOne mid-buffer).
	midSrc := `prefix_bytes_12345   {"k":"v\"1","arr":[1,2,3]}   tail`
	for _, startCur := range []int{18, 21, len(midSrc)} {
		for _, prof := range []Profile{ASTValidateProfile(false, 0), {MaxContainerDepth: StrictMaxContainerDepth, FullDocument: true, ASTCompatLex: true}} {
			ref := runOne(midSrc, startCur, prof, 1, true)
			gotNat := runOne(midSrc, startCur, prof, -1, false)
			gotSca := runOne(midSrc, startCur, prof, -1, true)
			if gotNat != ref || gotSca != ref {
				t.Fatalf("startCur=%d prof=%+v mismatch:\n  nat=%+v\n  sca=%+v\n  ref=%+v", startCur, prof, gotNat, gotSca, ref)
			}
		}
	}
}

func TestCompactFirstDirtyProfileSemantics(t *testing.T) {
	// 1. Full CompactProfile preserves independent HasSpace and NeedsRewrite even when whitespace appears first.
	srcLeadAndHTML := " {\"a\":\"<b>\",\"c\":\"\xff\"}"
	var m Machine
	m.Reset(CompactProfile(true, true), ModeCompactObserve)
	if res := m.Step(srcLeadAndHTML, true); res.Reason != StopDone {
		t.Fatalf("CompactProfile failed: %+v", res)
	}
	if !m.CompactObs.HasSpace || !m.CompactObs.NeedsRewrite || m.CompactObs.FirstDirtyPos != 0 {
		t.Fatalf("CompactProfile obs=%+v, want HasSpace=true NeedsRewrite=true FirstDirtyPos=0", m.CompactObs)
	}

	// 2. CompactFirstDirtyProfile records FirstDirtyPos=0 on leading space and skips later rewrite-need scans
	// while still validating full grammar, escapes, and control bytes to EOF.
	m.Reset(CompactFirstDirtyProfile(true, true), ModeCompactObserve)
	if res := m.Step(srcLeadAndHTML, true); res.Reason != StopDone {
		t.Fatalf("CompactFirstDirtyProfile failed: %+v", res)
	}
	if !m.CompactObs.HasSpace || m.CompactObs.NeedsRewrite || m.CompactObs.FirstDirtyPos != 0 {
		t.Fatalf("CompactFirstDirtyProfile obs=%+v, want HasSpace=true NeedsRewrite=false FirstDirtyPos=0", m.CompactObs)
	}

	// 3. Compact input with raw HTML or invalid UTF-8 still reports exact FirstDirtyPos and NeedsRewrite=true.
	srcRawLT := `{"a":"<b>"}`
	m.Reset(CompactFirstDirtyProfile(true, true), ModeCompactObserve)
	if res := m.Step(srcRawLT, true); res.Reason != StopDone {
		t.Fatalf("CompactFirstDirtyProfile rawLT failed: %+v", res)
	}
	if m.CompactObs.HasSpace || !m.CompactObs.NeedsRewrite || m.CompactObs.FirstDirtyPos != 5 {
		t.Fatalf("CompactFirstDirtyProfile rawLT obs=%+v, want HasSpace=false NeedsRewrite=true FirstDirtyPos=5", m.CompactObs)
	}

	// 4. Clean compact input reports FirstDirtyPos=-1.
	srcClean := `{"a":"ok","b":"\u003c"}`
	m.Reset(CompactFirstDirtyProfile(true, true), ModeCompactObserve)
	if res := m.Step(srcClean, true); res.Reason != StopDone {
		t.Fatalf("CompactFirstDirtyProfile clean failed: %+v", res)
	}
	if m.CompactObs.HasSpace || m.CompactObs.NeedsRewrite || m.CompactObs.FirstDirtyPos != -1 {
		t.Fatalf("CompactFirstDirtyProfile clean obs=%+v, want clean/-1", m.CompactObs)
	}

	// 5. Invalid escapes, raw control bytes, or syntax errors AFTER leading space are still rejected identically.
	for _, bad := range []struct {
		src  string
		code ScanError
	}{
		{` {"a":"\q"}`, ErrInvalidEscape},
		{" {\"a\":\"\x01\"}", ErrControlChar},
		{` {"a":1} trailing`, ErrInvalidChar},
		{` {"a":"\u123"}`, ErrInvalidEscapeUTF},
	} {
		var mFull, mFirst Machine
		mFull.Reset(CompactProfile(true, true), ModeCompactObserve)
		mFirst.Reset(CompactFirstDirtyProfile(true, true), ModeCompactObserve)
		rFull := mFull.Step(bad.src, true)
		rFirst := mFirst.Step(bad.src, true)
		if rFirst != rFull || rFirst.Reason != StopError || rFirst.Code != bad.code {
			t.Fatalf("bad=%q: first=%+v full=%+v want code=%v", bad.src, rFirst, rFull, bad.code)
		}
	}
}

func TestCompactObserveFastAuthorityParity(t *testing.T) {
	// Verify strict-surrogate CompactProfile and non-compact modes do not enter fast path.
	strictComp := CompactProfile(true, true)
	strictComp.RejectLoneSurrogate = true
	var mg Machine
	mg.Reset(strictComp, ModeCompactObserve)
	if mg.canEnterCompactObserve(`{"a":"\uD800"}`, true) {
		t.Fatal("strict-surrogate CompactProfile must not enter stepCompactObserve")
	}
	mg.Reset(CompactProfile(true, true), ModeCompactObserve)
	if !mg.canEnterCompactObserve(`{"a":1}`, true) {
		t.Fatal("CompactProfile final=true should enter stepCompactObserve")
	}
	if mg.canEnterCompactObserve(`{"a":1}`, false) {
		t.Fatal("CompactProfile final=false must not enter stepCompactObserve")
	}

	pad58 := strings.Repeat("a", 58)
	pad1018 := strings.Repeat("a", 1018)
	inputs := []string{
		`{}`,
		`[]`,
		`""`,
		`{"a":"ok","b":"\u003c\u003e\u0026","c":"日本語🚀","n":-0.000,"e":1E999999,"t":true,"f":false,"z":null}`,
		` {"a":"ok","b":"\u003c"}`,
		`{"a" : "ok"}`,
		`{"a":"ok"}   `,
		`{"a":"<b>"}`,
		`{"a":"a&b"}`,
		`{"a":"x>y"}`,
		"{\"a\":\"line\u2028sep\"}",
		"{\"a\":\"para\u2029sep\"}",
		"{\"a\":\"bad\xffutf8\"}",
		"{\"a\":\"\\\\<raw_lt\"}",
		"{\"a\":\"\\\"<quoted_lt\"}",
		"{\"a\":\"\\\\\u2028esc\"}",
		"{\"a\":\"\\\\\xffbad\"}",
		"{\"a\":\"ébbbbbbb\\\\<\"}",
		"{\"a\":\"ébbbbbbb\\\\u<\"}",
		"{\"a\":\"ébbbbbbb\\\\\u2028\"}",
		" {\"a\":\"<b>\",\"c\":\"\xff\"}",
		`{"k":"` + pad58 + `plain_cross64","k2":"` + pad58 + `\u003c_esc_cross64"}`,
		`{"k":"` + pad58 + `<dirty_cross64"}`,
		`{"k":"` + pad1018 + `plain_cross1024","after":123}`,
		`{"k":"` + pad1018 + `\u003c_esc_cross1024","after":true}`,
		`{"k":"` + pad1018 + `<dirty_cross1024","after":false}`,
		`{"` + pad58 + `":1234567890.123e+4,"b":false}`,
		`{"k":"` + pad1018 + `","n":12345678901234567890}`,
		`{"a":1} trailing`,
		`{"a":01}`,
		`{"a":123+456}`,
		`0+1`,
		`123+456`,
		`{"a":"<b>\q"}`,
		` {"a":"<b>\q"}`,
		"{\"a\":\"\x01\"}",
		" {\"a\":\"\x01\"}",
		`{"a":"\u123"}`,
		`{"a":"unterminated`,
		`{"a":"` + pad1018 + `unterminated`,
		``,
		`   `,
		`[1,]`,
		`{,}`,
		strings.Repeat("[", CompactMaxContainerDepth) + "0" + strings.Repeat("]", CompactMaxContainerDepth),
		strings.Repeat("[", CompactMaxContainerDepth+1) + "0" + strings.Repeat("]", CompactMaxContainerDepth+1),
	}

	var profiles []Profile
	for _, eh := range []bool{false, true} {
		for _, vu := range []bool{false, true} {
			profiles = append(profiles, CompactProfile(eh, vu), CompactFirstDirtyProfile(eh, vu))
		}
	}

	type snap struct {
		res      ScanResult
		obs      CompactObservation
		maxDepth uint32
		nodeLen  int
		unescCap int
	}
	runOne := func(src string, prof Profile, batchCap int, forceScalar bool) snap {
		var m Machine
		m.Reset(prof, ModeCompactObserve)
		m.SetForceScalar(forceScalar)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		res := m.Step(src, true)
		return snap{
			res:      res,
			obs:      m.CompactObs,
			maxDepth: m.Stat.MaxDepth,
			nodeLen:  m.NodeLen,
			unescCap: cap(m.Unesc),
		}
	}

	for pIdx, prof := range profiles {
		for _, src := range inputs {
			ref := runOne(src, prof, 1, true)
			for _, tc := range []struct {
				name     string
				batchCap int
				scalar   bool
			}{
				{"fast_native", -1, false},
				{"fast_scalar", -1, true},
				{"general_unbatched", 0, false},
			} {
				got := runOne(src, prof, tc.batchCap, tc.scalar)
				if got != ref {
					t.Fatalf("prof=%d (%+v) case=%s input=%q:\n  got =%+v\n  want=%+v", pIdx, prof, tc.name, src, got, ref)
				}
				if got.nodeLen != 0 || got.unescCap != 0 {
					t.Fatalf("prof=%d case=%s input=%q wrote tape/unesc", pIdx, tc.name, src)
				}
			}
		}
	}

	// Concrete EscapeHTML observation assertions on split-backslash + non-ASCII inputs.
	for _, r1Src := range []string{
		"{\"a\":\"ébbbbbbb\\\\<\"}",
		"{\"a\":\"ébbbbbbb\\\\u<\"}",
		"{\"a\":\"ébbbbbbb\\\\\u2028\"}",
	} {
		for _, prof := range []Profile{CompactProfile(true, false), CompactFirstDirtyProfile(true, true)} {
			for _, batchCap := range []int{-1, 1, 0} {
				got := runOne(r1Src, prof, batchCap, false)
				if got.res.Reason != StopDone || !got.obs.NeedsRewrite || got.obs.FirstDirtyPos != 5 {
					t.Fatalf("R1 compact observe src=%q prof=%+v batchCap=%d: got=%+v, want StopDone + NeedsRewrite + FirstDirtyPos=5",
						r1Src, prof, batchCap, got)
				}
			}
		}
	}
}

type multiFieldMatcher struct {
	names []string
}

func (m multiFieldMatcher) FieldCount() int        { return len(m.names) }
func (m multiFieldMatcher) FieldName(i int) string { return m.names[i] }
func (m multiFieldMatcher) MatchField(key string, caseSensitive, _ bool) int {
	for i, n := range m.names {
		if caseSensitive {
			if key == n {
				return i
			}
		} else if strings.EqualFold(key, n) {
			return i
		}
	}
	return -1
}

func TestSchemaFullEmitAuthorityParity(t *testing.T) {
	matcher := multiFieldMatcher{names: []string{"id", "title", "book_ids", "raw", "nested"}}
	nestedMatcher := multiFieldMatcher{names: []string{"k", "v"}}
	nestedPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     nestedMatcher,
		FieldNames: nestedMatcher.names,
		Fields:     []*Plan{{Kind: PlanScalar}, {Kind: PlanScalar}},
	}
	rootPlan := &Plan{
		Kind:       PlanStruct,
		Struct:     matcher,
		FieldNames: matcher.names,
		Fields: []*Plan{
			{Kind: PlanScalar},
			{Kind: PlanScalar},
			{Kind: PlanSlice, Elem: &Plan{Kind: PlanScalar}},
			{Kind: PlanRaw},
			nestedPlan,
		},
	}

	inputs := []string{
		`{"id":12125924,"noise":[` + strings.Repeat(`"x",`, 250) + `"end"],"title":"sonic-zero-copy-schema","book_ids":[101,102,103,104,105]}`,
		`{"book_ids":[1,2],"id":7,"id":8,"TITLE":"dup-and-reordered","unk1":123,"unk2":"abc","unk3":true,"unk4":false,"unk5":null,"unk6":{"a":[1,2]}}`,
		`{"id":1,"raw":{"a":[1,"x"]},"title":"ok"}`,
		`{"id":1,"raw":null,"title":"ok"}`,
		`{"id":1,"nested":{"unk":[1,2,3],"k":"hello\nworld","v":42},"title":"ok"}`,
		`{"\u0069d":99,"noise":"skip\nme","title":"line1\t\"quoted\"\u003c\u4e2d\u6587"}`,
		`{"\u0069d":99,"noise":"skip\nme","title":"` + strings.Repeat("a", 80) + `"}`,
		`{"id":1,"noise":"bad\q","title":"after"}`,
		`{"id":1,"noise":}`,
		`{"id":1,"noise" 2}`,
		`{"id":1,"noise":`,
		`{"id":1,"noise":[1,2`,
		`{"id":1,"noise":"unterminated`,
		`{"id":1,"noise":tr}`,
		`{"id":1,"noise":fals}`,
		`{"id":1,"title":"bad\q"}`,
		`{"id":1,"book_ids":[1,2,`,
	}

	type cfg struct {
		name           string
		caseSensitive  bool
		disUnknown     bool
		relaxedUnknown bool
	}
	cfgs := []cfg{
		{"relaxed", false, false, true},
		{"strict", false, false, false},
		{"case_sensitive_relaxed", true, false, true},
		{"disallow_unknown", false, true, false},
	}

	runScan := func(src string, c cfg, batchCap, initNodeCap int, forceScalar bool) (ScanResult, []Node, []byte, Stat) {
		var m Machine
		m.Reset(StrictProfile(false, false, false, false, true, false), ModeEmit)
		m.ConfigureSchema(rootPlan, c.caseSensitive, c.disUnknown, c.relaxedUnknown)
		m.SetForceScalar(forceScalar)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		buf := make([]Node, initNodeCap)
		for {
			m.Nodes = buf
			res := m.Step(src, true)
			if res.Reason == StopNeedOutput {
				next := len(buf) * 2
				if next < 2 {
					next = 2
				}
				grown := make([]Node, next)
				copy(grown[:res.Produced], buf[:res.Produced])
				buf = grown
				continue
			}
			var out []Node
			if res.Reason == StopDone {
				out = append([]Node(nil), buf[:res.Produced]...)
			}
			unesc := append([]byte(nil), m.Unesc...)
			m.ReleasePoolRefs()
			return res, out, unesc, m.Stat
		}
	}

	// Concrete decoded-bytes assertions so Unesc comparison cannot silently pass on nil == nil.
	for _, wantCase := range []struct {
		src           string
		wantUnescVals []string
	}{
		{
			src:           `{"id":1,"nested":{"unk":[1,2,3],"k":"hello\nworld","v":42},"title":"ok"}`,
			wantUnescVals: []string{"hello\nworld"},
		},
		{
			src:           `{"\u0069d":99,"noise":"skip\nme","title":"line1\t\"quoted\"\u003c\u4e2d\u6587"}`,
			wantUnescVals: []string{"id", "line1\t\"quoted\"<中文"},
		},
	} {
		res, nodes, unesc, _ := runScan(wantCase.src, cfgs[0], -1, 64, false)
		if res.Reason != StopDone {
			t.Fatalf("concrete unesc case %q failed: %+v", wantCase.src, res)
		}
		if len(unesc) == 0 {
			t.Fatalf("concrete unesc case %q returned empty Unesc arena", wantCase.src)
		}
		var gotVals []string
		for _, nd := range nodes {
			if (nd.Typ & TypeMask) == KStringEscaped {
				uoff, ulen, inUnesc := UnpackEscapedSpan(nd.Val)
				if !inUnesc || uoff+ulen > len(unesc) {
					t.Fatalf("concrete unesc case %q invalid packed span uoff=%d ulen=%d inUnesc=%v arenaLen=%d",
						wantCase.src, uoff, ulen, inUnesc, len(unesc))
				}
				gotVals = append(gotVals, string(unesc[uoff:uoff+ulen]))
			}
		}
		if len(gotVals) != len(wantCase.wantUnescVals) {
			t.Fatalf("concrete unesc case %q escaped strings = %q, want %q", wantCase.src, gotVals, wantCase.wantUnescVals)
		}
		for i, w := range wantCase.wantUnescVals {
			if gotVals[i] != w {
				t.Fatalf("concrete unesc case %q escaped[%d] = %q, want %q", wantCase.src, i, gotVals[i], w)
			}
		}
	}

	for _, c := range cfgs {
		for _, src := range inputs {
			refRes, refNodes, refUnesc, refStat := runScan(src, c, 1, 64, true)
			for _, tc := range []struct {
				name     string
				batchCap int
				initCap  int
				scalar   bool
			}{
				{"fast_native_64", -1, 64, false},
				{"fast_scalar_64", -1, 64, true},
				{"fast_native_grow8", -1, 8, false},
				{"fast_native_cap1", -1, 1, false},
			} {
				gotRes, gotNodes, gotUnesc, gotStat := runScan(src, c, tc.batchCap, tc.initCap, tc.scalar)
				if gotRes != refRes {
					t.Fatalf("cfg=%s case=%s input=%q:\n  got =%+v\n  want=%+v", c.name, tc.name, src, gotRes, refRes)
				}
				if refRes.Reason == StopDone {
					if len(gotNodes) != len(refNodes) {
						t.Fatalf("cfg=%s case=%s input=%q nodeLen=%d want=%d", c.name, tc.name, src, len(gotNodes), len(refNodes))
					}
					for i := range refNodes {
						if gotNodes[i] != refNodes[i] {
							t.Fatalf("cfg=%s case=%s input=%q node[%d]=%+v want=%+v", c.name, tc.name, src, i, gotNodes[i], refNodes[i])
						}
					}
					if !bytes.Equal(gotUnesc, refUnesc) {
						t.Fatalf("cfg=%s case=%s input=%q unesc=%q want=%q", c.name, tc.name, src, gotUnesc, refUnesc)
					}
					if gotStat != refStat {
						t.Fatalf("cfg=%s case=%s input=%q stat=%+v want=%+v", c.name, tc.name, src, gotStat, refStat)
					}
				}
			}
		}
	}
}

// TestPlainFullEmitBlockCapAndAuthorityParity verifies stepPlainFullEmit's
// 64-node block-level writable capacity gate (including trailing guard-slot
// isolation), confirms real P1 fast-path entry without SetBatchCapForTest, and
// checks bit-for-bit differential equivalence against stepGeneral on
// ScanResult, Node Tape, Unesc, and all seven Stat counters across cross-block
// keys/colons/values, escapes, long numbers, prefix/full-doc, invalid inputs,
// and growth-recovery transitions.
func TestPlainFullEmitBlockCapAndAuthorityParity(t *testing.T) {
	// 1. Explicitly verify canEnterPlainFullEmit around the 64-slot boundary
	// (remaining slots: 0, 7, 8, 63, 64, 65) at both NodeLen==0 and NodeLen>0.
	for _, baseUsed := range []int{0, 17} {
		for _, tc := range []struct {
			rem  int
			want bool
		}{
			{0, false},
			{7, false},
			{8, false},
			{63, false},
			{64, true},
			{65, true},
		} {
			var m Machine
			m.Reset(StrictProfile(true, false, false, false, true, true), ModeEmit)
			m.Nodes = make([]Node, baseUsed+tc.rem)
			m.NodeLen = baseUsed
			if got := m.canEnterPlainFullEmit(`{"a":1}`, true); got != tc.want {
				t.Fatalf("baseUsed=%d rem=%d canEnterPlainFullEmit=%v, want %v", baseUsed, tc.rem, got, tc.want)
			}
		}
	}

	// 2. Build boundary-focused corpus: cross-64B keys, colons, values,
	// escaped strings, long decimals, multi-block literals/numbers, dense
	// >64-node containers triggering mid-scan handoff + growth + P1 re-entry,
	// prefix vs full-doc trailing data, and cross-block errors.
	var denseObj strings.Builder
	denseObj.WriteByte('{')
	for i := 0; i < 45; i++ {
		if i > 0 {
			denseObj.WriteString(" , ")
		}
		denseObj.WriteString(`"k`)
		denseObj.WriteByte(byte('a' + (i % 26)))
		denseObj.WriteString(`":`)
		switch i % 5 {
		case 0:
			denseObj.WriteString(`"val`)
			denseObj.WriteByte(byte('0' + (i % 10)))
			denseObj.WriteByte('"')
		case 1:
			denseObj.WriteString(`123456789012345678`)
		case 2:
			denseObj.WriteString(`"e\n\t\u4e2d"`)
		case 3:
			denseObj.WriteString(`true`)
		case 4:
			denseObj.WriteString(`null`)
		}
	}
	denseObj.WriteByte('}')

	var denseArr strings.Builder
	denseArr.WriteByte('[')
	for i := 0; i < 80; i++ {
		if i > 0 {
			denseArr.WriteByte(',')
		}
		if i%3 == 0 {
			denseArr.WriteString("0")
		} else if i%3 == 1 {
			denseArr.WriteString(`"s"`)
		} else {
			denseArr.WriteString("false")
		}
	}
	denseArr.WriteByte(']')

	// Craft exact 64-byte boundary alignments for key, colon, value, comma, escape, number, and literal.
	pad56 := strings.Repeat(" ", 56)
	pad57 := strings.Repeat(" ", 57)
	pad58 := strings.Repeat(" ", 58)
	pad59 := strings.Repeat(" ", 59)
	pad60 := strings.Repeat(" ", 60)
	pad61 := strings.Repeat(" ", 61)
	pad62 := strings.Repeat(" ", 62)

	inputs := []string{
		// Cross-block key, colon at byte 63 / 64, cross-block plain & escaped values
		`{` + pad58 + `"cross_block_key":"plain_val","after":1}`,
		`{` + pad57 + `"k123":` + `"cross_val_past_64_bytes_boundary","next":"ok"}`,
		`{` + pad58 + `"k123"` + `:` + `"colon_at_64_val","k2":2}`,
		`{` + pad59 + `"k12"` + ` : ` + `"colon_in_next_block","k2":3}`,
		`{"a":"` + strings.Repeat("x", 57) + `","b":"comma_at_byte_64"}`,
		`{"a":"` + strings.Repeat("x", 56) + `","b":"comma_at_byte_63"}`,
		`{"a":"` + strings.Repeat("x", 55) + `\n` + strings.Repeat("y", 20) + `","b":true}`,
		`{"a":"` + strings.Repeat("x", 60) + `\u4e2d\uD83D\uDE00` + strings.Repeat("z", 10) + `","b":false}`,
		`{` + pad58 + `"esc\nkey_cross":"v\t1","plain":"v2"}`,
		// Multi-block numbers and literals crossing byte 63->64
		`{"n":` + pad57 + `1234567890123456789,"f":` + pad56 + `3.14159265358979323846,"e":1.25e+20,"z":-0,"zf":-0.0,"big":18446744073709551615,"min":-9223372036854775808}`,
		`{"t":` + pad57 + `true,"f":` + pad56 + `false,"n":` + pad57 + `null}`,
		`{"inf":` + pad56 + `1e400}`,
		// Dense containers exceeding 64 nodes (forces handoff + growth + P1 re-entry)
		denseObj.String(),
		denseArr.String(),
		strings.Repeat("[", 70) + `{"x":"y"}` + strings.Repeat("]", 70),
		// Prefix vs FullDocument trailing tokens across block boundary
		`{"k":"v"}` + pad60 + `trailing`,
		pad62 + `12345 trailing`,
		pad61 + `"hello_cross_block"   trailing`,
		// Invalid inputs across 64-byte boundaries
		`{` + pad58 + `"cross_key_no_colon" 123}`,
		`{` + pad58 + `"k":"` + strings.Repeat("a", 10) + `\q"}`,
		`{"k":"` + strings.Repeat("a", 50) + "\x01" + strings.Repeat("b", 20) + `"}`,
		`{"k":"` + strings.Repeat("a", 60) + `\uD800"}`,
		`{"k":` + pad58 + `truX}`,
		`{"k":` + pad58 + `falsX}`,
		`{"k":` + pad58 + `nulX}`,
		`{"k":` + pad58 + `01}`,
		`{"k":` + pad58 + `1e+}`,
		`{"k":1` + pad60 + `]`,
		`[1` + pad60 + `}`,
		`{` + pad60 + `1:2}`,
		`{"k":` + pad60 + `"unterminated`,
		`{"k":` + pad60,
		`{"k"` + pad60,
		`{"k":1,` + pad60,
		`[1,` + pad60,
		pad62 + `   `,
	}

	profiles := []Profile{
		StrictProfile(true, true, true, false, true, false),
		StrictProfile(true, false, false, false, true, true),
		StrictProfile(false, false, false, true, false, false),
		StrictProfile(true, false, true, true, false, true),
		StrictProfile(false, false, false, false, false, true),
	}

	guardCanary := Node{Typ: 0xdeadbeefcafebabe, Val: 0x123456789abcdef0}

	runWithGuard := func(src string, prof Profile, batchCap int, initNodeCap int, forceScalar bool) (ScanResult, []Node, []byte, Stat, bool, uint64) {
		var m Machine
		m.Reset(prof, ModeEmit)
		m.SetForceScalar(forceScalar)
		if batchCap >= 0 {
			m.SetBatchCapForTest(batchCap)
		}
		writableCap := initNodeCap
		backing := make([]Node, writableCap+1)
		backing[writableCap] = guardCanary
		enteredP1 := false

		for {
			m.Nodes = backing[:writableCap]
			if m.canEnterPlainFullEmit(src, true) {
				enteredP1 = true
			}
			res := m.Step(src, true)
			if backing[writableCap] != guardCanary {
				t.Fatalf("trailing guard slot corrupted (writableCap=%d batchCap=%d input=%q): got %+v", writableCap, batchCap, src, backing[writableCap])
			}
			if res.Reason == StopNeedOutput {
				nextCap := writableCap * 2
				if nextCap < 64 {
					nextCap = 64
				}
				grown := make([]Node, nextCap+1)
				copy(grown[:res.Produced], backing[:res.Produced])
				grown[nextCap] = guardCanary
				backing = grown
				writableCap = nextCap
				continue
			}
			var outNodes []Node
			if res.Reason == StopDone {
				outNodes = append([]Node(nil), backing[:res.Produced]...)
			}
			return res, outNodes, append([]byte(nil), m.Unesc...), m.Stat, enteredP1, m.batch.calls
		}
	}

	p1EnteredCount := 0
	var p1BatchCalls uint64

	for pIdx, prof := range profiles {
		for _, src := range inputs {
			refRes, refNodes, refUnesc, refStat, refP1, _ := runWithGuard(src, prof, 1, 64, true)
			if refP1 {
				t.Fatalf("reference stepGeneral unexpectedly satisfied canEnterPlainFullEmit")
			}

			for _, tc := range []struct {
				name        string
				batchCap    int
				initNodeCap int
				scalar      bool
				expectP1    bool
			}{
				{"p1_native_cap8", -1, 8, false, false},
				{"p1_native_cap63", -1, 63, false, false},
				{"p1_native_cap64", -1, 64, false, true},
				{"p1_native_cap65", -1, 65, false, true},
				{"p1_native_cap512", -1, 512, false, true},
				{"p1_scalar_cap64", -1, 64, true, true},
				{"p1_scalar_cap512", -1, 512, true, true},
				{"general_unbatched", 0, 512, false, false},
			} {
				gotRes, gotNodes, gotUnesc, gotStat, enteredP1, calls := runWithGuard(src, prof, tc.batchCap, tc.initNodeCap, tc.scalar)
				if tc.expectP1 && !enteredP1 {
					t.Fatalf("prof=%d case=%s input=%q: expected P1 entry, but canEnterPlainFullEmit was never true", pIdx, tc.name, src)
				}
				if enteredP1 {
					p1EnteredCount++
					p1BatchCalls += calls
				}
				if gotRes != refRes {
					t.Fatalf("prof=%d case=%s input=%q:\n  got  res=%+v\n  want res=%+v", pIdx, tc.name, src, gotRes, refRes)
				}
				if refRes.Reason == StopDone {
					if len(gotNodes) != len(refNodes) {
						t.Fatalf("prof=%d case=%s input=%q nodeLen=%d, want %d", pIdx, tc.name, src, len(gotNodes), len(refNodes))
					}
					for i := range refNodes {
						if gotNodes[i] != refNodes[i] {
							t.Fatalf("prof=%d case=%s input=%q node[%d]=%+v, want %+v", pIdx, tc.name, src, i, gotNodes[i], refNodes[i])
						}
					}
					if !bytes.Equal(gotUnesc, refUnesc) {
						t.Fatalf("prof=%d case=%s input=%q unesc=%q, want %q", pIdx, tc.name, src, gotUnesc, refUnesc)
					}
				}
				if gotStat != refStat {
					t.Fatalf("prof=%d case=%s input=%q stat=%+v, want %+v", pIdx, tc.name, src, gotStat, refStat)
				}
			}
		}
	}

	if p1EnteredCount == 0 || p1BatchCalls == 0 {
		t.Fatalf("expected P1 fast path to execute LexBatch calls, got entered=%d calls=%d", p1EnteredCount, p1BatchCalls)
	}
}
