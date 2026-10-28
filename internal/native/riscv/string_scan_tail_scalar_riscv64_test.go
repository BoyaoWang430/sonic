//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func BenchmarkNativeScanStringTailMatrix(b *testing.B) {
	cases := make([]struct{ name, body string }, 0, 36)
	for _, n := range []int{7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 23, 24, 31, 32, 33} {
		cases = append(cases, struct{ name, body string }{fmt.Sprintf("Plain/%d", n), strings.Repeat("p", n)})
	}
	cases = append(cases,
		struct{ name, body string }{"LongPlain", strings.Repeat("p", 4096)},
		struct{ name, body string }{"LongDenseEscapes", strings.Repeat(`\n\t\\\"`, 512)},
	)
	for _, tc := range cases {
		for _, scanAlign := range []int{0, 1} {
			b.Run(fmt.Sprintf("%s/Align%d", tc.name, scanAlign), func(b *testing.B) {
				quoted := `"` + tc.body + `"`
				storage, data, ptr := decodeAlignedRegion(len(quoted), (scanAlign+7)&7)
				copy(data, quoted)
				if int((uintptr(unsafe.Pointer(ptr))+1)&7) != scanAlign {
					b.Fatal("incorrect body alignment")
				}
				input := unsafe.String(ptr, len(quoted))
				wantEscape := strings.IndexByte(quoted, '\\')
				check := func(end, escape, pos int, code types.ParsingError) {
					if end != len(quoted) || escape != wantEscape || pos != -1 || code != 0 {
						b.Fatalf("scan=(%d,%d,%d,%v), want end=%d escape=%d", end, escape, pos, code, len(quoted), wantEscape)
					}
				}
				check(ScanString(input, 0, true))
				b.SetBytes(int64(len(quoted)))
				b.ReportAllocs()
				b.ResetTimer()
				var end, escape, pos int
				var code types.ParsingError
				for i := 0; i < b.N; i++ {
					end, escape, pos, code = ScanString(input, 0, true)
				}
				b.StopTimer()
				check(end, escape, pos, code)
				runtime.KeepAlive(storage)
			})
		}
	}
}
