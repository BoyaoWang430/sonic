//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// The fixed spellings also occur in the native-operation tests. Repetition
// changes input size without introducing a separate escaping implementation.
type nativeBenchText struct {
	name, body, decoded string
}

func nativeBenchTexts() []nativeBenchText {
	var cases []nativeBenchText
	for _, size := range []struct {
		name string
		n    int
	}{{"Short", 1}, {"Long", 256}} {
		for _, text := range []nativeBenchText{
			{"Plain", "hello JSON world", "hello JSON world"},
			{"Escaped", `hello\n\t\\\"world`, "hello\n\t\\\"world"},
			{"Unicode", "helloα，景😊", "helloα，景😊"},
			{"UnicodeEscaped", `\u4e2d\u6587\ud83d\ude00`, "中文😀"},
		} {
			cases = append(cases, nativeBenchText{size.name + "/" + text.name,
				strings.Repeat(text.body, size.n), strings.Repeat(text.decoded, size.n)})
		}
	}
	return cases
}

func BenchmarkNativeSkipSpace(b *testing.B) {
	for _, tc := range []struct {
		name string
		n    int
	}{{"Short", 4}, {"Long", 1024}} {
		b.Run(tc.name, func(b *testing.B) {
			src := strings.Repeat(" \t\r\n", tc.n) + "x"
			want := len(src) - 1
			if SkipSpace(src, 0) != want {
				b.Fatal("incorrect whitespace boundary")
			}
			b.SetBytes(int64(want))
			b.ReportAllocs()
			b.ResetTimer()
			var got int
			for i := 0; i < b.N; i++ {
				got = SkipSpace(src, 0)
			}
			b.StopTimer()
			if got != want {
				b.Fatalf("boundary=%d, want %d", got, want)
			}
		})
	}
}

func BenchmarkNativeScanString(b *testing.B) {
	for _, tc := range nativeBenchTexts() {
		for _, mode := range []struct {
			name     string
			validate bool
		}{{"Boundary", false}, {"Validate", true}} {
			b.Run(tc.name+"/"+mode.name, func(b *testing.B) {
				src := `"` + tc.body + `"`
				wantEscape := strings.IndexByte(src, '\\')
				check := func(end, escape, pos int, code types.ParsingError) {
					if end != len(src) || escape != wantEscape || pos != -1 || code != 0 {
						b.Fatalf("scan=(%d,%d,%d,%v)", end, escape, pos, code)
					}
				}
				check(ScanString(src, 0, mode.validate))
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				b.ResetTimer()
				var end, escape, pos int
				var code types.ParsingError
				for i := 0; i < b.N; i++ {
					end, escape, pos, code = ScanString(src, 0, mode.validate)
				}
				b.StopTimer()
				check(end, escape, pos, code)
			})
		}
	}
}

func BenchmarkNativeUnquote(b *testing.B) {
	for _, tc := range nativeBenchTexts() {
		b.Run(tc.name, func(b *testing.B) {
			dst := make([]byte, len(tc.body))
			check := func(written, pos int, code types.ParsingError) {
				if code != 0 || pos != -1 || written != len(tc.decoded) || string(dst[:written]) != tc.decoded {
					b.Fatalf("unquote=(%d,%d,%v), want %q", written, pos, code, tc.decoded)
				}
			}
			check(Unquote(tc.body, dst, 0))
			b.SetBytes(int64(len(tc.body)))
			b.ReportAllocs()
			b.ResetTimer()
			var written, pos int
			var code types.ParsingError
			for i := 0; i < b.N; i++ {
				written, pos, code = Unquote(tc.body, dst, 0)
			}
			b.StopTimer()
			check(written, pos, code)
		})
	}
}

// ParseString modifies escaped inputs. Restore a batch outside the timer so
// every timed call sees the original JSON, without per-operation timer calls.
// The result measures the kernel on freshly copied, cache-hot input; the copy
// and allocation costs are deliberately excluded.
func BenchmarkNativeParseString(b *testing.B) {
	for _, tc := range nativeBenchTexts() {
		b.Run(tc.name, func(b *testing.B) {
			src := `"` + tc.body + `"`
			batch := min(32, b.N)
			bufs := make([][]byte, batch)
			for i := range bufs {
				bufs[i] = []byte(src)
			}
			type result struct {
				end, written, pos, code int
				escaped                 bool
			}
			results := make([]result, batch)
			wantEscape := strings.Contains(tc.body, `\`)
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			b.StopTimer()
			for done := 0; done < b.N; {
				n := min(batch, b.N-done)
				for i := 0; i < n; i++ {
					copy(bufs[i], src)
				}
				b.StartTimer()
				for i := 0; i < n; i++ {
					r := &results[i]
					r.end, r.written, r.escaped, r.pos, r.code = ParseString(bufs[i], 0, true, false)
				}
				b.StopTimer()
				for i := 0; i < n; i++ {
					r := results[i]
					if r.code != 0 || r.end != len(src) || r.written != len(tc.decoded) || r.escaped != wantEscape || r.pos != -1 || string(bufs[i][1:1+r.written]) != tc.decoded {
						b.Fatalf("parse-string=%+v", r)
					}
				}
				done += n
			}
		})
	}
}

func BenchmarkNativeQuote(b *testing.B) {
	for _, tc := range nativeBenchTexts() {
		if strings.HasSuffix(tc.name, "/UnicodeEscaped") {
			continue // Quote emits Unicode literally; Unicode already covers it.
		}
		b.Run(tc.name, func(b *testing.B) {
			dst := make([]byte, len(tc.body))
			check := func(consumed, written int) {
				if consumed != len(tc.decoded) || written != len(tc.body) || string(dst[:written]) != tc.body {
					b.Fatalf("quote=(%d,%d)", consumed, written)
				}
			}
			check(Quote(tc.decoded, dst, 0))
			b.SetBytes(int64(len(tc.decoded)))
			b.ReportAllocs()
			b.ResetTimer()
			var consumed, written int
			for i := 0; i < b.N; i++ {
				consumed, written = Quote(tc.decoded, dst, 0)
			}
			b.StopTimer()
			check(consumed, written)
		})
	}
}

func BenchmarkNativeHTMLEscape(b *testing.B) {
	for _, size := range []struct {
		name string
		n    int
	}{{"Short", 1}, {"Long", 256}} {
		for _, tc := range []struct{ name, src, want string }{
			{"Plain", "hello JSON world", "hello JSON world"},
			{"EscapedUnicode", "hello\u2029\u2028<&>world", `hello\u2029\u2028\u003c\u0026\u003eworld`},
		} {
			b.Run(size.name+"/"+tc.name, func(b *testing.B) {
				src, want := strings.Repeat(tc.src, size.n), strings.Repeat(tc.want, size.n)
				dst := make([]byte, len(want))
				check := func(consumed, written int) {
					if consumed != len(src) || written != len(want) || string(dst[:written]) != want {
						b.Fatalf("html-escape=(%d,%d)", consumed, written)
					}
				}
				check(HTMLEscape(src, dst))
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				b.ResetTimer()
				var consumed, written int
				for i := 0; i < b.N; i++ {
					consumed, written = HTMLEscape(src, dst)
				}
				b.StopTimer()
				check(consumed, written)
			})
		}
	}
}

func BenchmarkNativeValidUTF8(b *testing.B) {
	for _, tc := range nativeBenchTexts() {
		if !strings.HasSuffix(tc.name, "/Plain") && !strings.HasSuffix(tc.name, "/Unicode") {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			if !ValidUTF8(tc.decoded) {
				b.Fatal("valid fixture rejected")
			}
			b.SetBytes(int64(len(tc.decoded)))
			b.ReportAllocs()
			b.ResetTimer()
			var valid bool
			for i := 0; i < b.N; i++ {
				valid = ValidUTF8(tc.decoded)
			}
			b.StopTimer()
			if !valid {
				b.Fatal("valid fixture rejected")
			}
		})
	}
}

func BenchmarkNativeInvalidUTF8(b *testing.B) {
	for _, size := range []struct {
		name string
		n    int
	}{{"Short", 1}, {"Long", 256}} {
		for _, tc := range []struct {
			name, src string
			invalid   []int
		}{
			{"Plain", "hello JSON world", nil},
			{"Unicode", "helloα，景😊", nil},
			{"MixedErrors", "x\xff中\xe2(\xa1y", []int{1, 5, 7}},
		} {
			b.Run(size.name+"/"+tc.name, func(b *testing.B) {
				src := strings.Repeat(tc.src, size.n)
				var want []int
				for i := 0; i < size.n; i++ {
					for _, offset := range tc.invalid {
						want = append(want, i*len(tc.src)+offset)
					}
				}
				positions := make([]int, len(want))
				check := func(next, count int, full bool) {
					if next != len(src) || count != len(want) || full || !equalUTF8Positions(positions[:count], want) {
						b.Fatalf("invalid-utf8=(%d,%d,%v)", next, count, full)
					}
				}
				check(InvalidUTF8(src, 0, positions))
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				b.ResetTimer()
				var next, count int
				var full bool
				for i := 0; i < b.N; i++ {
					next, count, full = InvalidUTF8(src, 0, positions)
				}
				b.StopTimer()
				check(next, count, full)
			})
		}
	}
}

func equalUTF8Positions(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
