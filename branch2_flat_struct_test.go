package sonic_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
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
	ReqID       *string          `json:"req_id"`
	ServiceName *string          `json:"service_name"`
	Caller      *string          `json:"caller"`
	Region      *string          `json:"region"`
	Cluster     *string          `json:"cluster"`
	Env         *string          `json:"env"`
	NoteEscaped *string          `json:"note_escaped"`
	EmptyStr    *string          `json:"empty_str"`
	UserID      *int64           `json:"user_id"`
	DeltaI64    *int64           `json:"delta_i64"`
	SeqInt      *int             `json:"seq_int"`
	CodeI32     *int32           `json:"code_i32"`
	NegI32      *int32           `json:"neg_i32"`
	HashU64     *uint64          `json:"hash_u64"`
	CountUint   *uint            `json:"count_uint"`
	FlagsU32    *uint32          `json:"flags_u32"`
	Active      *bool            `json:"active"`
	Cached      *bool            `json:"cached"`
	LatencyMs   *float64         `json:"latency_ms"`
	RatioF64    *float64         `json:"ratio_f64"`
	RatioF32    *float32         `json:"ratio_f32"`
	StrOptI64   *int64           `json:"str_opt_i64,string"`
	NamedScalar *b2NamedInt64    `json:"named_scalar"`
	NamedPtr    b2NamedPtrInt64  `json:"named_ptr"`
	CustomPtr   *b2CustomInt64   `json:"custom_ptr"`
	Meta        *b2NestedMeta    `json:"meta"`
	InlineMeta  b2NestedMeta     `json:"inline_meta"`
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

type b2BenchIDL24 struct {
	S1  *string  `json:"request_id"`
	S2  *string  `json:"tenant_code"`
	S3  *string  `json:"caller_service"`
	S4  *string  `json:"target_cluster"`
	S5  *string  `json:"region_zone"`
	S6  *string  `json:"protocol_ver"`
	S7  *string  `json:"account_group"`
	S8  *string  `json:"client_ip_addr"`
	S9  *string  `json:"user_agent_str"`
	S10 *string  `json:"routing_bucket"`
	I1  *int64   `json:"user_account_id"`
	I2  *int64   `json:"organization_id"`
	I3  *int64   `json:"created_unix_ms"`
	I4  *int64   `json:"updated_unix_ms"`
	I5  *int32   `json:"status_code_num"`
	I6  *int32   `json:"retry_attempt_n"`
	U1  *uint64  `json:"sequence_ticket"`
	U2  *uint32  `json:"feature_bitmask"`
	B1  *bool    `json:"is_authenticated"`
	B2  *bool    `json:"is_canary_traffic"`
	B3  *bool    `json:"enable_compression"`
	F1  *float64 `json:"upstream_latency_ms"`
	F2  *float64 `json:"downstream_score"`
	F3  *float32 `json:"sampling_probability"`
}

type b2OrderItem struct {
	SKU       string   `json:"sku_identifier"`
	Title     string   `json:"product_title"`
	Qty       int32    `json:"quantity_count"`
	UnitCents int64    `json:"unit_price_cents"`
	Discount  *int64   `json:"discount_cents"`
	PromoTag  *string  `json:"promotion_code"`
	TaxRate   float64  `json:"applied_tax_rate"`
}

type b2OrderBuyer struct {
	BuyerID   int64   `json:"buyer_member_id"`
	Email     string  `json:"contact_email"`
	VIP       bool    `json:"vip_membership"`
	TierLevel *int32  `json:"loyalty_tier_lv"`
	PhoneMask *string `json:"masked_phone_no"`
}

type b2OrderCoupon struct {
	CouponID string `json:"coupon_serial"`
	SaveAmt  int64  `json:"saved_amount"`
}

type b2Order struct {
	OrderID   int64          `json:"order_primary_id"`
	OrderNo   string         `json:"order_serial_str"`
	State     int32          `json:"fulfillment_state"`
	Paid      bool           `json:"payment_confirmed"`
	TotalAmt  int64          `json:"total_payable_amt"`
	Shipping  float64        `json:"shipping_fee_f64"`
	Note      *string        `json:"customer_remark"`
	Buyer     b2OrderBuyer   `json:"buyer_profile"`
	Coupon    *b2OrderCoupon `json:"applied_coupon"`
	Items     []b2OrderItem  `json:"order_line_items"`
	ExtraMeta any            `json:"gateway_channel_id"`
}

func buildB2BenchPayloads() (idlJSON string, ordersJSON string) {
	idlJSON = `{
		"routing_bucket":"bucket-west-09","user_agent_str":"SonicBench/2.0 (Linux; x86_64)","client_ip_addr":"10.128.44.19",
		"account_group":"acct_group_enterprise_001","protocol_ver":"h2c-v1","region_zone":"us-west-2b",
		"target_cluster":"core-trade-prod","caller_service":"edge-ingress-gw","tenant_code":"enterprise_01",
		"request_id":"req_sample_order_0000001",
		"sampling_probability":0.125,"downstream_score":98.625,"upstream_latency_ms":4.375,
		"enable_compression":true,"is_canary_traffic":false,"is_authenticated":true,
		"feature_bitmask":4026531840,"sequence_ticket":987654321012345,
		"retry_attempt_n":1,"status_code_num":200,
		"updated_unix_ms":1731312000456,"created_unix_ms":1731312000123,
		"organization_id":8800112233,"user_account_id":550099887766,
		"unmodeled_telemetry_tag":"ignored","unmodeled_ trace_flag":false
	}`

	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		coupon := "null"
		if i%2 == 0 {
			coupon = fmt.Sprintf(`{"saved_amount":%d,"coupon_serial":"CPN-%04d"}`, 300+i, i)
		}
		fmt.Fprintf(&sb, `{
			"gateway_channel_id":%d,
			"order_line_items":[
				{"applied_tax_rate":0.0825,"promotion_code":"SUMMER26","discount_cents":150,"unit_price_cents":2999,"quantity_count":2,"product_title":"Wireless Mechanical Keyboard","sku_identifier":"SKU-KB-%03d"},
				{"applied_tax_rate":0.0825,"promotion_code":null,"discount_cents":null,"unit_price_cents":1599,"quantity_count":1,"product_title":"USB-C Braided Cable 2m","sku_identifier":"SKU-CB-%03d"}
			],
			"applied_coupon":%s,
			"buyer_profile":{"masked_phone_no":"138****%04d","loyalty_tier_lv":3,"vip_membership":true,"contact_email":"buyer%d@example.com","buyer_member_id":%d},
			"customer_remark":"leave at front desk",
			"shipping_fee_f64":5.99,
			"total_payable_amt":%d,
			"payment_confirmed":true,
			"fulfillment_state":2,
			"order_serial_str":"ORD-20261111-%06d",
			"order_primary_id":%d,
			"unknown_audit_version":1
		}`, 10+i, i, i, coupon, i, i, 900000+i, 7297+i, i, 7000000+i)
	}
	sb.WriteByte(']')
	ordersJSON = sb.String()
	return idlJSON, ordersJSON
}

func BenchmarkBranch2FlatStruct(b *testing.B) {
	idlStr, ordersStr := buildB2BenchPayloads()
	idlBytes := []byte(idlStr)
	ordersBytes := []byte(ordersStr)

	b.Run("OptionalIDL24_Bytes/Default", func(b *testing.B) {
		b.SetBytes(int64(len(idlBytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out b2BenchIDL24
			if err := sonic.ConfigDefault.Unmarshal(idlBytes, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("OptionalIDL24_NoCopyStr/Default", func(b *testing.B) {
		b.SetBytes(int64(len(idlStr)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out b2BenchIDL24
			if err := sonic.ConfigDefault.UnmarshalFromString(idlStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("NestedOrderSlice30_Bytes/Default", func(b *testing.B) {
		b.SetBytes(int64(len(ordersBytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out []b2Order
			if err := sonic.ConfigDefault.Unmarshal(ordersBytes, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("NestedOrderSlice30_NoCopyStr/Default", func(b *testing.B) {
		b.SetBytes(int64(len(ordersStr)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out []b2Order
			if err := sonic.ConfigDefault.UnmarshalFromString(ordersStr, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}
