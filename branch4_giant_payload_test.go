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
)

type b4OrderRecord struct {
	OrderID      int64   `json:"order_id"`
	CustomerCode string  `json:"customer_code"`
	RegionName   string  `json:"region_name"`
	ItemTitle    string  `json:"item_title"`
	NoteText     string  `json:"note_text"`
	Quantity     int     `json:"quantity"`
	DeltaBalance int64   `json:"delta_balance"`
	UnitPrice    float64 `json:"unit_price"`
	DiscountRate float64 `json:"discount_rate"`
	PaidFlag     bool    `json:"paid_flag"`
	PriorityTier int     `json:"priority_tier"`
}

type b4GiantEnvelope struct {
	BatchName string                 `json:"batch_name"`
	Payload   sonic.NoCopyRawMessage `json:"payload"`
	RawBackup json.RawMessage        `json:"raw_backup"`
	ItemCount int                    `json:"item_count"`
}

func makeB4Records(n int, includeEscapes bool) []b4OrderRecord {
	out := make([]b4OrderRecord, n)
	for i := 0; i < n; i++ {
		note := "standard delivery window confirmed"
		if includeEscapes && i%7 == 0 {
			note = "line1\nline2\t\"quoted\" value \u0041 \U0001f600"
		}
		out[i] = b4OrderRecord{
			OrderID:      int64(1000000 + i),
			CustomerCode: fmt.Sprintf("cust-%06d", i),
			RegionName:   "us-east-cluster",
			ItemTitle:    "ergonomic mechanical keyboard kit",
			NoteText:     note,
			Quantity:     (i % 12) + 1,
			DeltaBalance: -int64((i % 500) * 15),
			UnitPrice:    float64(49+(i%100)) + 0.75,
			DiscountRate: 0.125,
			PaidFlag:     i%2 == 0,
			PriorityTier: i % 5,
		}
	}
	return out
}

func TestBranch4GiantPayloadParity(t *testing.T) {
	records := makeB4Records(11000, true)
	stdBytes, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if len(stdBytes) <= 1<<20 {
		t.Fatalf("expected > 1 MiB payload, got %d bytes", len(stdBytes))
	}
	rawStr := string(stdBytes)

	// 1. Giant Marshal parity & repeated pool reuse.
	for iter := 0; iter < 3; iter++ {
		gotBytes, merr := sonic.Marshal(records)
		if merr != nil {
			t.Fatalf("sonic.Marshal iter %d failed: %v", iter, merr)
		}
		var roundTrip []b4OrderRecord
		if uerr := json.Unmarshal(gotBytes, &roundTrip); uerr != nil {
			t.Fatalf("json.Unmarshal on sonic.Marshal output failed: %v", uerr)
		}
		if !reflect.DeepEqual(roundTrip, records) {
			t.Fatalf("sonic.Marshal round-trip mismatch at iter %d", iter)
		}
	}

	// 2. Giant Unmarshal([]byte) with caller buffer mutation safety.
	mutBuf := append([]byte(nil), stdBytes...)
	var gotFromBytes []b4OrderRecord
	if err := sonic.Unmarshal(mutBuf, &gotFromBytes); err != nil {
		t.Fatalf("sonic.Unmarshal giant bytes failed: %v", err)
	}
	for i := range mutBuf {
		mutBuf[i] = 'X'
	}
	if !reflect.DeepEqual(gotFromBytes, records) {
		t.Fatalf("Unmarshal([]byte) mismatch or aliased caller buffer")
	}

	// 3. Giant UnmarshalString (Default, Fastest, Std).
	for name, api := range map[string]sonic.API{
		"Default": sonic.ConfigDefault,
		"Fastest": sonic.ConfigFastest,
		"Std":     sonic.ConfigStd,
	} {
		var got []b4OrderRecord
		if err := api.UnmarshalFromString(rawStr, &got); err != nil {
			t.Fatalf("%s.UnmarshalFromString giant failed: %v", name, err)
		}
		if !reflect.DeepEqual(got, records) {
			t.Fatalf("%s.UnmarshalFromString giant mismatch", name)
		}
	}

	// 4. NoCopyRawMessage & json.RawMessage inside > 1 MiB envelope.
	envStr := `{"batch_name":"nightly-settlement","payload":` + rawStr + `,"raw_backup":[1,2,3],"item_count":11000}`
	var env b4GiantEnvelope
	if err := sonic.ConfigFastest.UnmarshalFromString(envStr, &env); err != nil {
		t.Fatalf("envelope UnmarshalFromString failed: %v", err)
	}
	if env.BatchName != "nightly-settlement" || env.ItemCount != 11000 || string(env.RawBackup) != "[1,2,3]" {
		t.Fatalf("unexpected envelope metadata: %+v", env.BatchName)
	}
	if string(env.Payload) != rawStr {
		t.Fatalf("envelope NoCopyRawMessage content mismatch (len=%d want=%d)", len(env.Payload), len(rawStr))
	}

	// 5. Invalid UTF-8 replacement under ConfigStd on > 1 MiB document.
	badUTF8Prefix := []byte(`[{"note":"bad`)
	badUTF8Prefix = append(badUTF8Prefix, 0xff, 0xfe)
	badUTF8Prefix = append(badUTF8Prefix, []byte(`utf8","pad":"`)...)
	badUTF8Doc := append(badUTF8Prefix, bytes.Repeat([]byte("a"), (1<<20)+4096)...)
	badUTF8Doc = append(badUTF8Doc, []byte(`"}]`)...)
	var utf8Out []map[string]string
	if err := sonic.ConfigStd.Unmarshal(badUTF8Doc, &utf8Out); err != nil {
		t.Fatalf("ConfigStd invalid UTF-8 giant Unmarshal failed: %v", err)
	}
	if len(utf8Out) != 1 || utf8Out[0]["note"] != "bad\ufffd\ufffdutf8" {
		t.Fatalf("unexpected UTF-8 replacement result: %#v", utf8Out)
	}

	// 5b. > 4 MiB zero-padded read-only scanner parity (Default, Fastest, Std) + UTF-8 + SYN-PREMUT.
	records5M := makeB4Records(18000, true)
	bytes5M, err := sonic.Marshal(records5M)
	if err != nil || len(bytes5M) <= 4<<20 {
		t.Fatalf("expected > 4 MiB payload, got len=%d err=%v", len(bytes5M), err)
	}
	str5M := string(bytes5M)
	var got5M []b4OrderRecord
	if err := sonic.ConfigDefault.Unmarshal(bytes5M, &got5M); err != nil || !reflect.DeepEqual(got5M, records5M) {
		t.Fatalf("> 4 MiB Unmarshal failed or mismatch: %v", err)
	}
	var got5MStr []b4OrderRecord
	if err := sonic.ConfigFastest.UnmarshalFromString(str5M, &got5MStr); err != nil || !reflect.DeepEqual(got5MStr, records5M) {
		t.Fatalf("> 4 MiB UnmarshalFromString failed or mismatch: %v", err)
	}
	badUTF8Doc5M := append([]byte(nil), badUTF8Prefix...)
	badUTF8Doc5M = append(badUTF8Doc5M, bytes.Repeat([]byte("a"), (4<<20)+4096)...)
	badUTF8Doc5M = append(badUTF8Doc5M, []byte(`"}]`)...)
	var utf8Out5M []map[string]string
	if err := sonic.ConfigStd.Unmarshal(badUTF8Doc5M, &utf8Out5M); err != nil {
		t.Fatalf("ConfigStd invalid UTF-8 > 4 MiB Unmarshal failed: %v", err)
	}
	corrupt5M := str5M[:len(str5M)-2] + `, {"broken": tru}]`
	prePop5M := []b4OrderRecord{{OrderID: 99, CustomerCode: "keep5m"}}
	if err := sonic.UnmarshalString(corrupt5M, &prePop5M); err == nil {
		t.Fatalf("expected syntax error on corrupt > 4 MiB payload")
	}
	if len(prePop5M) != 1 || prePop5M[0].OrderID != 99 {
		t.Fatalf("SYN-PREMUT violated on > 4 MiB payload: %+v", prePop5M)
	}

	// 6. SYN-PREMUT: trailing syntax error near end of > 1 MiB payload must not mutate pre-populated target.
	corruptStr := rawStr[:len(rawStr)-2] + `, {"broken": tru}]`
	prePop := []b4OrderRecord{{OrderID: 42, CustomerCode: "keep"}}
	if err := sonic.UnmarshalString(corruptStr, &prePop); err == nil {
		t.Fatalf("expected syntax error on corrupt giant payload, got nil")
	}
	if len(prePop) != 1 || prePop[0].OrderID != 42 || prePop[0].CustomerCode != "keep" {
		t.Fatalf("SYN-PREMUT violated on giant payload: %+v", prePop)
	}

	// 7. Multi-value Decoder over > 1 MiB JSONL stream.
	var sb strings.Builder
	smallBatch := makeB4Records(1, false)[0]
	lineBytes, _ := json.Marshal(smallBatch)
	lineCount := ((1 << 20) / len(lineBytes)) + 200
	for i := 0; i < lineCount; i++ {
		sb.Write(lineBytes)
		sb.WriteByte('\n')
	}
	jsonl := sb.String()
	if len(jsonl) <= 1<<20 {
		t.Fatalf("expected > 1 MiB jsonl, got %d", len(jsonl))
	}
	dec := decoder.NewDecoder(jsonl)
	for i := 0; i < lineCount; i++ {
		var rec b4OrderRecord
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("JSONL Decode #%d failed: %v", i, err)
		}
		if rec != smallBatch {
			t.Fatalf("JSONL Decode #%d mismatch", i)
		}
	}
	if err := dec.CheckTrailings(); err != nil {
		t.Fatalf("JSONL CheckTrailings failed: %v", err)
	}
}

func BenchmarkBranch4GiantPayload(b *testing.B) {
	rec2M := makeB4Records(9500, false)
	bytes2M, err := json.Marshal(rec2M)
	if err != nil {
		b.Fatal(err)
	}
	str2M := string(bytes2M)

	rec8M := makeB4Records(38000, false)
	bytes8M, err := json.Marshal(rec8M)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("Unmarshal_2MiB_Bytes/Default", func(b *testing.B) {
		b.SetBytes(int64(len(bytes2M)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out []b4OrderRecord
			if err := sonic.ConfigDefault.Unmarshal(bytes2M, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("UnmarshalString_2MiB/Fastest", func(b *testing.B) {
		b.SetBytes(int64(len(str2M)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out []b4OrderRecord
			if err := sonic.ConfigFastest.UnmarshalFromString(str2M, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Unmarshal_8MiB_Bytes/Default", func(b *testing.B) {
		b.SetBytes(int64(len(bytes8M)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out []b4OrderRecord
			if err := sonic.ConfigDefault.Unmarshal(bytes8M, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Marshal_2MiB_Slice/Default", func(b *testing.B) {
		b.SetBytes(int64(len(bytes2M)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := sonic.ConfigDefault.Marshal(rec2M)
			if err != nil || len(out) == 0 {
				b.Fatal(err)
			}
		}
	})
}
