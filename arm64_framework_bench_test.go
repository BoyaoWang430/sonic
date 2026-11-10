//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native"

	"github.com/bytedance/sonic/ast"
	sonicutf8 "github.com/bytedance/sonic/utf8"
)

// Keep these public-API fixtures identical when comparing revisions. They
// deliberately separate the new kernels from end-to-end Twitter workloads.
func BenchmarkARMNativeCoverage(b *testing.B) {
	container := "[" + strings.TrimSuffix(strings.Repeat(`{"n":123,"s":"a\\\"[x]","a":[1,2,3]},`, 256), ",") + "]"
	escaped := `"` + strings.Repeat(`ab\\\"cd\n\u4e2d`, 512) + `"`
	for _, tc := range []struct {
		name, src string
		strict    bool
	}{
		{"Container/Fast", container, false},
		{"Container/Strict", container, true},
		{"EscapedString/Fast", escaped, false},
		{"EscapedString/Strict", escaped, true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			if !json.Valid([]byte(tc.src)) {
				b.Fatal("invalid fixture")
			}
			search := ast.NewSearcher(tc.src)
			search.ValidateJSON = tc.strict
			check := func() {
				node, err := search.GetByPath()
				if err != nil {
					b.Fatal(err)
				}
				raw, err := node.Raw()
				if err != nil || raw != tc.src {
					b.Fatalf("raw length=%d err=%v", len(raw), err)
				}
			}
			check()
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := search.GetByPath(); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			check()
		})
	}
	for _, tc := range []struct{ name, token string }{
		{"ShortIntegers", "1,23,-456,0,789"},
		{"Mixed", "1.23456789,-2.718281828e+10,18446744073709551615,-1234567"},
		{"LongDecimals", "0." + strings.Repeat("1234567890", 12) + "e-10"},
	} {
		src := "[" + strings.TrimSuffix(strings.Repeat(tc.token+",", 128), ",") + "]"
		b.Run("Numbers/"+tc.name, func(b *testing.B) {
			var want []json.Number
			if err := json.Unmarshal([]byte(src), &want); err != nil {
				b.Fatal(err)
			}
			api := (Config{UseNumber: true}).Froze()
			var got []json.Number
			if err := api.UnmarshalFromString(src, &got); err != nil || len(got) != len(want) {
				b.Fatalf("len=%d err=%v", len(got), err)
			}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got = got[:0]
				if err := api.UnmarshalFromString(src, &got); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			for i := range want {
				if got[i] != want[i] {
					b.Fatalf("number %d: %s != %s", i, got[i], want[i])
				}
			}
		})
	}
	type record struct {
		A bool
		B string
		C []int
	}
	compact := []byte("[" + strings.TrimSuffix(strings.Repeat(`{"A":true,"B":"x\ny","C":[1,2,3]},`, 128), ",") + "]")
	for _, tc := range []struct{ name, indent string }{{"Compact", ""}, {"Pretty", "   "}, {"WideIndent", strings.Repeat(" ", 24)}} {
		src := string(compact)
		if tc.indent != "" {
			var dst bytes.Buffer
			if err := json.Indent(&dst, compact, "", tc.indent); err != nil {
				b.Fatal(err)
			}
			src = dst.String()
		}
		b.Run("Whitespace/"+tc.name, func(b *testing.B) {
			var got []record
			check := func() {
				if err := ConfigStd.UnmarshalFromString(src, &got); err != nil {
					b.Fatal(err)
				}
				if len(got) != 128 || !got[0].A || got[127].B != "x\ny" || len(got[127].C) != 3 {
					b.Fatal("incorrect fixture decode")
				}
			}
			check()
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got = got[:0]
				if err := ConfigStd.UnmarshalFromString(src, &got); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			check()
		})
	}
	for _, tc := range []struct{ name, unit string }{{"ASCII", "hello JSON"}, {"CJK", "中文测试"}, {"Emoji", "😀🎉🌍"}, {"Mixed", "JSON 中文😀\n"}} {
		src := strings.Repeat(tc.unit, 512)
		for _, invalid := range []bool{false, true} {
			name, input := tc.name, src
			if invalid {
				name += "InvalidTail"
				input += "\xff"
			}
			b.Run("UTF8/"+name, func(b *testing.B) {
				if sonicutf8.ValidateString(input) == invalid {
					b.Fatal("incorrect UTF8 result")
				}
				b.SetBytes(int64(len(input)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if sonicutf8.ValidateString(input) == invalid {
						b.Fatal("incorrect UTF8 result")
					}
				}
			})
		}
	}
}

// BenchmarkARMNativeKernels calls the same native API on both revisions. Output
// buffers are reused; assertions outside the timer verify the complete result.
func BenchmarkARMNativeKernels(b *testing.B) {
	for _, tc := range []struct{ name, src string }{
		{"Short", "hello JSON"},
		{"ASCII", strings.Repeat("hello JSON", 512)},
		{"Escaped", strings.Repeat("hello\n\t\"\\中文", 512)},
	} {
		quoted, err := json.Marshal(tc.src)
		if err != nil {
			b.Fatal(err)
		}
		body := string(quoted[1 : len(quoted)-1])
		b.Run("Quote/"+tc.name, func(b *testing.B) {
			dst := make([]byte, len(tc.src)*6+16)
			call := func() (int, int) {
				n := len(dst)
				ret := native.Quote(armNativeStringData(tc.src), len(tc.src), unsafe.Pointer(&dst[0]), &n, 0)
				return ret, n
			}
			check := func() {
				ret, n := call()
				if ret < 0 || string(dst[:n]) != body {
					b.Fatalf("quote result=%d bytes=%d", ret, n)
				}
			}
			check()
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				call()
			}
			b.StopTimer()
			check()
		})
		b.Run("Unquote/"+tc.name, func(b *testing.B) {
			dst := make([]byte, len(body)+16)
			call := func() int {
				pos := -1
				return native.Unquote(armNativeStringData(body), len(body), unsafe.Pointer(&dst[0]), &pos, 0)
			}
			check := func() {
				n := call()
				if n < 0 || string(dst[:n]) != tc.src {
					b.Fatalf("unquote result=%d", n)
				}
			}
			check()
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				call()
			}
			b.StopTimer()
			check()
		})
	}
	for _, tc := range []struct{ name, src string }{
		{"ASCII", strings.Repeat("hello JSON", 512)},
		{"Escaped", strings.Repeat("<script>&中文\u2028\u2029", 512)},
	} {
		var want bytes.Buffer
		json.HTMLEscape(&want, []byte(tc.src))
		b.Run("HTMLEscape/"+tc.name, func(b *testing.B) {
			dst := make([]byte, len(tc.src)*6+16)
			call := func() (int, int) {
				n := len(dst)
				ret := native.HTMLEscape(armNativeStringData(tc.src), len(tc.src), unsafe.Pointer(&dst[0]), &n)
				return ret, n
			}
			check := func() {
				ret, n := call()
				if ret < 0 || !bytes.Equal(dst[:n], want.Bytes()) {
					b.Fatalf("HTML escape result=%d bytes=%d", ret, n)
				}
			}
			check()
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				call()
			}
			b.StopTimer()
			check()
		})
	}
}

func armNativeStringData(s string) unsafe.Pointer {
	return unsafe.Pointer(unsafe.StringData(s))
}
