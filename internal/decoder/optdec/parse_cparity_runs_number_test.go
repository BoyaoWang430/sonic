//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"strconv"
	"strings"
	"testing"
)

// Guard the transition back to the assembly integer run as well as the
// non-batching whitespace/object paths. Pure integer arrays never enter the
// Go numeric fallback and therefore cannot expose this transition cost.
func BenchmarkCParityRunsNumericTransitions(b *testing.B) {
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
			armAnalysisParseStages[interface{}](b, fixture.src, true)
		})
	}
}
