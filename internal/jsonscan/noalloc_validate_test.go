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
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

type singleKeepMatcher struct {
	name string
}

func (m singleKeepMatcher) MatchField(key string, caseSensitive, keyASCII bool) int {
	if caseSensitive {
		if key == m.name {
			return 0
		}
		return -1
	}
	if strings.EqualFold(key, m.name) {
		return 0
	}
	return -1
}

func (m singleKeepMatcher) FieldName(idx int) string {
	if idx == 0 {
		return m.name
	}
	return ""
}

func (m singleKeepMatcher) FieldCount() int { return 1 }

func TestG2MultiMiBUnknownEscapedNoUnescGrowthAndReuse(t *testing.T) {
	// ~2.1 MiB escaped payload containing basic escapes, BMP \u0061, valid surrogate pairs,
	// raw multi-byte UTF-8, and long even backslash runs.
	unit := `\u0061\uD83D\uDE00\"\\\\\/\b\f\n\r\t中文`
	hugeEscaped := strings.Repeat(unit, (2<<20)/len(unit)+1)
	doc := `{"keep":42,"unused":"` + hugeEscaped + `"}`

	plan := &Plan{
		Kind:   PlanStruct,
		Struct: singleKeepMatcher{name: "keep"},
		Fields: []*Plan{{Kind: PlanScalar}},
	}

	profiles := []struct {
		name string
		prof Profile
	}{
		{"default_relaxed_ctrl_allow_surr", StrictProfile(false, false, false, false, true, true)},
		{"default_relaxed_ctrl_strict_surr", StrictProfile(false, false, true, false, true, true)},
		{"strict_ctrl_allow_surr", StrictProfile(true, true, false, false, true, true)},
		{"strict_ctrl_strict_surr", StrictProfile(true, true, true, false, true, true)},
	}

	var m Machine
	nodes := make([]Node, 32)

	for _, pc := range profiles {
		// 1. Schema projection (ModeEmit + unknown field -> ModeValidateOnly)
		m.Reset(pc.prof, ModeEmit)
		m.ConfigureSchema(plan, false, false, false)
		m.Nodes = nodes
		res := m.Step(doc, true)
		if res.Reason != StopDone || res.Code != ErrNone || res.Consumed != len(doc) {
			t.Fatalf("%s schema single-pass failed: %+v", pc.name, res)
		}
		if len(m.Unesc) != 0 || cap(m.Unesc) != 0 {
			t.Fatalf("%s schema allocated Unesc for unused field: len=%d cap=%d", pc.name, len(m.Unesc), cap(m.Unesc))
		}

		// 2. Chunked streaming (4 KiB chunks) over the same 2.1 MiB doc: must stay O(1) Unesc
		m.Reset(pc.prof, ModeEmit)
		m.ConfigureSchema(plan, false, false, false)
		m.Nodes = nodes
		for end := 4096; end < len(doc); end += 4096 {
			r := m.Step(doc[:end], false)
			if r.Reason != StopNeedInput {
				t.Fatalf("%s chunked expected StopNeedInput at %d, got %+v", pc.name, end, r)
			}
			if cap(m.Unesc) != 0 {
				t.Fatalf("%s chunked grew Unesc at %d: cap=%d", pc.name, end, cap(m.Unesc))
			}
		}
		resFinal := m.Step(doc, true)
		if resFinal.Reason != StopDone || resFinal.Code != ErrNone {
			t.Fatalf("%s chunked final failed: %+v", pc.name, resFinal)
		}
		if cap(m.Unesc) != 0 {
			t.Fatalf("%s chunked final grew Unesc: cap=%d", pc.name, cap(m.Unesc))
		}

		// 3. ModeValidateOnly & ModeRawSpan directly
		for _, mode := range []OutputMode{ModeValidateOnly, ModeRawSpan} {
			m.Reset(pc.prof, mode)
			m.Nodes = nodes
			r := m.Step(doc, true)
			if r.Reason != StopDone || r.Code != ErrNone {
				t.Fatalf("%s mode=%v failed: %+v", pc.name, mode, r)
			}
			if cap(m.Unesc) != 0 {
				t.Fatalf("%s mode=%v grew Unesc: cap=%d", pc.name, mode, cap(m.Unesc))
			}
		}
	}

	// 4. Default CompactProfile (allows lone surrogate) vs custom strict-surrogate CompactProfile
	loneSurrDoc := `{"keep":1,"unused":"\uD800` + strings.Repeat(`\u0061`, 10000) + `"}`
	m.Reset(CompactProfile(true, true), ModeCompactObserve)
	rCompDef := m.Step(loneSurrDoc, true)
	if rCompDef.Reason != StopDone || rCompDef.Code != ErrNone || cap(m.Unesc) != 0 {
		t.Fatalf("default CompactProfile must accept lone surrogate with 0 Unesc: res=%+v cap=%d", rCompDef, cap(m.Unesc))
	}

	strictCompProf := CompactProfile(true, true)
	strictCompProf.RejectLoneSurrogate = true
	m.Reset(strictCompProf, ModeCompactObserve)
	rCompStrict := m.Step(loneSurrDoc, true)
	if rCompStrict.Reason != StopError || rCompStrict.Code != ErrInvalidEscapeUTF || rCompStrict.ErrorOffset != 20 || cap(m.Unesc) != 0 {
		t.Fatalf("strict CompactProfile mismatch: res=%+v cap=%d", rCompStrict, cap(m.Unesc))
	}

	// 5. Pre-existing Unesc from an earlier selected escaped field + Reset reuse:
	// skipping a 2.1 MiB unknown field must NOT grow m.Unesc beyond its initial 256B capacity.
	docWithSelectedEsc := `{"keep":"ok\n","unused":"` + hugeEscaped + `"}`
	m.Reset(StrictProfile(true, true, true, false, true, true), ModeEmit)
	m.ConfigureSchema(plan, false, false, false)
	m.Nodes = nodes
	rSel := m.Step(docWithSelectedEsc, true)
	if rSel.Reason != StopDone || rSel.Code != ErrNone {
		t.Fatalf("docWithSelectedEsc failed: %+v", rSel)
	}
	if len(m.Unesc) != 3 || cap(m.Unesc) > 256 {
		t.Fatalf("expected Unesc len=3 and cap<=256, got len=%d cap=%d", len(m.Unesc), cap(m.Unesc))
	}

	// Measure B/op & allocs/op deterministically after warmup (G2.4).
	// Settle GC and suspend concurrent background GC/scavenger during measurement
	// (matching stdlib testing.AllocsPerRun) so multi-MiB setup strings from earlier
	// sub-checks cannot trigger unrelated runtime allocations mid-measurement under -race.
	profBench := StrictProfile(true, true, true, false, true, true)
	m.Reset(profBench, ModeEmit)
	m.ConfigureSchema(plan, false, false, false)
	m.Nodes = nodes
	if r := m.Step(doc, true); r.Reason != StopDone {
		t.Fatalf("warmup stop: %+v", r)
	}
	prevProcs := runtime.GOMAXPROCS(1)
	runtime.GC()
	prevGC := debug.SetGCPercent(-1)
	var msBefore, msAfter runtime.MemStats
	runtime.ReadMemStats(&msBefore)
	const measureRuns = 20
	for i := 0; i < measureRuns; i++ {
		m.Reset(profBench, ModeEmit)
		m.ConfigureSchema(plan, false, false, false)
		m.Nodes = nodes
		r := m.Step(doc, true)
		if r.Reason != StopDone {
			debug.SetGCPercent(prevGC)
			runtime.GOMAXPROCS(prevProcs)
			t.Fatalf("unexpected stop: %+v", r)
		}
	}
	runtime.ReadMemStats(&msAfter)
	debug.SetGCPercent(prevGC)
	runtime.GOMAXPROCS(prevProcs)
	deltaBytes := (msAfter.TotalAlloc - msBefore.TotalAlloc) / measureRuns
	deltaAllocs := (msAfter.Mallocs - msBefore.Mallocs) / measureRuns
	if deltaBytes != 0 || deltaAllocs != 0 {
		t.Fatalf("expected 0 B/op and 0 allocs/op on 2.1 MiB unknown escaped string, got %d B/op, %d allocs/op",
			deltaBytes, deltaAllocs)
	}
}

func TestG2EveryHexSplitAndEarlyErrorInHugeString(t *testing.T) {
	// Split at every single byte position across valid & invalid surrogate/escape sequences,
	// verifying ModeEmit == ModeValidateOnly == ModeRawSpan on acceptance and ErrorOffset.
	inputs := []string{
		`{"k":"\uD83D\uDE00\u0061\\\\\""}`,
		`{"k":"\uD83D\u0061"}`,
		`{"k":"\uDC00"}`,
		`{"k":"\uD83D\u123G"}`,
		`{"k":"a\x01\u0061"}`,
		`{"k":"\uD83D` + strings.Repeat("x", 64) + `"}`,
	}
	for _, prof := range []Profile{
		StrictProfile(false, false, false, false, true, true),
		StrictProfile(false, false, true, false, true, true),
		StrictProfile(true, true, false, false, true, true),
		StrictProfile(true, true, true, false, true, true),
	} {
		for _, inp := range inputs {
			var mEmit Machine
			nodes := make([]Node, 32)
			mEmit.Reset(prof, ModeEmit)
			mEmit.Nodes = nodes
			want := mEmit.Step(inp, true)

			for _, mode := range []OutputMode{ModeValidateOnly, ModeRawSpan} {
				for split := 1; split < len(inp); split++ {
					var mSub Machine
					mSub.Reset(prof, mode)
					mSub.Nodes = nodes
					var got ScanResult
					earlyStop := false
					for end := split; end < len(inp); end += split {
						r := mSub.Step(inp[:end], false)
						if r.Reason == StopError {
							got = r
							earlyStop = true
							break
						}
						if r.Reason != StopNeedInput {
							t.Fatalf("unexpected non-NeedInput before EOF: %+v", r)
						}
					}
					if !earlyStop {
						got = mSub.Step(inp, true)
					}
					if got.Reason != want.Reason || got.Code != want.Code || got.ErrorOffset != want.ErrorOffset {
						t.Fatalf("mode=%v split=%d inp=%q prof=%+v: got %+v, want %+v",
							mode, split, inp, prof, got, want)
					}
					if cap(mSub.Unesc) != 0 {
						t.Fatalf("mode=%v grew Unesc: cap=%d", mode, cap(mSub.Unesc))
					}
				}
			}
		}
	}

	// Error near start of a 2 MiB string must report exact offset with 0 Unesc capacity.
	badFront := `{"keep":1,"unused":"abc\uD800xyz` + strings.Repeat(`\u0061`, 350000) + `"}`
	var m Machine
	m.Reset(StrictProfile(true, true, true, false, true, true), ModeValidateOnly)
	rBad := m.Step(badFront, true)
	if rBad.Reason != StopError || rBad.Code != ErrInvalidEscapeUTF || rBad.ErrorOffset != 23 || cap(m.Unesc) != 0 {
		t.Fatalf("unexpected early error result: %+v cap=%d", rBad, cap(m.Unesc))
	}
}
