//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"strings"
	"testing"
)

// These synthetic fixtures expose the repeated work hidden by Twitter's many
// short keys. They use the same options, restored input, tape checks, and stage
// timing as the retained C and Go comparison. They are not traffic weights.
func BenchmarkARMParityWorkloads(b *testing.B) {
	space := strings.Repeat(" \t\r\n", 256)
	for _, fixture := range []struct{ name, src string }{
		{"LongString", `"` + strings.Repeat("abcdefgh", 1024) + `"`},
		{"EscapedString", `"` + strings.Repeat(`abcdefgh\n`, 512) + `"`},
		{"LongWhitespace", "[" + space + "1," + space + "2]"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			armAnalysisParseStages[interface{}](b, fixture.src, true)
		})
	}
}
