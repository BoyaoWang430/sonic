//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func BenchmarkNativeScanNumber(b *testing.B)       { benchmarkNumberScan(b, 0) }
func BenchmarkNativeScanNumberStrict(b *testing.B) { benchmarkNumberScan(b, 1) }
func BenchmarkNativeSkipNumber(b *testing.B)       { benchmarkNumberScan(b, 2) }

func benchmarkNumberScan(b *testing.B, mode int) {
	sequential := make([]string, 1024)
	for i := range sequential {
		sequential[i] = strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name   string
		tokens []string
	}{
		{"ShortInteger", []string{"-123456789"}},
		{"Integer8Digits", []string{"12345678"}},
		{"Integer9Digits", []string{"123456789"}},
		{"Integer64Digits", []string{strings.Repeat("7", 64)}},
		{"SequentialIntegers", sequential},
		{"ShortFloat", []string{"-12.345e+67"}},
		{"LongInteger", []string{strings.Repeat("9", 1024)}},
		{"LongFloat", []string{"-1." + strings.Repeat("2", 1024) + "e-999"}},
		{"DenseIntegers", strings.Fields(strings.Repeat("0 -1 42 1234 -987654321 18446744073709551616 ", 128))},
		{"DenseFloats", strings.Fields(strings.Repeat("0.0 -1.25 42e3 1e-99 -987.654E+21 1e999 ", 128))},
	} {
		b.Run(tc.name, func(b *testing.B) {
			src := strings.Join(tc.tokens, ",") + "]"
			starts := make([]int, len(tc.tokens))
			wantSum, bytes := 0, 0
			for i, token := range tc.tokens {
				if i > 0 {
					starts[i] = starts[i-1] + len(tc.tokens[i-1]) + 1
				}
				wantEnd := starts[i] + len(token)
				wantSum += wantEnd
				bytes += len(token)
				var end int
				var err types.ParsingError
				switch mode {
				case 0:
					end, err = ScanNumber(src, starts[i])
				case 1:
					end, err = ScanNumberStrict(src, starts[i])
				case 2:
					end = SkipNumber(src, starts[i])
				}
				if end != wantEnd || err != 0 {
					b.Fatalf("token %d: (%d,%v), want (%d,0)", i, end, err, wantEnd)
				}
			}
			// Each iteration scans every token from its known start. This measures
			// complete number operations, without JSON container traversal or any
			// numeric conversion. Bytes excludes delimiters; preparation is untimed.
			b.SetBytes(int64(bytes))
			b.ReportAllocs()
			b.ResetTimer()
			var sum int
			var errors types.ParsingError
			switch mode {
			case 0:
				for i := 0; i < b.N; i++ {
					for _, start := range starts {
						end, err := ScanNumber(src, start)
						sum += end
						errors |= err
					}
				}
			case 1:
				for i := 0; i < b.N; i++ {
					for _, start := range starts {
						end, err := ScanNumberStrict(src, start)
						sum += end
						errors |= err
					}
				}
			case 2:
				for i := 0; i < b.N; i++ {
					for _, start := range starts {
						sum += SkipNumber(src, start)
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(len(starts)), "tokens/op")
			if sum != b.N*wantSum || errors != 0 {
				b.Fatalf("sum=%d errors=%v, want sum %d", sum, errors, b.N*wantSum)
			}
		})
	}
}
