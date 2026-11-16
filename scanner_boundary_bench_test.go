//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic/ast"
	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/native/types"
)

// Searcher.ValidateJSON requests structural validation; it does not set the
// native F_VALIDATE_STRING flag. Measure both contracts explicitly instead of
// attributing strict escape-scanner gains to the default AST search path.
func BenchmarkARMScannerBoundary(b *testing.B) {
	for _, tc := range []struct{ name, src string }{
		{"PlainString", `"` + strings.Repeat("a", 4096) + `"`},
		{"DenseEscapes", `"` + strings.Repeat(`ab\\\"cd\n\u4e2d`, 512) + `"`},
		{"EarlyEscapeLongTail", `"\n` + strings.Repeat("a", 4096) + `"`},
		{"EscapedObjects", `[` + strings.Repeat(`{"s":"a\n\u1234","n":123},`, 255) + `null]`},
		{"SmallIntegers", `[` + strings.Repeat(`1,23,-456,0,789,`, 128) + `0]`},
		{"MixedFloats", `[` + strings.Repeat(`1.25,-12.345e+67,42e3,1e-99,`, 128) + `0]`},
		{"LongDecimals", `[` + strings.Repeat(`0.`+strings.Repeat("1", 120)+`e-10,`, 128) + `0]`},
	} {
		if !json.Valid([]byte(tc.src)) {
			b.Fatal("invalid fixture", tc.name)
		}
		b.Run(tc.name+"/NativeStrictStrings", func(b *testing.B) {
			machine := types.NewStateMachine()
			defer types.FreeStateMachine(machine)
			check := func() {
				pos := 0
				if start := native.ValidateOne(&tc.src, &pos, machine, types.F_VALIDATE_STRING); start != 0 || pos != len(tc.src) {
					b.Fatalf("start=%d end=%d, want (0,%d)", start, pos, len(tc.src))
				}
			}
			check()
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				check()
			}
		})
		b.Run(tc.name+"/ASTSearch", func(b *testing.B) {
			searcher := ast.NewSearcher(tc.src)
			node, err := searcher.GetByPath()
			if err != nil {
				b.Fatal(err)
			}
			if raw, err := node.Raw(); err != nil || raw != tc.src {
				b.Fatal("incorrect search result", err)
			}
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := searcher.GetByPath(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
