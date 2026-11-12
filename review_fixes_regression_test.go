//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic/ast"
	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
	"github.com/stretchr/testify/require"
)

func TestReviewFix_P0_1_ASTNodeSetCorruptJSONNoCrash(t *testing.T) {
	badObjectInputs := []string{
		`{bad}`,
		`{"a": 1`,
		`{"a": `,
		`{"a": [1, 2`,
		`{"k1": "v1", "k2": tru}`,
	}
	for _, raw := range badObjectInputs {
		// 1. _V_RAW node from Searcher
		root, err := GetFromString(raw)
		if err == nil {
			_, setErr := root.Set("x", ast.NewString("val"))
			require.Error(t, setErr, "raw=%q should fail Set", raw)
			// Calling Set or Check again must not crash due to half-mutated state.
			_, setErr2 := root.Set("y", ast.NewNumber("2"))
			require.Error(t, setErr2, "raw=%q second Set should also fail cleanly", raw)
		}

		// 2. _V_OBJECT_LAZY node from NewParserObj
		p := ast.NewParserObj(raw)
		node, perr := p.Parse()
		if perr == 0 {
			_, setErr := node.Set("x", ast.NewString("val"))
			require.Error(t, setErr, "lazy=%q should fail Set", raw)

			p2 := ast.NewParserObj(raw)
			node2, perr2 := p2.Parse()
			if perr2 == 0 {
				_, unsetErr := node2.Unset("x")
				require.Error(t, unsetErr, "lazy=%q should fail Unset", raw)
			}
		}
	}

	// _V_ARRAY_LAZY SetByIndex on malformed array must return syntax error, not ErrNotExist.
	pa := ast.NewParserObj(`[1, 2`)
	arrNode, perr := pa.Parse()
	require.Zero(t, perr)
	ok, err := arrNode.SetByIndex(5, ast.NewNumber("9"))
	require.False(t, ok)
	require.Error(t, err)
	require.NotErrorIs(t, err, ast.ErrNotExist)
}

func TestReviewFix_P1_1_ControlCharBeforeBackslashRejected(t *testing.T) {
	type payload struct {
		Skip string `json:"skip_unused"`
		S    string `json:"s"`
	}

	// Test both short (<16B) and long (>32B, >64B) prefixes before raw control + valid escape.
	for _, padLen := range []int{0, 5, 15, 16, 28, 33, 65} {
		pad := strings.Repeat("a", padLen)
		cases := []string{
			fmt.Sprintf(`{"skip":"%s\x00b\\nc","s":"ok"}`, pad),
			fmt.Sprintf("{\"skip\":\"ok\",\"s\":\"%s\x00b\\\\nc\"}", pad),
			fmt.Sprintf("{\"skip\":\"%s\nline2\\\\t\",\"s\":\"ok\"}", pad),
			fmt.Sprintf("{\"s\":\"%s\t\\\\\\\"end\"}", pad),
			fmt.Sprintf("{\"s\":\"%s\x1f\\\\u0041\"}", pad),
		}
		// Replace literal \x00 in the first case with an actual NUL byte too.
		cases[0] = fmt.Sprintf("{\"skip\":\"%s\x00b\\\\nc\",\"s\":\"ok\"}", pad)

		for _, js := range cases {
			var stdOut payload
			stdErr := json.Unmarshal([]byte(js), &stdOut)
			require.Error(t, stdErr, "stdlib must reject %q", js)

			var sOut payload
			require.Error(t, ConfigStd.Unmarshal([]byte(js), &sOut), "ConfigStd.Unmarshal must reject %q", js)

			var anyOut any
			require.Error(t, ConfigStd.Unmarshal([]byte(js), &anyOut), "ConfigStd.Unmarshal(any) must reject %q", js)

			// ConfigDefault / ConfigFastest (ValidateString=false) allow unvalidated bytes.
			var fastOut payload
			require.NoError(t, ConfigFastest.Unmarshal([]byte(js), &fastOut))
			require.NoError(t, Unmarshal([]byte(js), &fastOut))
		}
	}

	// Ensure invalid escape before control char still reports invalid escape first.
	badEscBeforeCtrl := "{\"s\":\"\\q\x01\"}"
	var v any
	err := Unmarshal([]byte(badEscBeforeCtrl), &v)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escaped")
}

func TestReviewFix_P1_2_NullElementsInMapStringAndTypedSlices(t *testing.T) {
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
	require.NoError(t, Unmarshal(input, &got))
	require.Equal(t, want, got)

	// Also verify top-level slices and pre-populated slices match encoding/json.
	wantPre := []int{100, 200, 300}
	gotPre := []int{100, 200, 300}
	require.NoError(t, json.Unmarshal([]byte(`[1, null, 3]`), &wantPre))
	require.NoError(t, Unmarshal([]byte(`[1, null, 3]`), &gotPre))
	require.Equal(t, wantPre, gotPre)

	wantMap := map[string]string{"a": "old", "keep": "yes"}
	gotMap := map[string]string{"a": "old", "keep": "yes"}
	require.NoError(t, json.Unmarshal([]byte(`{"a": null, "b": null, "c": "new"}`), &wantMap))
	require.NoError(t, Unmarshal([]byte(`{"a": null, "b": null, "c": "new"}`), &gotMap))
	require.Equal(t, wantMap, gotMap)
}

func TestReviewFix_P1_3_MapsReplacePrepopulatedEntriesMatchingStdlib(t *testing.T) {
	type myStr string
	type subVal struct {
		Old bool
		New bool
	}

	t.Run("map[string]int", func(t *testing.T) {
		want := map[string]int{"k": 42, "other": 7}
		got := map[string]int{"k": 42, "other": 7}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]int64", func(t *testing.T) {
		want := map[string]int64{"k": 42}
		got := map[string]int64{"k": 42}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]uint64", func(t *testing.T) {
		want := map[string]uint64{"k": 42}
		got := map[string]uint64{"k": 42}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]float64", func(t *testing.T) {
		want := map[string]float64{"k": 3.5}
		got := map[string]float64{"k": 3.5}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]bool", func(t *testing.T) {
		want := map[string]bool{"k": true}
		got := map[string]bool{"k": true}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[int]string", func(t *testing.T) {
		want := map[int]string{1: "old", 2: "keep"}
		got := map[int]string{1: "old", 2: "keep"}
		require.NoError(t, json.Unmarshal([]byte(`{"1": null, "3": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"1": null, "3": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[int64]string", func(t *testing.T) {
		want := map[int64]string{1: "old"}
		got := map[int64]string{1: "old"}
		require.NoError(t, json.Unmarshal([]byte(`{"1": null, "3": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"1": null, "3": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("generic map[myStr]int", func(t *testing.T) {
		want := map[myStr]int{"k": 42}
		got := map[myStr]int{"k": 42}
		require.NoError(t, json.Unmarshal([]byte(`{"k": null, "new": null}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k": null, "new": null}`), &got))
		require.Equal(t, want, got)
	})

	t.Run("map[string]Struct replaces rather than merges", func(t *testing.T) {
		want := map[string]subVal{"k": {Old: true}}
		got := map[string]subVal{"k": {Old: true}}
		require.NoError(t, json.Unmarshal([]byte(`{"k":{"New":true}}`), &want))
		require.NoError(t, Unmarshal([]byte(`{"k":{"New":true}}`), &got))
		require.Equal(t, want, got)
	})
}

type customMarshalerVal int

func (v customMarshalerVal) MarshalJSON() ([]byte, error) {
	return []byte(`1`), nil
}

func TestReviewFix_P1_4_NegativeZeroFloatOmitEmptyAndMinIntPreserved(t *testing.T) {
	negZero64 := math.Copysign(0, -1)
	negZero32 := float32(math.Copysign(0, -1))

	// 1. Pure struct hitting encodeStructFieldsDefault & encodeStructFieldsSanitized
	type pureFloatStruct struct {
		F64 float64 `json:"f64,omitempty"`
		F32 float32 `json:"f32,omitempty"`
		I32 int32   `json:"i32,omitempty"`
		I64 int64   `json:"i64,omitempty"`
		S   string  `json:"s,omitempty"`
	}
	v1 := pureFloatStruct{
		F64: negZero64,
		F32: negZero32,
		I32: math.MinInt32,
		I64: math.MinInt64,
		S:   "<tag>",
	}
	stdBytes, err := json.Marshal(v1)
	require.NoError(t, err)

	gotFast, err := ConfigFastest.Marshal(v1)
	require.NoError(t, err)
	gotStd, err := ConfigStd.Marshal(v1)
	require.NoError(t, err)
	require.Equal(t, string(stdBytes), string(gotStd))
	require.NotContains(t, string(gotFast), `"f64"`)
	require.NotContains(t, string(gotFast), `"f32"`)
	require.Contains(t, string(gotFast), `"i32":-2147483648`)
	require.Contains(t, string(gotFast), `"i64":-9223372036854775808`)

	// 2. VM-dispatched struct (contains json.Marshaler field so it compiles per-field VM ops)
	type vmFloatStruct struct {
		M   customMarshalerVal `json:"m"`
		F64 float64            `json:"f64,omitempty"`
		F32 float32            `json:"f32,omitempty"`
		I32 int32              `json:"i32,omitempty"`
		I64 int64              `json:"i64,omitempty"`
	}
	v2 := vmFloatStruct{
		M:   1,
		F64: negZero64,
		F32: negZero32,
		I32: math.MinInt32,
		I64: math.MinInt64,
	}
	stdBytes2, err := json.Marshal(v2)
	require.NoError(t, err)
	gotStd2, err := ConfigStd.Marshal(v2)
	require.NoError(t, err)
	gotFast2, err := ConfigFastest.Marshal(v2)
	require.NoError(t, err)
	require.Equal(t, string(stdBytes2), string(gotStd2))
	require.Equal(t, string(stdBytes2), string(gotFast2))
}

type reviewPtrTM struct {
	s string
}

func (p *reviewPtrTM) MarshalText() ([]byte, error) {
	if p == nil {
		return []byte("typed_nil"), nil
	}
	return []byte(p.s), nil
}

func TestReviewFix_P1_5_NilInterfaceTextMarshalerMapKeyRejected(t *testing.T) {
	// Untyped nil interface key must fail on both unsorted and sorted modes (never "{null:1}" or `{"":1}`).
	mBad := map[encoding.TextMarshaler]int{nil: 1}
	_, errFast := ConfigFastest.Marshal(mBad)
	require.Error(t, errFast)
	_, errStd := ConfigStd.Marshal(mBad)
	require.Error(t, errStd)

	// Typed nil inside encoding.TextMarshaler interface still invokes MarshalText().
	var typedNil encoding.TextMarshaler = (*reviewPtrTM)(nil)
	mTypedNil := map[encoding.TextMarshaler]int{typedNil: 2}
	bFast, err := ConfigFastest.Marshal(mTypedNil)
	require.NoError(t, err)
	require.Equal(t, `{"typed_nil":2}`, string(bFast))
	bStd, err := ConfigStd.Marshal(mTypedNil)
	require.NoError(t, err)
	require.Equal(t, `{"typed_nil":2}`, string(bStd))

	// Concrete nil pointer key map[*reviewPtrTM]int{nil: 3} still encodes as "".
	mPtrNil := map[*reviewPtrTM]int{nil: 3}
	stdPtr, err := json.Marshal(mPtrNil)
	require.NoError(t, err)
	gotPtrFast, err := ConfigFastest.Marshal(mPtrNil)
	require.NoError(t, err)
	gotPtrStd, err := ConfigStd.Marshal(mPtrNil)
	require.NoError(t, err)
	require.Equal(t, string(stdPtr), string(gotPtrFast))
	require.Equal(t, string(stdPtr), string(gotPtrStd))
}

func TestReviewFix_P1_6_MultiChunkEscapedStringsRoundTrip(t *testing.T) {
	// Exercises >64B, >128B, >256B escaped strings and crossing escapes across RVV/SIMD chunk boundaries.
	for _, chunkPad := range []int{60, 62, 63, 64, 125, 127, 128, 253, 255, 256, 320} {
		orig := strings.Repeat("x", chunkPad) + "\n\t\"\\\u4e2d\U0001f680" + strings.Repeat("y", 150) + "\r\nend"
		encoded, err := Marshal(map[string]string{"k": orig})
		require.NoError(t, err)

		var out map[string]string
		require.NoError(t, ConfigStd.Unmarshal(encoded, &out))
		require.Equal(t, orig, out["k"])

		node, err := Get(encoded, "k")
		require.NoError(t, err)
		gotStr, err := node.String()
		require.NoError(t, err)
		require.Equal(t, orig, gotStr)
	}
}

func TestReviewFix_P2_1_SearcherCopyDoesNotMutateOrRace(t *testing.T) {
	raw := `{"a":{"b":"hello"},"arr":[10,20,30],"c":"world"}`
	s := ast.NewSearcher(raw)
	require.False(t, s.CopyReturn)

	nCopy, err := s.GetByPathCopy("a", "b")
	require.NoError(t, err)
	strCopy, err := nCopy.String()
	require.NoError(t, err)
	require.Equal(t, "hello", strCopy)
	require.False(t, s.CopyReturn, "GetByPathCopy must not mutate Searcher.CopyReturn")

	manyCopy, err := s.GetManyByPathCopy([]interface{}{"a", "b"}, []interface{}{"c"})
	require.NoError(t, err)
	require.Len(t, manyCopy, 2)
	require.False(t, s.CopyReturn, "GetManyByPathCopy must not mutate Searcher.CopyReturn")

	// Concurrent calls on a shared *Searcher must be race-free under -race.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for iter := 0; iter < 50; iter++ {
				if (id+iter)%2 == 0 {
					n, err := s.GetByPath("arr", 1)
					require.NoError(t, err)
					v, err := n.Int64()
					require.NoError(t, err)
					require.Equal(t, int64(20), v)
				} else {
					n, err := s.GetByPathCopy("c")
					require.NoError(t, err)
					v, err := n.String()
					require.NoError(t, err)
					require.Equal(t, "world", v)
				}
				ns, err := s.GetManyByPathCopy([]interface{}{"a", "b"}, []interface{}{"arr", 2})
				require.NoError(t, err)
				require.Len(t, ns, 2)
			}
		}(g)
	}
	wg.Wait()
	require.False(t, s.CopyReturn)
}

func TestReviewFix_P2_2_PatchByPathFloatFormattingConsistency(t *testing.T) {
	src := []byte(`{"f64":0,"f32":0}`)
	patched, err := ast.PatchByPath(src,
		ast.PathPatch{Path: []interface{}{"f64"}, Value: float64(1000000)},
		ast.PathPatch{Path: []interface{}{"f32"}, Value: float32(1000000)},
	)
	require.NoError(t, err)

	want, err := Marshal(struct {
		F64 float64 `json:"f64"`
		F32 float32 `json:"f32"`
	}{F64: 1000000, F32: 1000000})
	require.NoError(t, err)
	require.Equal(t, string(want), string(patched))
}

func TestReviewFix_P2_3_EncodeIntoRollsBackBufferOnError(t *testing.T) {
	type partialBad struct {
		A string  `json:"a"`
		B float64 `json:"b"`
	}
	buf := append(make([]byte, 0, 128), "prefix:"...)
	err := encoder.EncodeInto(&buf, partialBad{A: "hello", B: math.NaN()}, 0)
	require.Error(t, err)
	require.Equal(t, "prefix:", string(buf))
}

func TestReviewFix_P2_4_LoadsUseNumberReturnsSyntaxError(t *testing.T) {
	_, _, errLoads := ast.Loads(`{"a":`)
	_, _, errUseNum := ast.LoadsUseNumber(`{"a":`)
	require.Error(t, errLoads)
	require.Error(t, errUseNum)
	require.Equal(t, reflect.TypeOf(errLoads), reflect.TypeOf(errUseNum))
	require.Equal(t, errLoads.Error(), errUseNum.Error())
	require.Contains(t, errUseNum.Error(), "Syntax error at index")
}

func TestReviewFix_P2_5_CustomSliceMismatchErrorTypePreserved(t *testing.T) {
	type MyInt int64
	type MyInts []int64

	var customSlice MyInts
	err1 := Unmarshal([]byte(`"not_array"`), &customSlice)
	var mm1 *decoder.MismatchTypeError
	require.ErrorAs(t, err1, &mm1)
	require.Equal(t, reflect.TypeOf(MyInts(nil)), mm1.Type)

	var customElemSlice []MyInt
	err2 := Unmarshal([]byte(`["not_int"]`), &customElemSlice)
	var mm2 *decoder.MismatchTypeError
	require.ErrorAs(t, err2, &mm2)
	require.Equal(t, reflect.TypeOf(MyInt(0)), mm2.Type)

	var plainInts []int
	err3 := Unmarshal([]byte(`["not_int"]`), &plainInts)
	var mm3 *decoder.MismatchTypeError
	require.ErrorAs(t, err3, &mm3)
	require.Equal(t, reflect.TypeOf(int(0)), mm3.Type)

	var plainUints []uint
	err4 := Unmarshal([]byte(`["not_uint"]`), &plainUints)
	var mm4 *decoder.MismatchTypeError
	require.ErrorAs(t, err4, &mm4)
	require.Equal(t, reflect.TypeOf(uint(0)), mm4.Type)
}

func TestReviewFix_P2_6_GiantPayloadWithRawMessageSchemaBailoutUsesStreamSession(t *testing.T) {
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
	require.NoError(t, Unmarshal(b.Bytes(), &out))
	require.Equal(t, 42, out.ID)
	require.Equal(t, `{"hello":[1,2,3]}`, string(out.Raw))
	require.Equal(t, []int{7, 8, 9}, out.Nums)

	// Also verify DecodeWithSession path via Decoder.Decode.
	var outStream giantRawEnv
	dec := ConfigDefault.NewDecoder(bytes.NewReader(b.Bytes()))
	require.NoError(t, dec.Decode(&outStream))
	require.Equal(t, out, outStream)
}

func TestOptBatchA_PtrStructAndSortedMapsParity(t *testing.T) {
	type leafDense struct {
		Tag  string `json:"tag"`
		Code int64  `json:"code"`
	}
	type leafSparse struct {
		Opt string `json:"opt,omitempty"`
		Val int    `json:"val"`
	}
	type rootPtrStruct struct {
		First    *leafDense  `json:"first"`
		OmitLead *leafSparse `json:"omit_lead,omitempty"`
		Mid      *leafSparse `json:"mid"`
		Tail     string      `json:"tail"`
	}
	type ptrFreeBox struct {
		X float64 `json:"x"`
		Y int64   `json:"y"`
		Z bool    `json:"z"`
	}

	cases := []any{
		rootPtrStruct{First: nil, OmitLead: nil, Mid: nil, Tail: "a&b"},
		rootPtrStruct{
			First:    &leafDense{Tag: "<ok>", Code: 7},
			OmitLead: &leafSparse{Opt: "", Val: 9},
			Mid:      &leafSparse{Opt: "hi", Val: 11},
			Tail:     "done",
		},
		&rootPtrStruct{
			First:    nil,
			OmitLead: &leafSparse{Opt: "x&y", Val: 3},
			Mid:      nil,
			Tail:     "<end>",
		},
		ptrFreeBox{X: 1.5, Y: -42, Z: true},
		map[string]string{"z": "<1>", "a": "2&3", "m": "plain"},
		map[string]int{"c": -3, "a": 1, "b": 2},
		map[string]int64{"k2": 200, "k1": -100},
		map[string]uint64{"u2": 999, "u1": 0},
		map[string]float64{"f2": 2.5, "f1": -0.25},
		map[string]bool{"t": true, "f": false},
	}

	for idx, tc := range cases {
		want, err := json.Marshal(tc)
		require.NoError(t, err, "case %d std marshal", idx)

		gotStd, err := ConfigStd.Marshal(tc)
		require.NoError(t, err, "case %d ConfigStd marshal", idx)
		require.Equal(t, string(want), string(gotStd), "case %d ConfigStd parity", idx)

		gotFast, err := ConfigFastest.Marshal(tc)
		require.NoError(t, err, "case %d ConfigFastest marshal", idx)
		require.True(t, json.Valid(gotFast), "case %d ConfigFastest valid JSON", idx)
	}
}

func TestOptBatchC_DecoderPtrStructSlabAndEfaceTables(t *testing.T) {
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
	require.NoError(t, ConfigStd.Unmarshal(raw, &got))
	require.Equal(t, items, got)

	// Verify 1-char ASCII strings & negative integer tables in generic interface{} decode with CopyString.
	mutBuf := []byte(`{"empty":"","one":"Z","neg":-42,"big_neg":-5000,"arr":["0","9",-1,-128,42]}`)
	var genericOut any
	require.NoError(t, ConfigStd.Unmarshal(mutBuf, &genericOut))
	var stdOut any
	require.NoError(t, json.Unmarshal(mutBuf, &stdOut))
	// Overwrite mutBuf in place to confirm CopyString isolation.
	for i := range mutBuf {
		mutBuf[i] = '#'
	}
	require.Equal(t, stdOut, genericOut)
}

func TestOptBatchD_ASTMaterializedFastPathsAndTokenObjectSWAR(t *testing.T) {
	// Exercise tokenObject SWAR short/medium/long keys, backslash-after-quote in same 8B word, and escaped keys.
	raw := `[{"a":"\\","abcdefghi":"0123456789ab","seventeen_byte_key":"val_over_sixteen_bytes","esc\"key":"esc\"val","sp" : "x"},{"target":99}]`
	target, err := Get([]byte(raw), 1, "target")
	require.NoError(t, err)
	v, err := target.Int64()
	require.NoError(t, err)
	require.Equal(t, int64(99), v)

	r0 := ast.NewRaw(raw)
	require.NoError(t, r0.Check())

	// Materialized V_ARRAY (>16 elements to cover both head and tail chunks) + UnsetByIndex + Index bounds.
	var arrNodes []ast.Node
	for i := 0; i < 20; i++ {
		arrNodes = append(arrNodes, ast.NewNumber(fmt.Sprintf("%d", i)))
	}
	arr := ast.NewArray(arrNodes)
	n18, err := arr.Index(18).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(18), n18)
	require.Nil(t, arr.Index(20))
	require.Nil(t, arr.Index(-1))

	ok, err := arr.UnsetByIndex(2)
	require.NoError(t, err)
	require.True(t, ok)
	shifted, err := arr.Index(2).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(3), shifted)
	ok, err = arr.UnsetByIndex(99)
	require.Error(t, err)
	require.False(t, ok)

	// Materialized V_OBJECT (>16 pairs) + Get + Index + UnsetByIndex.
	var pairs []ast.Pair
	for i := 0; i < 20; i++ {
		pairs = append(pairs, ast.NewPair(fmt.Sprintf("k%d", i), ast.NewNumber(fmt.Sprintf("%d", i*10))))
	}
	obj := ast.NewObject(pairs)
	gv, err := obj.Get("k17").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(170), gv)
	require.Nil(t, obj.Get("missing"))
	iv, err := obj.Index(17).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(170), iv)
	require.False(t, obj.Index(25).Valid())
	ok, err = obj.UnsetByIndex(1)
	require.NoError(t, err)
	require.True(t, ok)
	afterUnset, err := obj.Index(1).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(20), afterUnset)
}

func TestReviewRound2_P1_1_ASTTombstoneEmptyKeyAndIndex(t *testing.T) {
	// 1. Object containing a valid empty key "": Unset("a") must not shadow "": 42 or produce duplicate "".
	n1, err := GetFromString(`{"a": 1, "": 42}`)
	require.NoError(t, err)
	existed, err := n1.Unset("a")
	require.NoError(t, err)
	require.True(t, existed)

	emptyVal, err := n1.Get("").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(42), emptyVal)

	replaced, err := n1.Set("", ast.NewNumber("99"))
	require.NoError(t, err)
	require.True(t, replaced)

	out1, err := n1.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, `{"":99}`, string(out1))

	m1, err := n1.MapUseNode()
	require.NoError(t, err)
	require.Len(t, m1, 1)
	mn := m1[""]
	v99, err := mn.Int64()
	require.NoError(t, err)
	require.Equal(t, int64(99), v99)

	// 2. Object without empty key: Unset("a") must still return nil / !Valid() / ErrNotExist on Get("").
	n2, err := GetFromString(`{"a": 1, "b": 2}`)
	require.NoError(t, err)
	existed, err = n2.Unset("a")
	require.NoError(t, err)
	require.True(t, existed)
	gEmpty := n2.Get("")
	require.Nil(t, gEmpty)
	require.False(t, gEmpty.Valid())
	require.ErrorIs(t, gEmpty.Check(), ast.ErrNotExist)

	// 3. Large indexed object (>16 keys, triggers BuildIndex) with "", Unset, UnsetByIndex, SortKeys, Pop.
	var pairs []ast.Pair
	pairs = append(pairs, ast.NewPair("", ast.NewNumber("777")))
	for i := 0; i < 20; i++ {
		pairs = append(pairs, ast.NewPair(fmt.Sprintf("k%02d", i), ast.NewNumber(fmt.Sprintf("%d", i))))
	}
	bigObj := ast.NewObject(pairs)
	require.NoError(t, bigObj.LoadAll())

	ok, err := bigObj.Unset("k00")
	require.NoError(t, err)
	require.True(t, ok)
	require.Nil(t, bigObj.Get("k00"))

	ok, err = bigObj.UnsetByIndex(1) // removes k01
	require.NoError(t, err)
	require.True(t, ok)
	require.Nil(t, bigObj.Get("k01"))

	// Unset trailing k19 then Pop (exercises Pop over trailing tombstones without deleting "" from index).
	ok, err = bigObj.Unset("k19")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, bigObj.Pop()) // pops k18
	require.Nil(t, bigObj.Get("k18"))

	require.NoError(t, bigObj.SortKeys(false))
	v777, err := bigObj.Get("").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(777), v777)

	// 4. Explicit Set("", Node{}) placeholder remains addressable via Get("") (TestNodeAny contract).
	placeholderObj := ast.Node{}
	_, err = placeholderObj.Set("", ast.Node{})
	require.NoError(t, err)
	require.NoError(t, placeholderObj.Get("").AddAny(nil))
	pOut, err := placeholderObj.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, `{"":[null]}`, string(pOut))

	// 5. Soft-deleted array ArrayUseNode consistency.
	arrNode, err := GetFromString(`[10, 20, 30]`)
	require.NoError(t, err)
	ok, err = arrNode.UnsetByIndex(0)
	require.NoError(t, err)
	require.True(t, ok)
	elems, err := arrNode.ArrayUseNode()
	require.NoError(t, err)
	require.Len(t, elems, 2)
	e0, _ := elems[0].Int64()
	e1, _ := elems[1].Int64()
	require.Equal(t, int64(20), e0)
	require.Equal(t, int64(30), e1)
}

func TestReviewRound2_P1_2_AsSliceEfaceErrorReturned(t *testing.T) {
	// End-to-end verification alongside optdec.TestAsSliceEfacePropagatesElementError.
	var top []any
	err := Unmarshal([]byte(`[1, 1e999, "ok"]`), &top)
	require.Error(t, err)

	type wrap struct {
		Items []any `json:"items"`
	}
	var w wrap
	err2 := Unmarshal([]byte(`{"items":[1, 1e999, "ok"]}`), &w)
	require.Error(t, err2)
}

func TestReviewRound2_P1_3_AsSliceBytesArrayNullAndMismatch(t *testing.T) {
	// 1. Pre-populated slice preserves element on null.
	s1 := []byte{9, 8, 7, 6}
	std1 := []byte{9, 8, 7, 6}
	require.NoError(t, Unmarshal([]byte(`[1, null, 3]`), &s1))
	require.NoError(t, json.Unmarshal([]byte(`[1, null, 3]`), &std1))
	require.Equal(t, std1, s1)

	// 2. Nested object inside []byte array skips subtree cleanly via Next() and reports byte element type.
	s2 := []byte{9, 8, 7, 6}
	std2 := []byte{9, 8, 7, 6}
	errSonic := Unmarshal([]byte(`[{"a": 1}, 2]`), &s2)
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
	errElem := Unmarshal([]byte(`[1, "x", 3]`), &mb)
	var mmElem *decoder.MismatchTypeError
	require.ErrorAs(t, errElem, &mmElem)
	require.Equal(t, reflect.TypeOf(MyByte(0)), mmElem.Type)
	require.Equal(t, MyBytes{1, 0, 3}, mb)

	var mb2 MyBytes
	errOuter := Unmarshal([]byte(`123`), &mb2)
	var mmOuter *decoder.MismatchTypeError
	require.ErrorAs(t, errOuter, &mmOuter)
	require.Equal(t, reflect.TypeOf(MyBytes(nil)), mmOuter.Type)
}

func TestReviewRound2_P2_1_PtrStructAndLargeStrPtrGCReclaim(t *testing.T) {
	type richItem struct {
		ID      int    `json:"id"`
		Payload []byte `json:"payload"`
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
		require.NoError(t, Unmarshal(raw, &decoded))
		require.Len(t, decoded, 16)
		// Attach finalizers to sibling *richItem and sibling *string (> maxArenaStrLen)
		// while keeping decoded[0] alive.
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

func TestReviewRound2_Opt1_ZeroCopySchemaProjAndEscapedCoexistence(t *testing.T) {
	type mixedProj struct {
		Title   string   `json:"title"`
		Escaped string   `json:"escaped"`
		IDs     []int    `json:"ids"`
		Dynamic []any    `json:"dynamic"`
		TailNum float64  `json:"tail_num"`
		TailStr string   `json:"tail_str"`
		Raw     []byte   `json:"raw"`
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
		require.NoError(t, ConfigStd.UnmarshalFromString(payload, &gotSonic))
		require.Equal(t, gotStd, gotSonic)

		// Truncated prefixes near tail boundaries must always error cleanly without out-of-bounds reads.
		for cut := 1; cut <= 8 && cut < len(payload); cut++ {
			var bad mixedProj
			require.Error(t, UnmarshalString(payload[:len(payload)-cut], &bad))
		}
	}

	// Multi-value JSONL stream over zero-copy schema projection.
	line := `{"title":"row","escaped":"x\ny","ids":[1,2],"tail_num":9.5,"tail_str":"end"}`
	stream := strings.Repeat(line+"\n", 8)
	dec := ConfigStd.NewDecoder(strings.NewReader(stream))
	_ = dec
	sdec := decoder.NewDecoder(stream)
	sdec.ValidateString()
	for i := 0; i < 8; i++ {
		var row mixedProj
		require.NoError(t, sdec.Decode(&row))
		require.Equal(t, "x\ny", row.Escaped)
		require.Equal(t, []int{1, 2}, row.IDs)
	}
}

func TestReviewRound2_Opt2_FlatStructDirectDecode(t *testing.T) {
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
	require.NoError(t, ConfigStd.UnmarshalFromString(raw, &gotSonic))
	require.Equal(t, gotStd, gotSonic)

	// CopyString independence: mutating input buffer must not mutate decoded strings.
	buf := []byte(`{"id":42,"name":"immutable-copy","comment":"second-copy"}`)
	var copied flatRecord
	decCopy := decoder.NewDecoder(string(buf))
	decCopy.CopyString()
	require.NoError(t, Unmarshal(buf, &copied)) // ConfigDefault sets CopyString=true
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
	require.NoError(t, UnmarshalString(dupJSON, &pre))
	require.Equal(t, preStd, pre)
	require.Equal(t, int64(200), pre.ID)
	require.Equal(t, "keep", pre.Name)
	require.False(t, pre.Active)

	// Speculative rollback atomicity: if a syntax or mismatch error occurs mid-object,
	// tryDecodeFlatStruct must not have partially mutated fields before falling back,
	// and final result must match Sonic's standard decoder behavior.
	errInput := `{"id":999,"i32":"not_an_int","name":"after_err"}`
	var errGot flatRecord
	require.Error(t, UnmarshalString(errInput, &errGot))
	require.Equal(t, int64(999), errGot.ID)
	require.Equal(t, "after_err", errGot.Name)

	// Syntax error before '}' leaves pre-populated fields untouched on bailout if error occurs early.
	var syntaxGot, syntaxStd flatRecord
	syntaxGot.Name = "untouched"
	syntaxStd.Name = "untouched"
	badSyntax := `{"id":123, broken}`
	require.Error(t, json.Unmarshal([]byte(badSyntax), &syntaxStd))
	require.Error(t, UnmarshalString(badSyntax, &syntaxGot))
	require.Equal(t, "untouched", syntaxGot.Name)

	// Invalid UTF-8 replacement under ConfigStd (ValidateString = true).
	badUTF8 := "{\"id\":7,\"name\":\"bad" + string([]byte{0xff}) + "byte\"}"
	var utf8Got flatRecord
	require.NoError(t, ConfigStd.UnmarshalFromString(badUTF8, &utf8Got))
	require.Equal(t, "bad\ufffdbyte", utf8Got.Name)

	// DisallowUnknownFields & CaseSensitive.
	strictCfg := Config{
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

func TestReviewRound2_Opt3_PtrStructSliceAndTypedMapSymmetry(t *testing.T) {
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
			continue // nil element pointer
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

	// Field OP_slice_ptr_struct (both sanitized ConfigStd and unsanitized ConfigDefault).
	bStd, err := json.Marshal(src)
	require.NoError(t, err)
	bSonicStd, err := ConfigStd.Marshal(src)
	require.NoError(t, err)
	require.Equal(t, string(bStd), string(bSonicStd))

	bDef, err := ConfigDefault.Marshal(src)
	require.NoError(t, err)
	var decHolder, decStdHolder holder
	require.NoError(t, json.Unmarshal(bDef, &decStdHolder))
	require.NoError(t, ConfigStd.Unmarshal(bDef, &decHolder))
	require.Equal(t, decStdHolder, decHolder)

	// Root []*subItem encode & decode (including nil, empty, NoNullSliceOrMap).
	rootStd, err := json.Marshal(items)
	require.NoError(t, err)
	rootSonic, err := ConfigStd.Marshal(items)
	require.NoError(t, err)
	require.Equal(t, string(rootStd), string(rootSonic))

	var rootDec []*subItem
	require.NoError(t, Unmarshal(rootSonic, &rootDec))
	require.Equal(t, items, rootDec)

	noNullCfg := Config{NoNullSliceOrMap: true}.Froze()
	var nilPtrSlice []*subItem
	nnBytes, err := noNullCfg.Marshal(nilPtrSlice)
	require.NoError(t, err)
	require.Equal(t, "[]", string(nnBytes))
	_ = encoder.EscapeHTML

	// Typed map[string]T fast paths (all signed/unsigned widths, float32, SubStruct, *SubStruct).
	type allMaps struct {
		MI8    map[string]int8     `json:"mi8"`
		MI16   map[string]int16    `json:"mi16"`
		MI32   map[string]int32    `json:"mi32"`
		MU     map[string]uint     `json:"mu"`
		MU8    map[string]uint8    `json:"mu8"`
		MU16   map[string]uint16   `json:"mu16"`
		MU32   map[string]uint32   `json:"mu32"`
		MUPtr  map[string]uintptr  `json:"muptr"`
		MF32   map[string]float32  `json:"mf32"`
		MStr   map[string]subItem  `json:"mstr"`
		MPtr   map[string]*subItem `json:"mptr"`
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
	require.NoError(t, ConfigStd.UnmarshalFromString(mapJSON, &gotMaps))
	require.Equal(t, stdMaps, gotMaps)

	// Overflow checks on narrow map types.
	var badI8 map[string]int8
	require.Error(t, UnmarshalString(`{"a":128}`, &badI8))
	var badU8 map[string]uint8
	require.Error(t, UnmarshalString(`{"a":256}`, &badU8))
	var badF32 map[string]float32
	require.Error(t, UnmarshalString(`{"a":1e50}`, &badF32))
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

func BenchmarkReviewRound2_FlatStructDirect(b *testing.B) {
	raw := `{"id":9876543210,"user_id":12345678,"code":200,"flags":15,"latency_ms":12.375,"success":true,"service":"order-gateway","endpoint":"/api/v2/checkout","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","region":"ap-northeast-1"}`
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	var dst benchFlatEvent
	for i := 0; i < b.N; i++ {
		if err := ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReviewRound2_ZeroCopySchemaProj(b *testing.B) {
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
		if err := ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReviewRound2_PtrStructSliceEncode(b *testing.B) {
	items := make([]*benchPtrElem, 32)
	for i := range items {
		items[i] = &benchPtrElem{ID: int64(i + 1), Name: "elem-name", Score: 98.5, Valid: true}
	}
	out, _ := ConfigFastest.Marshal(items)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ConfigFastest.Marshal(items); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReviewRound2_PtrStructSliceDecode(b *testing.B) {
	items := make([]*benchPtrElem, 32)
	for i := range items {
		items[i] = &benchPtrElem{ID: int64(i + 1), Name: "elem-name", Score: 98.5, Valid: true}
	}
	raw, _ := ConfigFastest.MarshalToString(items)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var dst []*benchPtrElem
		if err := ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReviewRound2_MapStrI32Decode(b *testing.B) {
	raw := `{"k0":10,"k1":20,"k2":30,"k3":40,"k4":50,"k5":60,"k6":70,"k7":80,"k8":90,"k9":100,"k10":110,"k11":120}`
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var dst map[string]int32
		if err := ConfigFastest.UnmarshalFromString(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}
