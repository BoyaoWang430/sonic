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

func TestBuildValidatedSourceSpansParityWithTape(t *testing.T) {
	prefix := "   \n\t "
	docBody := `{
		"plain": "hello",
		"e\u0073c": "a\nb\u4e2d\ud83d\ude00",
		"int": 42,
		"neg": -99,
		"dec": -123456.7890125,
		"huge": 1e400,
		"arr": [true, false, null, {"nested": [1, "x\"y", []]}, {}]
	}`
	src := prefix + docBody + ` {"trailing": 2}`
	startOff := 3

	var mIdx Machine
	mIdx.ResetValidatedDoc(true, false, startOff)
	dst := make([]uint64, 128)
	vdoc, res := mIdx.ScanValidatedDoc(src, dst)
	if res.Reason != StopDone || !vdoc.IsValid() {
		t.Fatalf("ScanValidatedDoc failed: %+v", res)
	}

	spans, err := BuildValidatedSourceSpans(vdoc, startOff, true, false, false, nil)
	if err != nil {
		t.Fatalf("BuildValidatedSourceSpans failed: %v", err)
	}
	if len(spans) != len(vdoc.Entries()) {
		t.Fatalf("len(spans)=%d != len(entries)=%d", len(spans), len(vdoc.Entries()))
	}

	// Compare every single EntryPosition against ModeEmit Node.Position(), and verify raw [RawStart:RawEnd].
	var mTape Machine
	mTape.ResetFastEmit(false, true, false, true, false, startOff)
	nodes := make([]Node, len(spans)+8)
	mTape.Nodes = nodes
	tRes := mTape.Step(src[:vdoc.Consumed()], true)
	if tRes.Reason != StopDone || mTape.NodeLen != len(spans) {
		t.Fatalf("ModeEmit mismatch: res=%+v nodeLen=%d want=%d", tRes, mTape.NodeLen, len(spans))
	}

	for i, sp := range spans {
		kind := vdoc.Entries()[i] & IdxKindMask
		gotPos := sp.EntryPosition(kind)
		wantPos := int(nodes[i].Typ >> PosBits)
		if gotPos != wantPos {
			t.Fatalf("entry %d (kind=%d): EntryPosition=%d != Node.Position=%d (raw=%q)",
				i, kind, gotPos, wantPos, src[sp.RawStart:sp.RawEnd])
		}
		raw := src[sp.RawStart:sp.RawEnd]
		switch kind {
		case IdxStringPlain, IdxStringEsc:
			if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
				t.Fatalf("entry %d string raw span missing quotes: %q", i, raw)
			}
		case IdxObject:
			if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
				t.Fatalf("entry %d object raw span invalid: %q", i, raw)
			}
		case IdxArray:
			if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
				t.Fatalf("entry %d array raw span invalid: %q", i, raw)
			}
		case IdxNull:
			if raw != "null" {
				t.Fatalf("entry %d null raw span=%q", i, raw)
			}
		case IdxTrue:
			if raw != "true" {
				t.Fatalf("entry %d true raw span=%q", i, raw)
			}
		case IdxFalse:
			if raw != "false" {
				t.Fatalf("entry %d false raw span=%q", i, raw)
			}
		}
	}

	// Unvalidated ValidatedDoc must fail explicitly, never returning silent offset 0.
	if _, err := BuildValidatedSourceSpans(ValidatedDoc{}, 0, true, false, false, nil); err == nil {
		t.Fatal("expected error for unvalidated ValidatedDoc")
	}

	// Non-strict control characters accepted by primary scan (!rejectControl) must also succeed in span builder.
	rawCtrlSrc := "[\"a\x01b\"]"
	mIdx.ResetValidatedDoc(false, false, 0)
	vdocCtrl, resCtrl := mIdx.ScanValidatedDoc(rawCtrlSrc, dst)
	if resCtrl.Reason != StopDone {
		t.Fatalf("expected relaxed control string to pass: %+v", resCtrl)
	}
	spansCtrl, err := BuildValidatedSourceSpans(vdocCtrl, 0, false, false, false, nil)
	if err != nil || len(spansCtrl) != 2 || spansCtrl[1].RawStart != 1 || spansCtrl[1].RawEnd != 6 {
		t.Fatalf("unexpected relaxed control spans=%+v err=%v", spansCtrl, err)
	}
}

func TestResumableValidatedDocScanZeroPrefixRescan(t *testing.T) {
	// 64 KiB string prefix + 4,000 numbers: growing from cap=64 across multiple steps
	// must NOT re-lex the 64 KiB prefix.
	var sb strings.Builder
	sb.WriteString(`{"prefix":"`)
	sb.WriteString(strings.Repeat("x", 64<<10))
	sb.WriteString(`","escaped":"a\nb\tc","nums":[`)
	for i := 0; i < 4000; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("123")
	}
	sb.WriteString(`]}`)
	src := sb.String()

	ResetDiag()
	var m Machine
	m.ResetValidatedDoc(true, false, 0)
	buf := make([]uint64, 64)
	var ckpt ValidatedScanCheckpoint
	var vdoc ValidatedDoc
	resumes := 0
	for {
		out := m.ScanValidatedDocResumable(src, buf, MaxValidatedDocBytes, &ckpt, &vdoc)
		if out.Status == ValidatedScanComplete {
			break
		}
		if out.Status != ValidatedScanNeedsCapacity || !ckpt.HasProgress() {
			t.Fatalf("unexpected status=%d progress=%v res=%+v", out.Status, ckpt.HasProgress(), out.Result)
		}
		resumes++
		grown := make([]uint64, len(buf)*2)
		copy(grown[:out.Result.Produced], buf[:out.Result.Produced])
		buf = grown
	}
	if resumes < 4 || !vdoc.IsValid() || len(vdoc.Entries()) != 4007 {
		t.Fatalf("unexpected resumes=%d valid=%v entries=%d", resumes, vdoc.IsValid(), len(vdoc.Entries()))
	}
	if string(vdoc.Unesc()) != "a\nb\tc" {
		t.Fatalf("unexpected unesc=%q", string(vdoc.Unesc()))
	}
	if DiagEnabled {
		snap := ReadDiag()
		// With 0 prefix restarts, total lexed bytes cannot exceed len(src) + 1 batch slack.
		if snap.BytesLexed > uint64(len(src)+1024) {
			t.Fatalf("R2-G prefix rescan detected! BytesLexed=%d for len(src)=%d across %d resumes",
				snap.BytesLexed, len(src), resumes)
		}
	}
}
