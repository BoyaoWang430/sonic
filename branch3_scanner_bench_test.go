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

type b3ConfigSection struct {
	SectionName string            `json:"section_name"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	MaxWorkers  int               `json:"max_workers"`
	RatioLimit  float64           `json:"ratio_limit"`
	Hosts       []string          `json:"hosts"`
	Ports       []int             `json:"ports"`
	Labels      map[string]string `json:"labels"`
}

type b3NestedConfig struct {
	AppName     string            `json:"app_name"`
	Environment string            `json:"environment"`
	Version     int               `json:"version"`
	Sections    []b3ConfigSection `json:"sections"`
}

type b3MetricPoint struct {
	ID     int64   `json:"id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Z      float64 `json:"z"`
	Active bool    `json:"active"`
}

type b3FeedArticle struct {
	ArticleID   int64    `json:"article_id"`
	Title       string   `json:"title"`
	AuthorName  string   `json:"author_name"`
	Category    string   `json:"category"`
	PublishedAt string   `json:"published_at"`
	Summary     string   `json:"summary"`
	Tags        []string `json:"tags"`
	Pinned      bool     `json:"pinned"`
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

func makeBranch3Inputs() (string, string, string, string) {
	rpc := b3RPCMessage{
		ServiceName:   "checkout-orchestrator",
		EndpointPath:  "/api/v2/orders/confirm_payment",
		RegionCluster: "ap-southeast-primary",
		CallerTeam:    "commerce-platform-core",
		ProtocolVer:   "http2-json-v2",
		TimestampText: "Wed Nov 11 16:30:00 +0000 2026",
		StatusLabel:   "COMPLETED_OK",
		TraceParent:   "trace-span-parent-identifier-01",
		SpanCategory:  "rpc_inbound_handler",
		ClientHost:    "edge-ingress-node-042.internal",
		ServerHost:    "order-worker-node-118.internal",
		RouteGroup:    "production-default-pool",
		RequestID:     900123456789,
		TenantID:      4200199,
		ShardIndex:    16,
		RetryAttempt:  0,
		PayloadBytes:  4096,
		TimeoutMillis: 1500,
		LatencyMillis: 3.8425,
		CpuUsageRatio: 0.421875,
		SampleWeight:  1.0,
		CacheHit:      true,
		CompressionOn: false,
		CanaryTraffic: false,
	}
	rpcBytes, _ := json.Marshal(rpc)

	cfg := b3NestedConfig{
		AppName:     "realtime-stream-indexer",
		Environment: "production",
		Version:     20261111,
		Sections:    make([]b3ConfigSection, 8),
	}
	for i := range cfg.Sections {
		cfg.Sections[i] = b3ConfigSection{
			SectionName: fmt.Sprintf("pipeline_stage_%02d", i),
			Description: " processes incoming document batches and updates inverted index shards ",
			Enabled:     i%2 == 0,
			MaxWorkers:  16 + i*4,
			RatioLimit:  0.125 * float64(i+1),
			Hosts:       []string{"indexer-node-alpha.local", "indexer-node-beta.local", "indexer-node-gamma.local"},
			Ports:       []int{8080 + i, 9090 + i, 10090 + i},
			Labels: map[string]string{
				"tier":   "storage",
				"zone":   fmt.Sprintf("zone-%d", i%3),
				"policy": "strict-quorum",
			},
		}
	}
	prettyCfgBytes, _ := json.MarshalIndent(cfg, "", "  ")

	points := make([]b3MetricPoint, 128)
	for i := range points {
		points[i] = b3MetricPoint{
			ID:     int64(100000 + i),
			X:      float64(i)*1.25 + 0.125,
			Y:      -float64(i)*0.75 - 0.5,
			Z:      float64(i%17) * 3.125,
			Active: i%3 != 0,
		}
	}
	pointsBytes, _ := json.Marshal(points)

	feed := make([]b3FeedArticle, 32)
	for i := range feed {
		feed[i] = b3FeedArticle{
			ArticleID:   int64(500000 + i),
			Title:       fmt.Sprintf("Understanding Vectorized JSON Parsing Stage %02d", i),
			AuthorName:  "Engineering Performance Team",
			Category:    "systems-architecture-benchmarks",
			PublishedAt: "Wed Nov 11 16:45:12 +0000 2026",
			Summary:     "Line one of summary\nLine two with \"quoted\" segment\tand clean ASCII text for benchmarking.",
			Tags:        []string{"json", "simd", "scanner", "compiler", "latency"},
			Pinned:      i == 0,
		}
	}
	feedBytes, _ := json.Marshal(feed)

	return string(rpcBytes), string(prettyCfgBytes), string(pointsBytes), string(feedBytes)
}

func BenchmarkBranch3Scanner(b *testing.B) {
	rpcStr, prettyCfgStr, pointsStr, feedStr := makeBranch3Inputs()

	b.Run("MinifiedStruct24/Default", func(b *testing.B) {
		b.SetBytes(int64(len(rpcStr)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var out b3RPCMessage
			if err := sonic.ConfigDefault.UnmarshalFromString(rpcStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("MinifiedStruct24/Fastest", func(b *testing.B) {
		b.SetBytes(int64(len(rpcStr)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var out b3RPCMessage
			if err := sonic.ConfigFastest.UnmarshalFromString(rpcStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("PrettyNestedConfig/Default", func(b *testing.B) {
		b.SetBytes(int64(len(prettyCfgStr)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var out b3NestedConfig
			if err := sonic.ConfigDefault.UnmarshalFromString(prettyCfgStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("PrettyNestedConfig/Fastest", func(b *testing.B) {
		b.SetBytes(int64(len(prettyCfgStr)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var out b3NestedConfig
			if err := sonic.ConfigFastest.UnmarshalFromString(prettyCfgStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("MinifiedFloatIntArray/Fastest", func(b *testing.B) {
		b.SetBytes(int64(len(pointsStr)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var out []b3MetricPoint
			if err := sonic.ConfigFastest.UnmarshalFromString(pointsStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("StringHeavyFeed/Fastest", func(b *testing.B) {
		b.SetBytes(int64(len(feedStr)))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var out []b3FeedArticle
			if err := sonic.ConfigFastest.UnmarshalFromString(feedStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}
