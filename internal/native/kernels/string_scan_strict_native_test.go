//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

func TestScanStringEveryEscapeByte(t *testing.T) {
	// Exercise the complete byte domain around block boundaries: bytes above
	// ASCII must not alias a valid escape or hexadecimal digit. Both entry
	// shapes are compared with the independent byte-oriented reference.
	check := func(content string) {
		want := stringBlockReference(`"`+content, 0, true)
		checkStringScan(t, `"`+content, 0, true, stringScanResult{
			want.end, want.escape, want.errorPos, want.err,
		})
		checkScanStringBlock(t, rt.StringData(content), len(content), 1)
	}
	for _, prefix := range []int{0, 7, 15, 16, 31, 63} {
		plain := strings.Repeat("a", prefix)
		for c := 0; c < 256; c++ {
			check(plain + "\\" + string(byte(c)) + `1234"#"\`)
			for digit := 0; digit < 4; digit++ {
				hex := []byte(`\u1234"#"\`)
				hex[2+digit] = byte(c)
				check(plain + string(hex))
			}
		}
	}
}
