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
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

type b3RPCMessage struct {
	ServiceName   string  `json:"service_name"`
	EndpointPath  string  `json:"endpoint_path"`
	RegionCluster string  `json:"region_cluster"`
	CallerTeam    string  `json:"caller_team"`
	ProtocolVer   string  `json:"protocol_ver"`
	TimestampText string  `json:"timestamp_text"`
	StatusLabel   string  `json:"status_label"`
	TraceParent   string  `json:"trace_parent"`
	SpanCategory  string  `json:"span_category"`
	ClientHost    string  `json:"client_host"`
	ServerHost    string  `json:"server_host"`
	RouteGroup    string  `json:"route_group"`
	RequestID     int64   `json:"request_id"`
	TenantID      int64   `json:"tenant_id"`
	ShardIndex    int     `json:"shard_index"`
	RetryAttempt  int     `json:"retry_attempt"`
	PayloadBytes  int     `json:"payload_bytes"`
	TimeoutMillis int     `json:"timeout_millis"`
	LatencyMillis float64 `json:"latency_millis"`
	CpuUsageRatio float64 `json:"cpu_usage_ratio"`
	SampleWeight  float64 `json:"sample_weight"`
	CacheHit      bool    `json:"cache_hit"`
	CompressionOn bool    `json:"compression_on"`
	CanaryTraffic bool    `json:"canary_traffic"`
}

func TestBranch3ScannerParity(t *testing.T) {
	lengths := []int{0, 1, 7, 8, 15, 16, 17, 29, 30, 31, 32, 33, 63, 64, 129}
	for _, n := range lengths {
		plain := strings.Repeat("a", n)
		escaped := strings.Repeat("b", n/2) + `\n\"\\\t` + strings.Repeat("c", n-n/2)
		doc := fmt.Sprintf(`{"plain":%q,"escaped":"%s","num":-12345.675,"u64":18446744073709551615,"negbig":-9223372036854775809,"arr":[1, 2, 3, true, false, null]}`, plain, escaped)

		var want, gotDefault, gotFast, gotStd map[string]any
		if err := json.Unmarshal([]byte(doc), &want); err != nil {
			t.Fatalf("len=%d stdlib err: %v", n, err)
		}
		if err := sonic.ConfigDefault.UnmarshalFromString(doc, &gotDefault); err != nil {
			t.Fatalf("len=%d ConfigDefault err: %v", n, err)
		}
		if err := sonic.ConfigFastest.UnmarshalFromString(doc, &gotFast); err != nil {
			t.Fatalf("len=%d ConfigFastest err: %v", n, err)
		}
		if err := sonic.ConfigStd.UnmarshalFromString(doc, &gotStd); err != nil {
			t.Fatalf("len=%d ConfigStd err: %v", n, err)
		}
		if !reflect.DeepEqual(gotDefault, want) || !reflect.DeepEqual(gotFast, want) || !reflect.DeepEqual(gotStd, want) {
			t.Fatalf("len=%d mismatch:\nwant=%#v\ndef=%#v\nfast=%#v\nstd=%#v", n, want, gotDefault, gotFast, gotStd)
		}
	}

	// Whitespace variations before/after ':', ',', '{}', '[]', and escaped keys.
	weirdWS := "{\n\t \"service_name\" \t\r\n : \t\r\n \"order-gateway\" ,\n  \"request_id\" : 987654321 ,\n  \"latency_millis\":  12.375 ,\n  \"cache_hit\" : true ,\n  \"canary_traffic\":false\n}"
	var wantMsg, gotMsg b3RPCMessage
	if err := json.Unmarshal([]byte(weirdWS), &wantMsg); err != nil {
		t.Fatalf("weirdWS stdlib err: %v", err)
	}
	if err := sonic.UnmarshalString(weirdWS, &gotMsg); err != nil {
		t.Fatalf("weirdWS sonic err: %v", err)
	}
	if gotMsg != wantMsg {
		t.Fatalf("weirdWS mismatch: got=%+v want=%+v", gotMsg, wantMsg)
	}

	// Escaped keys and Unicode surrogates.
	escDoc := `{"k\n1":"val\"one","u\u0041":"\u4e2d\u6587\ud83d\ude00end","empty_obj":{},"empty_arr":[],"space_obj":{   },"space_arr":[   ]}`
	var wantEsc, gotEsc map[string]any
	if err := json.Unmarshal([]byte(escDoc), &wantEsc); err != nil {
		t.Fatalf("escDoc stdlib err: %v", err)
	}
	if err := sonic.UnmarshalString(escDoc, &gotEsc); err != nil {
		t.Fatalf("escDoc sonic err: %v", err)
	}
	if !reflect.DeepEqual(gotEsc, wantEsc) {
		t.Fatalf("escDoc mismatch: got=%#v want=%#v", gotEsc, wantEsc)
	}

	// Syntax error cases (general).
	badInputs := []string{
		`{"a" 1}`,
		`{"a":1 "b":2}`,
		`[1 2]`,
		`{"a":"unterminated`,
		`{"a":tru}`,
		`{"a":01}`,
	}
	for _, bad := range badInputs {
		var v map[string]any
		if err := sonic.ConfigDefault.UnmarshalFromString(bad, &v); err == nil {
			t.Fatalf("expected error for %q, got nil", bad)
		}
	}

	// Control characters: rejected under ConfigStd (ValidateString=true), allowed under ConfigDefault.
	ctrlInputs := []string{
		"{\"a\":\"raw\x01ctrl\"}",
		"{\"a\":\"\\nraw\x01\\nesc\"}",
		"{\"a\":\"\x0101234567890123456789012345678901\\nesc\"}",
	}
	for _, rawCtrl := range ctrlInputs {
		var v map[string]any
		if err := sonic.ConfigStd.UnmarshalFromString(rawCtrl, &v); err == nil {
			t.Fatalf("expected ConfigStd error for %q, got nil", rawCtrl)
		}
		if err := sonic.ConfigDefault.UnmarshalFromString(rawCtrl, &v); err != nil {
			t.Fatalf("unexpected ConfigDefault error for %q: %v", rawCtrl, err)
		}
	}
}
