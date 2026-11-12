//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"strings"
	"testing"
)

// These fixtures expose the proposed Go/assembly boundary independently of
// Twitter's short keys. They use the shared stage fixture, including stdlib
// decoding checks, restored input, warmed tape, and allocation reporting.
func BenchmarkARMHybridWorkloads(b *testing.B) {
	integers := []string{
		"0", "7", "-1", "42", "-12345", "2147483647", "-2147483648",
		"9007199254740991", "9223372036854775807", "-9223372036854775808",
	}
	var numbers strings.Builder
	numbers.WriteByte('[')
	for i := 0; i < 256; i++ {
		if i != 0 {
			numbers.WriteByte(',')
		}
		numbers.WriteString(integers[i%len(integers)])
	}
	numbers.WriteByte(']')

	const nested = `{"a":[1,{"b":true}],"c":[[],{}]}`
	for _, fixture := range []struct{ name, src string }{
		{"NumericArray", numbers.String()},
		{"UnicodeString", `"` + strings.Repeat(`ascii\n\u4e2d\ud83d\ude80`, 256) + `"`},
		{"ContainerArray", "[" + strings.Repeat(nested+",", 127) + nested + "]"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			armAnalysisParseStages[interface{}](b, fixture.src, true)
		})
	}
}
