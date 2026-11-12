//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic_test

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

// Match the parser-stage transition fixtures while measuring the complete byte
// API: a fresh destination, input ownership/copy, tape parsing, and binding.
func BenchmarkARMPublicNumericTransitions(b *testing.B) {
	integers := []string{"1.5"}
	alternating := make([]string, 0, 256)
	alternating8 := make([]string, 0, 256)
	alternating19 := make([]string, 0, 256)
	fields := make([]string, 256)
	for i := 0; i < 256; i++ {
		integer := strconv.Itoa(i)
		integers = append(integers, integer)
		fields[i] = `"n` + integer + `":1.5`
		if i < 128 {
			alternating = append(alternating, "1.5", integer)
			alternating8 = append(alternating8, "1.5", strconv.Itoa(10000000+i))
			alternating19 = append(alternating19, "1.5", strconv.FormatUint(1000000000000000000+uint64(i), 10))
		}
	}
	for _, fixture := range []struct{ name, src string }{
		{"FloatThenIntegers", "[" + strings.Join(integers, ",") + "]"},
		{"Alternating", "[" + strings.Join(alternating, ",") + "]"},
		{"Alternating8Digits", "[" + strings.Join(alternating8, ",") + "]"},
		{"Alternating19Digits", "[" + strings.Join(alternating19, ",") + "]"},
		{"WhitespaceFloats", "[" + strings.Repeat("1.5, \t-2.718281828e+10, \t", 127) + "1.5, \t-2.718281828e+10]"},
		{"ObjectFloats", "{" + strings.Join(fields, ",") + "}"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			src := []byte(fixture.src)
			var want, last interface{}
			if err := json.Unmarshal(src, &want); err != nil {
				b.Fatal(err)
			}
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
