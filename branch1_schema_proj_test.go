package sonic_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/testdata"
)

type sparseTwitterFeed struct {
	Statuses []struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
		User struct {
			ID         int64  `json:"id"`
			ScreenName string `json:"screen_name"`
		} `json:"user"`
		RetweetCount int `json:"retweet_count"`
	} `json:"statuses"`
	SearchMetadata struct {
		Count int     `json:"count"`
		MaxID int64   `json:"max_id"`
		Time  float64 `json:"completed_in"`
	} `json:"search_metadata"`
}

type rawEnvelopeStd struct {
	Code    int             `json:"code"`
	Msg     string          `json:"msg"`
	TraceID string          `json:"trace_id"`
	Data    json.RawMessage `json:"data"`
}

type rawEnvelopeNoCopy struct {
	Code    int                    `json:"code"`
	Msg     string                 `json:"msg"`
	TraceID string                 `json:"trace_id"`
	Data    sonic.NoCopyRawMessage `json:"data"`
}

type wideProjectionTarget struct {
	EventID   string  `json:"event_id"`
	Timestamp int64   `json:"timestamp"`
	UserID    int64   `json:"user_id"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
}

type densePoint struct {
	X int64   `json:"x"`
	Y int64   `json:"y"`
	Z float64 `json:"z"`
	W bool    `json:"w"`
}

type mustNotCallUnmarshaler struct {
	called bool
}

func (m *mustNotCallUnmarshaler) UnmarshalJSON(b []byte) error {
	m.called = true
	return fmt.Errorf("UnmarshalJSON must not be called when document has syntax error")
}

func buildWideEventJSON() string {
	var sb strings.Builder
	sb.WriteByte('{')
	sb.WriteString(`"event_id":"evt-20261111-99887766",`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, `"extra_str_%d":"payload_segment_value_%d_abcdefghijklmnopqrstuvwxyz",`, i, i)
	}
	sb.WriteString(`"timestamp":1762848000123,`)
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&sb, `"extra_metric_%d":%d.%d,`, i, i*17+3, i*991+7)
	}
	sb.WriteString(`"user_id":9081726354,`)
	for i := 0; i < 17; i++ {
		fmt.Fprintf(&sb, `"extra_nested_%d":{"k1":"v_%d","nums":[1,2,3,%d],"flag":true,"sub":{"ok":false,"n":null}},`, i, i, i)
	}
	sb.WriteString(`"latency_ms":14.875,"success":true}`)
	return sb.String()
}

func buildDensePointsJSON(n int) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"x":%d,"y":%d,"z":%d.25,"w":%v}`, i*3, -i*7, i, i%2 == 0)
	}
	sb.WriteByte(']')
	return sb.String()
}

func TestBranch1SchemaProjection(t *testing.T) {
	// 1. Sparse TwitterJson parity across ConfigDefault / ConfigStd / ConfigFastest
	var wantFeed sparseTwitterFeed
	if err := json.Unmarshal([]byte(testdata.TwitterJson), &wantFeed); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []struct {
		name string
		api  sonic.API
	}{
		{"Default", sonic.ConfigDefault},
		{"Std", sonic.ConfigStd},
		{"Fastest", sonic.ConfigFastest},
	} {
		var gotBytes, gotStr sparseTwitterFeed
		if err := cfg.api.Unmarshal([]byte(testdata.TwitterJson), &gotBytes); err != nil {
			t.Fatalf("%s Unmarshal err: %v", cfg.name, err)
		}
		if err := cfg.api.UnmarshalFromString(testdata.TwitterJson, &gotStr); err != nil {
			t.Fatalf("%s UnmarshalFromString err: %v", cfg.name, err)
		}
		if !reflect.DeepEqual(gotBytes, wantFeed) || !reflect.DeepEqual(gotStr, wantFeed) {
			t.Fatalf("%s SparseTwitterFeed mismatch", cfg.name)
		}
	}

	// 2. RawMessage & NoCopyRawMessage envelope (including null and nested TwitterJson)
	envJSON := `{"code":200,"msg":"ok","extra":{"ignore":[1,2,3]},"trace_id":"tr-42","data":` + testdata.TwitterJson + `}`
	var wantEnv rawEnvelopeStd
	if err := json.Unmarshal([]byte(envJSON), &wantEnv); err != nil {
		t.Fatal(err)
	}
	var gotEnvStd rawEnvelopeStd
	if err := sonic.ConfigDefault.Unmarshal([]byte(envJSON), &gotEnvStd); err != nil {
		t.Fatal(err)
	}
	if gotEnvStd.Code != wantEnv.Code || gotEnvStd.Msg != wantEnv.Msg || gotEnvStd.TraceID != wantEnv.TraceID || !bytes.Equal(gotEnvStd.Data, wantEnv.Data) {
		t.Fatalf("rawEnvelopeStd mismatch")
	}

	var gotEnvNC rawEnvelopeNoCopy
	if err := sonic.ConfigDefault.UnmarshalFromString(envJSON, &gotEnvNC); err != nil {
		t.Fatal(err)
	}
	if string(gotEnvNC.Data) != testdata.TwitterJson {
		t.Fatalf("NoCopyRawMessage mismatch len=%d want=%d", len(gotEnvNC.Data), len(testdata.TwitterJson))
	}

	// Null RawMessage pointer vs value
	type ptrRawBox struct {
		A *json.RawMessage        `json:"a"`
		B *sonic.NoCopyRawMessage `json:"b"`
		C json.RawMessage         `json:"c"`
	}
	rawInit := json.RawMessage(`"init"`)
	ncInit := sonic.NoCopyRawMessage(`"init"`)
	pb := ptrRawBox{A: &rawInit, B: &ncInit}
	nullEnv := `{"pad":"` + strings.Repeat("x", 256) + `","a":null,"b":null,"c":null}`
	if err := sonic.ConfigDefault.UnmarshalFromString(nullEnv, &pb); err != nil {
		t.Fatal(err)
	}
	if pb.A != nil || pb.B != nil || string(pb.C) != "null" {
		t.Fatalf("null raw handling mismatch: A=%v B=%v C=%q", pb.A, pb.B, pb.C)
	}

	// 3. Wide event projection + duplicate keys + escaped keys + case-insensitive matching
	wideJSON := buildWideEventJSON()
	var wantWide, gotWide wideProjectionTarget
	if err := json.Unmarshal([]byte(wideJSON), &wantWide); err != nil {
		t.Fatal(err)
	}
	if err := sonic.ConfigDefault.UnmarshalFromString(wideJSON, &gotWide); err != nil {
		t.Fatal(err)
	}
	if gotWide != wantWide {
		t.Fatalf("wide projection mismatch: got=%+v want=%+v", gotWide, wantWide)
	}

	dupEscJSON := `{"pad":"` + strings.Repeat("z", 300) + `","event_id":"first","EV\u0045NT_ID":"second","timestamp":11,"user_id":22,"latency_ms":1.5,"success":true}`
	var wantDup, gotDup wideProjectionTarget
	if err := json.Unmarshal([]byte(dupEscJSON), &wantDup); err != nil {
		t.Fatal(err)
	}
	if err := sonic.ConfigDefault.UnmarshalFromString(dupEscJSON, &gotDup); err != nil {
		t.Fatal(err)
	}
	if gotDup != wantDup || gotDup.EventID != "second" {
		t.Fatalf("duplicate/escaped key mismatch: got=%+v want=%+v", gotDup, wantDup)
	}

	// 4. DisallowUnknownFields & SYN-PREMUT invariants
	dec := decoder.NewDecoder(wideJSON)
	dec.DisallowUnknownFields()
	var disTarget wideProjectionTarget
	if err := dec.Decode(&disTarget); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}

	type preMutStruct struct {
		Guard mustNotCallUnmarshaler `json:"guard"`
		ID    int                    `json:"id"`
	}
	badPayloads := []string{
		`{"guard":{"ok":1},"skip_pad":"` + strings.Repeat("a", 300) + `","bad_esc":"\q","id":1}`,
		`{"guard":{"ok":1},"skip_pad":"` + strings.Repeat("a", 300) + `","bad_num":12.e,"id":1}`,
		`{"guard":{"ok":1},"skip_pad":"` + strings.Repeat("a", 300) + `","bad_inf":1e309,"id":1}`,
		`{"guard":{"ok":1},"skip_pad":"` + strings.Repeat("a", 300) + `","bad_trunc":{"a":[1,2},"id":1}`,
		`{"guard":{"ok":1},"skip_pad":"` + strings.Repeat("a", 300) + `","bad_ctrl":"hello` + "\x01" + `world","id":1}`,
	}
	for idx, bad := range badPayloads {
		var pm preMutStruct
		err := sonic.ConfigStd.UnmarshalFromString(bad, &pm)
		if err == nil {
			t.Fatalf("bad case %d expected syntax error, got nil", idx)
		}
		if pm.Guard.called {
			t.Fatalf("bad case %d violated SYN-PREMUT: UnmarshalJSON called before syntax error!", idx)
		}
	}

	// Valid 3-digit exponent float (< MaxFloat64) in skipped subtree succeeds via fallback
	var okTarget wideProjectionTarget
	bigValidExpJSON := `{"event_id":"ok","skip_big":1.79e308,"pad":"` + strings.Repeat("p", 300) + `","timestamp":9,"user_id":8,"latency_ms":0.5,"success":true}`
	if err := sonic.ConfigDefault.UnmarshalFromString(bigValidExpJSON, &okTarget); err != nil || okTarget.EventID != "ok" {
		t.Fatalf("valid 1.79e308 in skipped subtree failed: err=%v target=%+v", err, okTarget)
	}

	// 5. Dense slice adaptive handoff parity
	pointsJSON := buildDensePointsJSON(200)
	var wantPts, gotPts []densePoint
	if err := json.Unmarshal([]byte(pointsJSON), &wantPts); err != nil {
		t.Fatal(err)
	}
	if err := sonic.ConfigDefault.UnmarshalFromString(pointsJSON, &gotPts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotPts, wantPts) {
		t.Fatalf("dense points mismatch")
	}

	// 6. Multi-value stream session with escaped keys and skipped subtrees
	var streamBuf strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&streamBuf, "{\"ev\\u0065nt_id\":\"e-%d\",\"skip\":{\"blob\":\"%s\",\"arr\":[1,2,3]},\"timestamp\":%d,\"user_id\":%d,\"latency_ms\":1.25,\"success\":true}\n",
			i, strings.Repeat("s", 260), i*10, i)
	}
	sdec := decoder.NewDecoder(streamBuf.String())
	for i := 0; i < 20; i++ {
		var item wideProjectionTarget
		if err := sdec.Decode(&item); err != nil {
			t.Fatalf("stream item %d err: %v", i, err)
		}
		if item.EventID != fmt.Sprintf("e-%d", i) || item.Timestamp != int64(i*10) {
			t.Fatalf("stream item %d mismatch: %+v", i, item)
		}
	}
}

func BenchmarkBranch1Projection(b *testing.B) {
	twitterBytes := []byte(testdata.TwitterJson)
	envNoCopyStr := `{"code":200,"msg":"ok","trace_id":"tr-9981","data":` + testdata.TwitterJson + `}`
	envStdBytes := []byte(envNoCopyStr)
	wideBytes := []byte(buildWideEventJSON())
	denseBytes := []byte(buildDensePointsJSON(200))

	b.Run("SparseTwitterFeed/Default", func(b *testing.B) {
		b.SetBytes(int64(len(twitterBytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out sparseTwitterFeed
			if err := sonic.ConfigDefault.Unmarshal(twitterBytes, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("RawEnvelopeStd/Default", func(b *testing.B) {
		b.SetBytes(int64(len(envStdBytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out rawEnvelopeStd
			if err := sonic.ConfigDefault.Unmarshal(envStdBytes, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("RawEnvelopeNoCopyStr/Default", func(b *testing.B) {
		b.SetBytes(int64(len(envNoCopyStr)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out rawEnvelopeNoCopy
			if err := sonic.ConfigDefault.UnmarshalFromString(envNoCopyStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("WideEvent60To5/Default", func(b *testing.B) {
		b.SetBytes(int64(len(wideBytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out wideProjectionTarget
			if err := sonic.ConfigDefault.Unmarshal(wideBytes, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("DensePointSlice200/Default", func(b *testing.B) {
		b.SetBytes(int64(len(denseBytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out []densePoint
			if err := sonic.ConfigDefault.Unmarshal(denseBytes, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}
