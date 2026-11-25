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

package sonic_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"testing"

	"github.com/bytedance/sonic"
)

type b2NamedInt64 int64
type b2NamedPtrInt64 *int64

type b2CustomInt64 int64

func (p *b2CustomInt64) UnmarshalJSON(b []byte) error {
	var v int64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = b2CustomInt64(v + 1000)
	return nil
}

type b2NestedMeta struct {
	TraceID   *string  `json:"trace_id"`
	SpanID    *uint64  `json:"span_id"`
	Sampled   *bool    `json:"sampled"`
	Score     *float32 `json:"score"`
	Weight    float32  `json:"weight"`
	ExtraInfo any      `json:"extra_info"`
}

type b2IDLRecord struct {
	ReqID       *string         `json:"req_id"`
	ServiceName *string         `json:"service_name"`
	Caller      *string         `json:"caller"`
	Region      *string         `json:"region"`
	Cluster     *string         `json:"cluster"`
	Env         *string         `json:"env"`
	NoteEscaped *string         `json:"note_escaped"`
	EmptyStr    *string         `json:"empty_str"`
	UserID      *int64          `json:"user_id"`
	DeltaI64    *int64          `json:"delta_i64"`
	SeqInt      *int            `json:"seq_int"`
	CodeI32     *int32          `json:"code_i32"`
	NegI32      *int32          `json:"neg_i32"`
	HashU64     *uint64         `json:"hash_u64"`
	CountUint   *uint           `json:"count_uint"`
	FlagsU32    *uint32         `json:"flags_u32"`
	Active      *bool           `json:"active"`
	Cached      *bool           `json:"cached"`
	LatencyMs   *float64        `json:"latency_ms"`
	RatioF64    *float64        `json:"ratio_f64"`
	RatioF32    *float32        `json:"ratio_f32"`
	StrOptI64   *int64          `json:"str_opt_i64,string"`
	NamedScalar *b2NamedInt64   `json:"named_scalar"`
	NamedPtr    b2NamedPtrInt64 `json:"named_ptr"`
	CustomPtr   *b2CustomInt64  `json:"custom_ptr"`
	Meta        *b2NestedMeta   `json:"meta"`
	InlineMeta  b2NestedMeta    `json:"inline_meta"`
}

func TestBranch2FlatStruct(t *testing.T) {
	t.Run("StdlibParityAndInPlaceReuse", func(t *testing.T) {
		payload := `{
			"service_name": "checkout-gateway",
			"req_id": "req-99887766",
			"caller": "mobile-api",
			"region": "ap-northeast-1",
			"cluster": "prod-main-04",
			"env": "production",
			"note_escaped": "line1\nline2\t\"quoted\"",
			"empty_str": "",
			"user_id": 9007199254740993,
			"delta_i64": -9876543210,
			"seq_int": -42,
			"code_i32": 2147483647,
			"neg_i32": -2147483648,
			"hash_u64": 18446744073709551615,
			"count_uint": 123456,
			"flags_u32": 4294967295,
			"active": true,
			"cached": false,
			"latency_ms": 12.345,
			"ratio_f64": 100,
			"ratio_f32": 3.5,
			"str_opt_i64": "777",
			"named_scalar": 55,
			"named_ptr": 66,
			"custom_ptr": 77,
			"unknown_extra_long_field": {"nested": [1, 2, 3]},
			"meta": {
				"sampled": true,
				"span_id": 445566,
				"trace_id": "tr-abc-123",
				"score": 1.25,
				"weight": 2.5,
				"extra_info": 12345
			},
			"inline_meta": {
				"trace_id": "tr-inline",
				"span_id": 99,
				"sampled": false,
				"score": -4.5,
				"weight": 8.0,
				"extra_info": "ok"
			}
		}`

		var want, gotDefault, gotStd b2IDLRecord
		if err := json.Unmarshal([]byte(payload), &want); err != nil {
			t.Fatalf("stdlib unmarshal failed: %v", err)
		}
		if err := sonic.ConfigDefault.UnmarshalFromString(payload, &gotDefault); err != nil {
			t.Fatalf("ConfigDefault unmarshal failed: %v", err)
		}
		if err := sonic.ConfigStd.Unmarshal([]byte(payload), &gotStd); err != nil {
			t.Fatalf("ConfigStd unmarshal failed: %v", err)
		}
		if !reflect.DeepEqual(gotDefault, want) {
			t.Fatalf("ConfigDefault mismatch:\ngot:  %#v\nwant: %#v", gotDefault, want)
		}
		if !reflect.DeepEqual(gotStd, want) {
			t.Fatalf("ConfigStd mismatch:\ngot:  %#v\nwant: %#v", gotStd, want)
		}

		// Verify pre-populated pointers are overwritten in place when non-null, and nilled when null.
		existingStr := "old"
		existingI64 := int64(11)
		nullTargetI64 := int64(22)
		pre := b2IDLRecord{
			ReqID:    &existingStr,
			UserID:   &existingI64,
			DeltaI64: &nullTargetI64,
		}
		updateJSON := `{"req_id":"new-id","user_id":888,"delta_i64":null}`
		if err := sonic.UnmarshalString(updateJSON, &pre); err != nil {
			t.Fatalf("in-place update failed: %v", err)
		}
		if pre.ReqID != &existingStr || existingStr != "new-id" {
			t.Fatalf("expected in-place *string overwrite, got ptr=%p (want %p) val=%q", pre.ReqID, &existingStr, existingStr)
		}
		if pre.UserID != &existingI64 || existingI64 != 888 {
			t.Fatalf("expected in-place *int64 overwrite, got ptr=%p (want %p) val=%d", pre.UserID, &existingI64, existingI64)
		}
		if pre.DeltaI64 != nil {
			t.Fatalf("expected null to clear *int64 to nil, got %v", *pre.DeltaI64)
		}
	})

	t.Run("PointerSlabIsolationAcrossPooledCalls", func(t *testing.T) {
		const N = 200
		records := make([]b2IDLRecord, N)
		var streamBuf bytes.Buffer
		for i := 0; i < N; i++ {
			doc := fmt.Sprintf(`{"req_id":"r-%d","service_name":"svc-%d","user_id":%d,"code_i32":%d,"active":%t,"latency_ms":%d.5}`,
				i, i, int64(100000+i), int32(i*3), i%2 == 0, i)
			if err := sonic.Unmarshal([]byte(doc), &records[i]); err != nil {
				t.Fatalf("iter %d unmarshal err: %v", i, err)
			}
			streamBuf.WriteString(doc)
			streamBuf.WriteByte('\n')
		}
		runtime.GC()
		for i := 0; i < N; i++ {
			wantReq := fmt.Sprintf("r-%d", i)
			wantSvc := fmt.Sprintf("svc-%d", i)
			if records[i].ReqID == nil || *records[i].ReqID != wantReq ||
				records[i].ServiceName == nil || *records[i].ServiceName != wantSvc ||
				records[i].UserID == nil || *records[i].UserID != int64(100000+i) ||
				records[i].CodeI32 == nil || *records[i].CodeI32 != int32(i*3) ||
				records[i].Active == nil || *records[i].Active != (i%2 == 0) ||
				records[i].LatencyMs == nil || *records[i].LatencyMs != float64(i)+0.5 {
				t.Fatalf("iter %d corrupted after pooled reuse + GC: %+v", i, records[i])
			}
		}

		dec := sonic.ConfigDefault.NewDecoder(&streamBuf)
		streamRecords := make([]b2IDLRecord, N)
		for i := 0; i < N; i++ {
			if err := dec.Decode(&streamRecords[i]); err != nil {
				t.Fatalf("stream iter %d err: %v", i, err)
			}
		}
		runtime.GC()
		for i := 0; i < N; i++ {
			if *streamRecords[i].ReqID != fmt.Sprintf("r-%d", i) || *streamRecords[i].UserID != int64(100000+i) {
				t.Fatalf("stream iter %d corrupted: %+v", i, streamRecords[i])
			}
		}
	})

	t.Run("CaseFoldingUnicodeAndMismatchErrors", func(t *testing.T) {
		type foldStruct struct {
			KelvinField     int     `json:"kelvin_field"`
			LongMiddleAlpha *string `json:"long_middle_alpha_name"`
			LongMiddleBeta  *string `json:"long_middle_beta__name"`
		}
		// \u212A is Unicode Kelvin sign 'K' (3 UTF-8 bytes), which folds to ASCII 'k' (1 byte).
		raw := "{\"\\u212Aelvin_field\": 42, \"LONG_MIDDLE_BETA__NAME\": \"hit\", \"unknown_twenty_two_len\": 1}"
		var want, got foldStruct
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatalf("stdlib fold err: %v", err)
		}
		if err := sonic.UnmarshalString(raw, &got); err != nil {
			t.Fatalf("sonic fold err: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fold mismatch: got=%#v want=%#v", got, want)
		}

		// Overflow & type mismatch checks on specialized pointer and float32 fields
		badInputs := []string{
			`{"code_i32": 3000000000}`,
			`{"flags_u32": 5000000000}`,
			fmt.Sprintf(`{"ratio_f32": %g}`, math.MaxFloat64),
			`{"user_id": "not-a-number"}`,
			`{"req_id": 12345}`,
		}
		for _, bad := range badInputs {
			var r b2IDLRecord
			if err := sonic.UnmarshalString(bad, &r); err == nil {
				t.Fatalf("expected error for %s, got nil", bad)
			}
		}
	})
}
