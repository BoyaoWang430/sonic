//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package alg

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

func TestRVVFloatAppend(t *testing.T) {
	values := []float64{0, math.Copysign(0, -1), math.SmallestNonzeroFloat64, math.MaxFloat64, 1e-6, 1e21, math.NaN(), math.Inf(1), math.Inf(-1)}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1024; i++ {
		values = append(values, math.Float64frombits(rng.Uint64()))
	}
	for _, bits := range []int{32, 64} {
		for _, value := range values {
			var v any = value
			if bits == 32 {
				v = float32(value)
			}
			want, err := json.Marshal(v)
			if value == 0 || bits == 32 && float32(value) == 0 {
				want = []byte("0")
			}
			if err != nil {
				want = nil
			}
			for _, capacity := range []int{7, 8, 16, 64} {
				dst := make([]byte, 7, capacity)
				copy(dst, "prefix:")
				var got []byte
				if bits == 32 {
					got = F32toa(dst, float32(value))
				} else {
					got = F64toa(dst, value)
				}
				if string(got) != "prefix:"+string(want) {
					t.Fatalf("bits=%d cap=%d value=%g: got %q want %q", bits, capacity, value, got, want)
				}
			}
		}
	}
}
