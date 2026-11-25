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

type countingFieldMatcher struct {
	fields []string
	calls  int
}

func (c *countingFieldMatcher) FieldCount() int        { return len(c.fields) }
func (c *countingFieldMatcher) FieldName(i int) string { return c.fields[i] }
func (c *countingFieldMatcher) MatchField(key string, caseSensitive, keyASCII bool) int {
	c.calls++
	for i, f := range c.fields {
		if f == key || (!caseSensitive && strings.EqualFold(f, key)) {
			return i
		}
	}
	return -1
}

func TestG4BatchConsumerErrorOrderAndSingleValueLookahead(t *testing.T) {
	for _, bcap := range []int{0, 1, 2, 16, 64} {
		// 1. Missing colon beats later invalid escape (`{"a" 1, "b":"\q"}`) and control char.
		for _, src := range []string{`{"a" 1, "b":"\q"}`, "{\"a\" 1, \"b\":\"\x01\"}", `{"a" "\q"}`} {
			var m Machine
			m.Nodes = make([]Node, 16)
			m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
			m.SetBatchCapForTest(bcap)
			res := m.Step(src, true)
			if res.Reason != StopError || res.Code != ErrExpectColon || res.ErrorOffset != 5 {
				t.Fatalf("bcap=%d %q: got %+v, want ErrExpectColon at 5", bcap, src, res)
			}
		}

		// Nonfinal missing colon (`{"a" 123` with final=false) MUST fail immediately with ErrExpectColon,
		// not wait for more digits of 123!
		{
			var m Machine
			m.Nodes = make([]Node, 16)
			m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
			m.SetBatchCapForTest(bcap)
			res := m.Step(`{"a" 123`, false)
			if res.Reason != StopError || res.Code != ErrExpectColon || res.ErrorOffset != 5 {
				t.Fatalf("bcap=%d nonfinal missing colon: got %+v, want ErrExpectColon at 5", bcap, res)
			}
		}

		// 2. Single-value prefix succeeds on `{}` even when next top-level value is malformed (`{} "\q"`, `{} @@@`),
		// whereas FullDocument rejects at offset 3!
		for _, src := range []string{`{} "\q"`, `{} @@@`, "{} \"\x01\""} {
			var mSingle Machine
			mSingle.Nodes = make([]Node, 8)
			mSingle.Reset(StrictProfile(true, true, true, false, true, false), ModeEmit)
			mSingle.SetBatchCapForTest(bcap)
			rSingle := mSingle.Step(src, true)
			if rSingle.Reason != StopDone || rSingle.Consumed != 2 || mSingle.NodeLen != 1 {
				t.Fatalf("bcap=%d single-value %q: got %+v nodeLen=%d, want StopDone Consumed=2", bcap, src, rSingle, mSingle.NodeLen)
			}

			var mFull Machine
			mFull.Nodes = make([]Node, 8)
			mFull.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
			mFull.SetBatchCapForTest(bcap)
			rFull := mFull.Step(src, true)
			if rFull.Reason != StopError || rFull.Code != ErrInvalidChar || rFull.ErrorOffset != 3 {
				t.Fatalf("bcap=%d full-doc %q: got %+v, want ErrInvalidChar at 3", bcap, src, rFull)
			}
		}

		// 3. Mismatched brackets `{]`, `[}`, `{,}`, `[1,]` rejected by Go structural grammar!
		bracketCases := []struct {
			src     string
			wantErr ScanError
			wantOff int
		}{
			{`{]`, ErrExpectKey, 1},
			{`[}`, ErrInvalidChar, 1},
			{`{,}`, ErrExpectKey, 1},
			{`[1,]`, ErrInvalidChar, 3},
			{`{"a":1 ]`, ErrExpectObjCommaOrEnd, 7},
			{`[1 }`, ErrExpectArrCommaOrEnd, 3},
		}
		for _, bc := range bracketCases {
			var m Machine
			m.Nodes = make([]Node, 8)
			m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
			m.SetBatchCapForTest(bcap)
			res := m.Step(bc.src, true)
			if res.Reason != StopError || res.Code != bc.wantErr || res.ErrorOffset != bc.wantOff {
				t.Fatalf("bcap=%d %q: got %+v, want (%v at %d)", bcap, bc.src, res, bc.wantErr, bc.wantOff)
			}
		}
	}
}

func TestG4BatchVsTapeCapacityCrossMatrixAndNoRescan(t *testing.T) {
	doc := `{"x": [1, -2, true, false, null, "plain", "esc\nstr", {"y": 3.5}], "unknown": {"skip": [1, 2, 3]}, "z": 42}`

	runCross := func(batchCap, initTapeCap, chunkStep int, plan *Plan) (ScanResult, []Node, []byte, Stat) {
		var m Machine
		var backing []Node
		if initTapeCap > 0 {
			backing = make([]Node, initTapeCap)
		}
		m.Nodes = backing
		m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
		m.SetBatchCapForTest(batchCap)
		if plan != nil {
			m.ConfigureSchema(plan, false, false, false)
		}

		inLen := chunkStep
		if inLen > len(doc) {
			inLen = len(doc)
		}
		for {
			final := inLen >= len(doc)
			res := m.Step(doc[:inLen], final)
			switch res.Reason {
			case StopDone, StopError:
				return res, append([]Node(nil), m.Nodes[:m.NodeLen]...), append([]byte(nil), m.Unesc...), m.Stat
			case StopNeedOutput:
				nextCap := len(m.Nodes) + 1
				if len(m.Nodes) >= 4 {
					nextCap = len(m.Nodes) * 2
				}
				grown := make([]Node, nextCap)
				copy(grown[:m.NodeLen], m.Nodes[:m.NodeLen])
				m.Nodes = grown
			case StopNeedInput:
				inLen += chunkStep
				if inLen > len(doc) {
					inLen = len(doc)
				}
			}
		}
	}

	baseRes, baseNodes, baseUnesc, baseStat := runCross(64, 64, len(doc), nil)
	if baseRes.Reason != StopDone {
		t.Fatalf("baseline failed: %+v", baseRes)
	}

	for _, bcap := range []int{0, 1, 2, 16, 64} {
		for _, tcap := range []int{0, 1, 2, 7, 64} {
			for _, cstep := range []int{1, 3, 17, len(doc)} {
				res, nodes, unesc, st := runCross(bcap, tcap, cstep, nil)
				if res != baseRes || st != baseStat || string(unesc) != string(baseUnesc) || len(nodes) != len(baseNodes) {
					t.Fatalf("mismatch at bcap=%d tcap=%d cstep=%d: res=%+v/%+v len=%d/%d stat=%+v/%+v",
						bcap, tcap, cstep, res, baseRes, len(nodes), len(baseNodes), st, baseStat)
				}
				for i := range baseNodes {
					if (baseNodes[i].Typ&TypeMask) == KNull || (baseNodes[i].Typ&TypeMask) == KTrue || (baseNodes[i].Typ&TypeMask) == KFalse {
						baseNodes[i].Val = 0
						nodes[i].Val = 0
					}
					if nodes[i] != baseNodes[i] {
						t.Fatalf("node[%d] mismatch at bcap=%d tcap=%d cstep=%d: got %+v want %+v",
							i, bcap, tcap, cstep, nodes[i], baseNodes[i])
					}
				}
			}
		}
	}

	// Verify StopNeedOutput NEVER re-invokes LexBatch from the batch start and NEVER
	// matches Schema fields more than once!
	matcher := &countingFieldMatcher{fields: []string{"alpha", "beta", "gamma"}}
	plan := &Plan{Kind: PlanStruct, Struct: matcher}
	smallObj := `{"k0":1,"k1":2,"k2":3,"k3":4,"k4":5}`
	var m Machine
	m.Nodes = make([]Node, 1) // force StopNeedOutput on every emitted node!
	m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
	m.ConfigureSchema(plan, false, false, false)
	for {
		res := m.Step(smallObj, true)
		if res.Reason == StopDone {
			break
		}
		if res.Reason != StopNeedOutput {
			t.Fatalf("unexpected reason: %+v", res)
		}
		grown := make([]Node, len(m.Nodes)+1)
		copy(grown[:m.NodeLen], m.Nodes[:m.NodeLen])
		m.Nodes = grown
	}
	if m.LexBatchCalls() != 1 {
		t.Fatalf("StopNeedOutput re-ran LexBatch %d times on 17-token object, want exactly 1", m.LexBatchCalls())
	}
	if matcher.calls != 5 {
		t.Fatalf("MatchField called %d times for 5 non-sequential keys across StopNeedOutput, want 5", matcher.calls)
	}

	// Verify >64KiB plain/escaped strings and >64KiB numbers across chunks succeed linearly
	// and Reset reuse after an error leaves zero stale batch state.
	bigPlain := `"` + strings.Repeat("p", 80<<10) + `\n"`
	var mReuse Machine
	mReuse.Nodes = make([]Node, 8)
	mReuse.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
	badRes := mReuse.Step(`{"a" 1}`, true)
	if badRes.Reason != StopError || badRes.Code != ErrExpectColon {
		t.Fatalf("expected error before reuse, got %+v", badRes)
	}
	mReuse.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
	inOff := 4096
	for {
		final := inOff >= len(bigPlain)
		if final {
			inOff = len(bigPlain)
		}
		r := mReuse.Step(bigPlain[:inOff], final)
		if r.Reason == StopDone {
			break
		}
		if r.Reason != StopNeedInput {
			t.Fatalf("bigPlain unexpected stop: %+v", r)
		}
		inOff += 16384
	}
	if mReuse.NodeLen != 1 || len(mReuse.Unesc) != (80<<10)+1 {
		t.Fatalf("bigPlain decoded len=%d, want %d", len(mReuse.Unesc), (80<<10)+1)
	}
}
