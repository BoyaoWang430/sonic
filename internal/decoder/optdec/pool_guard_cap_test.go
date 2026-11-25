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
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/jsonscan"
)

func buildIntArrayWithTotalNodes(totalNodes int) string {
	// "[1,1,...]" produces 1 KArray node + (totalNodes-1) KUint nodes = totalNodes nodes.
	if totalNodes <= 1 {
		return "[]"
	}
	elems := totalNodes - 1
	var sb strings.Builder
	sb.Grow(elems*2 + 2)
	sb.WriteByte('[')
	for i := 0; i < elems; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('1')
	}
	sb.WriteByte(']')
	return sb.String()
}

func parserRetainedScratchBytes(p *Parser) int {
	return physicalNodesCap(p.nodes)*nodeByteSize +
		cap(p.backup)*nodeByteSize +
		cap(p.unescBuf) +
		cap(p.idxEntries)*8 +
		cap(p.idxBackup)*8 +
		cap(p.idxSpans)*8
}

func TestR5_GuardPhysicalWritableAndPoolHardCap(t *testing.T) {
	// R5-T1: Guard critical node counts (cap-2, cap-1, cap) + materialization safety.
	t.Run("R5-T1_GuardCriticalNodeCountsAndMaterialization", func(t *testing.T) {
		const phys = 64
		for _, wantNodes := range []int{phys - 2, phys - 1, phys, phys + 1} {
			src := buildIntArrayWithTotalNodes(wantNodes)
			p := &Parser{
				Document: Document{
					Json:  src,
					nodes: make([]node, phys, phys),
				},
			}
			p.end = len(src)
			p.nbuf.init(p.nodes)
			if writableNodesCap(p.nodes) != phys-nodeGuardSlots {
				t.Fatalf("writableNodesCap got %d want %d", writableNodesCap(p.nodes), phys-nodeGuardSlots)
			}
			var got []any
			etp, vp, dec, _, err := unpackAndCompileRoot(&got)
			if err != nil {
				t.Fatal(err)
			}
			if code := p.scanDocumentInPlace(0, etp, nil); code != SONIC_OK {
				t.Fatalf("wantNodes=%d scan failed: %v", wantNodes, code)
			}
			if len(p.nodes) != wantNodes || cap(p.nodes) < wantNodes+nodeGuardSlots {
				t.Fatalf("wantNodes=%d guard violated: len=%d cap=%d", wantNodes, len(p.nodes), cap(p.nodes))
			}
			ctx := p.prepareDecodeContext(0, etp)
			if merr := dec.FromDom(vp, ctx.Root(), ctx); merr != nil {
				t.Fatalf("wantNodes=%d FromDom failed: %v", wantNodes, merr)
			}
			if len(got) != wantNodes-1 {
				t.Fatalf("wantNodes=%d got elems=%d want=%d", wantNodes, len(got), wantNodes-1)
			}
			p.reset()
			if physicalNodesCap(p.nodes) > maxPooledNodesCount || writableNodesCap(p.nodes) != physicalNodesCap(p.nodes)-nodeGuardSlots {
				t.Fatalf("after reset bad cap=%d", physicalNodesCap(p.nodes))
			}
		}

		// Also verify finalizeDocumentInPlace when nlen == maxPooledNodesCount directly:
		pEdge := &Parser{
			Document: Document{
				nodes: make([]node, maxPooledNodesCount, maxPooledNodesCount),
			},
		}
		pEdge.nbuf.nlen = maxPooledNodesCount
		pEdge.finalizeDocumentInPlace()
		if cap(pEdge.nodes) != maxPooledNodesCount+nodeGuardSlots {
			t.Fatalf("expected guard expansion to %d, got %d", maxPooledNodesCount+nodeGuardSlots, cap(pEdge.nodes))
		}
		if cap(pEdge.backup) != maxPooledNodesCount {
			t.Fatalf("expected p.backup to preserve %d pooled slice, got %d", maxPooledNodesCount, cap(pEdge.backup))
		}
		pEdge.reset()
		if physicalNodesCap(pEdge.nodes) != maxPooledNodesCount || pEdge.backup != nil {
			t.Fatalf("after reset expected exact %d pooled cap, got cap=%d backup=%d",
				maxPooledNodesCount, physicalNodesCap(pEdge.nodes), cap(pEdge.backup))
		}
	})

	// R5-T2 & R5-T3: 65535 / 65536 / 65537 node boundaries & same-instance small -> boundary -> huge -> small.
	t.Run("R5-T2_T3_Boundary65535To65537AndSameInstanceLifecycle", func(t *testing.T) {
		p := &Parser{
			Document: Document{
				nodes: make([]node, defaultNodesCount, defaultNodesCount),
			},
		}
		runOnSameParser := func(totalNodes int) {
			src := buildIntArrayWithTotalNodes(totalNodes)
			p.Json = src
			p.pos = 0
			p.end = len(src)
			p.nbuf.init(p.nodes)
			var dst any
			etp, vp, dec, _, err := unpackAndCompileRoot(&dst)
			if err != nil {
				t.Fatal(err)
			}
			if code := p.scanDocumentInPlace(0, etp, nil); code != SONIC_OK {
				t.Fatalf("totalNodes=%d scan failed: %v", totalNodes, code)
			}
			if len(p.nodes) != totalNodes || cap(p.nodes) < totalNodes+nodeGuardSlots {
				t.Fatalf("totalNodes=%d post-scan len=%d cap=%d", totalNodes, len(p.nodes), cap(p.nodes))
			}
			ctx := p.prepareDecodeContext(0, etp)
			if merr := dec.FromDom(vp, ctx.Root(), ctx); merr != nil {
				t.Fatalf("totalNodes=%d FromDom failed: %v", totalNodes, merr)
			}
			p.reset()
			if physicalNodesCap(p.nodes) > maxPooledNodesCount {
				t.Fatalf("totalNodes=%d breached pool hard cap: physicalCap=%d > %d",
					totalNodes, physicalNodesCap(p.nodes), maxPooledNodesCount)
			}
			if p.backup != nil {
				t.Fatalf("totalNodes=%d leaked non-nil backup after reset", totalNodes)
			}
		}

		// Small -> 65535 (fits exact 65536 physical w/ 1 guard) -> 65536 -> 65537 -> 150000 -> Small(16)
		for _, n := range []int{16, maxPooledNodesCount - 1, maxPooledNodesCount, maxPooledNodesCount + 1, 150000, 16} {
			runOnSameParser(n)
		}
		if physicalNodesCap(p.nodes) != maxPooledNodesCount {
			t.Fatalf("expected pooled cap to retain %d after lifecycle, got %d", maxPooledNodesCount, physicalNodesCap(p.nodes))
		}
	})

	// R5-T4: Artificial oversized state with backup == nil (and with corrupted oversized backup).
	t.Run("R5-T4_ResetIndependentDefenseWithoutBackup", func(t *testing.T) {
		p := &Parser{
			Document: Document{
				nodes: make([]node, 150004),
			},
		}
		p.unescBuf = make([]byte, maxPooledUnescCap+1024)
		p.idxEntries = make([]uint64, maxPooledIdxEntriesCount+1024)
		p.idxSpans = make([]jsonscan.ValidatedSourceSpan, maxPooledIdxEntriesCount+1024)
		p.reset()
		if physicalNodesCap(p.nodes) != defaultNodesCount {
			t.Fatalf("expected fallback to defaultNodesCount=%d when backup==nil, got %d", defaultNodesCount, physicalNodesCap(p.nodes))
		}
		if p.unescBuf != nil || p.idxEntries != nil || p.idxSpans != nil {
			t.Fatalf("expected oversized scratch buffers to be dropped: unesc=%d idx=%d spans=%d",
				cap(p.unescBuf), cap(p.idxEntries), cap(p.idxSpans))
		}

		// Also test corrupted oversized backup slices:
		p.nodes = make([]node, 200000)
		p.backup = make([]node, maxPooledNodesCount+1)
		p.idxEntries = make([]uint64, maxPooledIdxEntriesCount+500)
		p.idxBackup = make([]uint64, maxPooledIdxEntriesCount+1)
		p.reset()
		if physicalNodesCap(p.nodes) != defaultNodesCount || p.backup != nil || p.idxEntries != nil || p.idxBackup != nil {
			t.Fatalf("oversized backup escaped reset: nodesCap=%d idxCap=%d", physicalNodesCap(p.nodes), cap(p.idxEntries))
		}
	})

	// R5-T5: Syntax error / early return followed by reset drops all oversized scratch & side references.
	t.Run("R5-T5_ErrorEarlyReturnLeavesZeroSideRefs", func(t *testing.T) {
		badHuge := "[" + strings.Repeat("1,", 100000) + "x]"
		p := &Parser{
			Document: Document{
				Json:  badHuge,
				nodes: make([]node, defaultNodesCount, defaultNodesCount),
			},
		}
		p.end = len(badHuge)
		p.nbuf.init(p.nodes)
		code := p.scanDocumentInPlace(0, anyType, nil)
		if code == SONIC_OK {
			t.Fatal("expected syntax error on badHuge")
		}
		p.reset()
		if physicalNodesCap(p.nodes) > maxPooledNodesCount || p.Json != "" || p.jsMachine.Nodes != nil || p.ctx.Doc.nodes != nil || p.ctx.Doc.Json != "" {
			t.Fatalf("error reset leaked references or oversized nodes: cap=%d jsonLen=%d mNodes=%d",
				physicalNodesCap(p.nodes), len(p.Json), cap(p.jsMachine.Nodes))
		}

		// Same check on StreamSession:
		sess := getStreamSession()
		sess.beginComplete(0, true, badHuge, true)
		_, scode := sess.Step(badHuge, true)
		if scode == SONIC_OK {
			t.Fatal("expected stream error on badHuge")
		}
		sess.Reset()
		if physicalNodesCap(sess.p.nodes) > maxPooledNodesCount || sess.m.Nodes != nil || sess.p.backup != nil {
			t.Fatalf("StreamSession.Reset leaked oversized backing: cap=%d", physicalNodesCap(sess.p.nodes))
		}
		FreeStreamSession(sess)
	})

	// R5-T6 (T1): Alternating Index / Schema / Tape / oversized-Tape reuse on ONE explicit
	// injected *Parser instance (zero sync.Pool randomness!).
	t.Run("R5-T6_AlternatingIndexTapeSchemaReuse", func(t *testing.T) {
		type mixStruct struct {
			A int    `json:"a"`
			B string `json:"b"`
			C []int  `json:"c"`
		}
		largeSchemaSrc := `{"unknown":"` + strings.Repeat("u", 2048) + `","a":7,"b":"sch","c":[1,2]}`
		smallIdxSrc := `{"a":8,"b":"idx","c":[3,4,5]}`
		tapeAnySrc := `{"a":9,"b":"tape","c":[6]}`
		oversizedTapeSrc := `[` + strings.Repeat(`1,`, maxPooledNodesCount+1000) + `1]`

		fixedP := &Parser{
			Document: Document{
				nodes: make([]node, defaultNodesCount, defaultNodesCount),
			},
		}

		for round := 0; round < 10; round++ {
			var s1, s2 mixStruct
			var w1, w2 routeWitness
			if _, _, err := decodeOneWithControl(smallIdxSrc, 0, &s1, &decodeTestControl{pinnedParser: fixedP, witness: &w1}); err != nil || w1.IndexBinderEntered != 1 || s1.A != 8 {
				t.Fatalf("round %d index failed: err=%v w=%+v s1=%+v", round, err, w1, s1)
			}
			if physicalNodesCap(fixedP.nodes) > maxPooledNodesCount || fixedP.jsMachine.Nodes != nil || fixedP.jsMachine.ForceScalar() {
				t.Fatalf("round %d post-index pinned Parser leaked state: cap=%d", round, physicalNodesCap(fixedP.nodes))
			}

			if _, _, err := decodeOneWithControl(largeSchemaSrc, 0, &s2, &decodeTestControl{pinnedParser: fixedP, witness: &w2}); err != nil || w2.SchemaRouteSelected != 1 || s2.A != 7 {
				t.Fatalf("round %d schema failed: err=%v w=%+v s2=%+v", round, err, w2, s2)
			}
			if physicalNodesCap(fixedP.nodes) > maxPooledNodesCount || fixedP.jsMachine.Nodes != nil {
				t.Fatalf("round %d post-schema pinned Parser leaked state: cap=%d", round, physicalNodesCap(fixedP.nodes))
			}

			var a3 map[string]any
			var w3 routeWitness
			if _, _, err := decodeOneWithControl(tapeAnySrc, 0, &a3, &decodeTestControl{pinnedParser: fixedP, witness: &w3}); err != nil || w3.TapeRouteSelected != 1 || a3["b"] != "tape" {
				t.Fatalf("round %d tape failed: err=%v w=%+v a3=%+v", round, err, w3, a3)
			}

			if round == 3 {
				// Force oversized Tape growth (>maxPooledNodesCount) on the exact same pinned Parser,
				// verifying reset() restores bounded backing before subsequent Index/Schema rounds.
				var big []any
				var wBig routeWitness
				if _, _, err := decodeOneWithControl(oversizedTapeSrc, 0, &big, &decodeTestControl{pinnedParser: fixedP, forceRep: repForceTape, witness: &wBig}); err != nil || len(big) != maxPooledNodesCount+1001 {
					t.Fatalf("round %d oversized tape failed: err=%v len=%d", round, err, len(big))
				}
				if physicalNodesCap(fixedP.nodes) > maxPooledNodesCount || fixedP.backup != nil {
					t.Fatalf("round %d post-oversized pinned Parser exceeded cap bound: cap=%d backup=%v",
						round, physicalNodesCap(fixedP.nodes), fixedP.backup != nil)
				}
			}
		}
	})

	// R5-T7: Small capacities (0, 1, 2, 3) on Parser AND standalone jsonscan.Machine cap=0/1/2 unaffected.
	t.Run("R5-T7_SmallCapacitiesAndStandaloneJSONScanMachineContract", func(t *testing.T) {
		for _, capN := range []int{0, 1, 2, 3, 4} {
			p := &Parser{
				Document: Document{
					Json:  `{"a":[1,2],"b":"ok"}`,
					nodes: make([]node, capN),
				},
			}
			p.end = len(p.Json)
			p.nbuf.init(p.nodes)
			var dst map[string]any
			etp, vp, dec, _, err := unpackAndCompileRoot(&dst)
			if err != nil {
				t.Fatal(err)
			}
			if code := p.scanDocumentInPlace(0, etp, nil); code != SONIC_OK {
				t.Fatalf("capN=%d scan failed: %v", capN, code)
			}
			ctx := p.prepareDecodeContext(0, etp)
			if merr := dec.FromDom(vp, ctx.Root(), ctx); merr != nil {
				t.Fatalf("capN=%d FromDom failed: %v", capN, merr)
			}
			if !reflect.DeepEqual(dst, map[string]any{"a": []any{float64(1), float64(2)}, "b": "ok"}) {
				t.Fatalf("capN=%d unexpected dst: %#v", capN, dst)
			}
			p.reset()
		}

		// Standalone jsonscan.Machine bare Nodes cap=0 / cap=1 / cap=2 must NOT reserve a Parser guard slot:
		var m jsonscan.Machine
		m.Reset(jsonscan.StrictProfile(false, false, false, false, false, true), jsonscan.ModeEmit)
		m.Nodes = nil
		if res := m.Step(`1`, true); res.Reason != jsonscan.StopNeedOutput || res.Produced != 0 {
			t.Fatalf("standalone cap=0 expected StopNeedOutput/0, got %+v", res)
		}
		m.Reset(jsonscan.StrictProfile(false, false, false, false, false, true), jsonscan.ModeEmit)
		m.Nodes = make([]jsonscan.Node, 1)
		if res := m.Step(`1`, true); res.Reason != jsonscan.StopDone || res.Produced != 1 {
			t.Fatalf("standalone cap=1 single scalar expected StopDone/1, got %+v", res)
		}
		m.Reset(jsonscan.StrictProfile(false, false, false, false, false, true), jsonscan.ModeEmit)
		m.Nodes = make([]jsonscan.Node, 2)
		if res := m.Step(`[1]`, true); res.Reason != jsonscan.StopDone || res.Produced != 2 {
			t.Fatalf("standalone cap=2 [1] expected StopDone/2, got %+v", res)
		}
	})

	// R5-T8: Controlled same-instance retained memory bound + zero-alloc warm reuse proof.
	t.Run("R5-T8_ControlledRetainedMemoryAndWarmAllocs", func(t *testing.T) {
		p := parsePool.Get().(*Parser)
		defer p.free()

		// Drive huge Node Tape (>1 MiB), huge Index (>1 MiB), huge Spans (>1 MiB), and huge Unesc (>64 KiB)
		hugeTapeSrc := buildIntArrayWithTotalNodes(100000)
		p.Json = hugeTapeSrc
		p.end = len(hugeTapeSrc)
		p.nbuf.init(p.nodes)
		if code := p.scanDocumentInPlace(0, anyType, nil); code != SONIC_OK {
			t.Fatalf("hugeTapeSrc scan failed: %v", code)
		}
		p.unescBuf = make([]byte, maxPooledUnescCap+8192)
		p.idxBackup = make([]uint64, defaultIdxEntriesCount)
		p.idxEntries = make([]uint64, maxPooledIdxEntriesCount+8192)
		p.idxSpans = make([]jsonscan.ValidatedSourceSpan, maxPooledIdxEntriesCount+8192)
		p.reset()

		maxAllowedBytes := maxPooledNodesCount*nodeByteSize +
			maxPooledUnescCap +
			maxPooledIdxEntriesCount*8 +
			maxPooledIdxEntriesCount*8
		retained := parserRetainedScratchBytes(p)
		if retained > maxAllowedBytes {
			t.Fatalf("retained scratch %d B exceeds hard ceiling %d B", retained, maxAllowedBytes)
		}

		// Warm zero-alloc decode on static struct via DecodeOne:
		type smallDoc struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		var sd smallDoc
		var boxed any = &sd
		allocs := testing.AllocsPerRun(100, func() {
			_, _, err := DecodeOne(`{"id":1,"name":"sonic"}`, 0, boxed)
			if err != nil {
				t.Fatal(err)
			}
		})
		maxWarmAllocs := 0.0
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, st := range bi.Settings {
				if st.Key == "-gcflags" && strings.Contains(st.Value, "checkptr=2") {
					// Go's -d=checkptr=2 inserts runtime.checkptrArithmetic calls that
					// force stack-allocated pointer-carrying frames to escape (1 alloc/op).
					maxWarmAllocs = 1.0
					break
				}
			}
		}
		t.Logf("R5-T8: controlled Parser retained=%d B (ceiling=%d B), warm DecodeOne allocs/op=%.1f (max=%.1f)",
			retained, maxAllowedBytes, allocs, maxWarmAllocs)
		if allocs > maxWarmAllocs {
			t.Fatalf("expected <= %.0f allocs/op on warm DecodeOne static struct, got %.2f", maxWarmAllocs, allocs)
		}
	})
}
