/*
 * Copyright 2022 ByteDance Inc.
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

package sonic

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompatUnmarshalStd(t *testing.T) {
	var sobj = map[string]interface{}{}
	var jobj = map[string]interface{}{}
	var data = []byte(`{"a":1.00000001E-10}`)
	var str = string(data)
	serr := ConfigStd.UnmarshalFromString(str, &sobj)
	jerr := json.Unmarshal(data, &jobj)
	require.Equal(t, jerr, serr)
	require.Equal(t, jobj, sobj)
	data[2] = '0'
	require.Equal(t, jobj, sobj)

	sobj = map[string]interface{}{}
	jobj = map[string]interface{}{}
	data = []byte(`{"a":1}`)
	cfg := Config{
		UseNumber: true,
	}.Froze()
	serr = cfg.Unmarshal(data, &sobj)
	dec := json.NewDecoder(bytes.NewBuffer(data))
	dec.UseNumber()
	jerr = dec.Decode(&jobj)
	require.Equal(t, jerr, serr)
	require.Equal(t, jobj, sobj)

	x := struct {
		A json.Number
		B json.Number
	}{}
	y := struct {
		A json.Number
		B json.Number
	}{}
	data = []byte(`{"A":"1", "C":-1, "B":1}`)
	cfg = Config{
		DisallowUnknownFields: true,
	}.Froze()
	serr = cfg.Unmarshal(data, &x)
	dec = json.NewDecoder(bytes.NewBuffer(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	jerr = dec.Decode(&y)
	require.Equal(t, jerr, serr)
	// require.Equal(t, y, x)
}

func TestCompatMarshalStd(t *testing.T) {
	t.Parallel()
	var obj = map[string]interface{}{
		"c": json.RawMessage(" [ \"<&>\" ] "),
		"b": json.RawMessage(" [ ] "),
	}
	sout, serr := ConfigStd.Marshal(obj)
	jout, jerr := json.Marshal(obj)
	require.Equal(t, jerr, serr)
	require.Equal(t, string(jout), string(sout))

	obj = map[string]interface{}{
		"a": json.RawMessage(" [} "),
	}
	sout, serr = ConfigStd.Marshal(obj)
	jout, jerr = json.Marshal(obj)
	require.NotNil(t, jerr)
	require.NotNil(t, serr)
	require.Equal(t, string(jout), string(sout))

	obj = map[string]interface{}{
		"a": json.RawMessage("1"),
	}
	sout, serr = ConfigStd.MarshalIndent(obj, "xxxx", "  ")
	jout, jerr = json.MarshalIndent(obj, "xxxx", "  ")
	require.Equal(t, jerr, serr)
	require.Equal(t, string(jout), string(sout))
}

func TestCompatEncoderStd(t *testing.T) {
	var o = map[string]interface{}{
		"a": "<>",
		"b": json.RawMessage(" [ ] "),
	}
	var w1 = bytes.NewBuffer(nil)
	var w2 = bytes.NewBuffer(nil)
	var enc1 = json.NewEncoder(w1)
	var enc2 = ConfigStd.NewEncoder(w2)

	require.Nil(t, enc1.Encode(o))
	require.Nil(t, enc2.Encode(o))
	require.Equal(t, w1.String(), w2.String())

	enc1.SetEscapeHTML(true)
	enc2.SetEscapeHTML(true)
	enc1.SetIndent("\n", "  ")
	enc2.SetIndent("\n", "  ")
	require.Nil(t, enc1.Encode(o))
	require.Nil(t, enc2.Encode(o))
	require.Equal(t, w1.String(), w2.String())

	enc1.SetEscapeHTML(false)
	enc2.SetEscapeHTML(false)
	enc1.SetIndent("", "")
	enc2.SetIndent("", "")
	require.Nil(t, enc1.Encode(o))
	require.Nil(t, enc2.Encode(o))
	require.Equal(t, w1.String(), w2.String())
}

func TestCompatDecoderStd(t *testing.T) {
	var o1 = map[string]interface{}{}
	var o2 = map[string]interface{}{}
	var s = `{"a":"b"} {"1":"2"} a {}`
	var w1 = bytes.NewBuffer([]byte(s))
	var w2 = bytes.NewBuffer([]byte(s))
	var enc1 = json.NewDecoder(w1)
	var enc2 = ConfigStd.NewDecoder(w2)

	require.Equal(t, enc1.More(), enc2.More())
	require.Nil(t, enc1.Decode(&o1))
	require.Nil(t, enc2.Decode(&o2))
	require.Equal(t, w1.String(), w2.String())

	require.Equal(t, enc1.More(), enc2.More())
	require.Nil(t, enc1.Decode(&o1))
	require.Nil(t, enc2.Decode(&o2))
	require.Equal(t, w1.String(), w2.String())

	require.Equal(t, enc1.More(), enc2.More())
	require.NotNil(t, enc1.Decode(&o1))
	require.NotNil(t, enc2.Decode(&o2))
	require.Equal(t, w1.String(), w2.String())
}

func TestPretouch(t *testing.T) {
	var v map[string]interface{}
	if err := Pretouch(reflect.TypeOf(v)); err != nil {
		t.Errorf("err:%v", err)
	}

	if err := Pretouch(reflect.TypeOf(v),
		option.WithCompileRecursiveDepth(1),
		option.WithCompileMaxInlineDepth(2),
	); err != nil {
		t.Errorf("err:%v", err)
	}
}

func TestGet(t *testing.T) {
	var data = `{"a":"b"}`
	r, err := GetFromString(data, "a")
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.String()
	if err != nil {
		t.Fatal(err)
	}
	if v != "b" {
		t.Fatal(v)
	}
}

func TestUnmarshalWithTrailingChars(t *testing.T) {
	for i, str := range []string{
		"123",
		"123 ",
		"{} [] ",
		"{} [ ",
		"[],",
		"[]null",
		"false null",
	} {
		var msg1, msg2 json.RawMessage
		err1 := json.Unmarshal([]byte(str), &msg1)
		err2 := ConfigStd.UnmarshalFromString(str, &msg2)
		require.Equal(t, err1 == nil, err2 == nil, i)
		// sonic will not clear the unmarshaled value here, but encoding/json will
		// require.Equal(t, msg1, msg2)
	}
}

func TestUnmarshalJSONSuite(t *testing.T) {
	gzFile, err := os.Open("testdata/JSONTestSuite/testdata.json.gz")
	require.NoError(t, err)
	defer gzFile.Close()

	gzReader, err := gzip.NewReader(gzFile)
	require.NoError(t, err)
	defer gzReader.Close()

	data, err := io.ReadAll(gzReader)
	require.NoError(t, err)

	var tests map[string]string
	err = json.Unmarshal(data, &tests)
	require.NoError(t, err)

	for name, tt := range tests {
		b := []byte(tt)
		t.Run(name, func(t *testing.T) {
			serr := ConfigStd.Unmarshal(b, new(json.RawMessage))
			jerr := json.Unmarshal(b, new(json.RawMessage))
			assert.Equal(t, jerr != nil, serr != nil, "json: %v, sonic: %v", jerr, serr)

			serr = ConfigStd.Unmarshal(b, new(interface{}))
			jerr = json.Unmarshal(b, new(interface{}))
			assert.Equal(t, jerr != nil, serr != nil, "json: %v, sonic: %v", jerr, serr)
		})
	}
}

func TestCompatEncodeIntoAndMultiValueDecoder(t *testing.T) {
	// 1. EncodeInto must preserve existing prefix and never append a trailing newline.
	buf := []byte("prefix:")
	require.NoError(t, encoder.EncodeInto(&buf, map[string]string{"k": "<v>"}, encoder.EscapeHTML))
	require.Equal(t, `prefix:{"k":"\u003cv\u003e"}`, string(buf))

	// 2. Multi-value string decoder.NewDecoder must advance Pos() across calls and error at end.
	dec := decoder.NewDecoder("{\"a\":1}\n{\"a\":2} ")
	type item struct {
		A int `json:"a"`
	}
	var it1, it2, it3 item
	require.NoError(t, dec.Decode(&it1))
	require.Equal(t, 1, it1.A)
	require.Greater(t, dec.Pos(), 0)
	require.NoError(t, dec.Decode(&it2))
	require.Equal(t, 2, it2.A)
	require.Error(t, dec.Decode(&it3))

	// 3. StreamDecoder returns io.EOF after consuming all values.
	sdec := ConfigDefault.NewDecoder(bytes.NewBufferString("{\"a\":3}\n{\"a\":4} "))
	var sit1, sit2, sit3 item
	require.NoError(t, sdec.Decode(&sit1))
	require.NoError(t, sdec.Decode(&sit2))
	require.Equal(t, 4, sit2.A)
	require.Equal(t, io.EOF, sdec.Decode(&sit3))
}

func parseGoMinorVersion(ver string) (int, bool) {
	idx := strings.Index(ver, "go1.")
	if idx < 0 {
		return 0, false
	}
	rest := ver[idx+len("go1."):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	minor, err := strconv.Atoi(rest[:end])
	return minor, err == nil
}

func TestAPIKindPlatformAndBuildTagContract(t *testing.T) {
	want := UseStdJSON
	if !testSonicCompatBuildTag {
		minor, ok := parseGoMinorVersion(runtime.Version())
		if ok && minor >= 20 {
			switch runtime.GOARCH {
			case "amd64", "arm64":
				want = UseSonicJSON
			case "riscv64":
				if testSonicScalarBuildTag {
					want = UseSonicJSON
				} else if minor >= 26 {
					if bi, ok := debug.ReadBuildInfo(); ok {
						for _, s := range bi.Settings {
							if s.Key == "GORISCV64" && (s.Value == "rva22u64" || s.Value == "rva23u64" || strings.HasPrefix(s.Value, "rva22u64,") || strings.HasPrefix(s.Value, "rva23u64,")) {
								want = UseSonicJSON
								break
							}
						}
					}
				}
			}
		}
	}
	require.Equal(t, want, APIKind, "unexpected sonic.APIKind for GOARCH=%s version=%s compatTag=%v scalarTag=%v",
		runtime.GOARCH, runtime.Version(), testSonicCompatBuildTag, testSonicScalarBuildTag)
}

func TestCheckTrailingsAndBoundedSyntaxError(t *testing.T) {
	type item struct {
		A int `json:"a"`
	}

	// 1. Valid trailing whitespace returns nil on CheckTrailings.
	decWS := decoder.NewDecoder("{\"a\":1} \t\r\n")
	var vWS item
	require.NoError(t, decWS.Decode(&vWS))
	require.Equal(t, 1, vWS.A)
	require.NoError(t, decWS.CheckTrailings())

	// 2. Single-value and multi-value CheckTrailings across diverse trailing token starters.
	trailingCases := []struct {
		tail    string
		wantOff int
	}{
		{tail: "trailing", wantOff: 8},
		{tail: `"extra"`, wantOff: 8},
		{tail: `{"b":2}`, wantOff: 8},
		{tail: `[1,2]`, wantOff: 8},
		{tail: `true`, wantOff: 8},
		{tail: `false`, wantOff: 8},
		{tail: `null`, wantOff: 8},
		{tail: `-99`, wantOff: 8},
		{tail: `,`, wantOff: 8},
	}
	for _, tc := range trailingCases {
		src := `{"a":1} ` + tc.tail
		dec := decoder.NewDecoder(src)
		var dst item
		require.NoError(t, dec.Decode(&dst), "case %s", tc.tail)
		require.Equal(t, 1, dst.A)
		errTrail := dec.CheckTrailings()
		assertDecoderTrailingSyntaxError(t, errTrail, src, tc.wantOff)

		// UnmarshalFromString must also return a populated error with non-empty Error().
		var dstU item
		errU := ConfigStd.UnmarshalFromString(src, &dstU)
		require.Error(t, errU, "UnmarshalFromString case %s", tc.tail)
		require.NotEmpty(t, errU.Error(), "UnmarshalFromString error msg empty on %s", tc.tail)
		if APIKind == UseStdJSON {
			var gotSE, stdSE *json.SyntaxError
			require.True(t, errors.As(errU, &gotSE), "expected *json.SyntaxError on compat for %s, got %T", tc.tail, errU)
			var stdDst item
			stdErr := json.Unmarshal([]byte(src), &stdDst)
			require.ErrorAs(t, stdErr, &stdSE)
			require.Equal(t, stdSE.Offset, gotSE.Offset, "offset mismatch on %s", tc.tail)
		}
	}

	// 3. Multi-value Decode + CheckTrailings.
	multiGood := "{\"a\":10}\n{\"a\":20}  \t"
	mdecGood := decoder.NewDecoder(multiGood)
	var m1, m2 item
	require.NoError(t, mdecGood.Decode(&m1))
	require.NoError(t, mdecGood.Decode(&m2))
	require.Equal(t, 10, m1.A)
	require.Equal(t, 20, m2.A)
	require.NoError(t, mdecGood.CheckTrailings())

	multiBad := "{\"a\":10}\n{\"a\":20} -42"
	mdecBad := decoder.NewDecoder(multiBad)
	require.NoError(t, mdecBad.Decode(&m1))
	require.NoError(t, mdecBad.Decode(&m2))
	assertDecoderTrailingSyntaxError(t, mdecBad.CheckTrailings(), multiBad, strings.LastIndexByte(multiBad, '-'))

	// 4. Bounded O(1) allocation check on 4 MiB trailing garbage (never copies the 4 MiB tail).
	huge := "0 " + strings.Repeat("x", 4<<20)
	hdec := decoder.NewDecoder(huge)
	var hv int
	require.NoError(t, hdec.Decode(&hv))
	// Warm up once, then measure heap bytes allocated per CheckTrailings() and UnmarshalFromString().
	_ = hdec.CheckTrailings()
	var mBefore, mAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mBefore)
	for i := 0; i < 10; i++ {
		if err := hdec.CheckTrailings(); err == nil || err.Error() == "" {
			t.Fatalf("expected non-empty trailing error, got %v", err)
		}
		var uv int
		if err := ConfigStd.UnmarshalFromString(huge, &uv); err == nil || err.Error() == "" {
			t.Fatalf("expected non-empty UnmarshalFromString error, got %v", err)
		}
	}
	runtime.ReadMemStats(&mAfter)
	totalAllocDelta := mAfter.TotalAlloc - mBefore.TotalAlloc
	// 10 iterations copying 4 MiB would allocate >= 40 MiB (even 1 copy is >= 4 MiB);
	// O(1) bounded error construction stays < 2 MiB even if TestMain's async FreeOSMemory()
	// purges parsePool (~107 KiB per cold Parser) on all 10 iterations under -tags=sonic_scalar.
	require.Less(t, totalAllocDelta, uint64(2<<20),
		"trailing error construction copied large buffer: allocated %d bytes across 10 runs", totalAllocDelta)
}

func TestOptionBitsDistinctAndIndependentAcrossBackends(t *testing.T) {
	decOpts := []decoder.Options{
		decoder.OptionUseInt64,
		decoder.OptionUseNumber,
		decoder.OptionUseUnicodeErrors,
		decoder.OptionDisableUnknown,
		decoder.OptionCopyString,
		decoder.OptionValidateString,
		decoder.OptionNoValidateJSON,
		decoder.OptionCaseSensitive,
	}
	var seenDec decoder.Options
	for i, opt := range decOpts {
		require.NotZero(t, opt, "decoder option %d is zero", i)
		require.Zero(t, seenDec&opt, "decoder option %d (%#x) collides with %#x", i, opt, seenDec)
		seenDec |= opt
	}

	encOpts := []encoder.Options{
		encoder.SortMapKeys,
		encoder.EscapeHTML,
		encoder.CompactMarshaler,
		encoder.NoQuoteTextMarshaler,
		encoder.NoNullSliceOrMap,
		encoder.ValidateString,
		encoder.NoValidateJSONMarshaler,
		encoder.NoEncoderNewline,
		encoder.EncodeNullForInfOrNan,
		encoder.OnlyOmitNull,
	}
	var seenEnc encoder.Options
	for i, opt := range encOpts {
		require.NotZero(t, opt, "encoder option %d is zero", i)
		require.Zero(t, seenEnc&opt, "encoder option %d (%#x) collides with %#x", i, opt, seenEnc)
		seenEnc |= opt
	}

	type target struct {
		A int `json:"a"`
	}
	dec := decoder.NewDecoder(`{"a":7,"extra":"ignored"}`)
	dec.UseUnicodeErrors()
	var out target
	require.NoError(t, dec.Decode(&out))
	require.Equal(t, 7, out.A)
}

type portableAppenderVal struct {
	Tag string
	N   int
}

var (
	_ encoder.JSONAppender = portableAppenderVal{}
	_ json.Marshaler       = portableAppenderVal{}
)

func (p portableAppenderVal) AppendSonicJSON(dst []byte) ([]byte, error) {
	dst = append(dst, `{"tag":`...)
	dst = strconv.AppendQuote(dst, p.Tag)
	dst = append(dst, `,"n":`...)
	dst = strconv.AppendInt(dst, int64(p.N), 10)
	dst = append(dst, '}')
	return dst, nil
}

func (p portableAppenderVal) MarshalJSON() ([]byte, error) {
	return p.AppendSonicJSON(nil)
}

func TestPortableJSONAppenderAcrossBackends(t *testing.T) {
	type container struct {
		Direct portableAppenderVal            `json:"direct"`
		Ptr    *portableAppenderVal           `json:"ptr"`
		Slice  []portableAppenderVal          `json:"slice"`
		Map    map[string]portableAppenderVal `json:"map"`
		Ifc    interface{}                    `json:"ifc"`
	}
	root := portableAppenderVal{Tag: "root", N: 1}
	gotRoot, err := ConfigStd.Marshal(root)
	require.NoError(t, err)
	wantRoot, err := json.Marshal(root)
	require.NoError(t, err)
	require.Equal(t, string(wantRoot), string(gotRoot))

	ptrVal := portableAppenderVal{Tag: "ptr", N: 3}
	in := container{
		Direct: portableAppenderVal{Tag: "field", N: 2},
		Ptr:    &ptrVal,
		Slice:  []portableAppenderVal{{Tag: "elem0", N: 4}, {Tag: "elem1", N: 5}},
		Map:    map[string]portableAppenderVal{"k": {Tag: "mapv", N: 6}},
		Ifc:    portableAppenderVal{Tag: "any", N: 7},
	}
	got, err := ConfigStd.Marshal(in)
	require.NoError(t, err)
	want, err := json.Marshal(in)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}
