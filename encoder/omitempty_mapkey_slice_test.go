//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package encoder_test

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/encoder"
	"github.com/stretchr/testify/require"
)

type customMarshalerVal int

func (v customMarshalerVal) MarshalJSON() ([]byte, error) {
	return []byte(`1`), nil
}

func TestNegativeZeroFloatOmitEmptyAndMinIntPreserved(t *testing.T) {
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

	gotFast, err := sonic.ConfigFastest.Marshal(v1)
	require.NoError(t, err)
	gotStd, err := sonic.ConfigStd.Marshal(v1)
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
	gotStd2, err := sonic.ConfigStd.Marshal(v2)
	require.NoError(t, err)
	gotFast2, err := sonic.ConfigFastest.Marshal(v2)
	require.NoError(t, err)
	require.Equal(t, string(stdBytes2), string(gotStd2))
	require.Equal(t, string(stdBytes2), string(gotFast2))
}

type ptrTextMarshaler struct {
	s string
}

func (p *ptrTextMarshaler) MarshalText() ([]byte, error) {
	if p == nil {
		return []byte("typed_nil"), nil
	}
	return []byte(p.s), nil
}

func TestNilInterfaceTextMarshalerMapKeyRejected(t *testing.T) {
	// Untyped nil interface key must fail on both unsorted and sorted modes (never "{null:1}" or `{"":1}`).
	mBad := map[encoding.TextMarshaler]int{nil: 1}
	_, errFast := sonic.ConfigFastest.Marshal(mBad)
	require.Error(t, errFast)
	_, errStd := sonic.ConfigStd.Marshal(mBad)
	require.Error(t, errStd)

	// Typed nil inside encoding.TextMarshaler interface still invokes MarshalText().
	var typedNil encoding.TextMarshaler = (*ptrTextMarshaler)(nil)
	mTypedNil := map[encoding.TextMarshaler]int{typedNil: 2}
	bFast, err := sonic.ConfigFastest.Marshal(mTypedNil)
	require.NoError(t, err)
	require.Equal(t, `{"typed_nil":2}`, string(bFast))
	bStd, err := sonic.ConfigStd.Marshal(mTypedNil)
	require.NoError(t, err)
	require.Equal(t, `{"typed_nil":2}`, string(bStd))

	// Concrete nil pointer key map[*ptrTextMarshaler]int{nil: 3} still encodes as "".
	mPtrNil := map[*ptrTextMarshaler]int{nil: 3}
	stdPtr, err := json.Marshal(mPtrNil)
	require.NoError(t, err)
	gotPtrFast, err := sonic.ConfigFastest.Marshal(mPtrNil)
	require.NoError(t, err)
	gotPtrStd, err := sonic.ConfigStd.Marshal(mPtrNil)
	require.NoError(t, err)
	require.Equal(t, string(stdPtr), string(gotPtrFast))
	require.Equal(t, string(stdPtr), string(gotPtrStd))
}

func TestEncodeIntoRollsBackBufferOnError(t *testing.T) {
	type partialBad struct {
		A string  `json:"a"`
		B float64 `json:"b"`
	}
	buf := append(make([]byte, 0, 128), "prefix:"...)
	err := encoder.EncodeInto(&buf, partialBad{A: "hello", B: math.NaN()}, 0)
	require.Error(t, err)
	require.Equal(t, "prefix:", string(buf))
}

func TestPtrStructAndSortedMapsParity(t *testing.T) {
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

		gotStd, err := sonic.ConfigStd.Marshal(tc)
		require.NoError(t, err, "case %d ConfigStd marshal", idx)
		require.Equal(t, string(want), string(gotStd), "case %d ConfigStd parity", idx)

		gotFast, err := sonic.ConfigFastest.Marshal(tc)
		require.NoError(t, err, "case %d ConfigFastest marshal", idx)
		require.True(t, json.Valid(gotFast), "case %d ConfigFastest valid JSON", idx)
	}
}

func TestPtrStructSliceEncodeSymmetry(t *testing.T) {
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

	bStd, err := json.Marshal(src)
	require.NoError(t, err)
	bSonicStd, err := sonic.ConfigStd.Marshal(src)
	require.NoError(t, err)
	require.Equal(t, string(bStd), string(bSonicStd))

	rootStd, err := json.Marshal(items)
	require.NoError(t, err)
	rootSonic, err := sonic.ConfigStd.Marshal(items)
	require.NoError(t, err)
	require.Equal(t, string(rootStd), string(rootSonic))

	noNullCfg := sonic.Config{NoNullSliceOrMap: true}.Froze()
	var nilPtrSlice []*subItem
	nnBytes, err := noNullCfg.Marshal(nilPtrSlice)
	require.NoError(t, err)
	require.Equal(t, "[]", string(nnBytes))
}

type benchPtrElem struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
	Valid bool    `json:"valid"`
}

func BenchmarkPtrStructSliceEncode(b *testing.B) {
	items := make([]*benchPtrElem, 32)
	for i := range items {
		items[i] = &benchPtrElem{ID: int64(i + 1), Name: "elem-name", Score: 98.5, Valid: true}
	}
	out, _ := sonic.ConfigFastest.Marshal(items)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := sonic.ConfigFastest.Marshal(items); err != nil {
			b.Fatal(err)
		}
	}
}
