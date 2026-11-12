//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func BenchmarkNativeNonSpaceMask(b *testing.B) {
	src := strings.Repeat("  x \t\r\ny", 8)
	if mask, width := NonSpaceMask(src); mask != 0x8484848484848484 || width != 64 {
		b.Fatalf("mask=%x width=%d", mask, width)
	}
	b.SetBytes(64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NonSpaceMask(src)
	}
}

func BenchmarkNativeSkipContainer(b *testing.B) {
	for _, tt := range []struct{ name, src string }{
		{"Short", `[1,{"x":[2,3]},"tail"]`},
		{"Flat", `[` + strings.Repeat(`123,"hello",`, 512) + `null]`},
		{"Escaped", `[` + strings.Repeat(`"a\\\"[]b",`, 512) + `null]`},
		{"Nested", strings.Repeat("[", 512) + "0" + strings.Repeat("]", 512)},
	} {
		b.Run(tt.name, func(b *testing.B) {
			end, err := SkipContainer(tt.src, 0)
			if end != len(tt.src) || err != 0 {
				b.Fatalf("boundary=(%d,%v), want %d", end, err, len(tt.src))
			}
			b.SetBytes(int64(len(tt.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				end, err = SkipContainer(tt.src, 0)
			}
			b.StopTimer()
			if end != len(tt.src) || err != 0 {
				b.Fatalf("boundary=(%d,%v), want %d", end, err, len(tt.src))
			}
		})
	}
}

func BenchmarkNativeScanStringMasks(b *testing.B) {
	for _, tt := range []struct{ name, body string }{
		{"QuoteDense", strings.Repeat(`\"`, 1024)},
		{"BackslashDense", strings.Repeat(`\\`, 1024)},
		{"MixedEscapes", strings.Repeat(`a\n\t\\\"\u1234`, 128)},
	} {
		for _, strict := range []bool{false, true} {
			mode := "Boundary"
			if strict {
				mode = "Validate"
			}
			b.Run(tt.name+"/"+mode, func(b *testing.B) {
				src := `"` + tt.body + `"`
				wantEscape := 1 + strings.IndexByte(tt.body, '\\')
				check := func(end, escape, pos int, code types.ParsingError) {
					if end != len(src) || escape != wantEscape || pos != -1 || code != 0 {
						b.Fatalf("scan=(%d,%d,%d,%v)", end, escape, pos, code)
					}
				}
				check(ScanString(src, 0, strict))
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				b.ResetTimer()
				var end, escape, pos int
				var code types.ParsingError
				for i := 0; i < b.N; i++ {
					end, escape, pos, code = ScanString(src, 0, strict)
				}
				b.StopTimer()
				check(end, escape, pos, code)
			})
		}
	}
}

func BenchmarkNativeShortQuoteAndScan(b *testing.B) {
	b.Run("QuoteShort10", func(b *testing.B) {
		src := "created_at"
		dst := make([]byte, 32)
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c, w := Quote(src, dst, 0)
			if c != len(src) || w != len(src) {
				b.Fatalf("quote=(%d,%d)", c, w)
			}
		}
	})
	b.Run("ScanShortKeyRelaxed", func(b *testing.B) {
		src := `"id_str":1234567890,"text":"hello"}`
		b.SetBytes(8)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			end, esc, _, err := ScanString(src, 0, false)
			if end != 8 || esc != -1 || err != 0 {
				b.Fatalf("scan=(%d,%d,%v)", end, esc, err)
			}
		}
	})
	b.Run("ScanShortKeyStrict", func(b *testing.B) {
		src := `"id_str":1234567890,"text":"hello"}`
		b.SetBytes(8)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			end, esc, _, err := ScanString(src, 0, true)
			if end != 8 || esc != -1 || err != 0 {
				b.Fatalf("scan=(%d,%d,%v)", end, esc, err)
			}
		}
	})
}
