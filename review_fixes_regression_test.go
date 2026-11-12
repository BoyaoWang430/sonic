//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
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
