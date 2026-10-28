//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// These fixtures isolate bulk UTF-8 validation. All inputs end at a code-point
// boundary; SetBytes reports their actual size after repeating whole patterns.
// InvalidZeroCapacity exercises the shared entry used by ValidateUTF8Fast.
// Block uses a fixed 32-byte target so scalar and RVV runs share the same input.
func BenchmarkNativeUTF8Blocks(b *testing.B) {
	for _, size := range []struct {
		name string
		n    int
	}{{"Block", 32}, {"4KiB", 4096}, {"64KiB", 65536}} {
		for _, tc := range []struct{ name, text string }{
			{"ASCII", "hello JSON world"},
			{"TwoByte", "\u0080\u07ffα"},
			{"ThreeByte", "\u0800\ud7ff\ue000\uffff中"},
			{"FourByte", "\U00010000\U0010ffff😊"},
			{"Chinese", "中文输入与向量边界"},
			{"Emoji", "😊😀🌍🚀"},
			{"Mixed", "helloα，景😊"},
		} {
			src := strings.Repeat(tc.text, (size.n+len(tc.text)-1)/len(tc.text))
			if !utf8.ValidString(src) || !ValidUTF8(src) {
				b.Fatal("invalid benchmark fixture")
			}
			name := size.name + "/" + tc.name
			b.Run(name+"/Valid", func(b *testing.B) {
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				var valid bool
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					valid = ValidUTF8(src)
				}
				b.StopTimer()
				if !valid {
					b.Fatal("valid input rejected")
				}
			})
			b.Run(name+"/InvalidZeroCapacity", func(b *testing.B) {
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				var next, count int
				var full bool
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					next, count, full = InvalidUTF8(src, 0, nil)
				}
				b.StopTimer()
				if next != len(src) || count != 0 || full {
					b.Fatalf("scan=(%d,%d,%v)", next, count, full)
				}
			})
			b.Run(name+"/Stdlib", func(b *testing.B) {
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				var valid bool
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					valid = utf8.ValidString(src)
				}
				b.StopTimer()
				if !valid {
					b.Fatal("valid input rejected")
				}
			})
		}
	}
}
