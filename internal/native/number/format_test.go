package number_test

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"

	"github.com/bytedance/sonic/internal/native/number"
)

func TestAppendFloat(t *testing.T) {
	tests := []struct {
		v    float64
		bits int
		want string
	}{
		{0, 64, "0"}, {math.Copysign(0, -1), 64, "-0"},
		{1e-6, 64, "0.000001"}, {1e-7, 64, "1e-7"},
		{1e20, 64, "100000000000000000000"}, {1e21, 64, "1e+21"},
		{-1e-7, 64, "-1e-7"}, {-1e21, 64, "-1e+21"},
		{math.SmallestNonzeroFloat64, 64, "5e-324"},
		{math.MaxFloat64, 64, "1.7976931348623157e+308"},
		{1234e-320, 64, "1.234e-317"},
		{12.34, 64, "12.34"},
		{0, 32, "0"}, {math.Copysign(0, -1), 32, "-0"},
		{1e-6, 32, "0.000001"}, {1e-7, 32, "1e-7"},
		{1e20, 32, "100000000000000000000"}, {1e21, 32, "1e+21"},
		{math.SmallestNonzeroFloat32, 32, "1e-45"},
		{math.MaxFloat32, 32, "3.4028235e+38"},
		{16777217, 32, "16777216"}, {0.1, 32, "0.1"},
	}
	for _, tt := range tests {
		for _, capacity := range []int{0, 128} {
			dst := append(make([]byte, 0, capacity), "prefix:"...)
			got := string(number.AppendFloat(dst, tt.v, tt.bits))
			if got != "prefix:"+tt.want {
				t.Errorf("AppendFloat(%g, %d), cap=%d = %q, want %q", tt.v, tt.bits, capacity, got, "prefix:"+tt.want)
			}
		}
	}
}

func TestAppendFloatNonFinite(t *testing.T) {
	for _, bits := range []int{32, 64} {
		for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
			dst := []byte("unchanged")
			got := number.AppendFloat(dst, v, bits)
			if string(got) != "unchanged" || &got[0] != &dst[0] {
				t.Errorf("AppendFloat(%g, %d) modified dst: %q", v, bits, got)
			}
		}
	}
	if got := string(number.AppendFloat([]byte("prefix"), math.MaxFloat64, 32)); got != "prefix" {
		t.Errorf("float32 overflow appended %q", got)
	}
}

func TestAppendFloatInvalidBits(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("AppendFloat with invalid bits did not panic")
		}
	}()
	number.AppendFloat(nil, 1, 16)
}

func TestAppendFloatAgainstCompatibility(t *testing.T) {
	for _, boundary := range []float64{1e-6, 1e21} {
		for _, v := range []float64{math.Nextafter(boundary, 0), boundary, math.Nextafter(boundary, math.Inf(1))} {
			compareFloatWithJSON(t, v, 64)
			compareFloatWithJSON(t, -v, 64)
		}
		b32 := float32(boundary)
		for _, v := range []float32{math.Nextafter32(b32, 0), b32, math.Nextafter32(b32, float32(math.Inf(1)))} {
			compareFloatWithJSON(t, float64(v), 32)
			compareFloatWithJSON(t, -float64(v), 32)
		}
	}
	// Match the current native formatter's existing random comparison against
	// encoding/json, also the current compatibility backend, with a fixed seed.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 10000; i++ {
		compareFloatWithJSON(t, math.Float64frombits(rng.Uint64()), 64)
		compareFloatWithJSON(t, float64(math.Float32frombits(rng.Uint32())), 32)
	}
}

func compareFloatWithJSON(t *testing.T, v float64, bits int) {
	t.Helper()
	var value interface{} = v
	if bits == 32 {
		value = float32(v)
	}
	want, err := json.Marshal(value)
	got := number.AppendFloat(nil, v, bits)
	if err != nil {
		if len(got) != 0 {
			t.Errorf("non-finite %g (%d bits) formatted as %q", v, bits, got)
		}
	} else if string(got) != string(want) {
		t.Errorf("AppendFloat(%g, %d) = %q, want %q", v, bits, got, want)
	}
}

var formattedFloat []byte

func BenchmarkAppendFloat(b *testing.B) {
	for _, tc := range []struct {
		name   string
		bits   int
		values []float64
	}{
		{"Fixed64", 64, []float64{0, -0.25, 1.23456789, -2.718281828e10, 3.141592653589793, 1e20}},
		{"Scientific64", 64, []float64{1e-7, -1e21, 2.2250738585072014e-308, 5e-324, math.MaxFloat64}},
		{"Fixed32", 32, []float64{0, -0.25, 1.23456789, -2.718281828e10, 3.141592653589793, 1e20}},
		{"Scientific32", 32, []float64{1e-7, -1e21, math.SmallestNonzeroFloat32, math.MaxFloat32}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			buf := make([]byte, 0, 32*len(tc.values))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf = buf[:0]
				for _, v := range tc.values {
					buf = number.AppendFloat(buf, v, tc.bits)
				}
			}
			formattedFloat = buf
		})
	}
}
