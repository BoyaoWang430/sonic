//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"strings"
	"testing"
)

func rvvBooleanArray(count int) string {
	return "[" + strings.Repeat("true,", count-1) + "false]"
}

func BenchmarkRVVNodeGrowth(b *testing.B) {
	for _, tc := range []struct {
		name  string
		count int
	}{{"Resident32K", 32768}, {"Grow128K", 131072}, {"Grow512K", 524288}} {
		b.Run(tc.name, func(b *testing.B) {
			src := rvvBooleanArray(tc.count)
			p := newParser(src, 0, 0)
			if code := p.parse(); code != SONIC_OK || p.Pos() != len(src) {
				b.Fatal("invalid benchmark fixture")
			}
			p.free()
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := newParser(src, 0, 0)
				code, pos := p.parse(), p.Pos()
				p.free()
				if code != SONIC_OK || pos != len(src) {
					b.Fatalf("parse code=%d pos=%d", code, pos)
				}
			}
		})
	}
}
