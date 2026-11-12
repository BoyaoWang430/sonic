//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package decoder_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/decoder"
	"github.com/stretchr/testify/require"
)

func TestControlCharBeforeBackslashRejected(t *testing.T) {
	type payload struct {
		Skip string `json:"skip_unused"`
		S    string `json:"s"`
	}

	// Test both short (<16B) and long (>32B, >64B) prefixes before raw control + valid escape.
	for _, padLen := range []int{0, 5, 15, 16, 28, 33, 65} {
		pad := strings.Repeat("a", padLen)
		cases := []string{
			fmt.Sprintf("{\"skip\":\"%s\x00b\\\\nc\",\"s\":\"ok\"}", pad),
			fmt.Sprintf("{\"skip\":\"ok\",\"s\":\"%s\x00b\\\\nc\"}", pad),
			fmt.Sprintf("{\"skip\":\"%s\nline2\\\\t\",\"s\":\"ok\"}", pad),
			fmt.Sprintf("{\"s\":\"%s\t\\\\\\\"end\"}", pad),
			fmt.Sprintf("{\"s\":\"%s\x1f\\\\u0041\"}", pad),
		}

		for _, js := range cases {
			var stdOut payload
			stdErr := json.Unmarshal([]byte(js), &stdOut)
			require.Error(t, stdErr, "stdlib must reject %q", js)

			var sOut payload
			require.Error(t, sonic.ConfigStd.Unmarshal([]byte(js), &sOut), "ConfigStd.Unmarshal must reject %q", js)

			var anyOut any
			require.Error(t, sonic.ConfigStd.Unmarshal([]byte(js), &anyOut), "ConfigStd.Unmarshal(any) must reject %q", js)

			// ConfigDefault / ConfigFastest (ValidateString=false) allow unvalidated bytes.
			var fastOut payload
			require.NoError(t, sonic.ConfigFastest.Unmarshal([]byte(js), &fastOut))
			require.NoError(t, sonic.Unmarshal([]byte(js), &fastOut))
		}
	}

	// Ensure invalid escape before control char still reports invalid escape first.
	badEscBeforeCtrl := "{\"s\":\"\\q\x01\"}"
	var v any
	err := sonic.Unmarshal([]byte(badEscBeforeCtrl), &v)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escaped")
}

func TestNullElementsInMapStringAndTypedSlices(t *testing.T) {
	type holder struct {
		MS  map[string]string `json:"ms"`
		SI  []int             `json:"si"`
		S32 []int32           `json:"s32"`
		S64 []int64           `json:"s64"`
		SU  []uint            `json:"su"`
		SU3 []uint32          `json:"su3"`
		SU6 []uint64          `json:"su6"`
		SS  []string          `json:"ss"`
	}
	input := []byte(`{
		"ms": {"a": null, "b": "ok"},
		"si": [1, null, 3],
		"s32": [-2, null, 4],
		"s64": [10, null, 30],
		"su": [5, null, 7],
		"su3": [8, null, 9],
		"su6": [11, null, 13],
		"ss": ["x", null, "z"]
	}`)

	var want, got holder
	require.NoError(t, json.Unmarshal(input, &want))
	require.NoError(t, sonic.Unmarshal(input, &got))
	require.Equal(t, want, got)

	// Also verify top-level slices and pre-populated slices match encoding/json.
	wantPre := []int{100, 200, 300}
	gotPre := []int{100, 200, 300}
	require.NoError(t, json.Unmarshal([]byte(`[1, null, 3]`), &wantPre))
	require.NoError(t, sonic.Unmarshal([]byte(`[1, null, 3]`), &gotPre))
	require.Equal(t, wantPre, gotPre)

	wantMap := map[string]string{"a": "old", "keep": "yes"}
	gotMap := map[string]string{"a": "old", "keep": "yes"}
	require.NoError(t, json.Unmarshal([]byte(`{"a": null, "b": null, "c": "new"}`), &wantMap))
	require.NoError(t, sonic.Unmarshal([]byte(`{"a": null, "b": null, "c": "new"}`), &gotMap))
	require.Equal(t, wantMap, gotMap)
}

func TestMapsReplacePrepopulatedEntriesMatchingStdlib(t *testing.T) {
	type myStr string
	type subVal struct {
		Old bool
		New bool
	}

	t.Run("map[string]int", func(t *testing.T) {
		want := map[string]int{"k": 42, "other": 7}
		got := map[string]int{"k": 42, "other": 7}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]int64", func(t *testing.T) {
		want := map[string]int64{"k": 42}
		got := map[string]int64{"k": 42}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]uint64", func(t *testing.T) {
		want := map[string]uint64{"k": 42}
		got := map[string]uint64{"k": 42}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]float64", func(t *testing.T) {
		want := map[string]float64{"k": 3.5}
		got := map[string]float64{"k": 3.5}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]bool", func(t *testing.T) {
		want := map[string]bool{"k": true}
		got := map[string]bool{"k": true}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[int]string", func(t *testing.T) {
		want := map[int]string{1: "old", 2: "keep"}
		got := map[int]string{1: "old", 2: "keep"}
		require.NoError(t, json.Unmarshal([]byte(`{"1": null, "3": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"1": null, "3": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[int64]string", func(t *testing.T) {
		want := map[int64]string{1: "old"}
		got := map[int64]string{1: "old"}
		require.NoError(t, json.Unmarshal([]byte(`{"1": null, "3": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"1": null, "3": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("generic map[myStr]int", func(t *testing.T) {
		want := map[myStr]int{"k": 42}
		got := map[myStr]int{"k": 42}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]Struct replaces rather than merges", func(t *testing.T) {
		want := map[string]subVal{"k": {Old: true}}
		got := map[string]subVal{"k": {Old: true}}
		require.NoError(t, json.Unmarshal([]byte(`{"k":{"New":true}}`), &want))
		require.NoError(t, sonic.Unmarshal([]byte(`{"k":{"New":true}}`), &got))
		require.Equal(t, want, got)
	})
}

func TestMultiChunkEscapedStringsRoundTrip(t *testing.T) {
	// Exercises >64B, >128B, >256B escaped strings and crossing escapes across RVV/SIMD chunk boundaries.
	for _, chunkPad := range []int{60, 62, 63, 64, 125, 127, 128, 253, 255, 256, 320} {
		orig := strings.Repeat("x", chunkPad) + "\n\t\"\\\u4e2d\U0001f680" + strings.Repeat("y", 150) + "\r\nend"
		encoded, err := sonic.Marshal(map[string]string{"k": orig})
		require.NoError(t, err)

		var out map[string]string
		require.NoError(t, sonic.ConfigStd.Unmarshal(encoded, &out))
		require.Equal(t, orig, out["k"])
	}
}

func TestCustomSliceMismatchErrorTypePreserved(t *testing.T) {
	type MyInt int64
	type MyInts []int64

	var customSlice MyInts
	err1 := sonic.Unmarshal([]byte(`"not_array"`), &customSlice)
	var mm1 *decoder.MismatchTypeError
	require.ErrorAs(t, err1, &mm1)
	require.Equal(t, reflect.TypeOf(MyInts(nil)), mm1.Type)

	var customElemSlice []MyInt
	err2 := sonic.Unmarshal([]byte(`["not_int"]`), &customElemSlice)
	var mm2 *decoder.MismatchTypeError
	require.ErrorAs(t, err2, &mm2)
	require.Equal(t, reflect.TypeOf(MyInt(0)), mm2.Type)

	var plainInts []int
	err3 := sonic.Unmarshal([]byte(`["not_int"]`), &plainInts)
	var mm3 *decoder.MismatchTypeError
	require.ErrorAs(t, err3, &mm3)
	require.Equal(t, reflect.TypeOf(int(0)), mm3.Type)

	var plainUints []uint
	err4 := sonic.Unmarshal([]byte(`["not_uint"]`), &plainUints)
	var mm4 *decoder.MismatchTypeError
	require.ErrorAs(t, err4, &mm4)
	require.Equal(t, reflect.TypeOf(uint(0)), mm4.Type)
}

func TestGiantPayloadWithRawMessageSchemaBailoutUsesStreamSession(t *testing.T) {
	type giantRawEnv struct {
		ID   int             `json:"id"`
		Raw  json.RawMessage `json:"raw"`
		Nums []int           `json:"nums"`
	}

	// Build a >4 MiB payload with deep unknown nesting (>64 levels) before the matched fields,
	// forcing schemaScanner to bail out (depth > 64) on a >4 MiB hasRawSubtree struct so it
	// exercises the StreamSession fallback path.
	var b bytes.Buffer
	b.Grow(4*1024*1024 + 4096)
	b.WriteString(`{"deep":`)
	b.WriteString(strings.Repeat("[", 70))
	b.WriteString("1")
	b.WriteString(strings.Repeat("]", 70))
	b.WriteString(`,"pad":"`)
	b.WriteString(strings.Repeat("p", 4*1024*1024+1024))
	b.WriteString(`","id":42,"raw":{"hello":[1,2,3]},"nums":[7,8,9]}`)

	var out giantRawEnv
	require.NoError(t, sonic.Unmarshal(b.Bytes(), &out))
	require.Equal(t, 42, out.ID)
	require.Equal(t, `{"hello":[1,2,3]}`, string(out.Raw))
	require.Equal(t, []int{7, 8, 9}, out.Nums)

	// Also verify DecodeWithSession path via Decoder.Decode.
	var outStream giantRawEnv
	dec := sonic.ConfigDefault.NewDecoder(bytes.NewReader(b.Bytes()))
	require.NoError(t, dec.Decode(&outStream))
	require.Equal(t, out, outStream)
}

func TestPtrStructSlabAndEfaceTables(t *testing.T) {
	type s1 struct{ A string }
	type s2 struct{ B int }
	type s3 struct{ C bool }
	type s4 struct{ D float64 }
	type s5 struct{ E string }
	type item struct {
		P1 *s1   `json:"p1"`
		P2 *s2   `json:"p2"`
		P3 *s3   `json:"p3"`
		P4 *s4   `json:"p4"`
		P5 *s5   `json:"p5"`
		SP []*s1 `json:"sp"`
	}

	var items []item
	for i := 0; i < 20; i++ {
		items = append(items, item{
			P1: &s1{A: "x"},
			P2: &s2{B: -i - 1},
			P3: &s3{C: i%2 == 0},
			P4: &s4{D: 1.25},
			P5: &s5{E: "y"},
			SP: []*s1{{A: "p"}, nil, {A: "q"}},
		})
	}
	raw, err := json.Marshal(items)
	require.NoError(t, err)

	var got []item
	require.NoError(t, sonic.ConfigStd.Unmarshal(raw, &got))
	require.Equal(t, items, got)

	// Verify 1-char ASCII strings & negative integer tables in generic interface{} decode with CopyString.
	mutBuf := []byte(`{"empty":"","one":"Z","neg":-42,"big_neg":-5000,"arr":["0","9",-1,-128,42]}`)
	var genericOut any
	require.NoError(t, sonic.ConfigStd.Unmarshal(mutBuf, &genericOut))
	var stdOut any
	require.NoError(t, json.Unmarshal(mutBuf, &stdOut))
	for i := range mutBuf {
		mutBuf[i] = '#'
	}
	require.Equal(t, stdOut, genericOut)
}

func TestAsSliceEfaceErrorReturned(t *testing.T) {
	var top []any
	err := sonic.Unmarshal([]byte(`[1, 1e999, "ok"]`), &top)
	require.Error(t, err)

	type wrap struct {
		Items []any `json:"items"`
	}
	var w wrap
	err2 := sonic.Unmarshal([]byte(`{"items":[1, 1e999, "ok"]}`), &w)
	require.Error(t, err2)
}

func TestAsSliceBytesArrayNullAndMismatch(t *testing.T) {
	// 1. Pre-populated slice preserves element on null.
	s1 := []byte{9, 8, 7, 6}
	std1 := []byte{9, 8, 7, 6}
	require.NoError(t, sonic.Unmarshal([]byte(`[1, null, 3]`), &s1))
	require.NoError(t, json.Unmarshal([]byte(`[1, null, 3]`), &std1))
	require.Equal(t, std1, s1)

	// 2. Nested object inside []byte array skips subtree cleanly via Next() and reports byte element type.
	s2 := []byte{9, 8, 7, 6}
	std2 := []byte{9, 8, 7, 6}
	errSonic := sonic.Unmarshal([]byte(`[{"a": 1}, 2]`), &s2)
	errStd := json.Unmarshal([]byte(`[{"a": 1}, 2]`), &std2)
	require.Error(t, errSonic)
	require.Error(t, errStd)
	require.Equal(t, std2, s2)
	var mm *decoder.MismatchTypeError
	require.ErrorAs(t, errSonic, &mm)
	require.Equal(t, reflect.TypeOf(byte(0)), mm.Type)
	require.Equal(t, 1, mm.Pos)

	// 3. Named byte and named byte-slice types preserve custom reflect.Type in MismatchTypeError.
	type MyByte uint8
	type MyBytes []MyByte
	var mb MyBytes
	errElem := sonic.Unmarshal([]byte(`[1, "x", 3]`), &mb)
	var mmElem *decoder.MismatchTypeError
	require.ErrorAs(t, errElem, &mmElem)
	require.Equal(t, reflect.TypeOf(MyByte(0)), mmElem.Type)
	require.Equal(t, MyBytes{1, 0, 3}, mb)

	var mb2 MyBytes
	errOuter := sonic.Unmarshal([]byte(`123`), &mb2)
	var mmOuter *decoder.MismatchTypeError
	require.ErrorAs(t, errOuter, &mmOuter)
	require.Equal(t, reflect.TypeOf(MyBytes(nil)), mmOuter.Type)
}

func TestPtrStructAndLargeStrPtrGCReclaim(t *testing.T) {
	type richItem struct {
		ID      int     `json:"id"`
		Payload []byte  `json:"payload"`
		BigStr  *string `json:"big_str"`
	}

	bigText := strings.Repeat("Z", 2048)
	var src []richItem
	for i := 0; i < 16; i++ {
		s := bigText
		src = append(src, richItem{
			ID:      i,
			Payload: bytes.Repeat([]byte{byte(i + 1)}, 4096),
			BigStr:  &s,
		})
	}
	raw, err := json.Marshal(src)
	require.NoError(t, err)

	var reclaimed int32
	keep := func() *richItem {
		var decoded []*richItem
		require.NoError(t, sonic.Unmarshal(raw, &decoded))
		require.Len(t, decoded, 16)
		runtime.SetFinalizer(decoded[5], func(*richItem) {
			atomic.AddInt32(&reclaimed, 1)
		})
		runtime.SetFinalizer(decoded[5].BigStr, func(*string) {
			atomic.AddInt32(&reclaimed, 1)
		})
		return decoded[0]
	}()

	for i := 0; i < 5 && atomic.LoadInt32(&reclaimed) < 2; i++ {
		runtime.GC()
	}
	runtime.KeepAlive(keep)
	require.Equal(t, int32(2), atomic.LoadInt32(&reclaimed), "sibling *richItem and >1KiB *string must be independently GC-reclaimable")
}

func TestZeroCopySchemaProjAndEscapedCoexistence(t *testing.T) {
	type mixedProj struct {
		Title   string  `json:"title"`
		Escaped string  `json:"escaped"`
		IDs     []int   `json:"ids"`
		Dynamic []any   `json:"dynamic"`
		TailNum float64 `json:"tail_num"`
		TailStr string  `json:"tail_str"`
		Raw     []byte  `json:"raw"`
	}

	// Test across boundary lengths around 64B and 256B (tailStart and safe64 transitions).
	for padLen := 0; padLen <= 340; padLen += 17 {
		pad := strings.Repeat("p", padLen)
		payload := fmt.Sprintf(
			`{"skip_head":{"nested":[1,true,null,{"pad":%q,"esc":"a\nb\u0041"}]},`+
				`"tit\u006ce":"hello-world","escaped":"line1\nline2\t\u4e2d\u6587",`+
				`"ids":[10,20,30],"dynamic":["plain","in_subtree\n\u0042",{"k":"v\tw"}],`+
				`"raw":"YWJj","tail_str":%q,"tail_num":123.456}`,
			pad, strings.Repeat("t", (padLen%70)+1),
		)

		var gotSonic, gotStd mixedProj
		require.NoError(t, json.Unmarshal([]byte(payload), &gotStd))
		require.NoError(t, sonic.ConfigStd.UnmarshalFromString(payload, &gotSonic))
		require.Equal(t, gotStd, gotSonic)

		// Truncated prefixes near tail boundaries must always error cleanly without out-of-bounds reads.
		for cut := 1; cut <= 8 && cut < len(payload); cut++ {
			var bad mixedProj
			require.Error(t, sonic.UnmarshalString(payload[:len(payload)-cut], &bad))
		}
	}

	// Multi-value JSONL stream over zero-copy schema projection.
	line := `{"title":"row","escaped":"x\ny","ids":[1,2],"tail_num":9.5,"tail_str":"end"}`
	stream := strings.Repeat(line+"\n", 8)
	sdec := decoder.NewDecoder(stream)
	sdec.ValidateString()
	for i := 0; i < 8; i++ {
		var row mixedProj
		require.NoError(t, sdec.Decode(&row))
		require.Equal(t, "x\ny", row.Escaped)
		require.Equal(t, []int{1, 2}, row.IDs)
	}
}

func TestFlatStructDirectDecode(t *testing.T) {
	type flatRecord struct {
		ID      int64   `json:"id"`
		I32     int32   `json:"i32"`
		U64     uint64  `json:"u64"`
		U32     uint32  `json:"u32"`
		Price   float64 `json:"price"`
		Ratio   float32 `json:"ratio"`
		Active  bool    `json:"active"`
		Name    string  `json:"name"`
		Empty   string  `json:"empty"`
		Comment string  `json:"comment"`
	}

	raw := `{"id":-9223372036854775808,"i32":-2147483648,"u64":999999999999999999,"u32":4294967295,` +
		`"price":-0.000125,"ratio":3.5,"active":true,"name":"sonic-direct","empty":"","comment":"中文UTF8",` +
		`"unknown_obj":{"a":[1,2,{"k":"esc\"quote\u0041"}],"b":false,"c":null}}`

	var gotSonic, gotStd flatRecord
	require.NoError(t, json.Unmarshal([]byte(raw), &gotStd))
	require.NoError(t, sonic.ConfigStd.UnmarshalFromString(raw, &gotSonic))
	require.Equal(t, gotStd, gotSonic)

	// CopyString independence: mutating input buffer must not mutate decoded strings.
	buf := []byte(`{"id":42,"name":"immutable-copy","comment":"second-copy"}`)
	var copied flatRecord
	require.NoError(t, sonic.Unmarshal(buf, &copied))
	for i := range buf {
		buf[i] = 'X'
	}
	require.Equal(t, "immutable-copy", copied.Name)
	require.Equal(t, "second-copy", copied.Comment)

	// Duplicate keys & null preservation on pre-populated struct.
	pre := flatRecord{ID: 777, Name: "keep", Active: true}
	dupJSON := `{"id":100,"id":200,"id":null,"name":null,"active":false}`
	preStd := pre
	require.NoError(t, json.Unmarshal([]byte(dupJSON), &preStd))
	require.NoError(t, sonic.UnmarshalString(dupJSON, &pre))
	require.Equal(t, preStd, pre)
	require.Equal(t, int64(200), pre.ID)
	require.Equal(t, "keep", pre.Name)
	require.False(t, pre.Active)

	// Speculative rollback atomicity on mismatch mid-object.
	errInput := `{"id":999,"i32":"not_an_int","name":"after_err"}`
	var errGot flatRecord
	require.Error(t, sonic.UnmarshalString(errInput, &errGot))
	require.Equal(t, int64(999), errGot.ID)
	require.Equal(t, "after_err", errGot.Name)

	// Syntax error before '}' leaves pre-populated fields untouched on bailout.
	var syntaxGot, syntaxStd flatRecord
	syntaxGot.Name = "untouched"
	syntaxStd.Name = "untouched"
	badSyntax := `{"id":123, broken}`
	require.Error(t, json.Unmarshal([]byte(badSyntax), &syntaxStd))
	require.Error(t, sonic.UnmarshalString(badSyntax, &syntaxGot))
	require.Equal(t, "untouched", syntaxGot.Name)

	// Invalid UTF-8 replacement under ConfigStd (ValidateString = true).
	badUTF8 := "{\"id\":7,\"name\":\"bad" + string([]byte{0xff}) + "byte\"}"
	var utf8Got flatRecord
	require.NoError(t, sonic.ConfigStd.UnmarshalFromString(badUTF8, &utf8Got))
	require.Equal(t, "bad\ufffdbyte", utf8Got.Name)

	// DisallowUnknownFields & CaseSensitive.
	strictCfg := sonic.Config{
		DisallowUnknownFields: true,
		CaseSensitive:         true,
	}.Froze()
	var csGot flatRecord
	require.Error(t, strictCfg.UnmarshalFromString(`{"ID":1}`, &csGot))
	require.Error(t, strictCfg.UnmarshalFromString(`{"id":1,"extra":2}`, &csGot))
	require.NoError(t, strictCfg.UnmarshalFromString(`{"id":55,"name":"exact"}`, &csGot))
	require.Equal(t, int64(55), csGot.ID)
	require.Equal(t, "exact", csGot.Name)

	// Multi-line JSONL via decoder.NewDecoder.
	jsonl := `{"id":1,"name":"one"}` + "\n" + `{"id":2,"name":"two\nesc"}` + "\n" + `{"id":3,"name":"three"}`
	nd := decoder.NewDecoder(jsonl)
	nd.ValidateString()
	var r1, r2, r3 flatRecord
	require.NoError(t, nd.Decode(&r1))
	require.NoError(t, nd.Decode(&r2))
	require.NoError(t, nd.Decode(&r3))
	require.Equal(t, "one", r1.Name)
	require.Equal(t, "two\nesc", r2.Name)
	require.Equal(t, "three", r3.Name)
}

func TestPtrStructSliceAndTypedMapDecode(t *testing.T) {
	type subItem struct {
		ID   int    `json:"id"`
		Tag  string `json:"tag,omitempty"`
		Flag bool   `json:"flag"`
	}
	type holder struct {
		Items []*subItem `json:"items"`
		Empty []*subItem `json:"empty,omitempty"`
		NilS  []*subItem `json:"nil_s"`
	}

	items := make([]*subItem, 70)
	for i := range items {
		if i%11 == 3 {
			continue
		}
		items[i] = &subItem{
			ID:   i,
			Tag:  fmt.Sprintf("t<%d>&\"x\"", i),
			Flag: i%2 == 0,
		}
	}
	src := holder{
		Items: items,
		Empty: []*subItem{},
		NilS:  nil,
	}

	bDef, err := sonic.ConfigDefault.Marshal(src)
	require.NoError(t, err)
	var decHolder, decStdHolder holder
	require.NoError(t, json.Unmarshal(bDef, &decStdHolder))
	require.NoError(t, sonic.ConfigStd.Unmarshal(bDef, &decHolder))
	require.Equal(t, decStdHolder, decHolder)

	rootSonic, err := sonic.ConfigStd.Marshal(items)
	require.NoError(t, err)
	var rootDec []*subItem
	require.NoError(t, sonic.Unmarshal(rootSonic, &rootDec))
	require.Equal(t, items, rootDec)

	type allMaps struct {
		MI8   map[string]int8     `json:"mi8"`
		MI16  map[string]int16    `json:"mi16"`
		MI32  map[string]int32    `json:"mi32"`
		MU    map[string]uint     `json:"mu"`
		MU8   map[string]uint8    `json:"mu8"`
		MU16  map[string]uint16   `json:"mu16"`
		MU32  map[string]uint32   `json:"mu32"`
		MUPtr map[string]uintptr  `json:"muptr"`
		MF32  map[string]float32  `json:"mf32"`
		MStr  map[string]subItem  `json:"mstr"`
		MPtr  map[string]*subItem `json:"mptr"`
	}
	mapJSON := `{
		"mi8":{"a":-128,"b":127,"z":null},
		"mi16":{"a":-32768,"b":32767,"z":null},
		"mi32":{"a":-2147483648,"b":2147483647,"z":null},
		"mu":{"a":0,"b":42,"z":null},
		"mu8":{"a":0,"b":255,"z":null},
		"mu16":{"a":0,"b":65535,"z":null},
		"mu32":{"a":0,"b":4294967295,"z":null},
		"muptr":{"a":1,"b":1024,"z":null},
		"mf32":{"a":-1.5,"b":3.25,"z":null},
		"mstr":{"k\u0031":{"id":1,"tag":"ok","flag":true},"z":null},
		"mptr":{"k1":{"id":2,"tag":"ptr","flag":false},"z":null}
	}`
	initMaps := func() allMaps {
		old := subItem{ID: 99, Tag: "old", Flag: true}
		return allMaps{
			MI8:   map[string]int8{"z": 9},
			MI16:  map[string]int16{"z": 9},
			MI32:  map[string]int32{"z": 9},
			MU:    map[string]uint{"z": 9},
			MU8:   map[string]uint8{"z": 9},
			MU16:  map[string]uint16{"z": 9},
			MU32:  map[string]uint32{"z": 9},
			MUPtr: map[string]uintptr{"z": 9},
			MF32:  map[string]float32{"z": 9.5},
			MStr:  map[string]subItem{"z": old},
			MPtr:  map[string]*subItem{"z": &old},
		}
	}
	gotMaps := initMaps()
	stdMaps := initMaps()
	require.NoError(t, json.Unmarshal([]byte(mapJSON), &stdMaps))
	require.NoError(t, sonic.ConfigStd.UnmarshalFromString(mapJSON, &gotMaps))
	require.Equal(t, stdMaps, gotMaps)

	var badI8 map[string]int8
	require.Error(t, sonic.UnmarshalString(`{"a":128}`, &badI8))
	var badU8 map[string]uint8
	require.Error(t, sonic.UnmarshalString(`{"a":256}`, &badU8))
	var badF32 map[string]float32
	require.Error(t, sonic.UnmarshalString(`{"a":1e50}`, &badF32))
}

type benchFlatEvent struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"user_id"`
	Code      int32   `json:"code"`
	Flags     uint32  `json:"flags"`
	LatencyMs float64 `json:"latency_ms"`
	Success   bool    `json:"success"`
	Service   string  `json:"service"`
	Endpoint  string  `json:"endpoint"`
	TraceID   string  `json:"trace_id"`
	Region    string  `json:"region"`
}

type benchPtrElem struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
	Valid bool    `json:"valid"`
}

func BenchmarkFlatStructDirect(b *testing.B) {
	raw := `{"id":9876543210,"user_id":12345678,"code":200,"flags":15,"latency_ms":12.375,"success":true,"service":"order-gateway","endpoint":"/api/v2/checkout","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","region":"ap-northeast-1"}`
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	var dst benchFlatEvent
	for i := 0; i < b.N; i++ {
		if err := sonic.ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkZeroCopySchemaProj(b *testing.B) {
	type projTarget struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		BookIDs []int  `json:"book_ids"`
	}
	noise := strings.Repeat(`"x",`, 250)
	raw := `{"id":12125924,"noise":[` + noise + `"end"],"title":"sonic-zero-copy-schema","book_ids":[101,102,103,104,105]}`
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	var dst projTarget
	for i := 0; i < b.N; i++ {
		if err := sonic.ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPtrStructSliceDecode(b *testing.B) {
	items := make([]*benchPtrElem, 32)
	for i := range items {
		items[i] = &benchPtrElem{ID: int64(i + 1), Name: "elem-name", Score: 98.5, Valid: true}
	}
	raw, _ := sonic.ConfigFastest.MarshalToString(items)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var dst []*benchPtrElem
		if err := sonic.ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMapStrI32Decode(b *testing.B) {
	raw := `{"k0":10,"k1":20,"k2":30,"k3":40,"k4":50,"k5":60,"k6":70,"k7":80,"k8":90,"k9":100,"k10":110,"k11":120}`
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var dst map[string]int32
		if err := sonic.ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecoder_Binding_FlatWithPtrAndNested(b *testing.B) {
	type nestedSpan struct {
		TraceID string `json:"trace_id"`
		SpanID  uint64 `json:"span_id"`
		Sampled bool   `json:"sampled"`
	}
	type richEvent struct {
		ID      int64      `json:"id"`
		Level   int8       `json:"level"`
		Code    uint16     `json:"code"`
		UserID  *int64     `json:"user_id"`
		Latency *float64   `json:"latency"`
		Tag     *string    `json:"tag"`
		Span    nestedSpan `json:"span"`
	}
	raw := `{"id":9001,"level":3,"code":200,"user_id":424242,"latency":4.125,"tag":"edge-gw","span":{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":778899,"sampled":true}}`
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	var dst richEvent
	for i := 0; i < b.N; i++ {
		if err := sonic.ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}
