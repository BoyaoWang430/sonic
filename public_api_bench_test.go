//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic_test

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/bytedance/sonic/testdata"
)

// Use byte-input Unmarshal, including its source ownership and trailing-input
// checks. Each decode gets a fresh destination. Marshal allocates its output.
// All backend builds must use this exact fixture and configuration.
func BenchmarkARMPublicAPI(b *testing.B) {
	src := []byte(testdata.TwitterJson)
	var generic interface{}
	var binding testdata.TwitterStruct
	if err := json.Unmarshal(src, &generic); err != nil {
		b.Fatal(err)
	}
	if err := json.Unmarshal(src, &binding); err != nil {
		b.Fatal(err)
	}
	for _, cfg := range []struct {
		name string
		api  sonic.API
	}{
		{"Default", sonic.ConfigDefault},
		{"Std", sonic.ConfigStd},
	} {
		b.Run(cfg.name+"/Marshal/Generic", func(b *testing.B) {
			check := func(out []byte, err error) {
				b.Helper()
				if err != nil {
					b.Fatal(err)
				}
				var got interface{}
				if err := json.Unmarshal(out, &got); err != nil || !reflect.DeepEqual(got, generic) {
					b.Fatal("generic marshal output mismatch", err)
				}
			}
			out, err := cfg.api.Marshal(generic)
			check(out, err)
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err = cfg.api.Marshal(generic)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			check(out, err)
		})
		b.Run(cfg.name+"/Marshal/Binding", func(b *testing.B) {
			check := func(out []byte, err error) {
				b.Helper()
				if err != nil {
					b.Fatal(err)
				}
				var got testdata.TwitterStruct
				if err := json.Unmarshal(out, &got); err != nil || !reflect.DeepEqual(got, binding) {
					b.Fatal("binding marshal output mismatch", err)
				}
			}
			out, err := cfg.api.Marshal(&binding)
			check(out, err)
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err = cfg.api.Marshal(&binding)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			check(out, err)
		})
		b.Run(cfg.name+"/Unmarshal/Generic", func(b *testing.B) {
			var last interface{}
			if err := cfg.api.Unmarshal(src, &last); err != nil || !reflect.DeepEqual(last, generic) {
				b.Fatal("generic unmarshal output mismatch", err)
			}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var got interface{}
				if err := cfg.api.Unmarshal(src, &got); err != nil {
					b.Fatal(err)
				}
				last = got
			}
			b.StopTimer()
			if !reflect.DeepEqual(last, generic) {
				b.Fatal("generic unmarshal output mismatch")
			}
		})
		b.Run(cfg.name+"/Unmarshal/Binding", func(b *testing.B) {
			last := new(testdata.TwitterStruct)
			if err := cfg.api.Unmarshal(src, last); err != nil || !reflect.DeepEqual(*last, binding) {
				b.Fatal("binding unmarshal output mismatch", err)
			}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var got testdata.TwitterStruct
				if err := cfg.api.Unmarshal(src, &got); err != nil {
					b.Fatal(err)
				}
				last = &got
			}
			b.StopTimer()
			if !reflect.DeepEqual(*last, binding) {
				b.Fatal("binding unmarshal output mismatch")
			}
		})
	}
}

// These valid inputs exercise complete public API operations on generic Go
// values. Unicode escape spelling belongs to the Unmarshal input: Marshal
// receives the decoded Unicode string and may emit ordinary UTF-8 instead.
func BenchmarkARMPublicShapeCoverage(b *testing.B) {
	integers := make([]string, 256)
	floats := make([]string, 256)
	floatTokens := []string{
		"1.23456789", "-2.718281828e+10",
		"18446744073709551616", "1.23456789e-120",
	}
	for i := range integers {
		integers[i] = strconv.Itoa(i)
		floats[i] = floatTokens[i%len(floatTokens)]
	}
	const record = `{"id":7,"a":[1,true,null],"b":{"s":"x","c":[{},[]]}}`
	for _, tc := range []struct{ name, src string }{
		{"SmallIntegers", "[" + strings.Join(integers, ",") + "]"},
		{"MixedFloats", "[" + strings.Join(floats, ",") + "]"},
		{"NestedContainers", "[" + strings.Repeat(record+",", 63) + record + "]"},
		{"DenseASCIIEscapes", `"` + strings.Repeat(`a\nb\tc\"d\\e\r\f\b/`, 192) + `"`},
		{"DenseUnicodeEscapes", `"` + strings.Repeat(`\u4e2d\u6587\u263a\ud83d\ude00`, 128) + `"`},
		{"LongPlainString", `"` + strings.Repeat("abcdef0123456789", 256) + `"`},
	} {
		src := []byte(tc.src)
		var want interface{}
		if err := json.Unmarshal(src, &want); err != nil {
			b.Fatal(tc.name, "invalid fixture", err)
		}
		b.Run(tc.name+"/Marshal", func(b *testing.B) {
			check := func(out []byte, err error) {
				b.Helper()
				if err != nil {
					b.Fatal(err)
				}
				var got interface{}
				if err := json.Unmarshal(out, &got); err != nil || !reflect.DeepEqual(got, want) {
					b.Fatal("marshal output mismatch", err)
				}
			}
			out, err := sonic.Marshal(want)
			check(out, err)
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err = sonic.Marshal(want)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			check(out, err)
		})
		b.Run(tc.name+"/Unmarshal", func(b *testing.B) {
			var last interface{}
			if err := sonic.Unmarshal(src, &last); err != nil || !reflect.DeepEqual(last, want) {
				b.Fatal("unmarshal output mismatch", err)
			}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var got interface{}
				if err := sonic.Unmarshal(src, &got); err != nil {
					b.Fatal(err)
				}
				last = got
			}
			b.StopTimer()
			if !reflect.DeepEqual(last, want) {
				b.Fatal("unmarshal output mismatch")
			}
		})
	}
}

// Unlike GetFull, this starts from fresh raw JSON on every iteration and
// recursively builds Go maps/slices/scalars for the entire document.
func BenchmarkARMASTMaterialize(b *testing.B) {
	src := testdata.TwitterJson
	var want interface{}
	if err := json.Unmarshal([]byte(src), &want); err != nil {
		b.Fatal(err)
	}
	root := ast.NewRaw(src)
	got, err := root.Interface()
	if err != nil || !reflect.DeepEqual(got, want) {
		b.Fatal("AST materialization mismatch", err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root := ast.NewRaw(src)
		got, err = root.Interface()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if !reflect.DeepEqual(got, want) {
		b.Fatal("AST materialization mismatch")
	}
}
