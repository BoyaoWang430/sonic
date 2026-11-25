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
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/decoder/errors"
	"github.com/bytedance/sonic/internal/jsonscan"
)

type schemaHandoffChild struct {
	K int64  `json:"k"`
	V string `json:"v"`
}

type schemaHandoffTarget struct {
	ID       int64                `json:"id"`
	UID      int64                `json:"uid"`
	AuthorID int64                `json:"author_id"`
	Status   int32                `json:"status"`
	Score    float64              `json:"score"`
	Rate     float64              `json:"rate"`
	Enabled  bool                 `json:"enabled"`
	Verified bool                 `json:"verified"`
	Region   string               `json:"region"`
	Lang     string               `json:"lang"`
	Title    string               `json:"title"`
	Desc     string               `json:"desc"`
	Tag      string               `json:"tag"`
	Child    schemaHandoffChild   `json:"child"`
	Items    []schemaHandoffChild `json:"items"`
}

// TestSchemaWindowHandoffDecodeContracts verifies end-to-end DecodeOne parity
// (final struct values, exact error type/offset/consumed cursor, DisallowUnknownFields)
// under real UseNumber+zero-copy options against encoding/json and forced-Tape,
// and asserts SchemaRouteHits + LexBatch window bounds under -tags=sonic_diag.
func TestSchemaWindowHandoffDecodeContracts(t *testing.T) {
	opts := uint64(1 << _F_use_number)
	optsDis := opts | (1 << _F_disable_unknown)

	knownPairs := []string{
		`"id":7349812345678901234`,
		`"uid":91827364551234`,
		`"author_id":66554433221100`,
		`"status":2`,
		`"score":98.625`,
		`"rate":0.875`,
		`"enabled":true`,
		`"verified":false`,
		`"region":"cn-north-1"`,
		`"lang":"zh-CN"`,
		`"title":"schema_window_handoff_title"`,
		`"desc":"` + strings.Repeat("description_segment_0123456789 ", 10) + `"`,
		`"tag":"feed_v3"`,
		`"child":{"k":42,"v":"child_ok"}`,
		`"items":[{"k":1,"v":"i1"},{"k":2,"v":"i2"},{"k":3,"v":"i3"}]`,
	}

	makeUnk := func(i int) string {
		switch i % 4 {
		case 0:
			return fmt.Sprintf(`"_u%02d":%d`, i, 1000+i*13)
		case 1:
			return fmt.Sprintf(`"_u%02d":"val_%02d"`, i, i)
		case 2:
			return fmt.Sprintf(`"_u%02d":{"a":%d,"b":"x%d"}`, i, i, i%10)
		default:
			return fmt.Sprintf(`"_u%02d":[%d,%d]`, i, i, i+1)
		}
	}

	var interParts []string
	u := 0
	for k := 0; k < len(knownPairs); k++ {
		interParts = append(interParts, knownPairs[k])
		n := 5
		if k < 5 {
			n = 6
		}
		for j := 0; j < n; j++ {
			interParts = append(interParts, makeUnk(u))
			u++
		}
	}
	var clustParts []string
	clustParts = append(clustParts, knownPairs[:7]...)
	for i := 0; i < 80; i++ {
		clustParts = append(clustParts, makeUnk(i))
	}
	clustParts = append(clustParts, knownPairs[7:]...)

	// Unknown field immediately followed by known nested struct & slice containers
	// (their child fields/elements consume postUnknownValueCooldown in stepGeneralMode).
	nestedAfterUnk := `{"id":1,"_u_pre":{"skip":[1,2,3]},"child":{"k":77,"v":"nested_ok"},"items":[{"k":1,"v":"a"},{"k":2,"v":"b"},{"k":3,"v":"c"}],"uid":2,"author_id":3,"status":4,"score":1.25,"rate":0.5,"enabled":true,"verified":false,"region":"us-west","lang":"en-US","title":"after_nested","desc":"` + strings.Repeat("d", 320) + `","tag":"ok"}`

	cases := []struct {
		name          string
		doc           string
		maxBatchCalls uint64
		maxLexExtra   uint64
	}{
		{"Clean_0Unk", "{" + strings.Join(knownPairs, ",") + "}", 2, 64},
		{"Interleaved_15K_80U", "{" + strings.Join(interParts, ",") + "}", 6, 128},
		{"Clustered_15K_80U", "{" + strings.Join(clustParts, ",") + "}", 6, 128},
		{"UnknownFollowedByNested", nestedAfterUnk, 4, 128},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wantStd schemaHandoffTarget
			decStd := json.NewDecoder(strings.NewReader(tc.doc))
			decStd.UseNumber()
			if err := decStd.Decode(&wantStd); err != nil {
				t.Fatalf("stdlib decode failed: %v", err)
			}

			var gotTape schemaHandoffTarget
			_, tapePos, tapeErr := decodeOneWithControl(tc.doc, opts, &gotTape, &decodeTestControl{forceRep: repForceTape})
			if tapeErr != nil {
				t.Fatalf("forced-tape decode failed: %v", tapeErr)
			}

			// Warm up once before diag check.
			var warm schemaHandoffTarget
			if _, _, err := DecodeOne(tc.doc, opts, &warm); err != nil {
				t.Fatalf("warm DecodeOne failed: %v", err)
			}

			if jsonscan.DiagEnabled {
				jsonscan.ResetDiag()
			}
			var gotAuto schemaHandoffTarget
			_, autoPos, autoErr := DecodeOne(tc.doc, opts, &gotAuto)
			if autoErr != nil {
				t.Fatalf("DecodeOne failed: %v", autoErr)
			}
			if autoPos != tapePos {
				t.Fatalf("consumed pos mismatch: auto=%d tape=%d", autoPos, tapePos)
			}
			if !reflect.DeepEqual(gotAuto, wantStd) || !reflect.DeepEqual(gotAuto, gotTape) {
				t.Fatalf("decoded value mismatch:\n  auto=%+v\n  tape=%+v\n  std =%+v", gotAuto, gotTape, wantStd)
			}
			if jsonscan.DiagEnabled {
				s := jsonscan.ReadDiag()
				if s.SchemaRouteHits != 1 || s.IndexRouteHits != 0 || s.TapeRouteHits != 0 {
					t.Fatalf("expected exclusive Schema route, got schema=%d index=%d tape=%d",
						s.SchemaRouteHits, s.IndexRouteHits, s.TapeRouteHits)
				}
				if s.BytesLexed > uint64(len(tc.doc))+tc.maxLexExtra || s.BatchCalls > tc.maxBatchCalls {
					t.Fatalf("LexBatch window regression: len=%d BytesLexed=%d (max %d) BatchCalls=%d (max %d)",
						len(tc.doc), s.BytesLexed, uint64(len(tc.doc))+tc.maxLexExtra, s.BatchCalls, tc.maxBatchCalls)
				}
			}
		})
	}

	// Error parity (type, Pos, Code, consumed cursor) against forced-Tape for
	// DisallowUnknownFields and malformed unknown values across 64B/1024B alignments.
	errCases := []struct {
		name string
		doc  string
		flag uint64
	}{
		{"disallow_unknown", nestedAfterUnk, optsDis},
		{"bad_unknown_num_suffix_64B", `{"desc":"` + strings.Repeat("p", 48) + `","_bad":123+4,"title":"after","region":"r","lang":"l","tag":"t","id":1,"uid":2,"author_id":3,"status":1,"score":1.5,"rate":0.5,"enabled":true,"verified":false,"child":{"k":1,"v":"a"},"items":[]}`, opts},
		{"bad_unknown_missing_comma_1024B", `{"desc":"` + strings.Repeat("p", 1008) + `","_bad":{"a":1 "b":2},"title":"after","region":"r","lang":"l","tag":"t","id":1,"uid":2,"author_id":3,"status":1,"score":1.5,"rate":0.5,"enabled":true,"verified":false,"child":{"k":1,"v":"a"},"items":[]}`, opts},
		{"bad_unknown_eof_1024B", `{"desc":"` + strings.Repeat("p", 1006) + `","_bad":{"a":[1,2`, opts},
	}
	for _, ec := range errCases {
		t.Run(ec.name, func(t *testing.T) {
			var autoDst, tapeDst schemaHandoffTarget
			_, autoPos, autoErr := DecodeOne(ec.doc, ec.flag, &autoDst)
			_, tapePos, tapeErr := decodeOneWithControl(ec.doc, ec.flag, &tapeDst, &decodeTestControl{forceRep: repForceTape})
			if autoErr == nil || tapeErr == nil {
				t.Fatalf("expected error on both paths: autoErr=%v tapeErr=%v", autoErr, tapeErr)
			}
			if reflect.TypeOf(autoErr) != reflect.TypeOf(tapeErr) {
				t.Fatalf("error type mismatch: auto=%T (%v) tape=%T (%v)", autoErr, autoErr, tapeErr, tapeErr)
			}
			if seAuto, ok := autoErr.(errors.SyntaxError); ok {
				seTape := tapeErr.(errors.SyntaxError)
				if seAuto.Pos != seTape.Pos || seAuto.Code != seTape.Code {
					t.Fatalf("SyntaxError mismatch: auto=(pos=%d,code=%v) tape=(pos=%d,code=%v)",
						seAuto.Pos, seAuto.Code, seTape.Pos, seTape.Code)
				}
			}
			if autoPos != tapePos {
				t.Fatalf("error consumed pos mismatch: auto=%d tape=%d", autoPos, tapePos)
			}
		})
	}
}
