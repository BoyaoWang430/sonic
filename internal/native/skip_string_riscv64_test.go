//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVSkipStringEvents(t *testing.T) {
	for _, prefix := range []int{0, 1, 7, 8, 15, 16, 31, 63, 64, 65} {
		for value := 0; value < 256; value++ {
			for _, suffix := range []string{"", `"`, `" ,0]`, `\\"tail"`} {
				src := `xxx"` + strings.Repeat("a", prefix) + string([]byte{byte(value)}) + suffix
				for _, flags := range []uint64{0, types.F_VALIDATE_STRING, types.F_VALIDATE_STRING | noValidateJSON} {
					validate := flags&types.F_VALIDATE_STRING != 0 && flags&noValidateJSON == 0
					wantEnd, _, _, wantErr := riscv.ScanString(src, 3, validate)
					pos := 4
					if err := skipStringToken(src, &pos, flags); pos != wantEnd || err != wantErr {
						t.Fatalf("prefix=%d byte=%#x suffix=%q flags=%d: got (%d,%v), want (%d,%v)", prefix, value, suffix, flags, pos, err, wantEnd, wantErr)
					}
				}
			}
		}
	}
}
