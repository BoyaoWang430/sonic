//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package number

import (
	"math/rand"
	"testing"
)

// Compare the RISC-V scanner with the production Go fallback in the same build.
func TestNumberScanMatchesExistingGo(t *testing.T) {
	rng := rand.New(rand.NewSource(882613))
	alphabet := "00011123456789.eE++--\x00\xffxyz,} \n"
	for i := 0; i < 50000; i++ {
		buf := make([]byte, rng.Intn(160))
		for j := range buf {
			buf[j] = alphabet[rng.Intn(len(alphabet))]
		}
		src := string(buf)
		for _, start := range []int{-1, 0, len(src) / 2, len(src), len(src) + 1} {
			end, err := Scan(src, start)
			wantEnd, wantErr := scanGeneric(src, start)
			if end != wantEnd || err != wantErr {
				t.Fatalf("ScanNumber(%q,%d)=(%d,%v), Go=(%d,%v)", src, start, end, err, wantEnd, wantErr)
			}
		}
	}
}
