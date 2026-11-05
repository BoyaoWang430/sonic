//go:build arm64 && go1.20 && !go1.28

package optdec

import (
	"strconv"
	"strings"
	"testing"
)

// Short escaped values and keys expose the cost of crossing the Go/assembly
// boundary per token, separately from long-string scan and copy throughput.
func BenchmarkCParityRunsOwnershipWorkloads(b *testing.B) {
	const value = `"a\nb"`
	values := "[" + strings.Repeat(value+",", 255) + value + "]"
	var keys strings.Builder
	keys.WriteByte('{')
	for i := 0; i < 256; i++ {
		if i != 0 {
			keys.WriteByte(',')
		}
		number := strconv.Itoa(i)
		keys.WriteString(`"k`)
		keys.WriteString(number)
		keys.WriteString(`\n":`)
		keys.WriteString(number)
	}
	keys.WriteByte('}')
	for _, fixture := range []struct{ name, src string }{
		{"ShortEscapedValues", values},
		{"EscapedKeys", keys.String()},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			armAnalysisParseStages[interface{}](b, fixture.src, true)
		})
	}
}
