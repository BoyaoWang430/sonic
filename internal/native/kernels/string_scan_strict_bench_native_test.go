//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// Cover the transition from a plain prefix to validated escapes, including a
// long ordinary suffix. Changing the Go/assembly boundary must not optimize
// dense escapes at the expense of the common short or unescaped string.
func BenchmarkNativeScanStringStrictTransitions(b *testing.B) {
	for _, tc := range []struct{ name, body string }{
		{"Empty", ""},
		{"Plain7", strings.Repeat("a", 7)},
		{"Plain15", strings.Repeat("a", 15)},
		{"Plain16", strings.Repeat("a", 16)},
		{"Plain31", strings.Repeat("a", 31)},
		{"Plain64", strings.Repeat("a", 64)},
		{"Plain4096", strings.Repeat("a", 4096)},
		{"ShortEscape", `a\nb`},
		{"EarlyEscapeLongTail", `\n` + strings.Repeat("a", 4096)},
		{"SparseEscapes", strings.Repeat(strings.Repeat("a", 127)+`\n`, 32)},
		{"CrossingUnicode", strings.Repeat(strings.Repeat("a", 61)+`\u1234`, 32)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			src := `"` + tc.body + `"`
			wantEscape := strings.IndexByte(tc.body, '\\')
			if wantEscape >= 0 {
				wantEscape++
			}
			check := func(end, escape, pos int, err types.ParsingError) {
				b.Helper()
				if end != len(src) || escape != wantEscape || pos != -1 || err != 0 {
					b.Fatalf("scan=(%d,%d,%d,%v), want (%d,%d,-1,0)", end, escape, pos, err, len(src), wantEscape)
				}
			}
			check(ScanString(src, 0, true))
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			var end, escape, pos int
			var err types.ParsingError
			for i := 0; i < b.N; i++ {
				end, escape, pos, err = ScanString(src, 0, true)
			}
			b.StopTimer()
			check(end, escape, pos, err)
		})
	}
}
