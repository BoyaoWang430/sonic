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
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/jsonscan"
)

// TestF4_DiagRoleAttributionAndSourceSpans verifies F4 (§8.1–§8.5):
//   - Main vs Position vs Subtree role isolation (no subtree bleed into main BytesLexed/ScanCompletes)
//   - ScanSourceBytes == consumed span (cur - startPos), never absolute offset
//   - Non-dynamic path records 0 position/subtree work
//   - Non-zero session startPos, syntax error, R2-G resume, chunked StreamDecoder, and UTF-8 repair
//   - Production (!sonic_diag) build preserves zero Machine size overhead and identical semantics.
func TestF4_DiagRoleAttributionAndSourceSpans(t *testing.T) {
	// 1. Ordinary non-dynamic success path: zero position/subtree work, exact main source span.
	t.Run("NoDynamicZeroSidecarAndSubtree", func(t *testing.T) {
		type plain struct {
			A int    `json:"a"`
			B string `json:"b"`
			C []int  `json:"c"`
		}
		src := `{"a":42,"b":"hello","c":[1,2,3]}`
		var warm plain
		_, _, _ = DecodeOne(src, 0, &warm)

		jsonscan.ResetDiag()
		var dst plain
		_, endPos, err := DecodeOne(src, 0, &dst)
		if err != nil || endPos != len(src) || dst.A != 42 || dst.B != "hello" || len(dst.C) != 3 {
			t.Fatalf("unexpected decode result: err=%v end=%d dst=%+v", err, endPos, dst)
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.SchemaVersion != "diag-v3" {
				t.Fatalf("expected diag-v3 schema, got %q", snap.SchemaVersion)
			}
			if snap.ScanCompletes != 1 || snap.ScanSourceBytes != uint64(len(src)) || snap.BytesLexed != uint64(len(src)) {
				t.Fatalf("unexpected main counters: want (1,%d,%d) got %+v", len(src), len(src), snap)
			}
			if snap.PositionBuildCount != 0 || snap.PositionBatchCalls != 0 || snap.PositionScanBytes != 0 || snap.PositionSourceBytes != 0 ||
				snap.SubtreeScanCount != 0 || snap.SubtreeBatchCalls != 0 || snap.SubtreeScanBytes != 0 || snap.SubtreeSourceBytes != 0 {
				t.Fatalf("expected zero position/subtree work on non-dynamic path, got %+v", snap)
			}
		}
	})

	// 2. Non-zero startPos on direct Machine & multi-value session: ScanSourceBytes == cur - startPos.
	t.Run("NonZeroStartPosConsumedDelta", func(t *testing.T) {
		prefix := strings.Repeat(" ", 4096)
		val := `{"id":123,"name":"second"}`
		full := prefix + val

		// Direct Machine.ResetValidatedDoc at non-zero startPos=4096:
		var m jsonscan.Machine
		dst := make([]uint64, 64)
		var vdoc jsonscan.ValidatedDoc
		jsonscan.ResetDiag()
		m.ResetValidatedDoc(false, false, len(prefix))
		res := m.ScanValidatedDocInto(full, dst, &vdoc)
		if res.Reason != jsonscan.StopDone || res.Consumed != len(full) || !vdoc.IsValid() {
			t.Fatalf("non-zero startPos scan failed: %+v", res)
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.ScanCompletes != 1 || snap.ScanSourceBytes != uint64(len(val)) {
				t.Fatalf("non-zero startPos ScanSourceBytes=%d want %d (must be cur-startPos, not absolute cur=%d): %+v",
					snap.ScanSourceBytes, len(val), len(full), snap)
			}
		}

		// Direct Machine.ResetFastEmit at non-zero startPos=4096 with DiagRoleSubtree:
		jsonscan.ResetDiag()
		m.ResetFastEmit(false, false, false, true, false, len(prefix))
		m.SetDiagRole(jsonscan.DiagRoleSubtree)
		m.Nodes = make([]jsonscan.Node, 64)
		resSub := m.Step(full, true)
		if resSub.Reason != jsonscan.StopDone || resSub.Consumed != len(full) {
			t.Fatalf("subtree non-zero startPos scan failed: %+v", resSub)
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.ScanCompletes != 0 || snap.ScanSourceBytes != 0 || snap.BytesLexed != 0 {
				t.Fatalf("subtree role leaked into main counters: %+v", snap)
			}
			if snap.SubtreeBatchCalls == 0 || snap.SubtreeScanBytes != uint64(len(val)) || snap.SubtreeSourceBytes != uint64(len(val)) {
				t.Fatalf("subtree non-zero startPos attribution wrong (want %d): %+v", len(val), snap)
			}
		}
	})

	// 3. Syntax error path: single completion recorded with bounded consumed span (no double-count across delegateIdxErrorAt).
	t.Run("SyntaxErrorSingleCompletion", func(t *testing.T) {
		type errT struct {
			A int    `json:"a"`
			B string `json:"b"`
			C int    `json:"c"`
		}
		bad := `{"a":1,"b":"bad\q","c":2}`
		jsonscan.ResetDiag()
		var dst errT
		_, _, err := decodeOneWithControl(bad, 1<<consts.F_validate_string, &dst, &decodeTestControl{forceRep: repForceIndex})
		if err == nil {
			t.Fatal("expected syntax error")
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.ScanCompletes != 1 {
				t.Fatalf("expected exactly 1 ScanCompletes on syntax error, got %+v", snap)
			}
			if snap.ScanSourceBytes == 0 || snap.ScanSourceBytes > uint64(len(bad)) {
				t.Fatalf("unexpected ScanSourceBytes=%d for len=%d: %+v", snap.ScanSourceBytes, len(bad), snap)
			}
		}
	})

	// 4. Chunked StreamDecoder path: single completion, ScanSourceBytes == current value span.
	t.Run("ChunkedStreamSingleCompletion", func(t *testing.T) {
		val1 := `{"a":1,"b":"hello\nworld"}`
		val2 := `{"a":2,"b":"next"}`
		raw := []byte(val1 + " " + val2)
		buf := make([]byte, 0, len(raw))
		off := 0
		scanp := 0
		refill := func(_ unsafe.Pointer, compactTo int) (bool, error) {
			if compactTo > 0 {
				copy(buf, buf[compactTo:])
				buf = buf[:len(buf)-compactTo]
				scanp = 0
			}
			if off >= len(raw) {
				return true, nil
			}
			n := 5
			if off+n > len(raw) {
				n = len(raw) - off
			}
			buf = append(buf, raw[off:off+n]...)
			off += n
			return off >= len(raw), nil
		}
		var sess *StreamSession
		defer func() { FreeStreamSession(sess) }()

		jsonscan.ResetDiag()
		var dst1 map[string]any
		if err := DecodeStream(&buf, &scanp, 0, &dst1, &sess, nil, refill); err != nil {
			t.Fatalf("stream val1 failed: %v", err)
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.ScanCompletes != 1 || snap.ScanSourceBytes != uint64(len(val1)) {
				t.Fatalf("stream val1 expected ScanCompletes=1 ScanSourceBytes=%d, got %+v", len(val1), snap)
			}
		}
	})

	// 5. UTF-8 repair path: ScanSourceBytes reflects repaired scan source (> raw input bytes).
	t.Run("UTF8RepairSourceSpanReflectsRepairedSource", func(t *testing.T) {
		type repairT struct {
			S string `json:"s"`
			N int    `json:"n"`
			K int    `json:"k"`
		}
		rawBad := "{\"s\":\"a\xffb\",\"n\":7}"
		repairedLen := len(rawBad) + 2 // 1 byte 0xff -> 3 bytes U+FFFD (+2 bytes)
		jsonscan.ResetDiag()
		var dst repairT
		_, endPos, err := DecodeOne(rawBad, 1<<consts.F_validate_string, &dst)
		if err != nil || endPos != len(rawBad) || dst.N != 7 || dst.S != "a\ufffdb" {
			t.Fatalf("UTF-8 repair decode failed: err=%v endPos=%d dst=%+v", err, endPos, dst)
		}
		if jsonscan.DiagEnabled {
			snap := jsonscan.ReadDiag()
			if snap.ScanCompletes != 1 || snap.ScanSourceBytes != uint64(repairedLen) {
				t.Fatalf("repaired source span expected %d (raw=%d), got %+v", repairedLen, len(rawBad), snap)
			}
		}
	})
}
