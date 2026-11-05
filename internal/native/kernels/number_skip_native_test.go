//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import "testing"

// A relaxed candidate ends only at the seven documented delimiters, even
// when its contents are not a valid number. Exercise every byte at each
// position around the vector and block boundaries, with nonzero starts.
func TestSkipNumberBoundaries(t *testing.T) {
	for prefix := 0; prefix < 16; prefix++ {
		for offset := 0; offset <= 129; offset++ {
			for c := 0; c < 256; c++ {
				input := make([]byte, prefix+offset+18)
				for i := range input {
					input[i] = '7'
				}
				input[prefix+offset] = byte(c)
				input[len(input)-1] = ']'
				want := len(input) - 1
				switch byte(c) {
				case ',', ']', '}', ' ', '\t', '\n', '\r':
					want = prefix + offset
				}
				if got := SkipNumber(string(input), prefix); got != want {
					t.Fatalf("start=%d offset=%d byte=%02x: end=%d, want %d", prefix, offset, c, got, want)
				}
			}
		}
	}
}

// Dense matches exercise the block reduction as well as the first-hit mask.
// In particular, a whole vector of the largest delimiter class must not
// overflow the byte reduction and be mistaken for a block without a boundary.
func TestSkipNumberDenseBoundaries(t *testing.T) {
	for _, size := range []int{16, 17, 31, 32, 63, 64} {
		for c := 0; c < 256; c++ {
			input := make([]byte, size+1)
			for i := 0; i < size; i++ {
				input[i] = byte(c)
			}
			input[size] = ']'
			want := size
			switch byte(c) {
			case ',', ']', '}', ' ', '\t', '\n', '\r':
				want = 0
			}
			if got := SkipNumber(string(input), 0); got != want {
				t.Fatalf("size=%d byte=%02x: end=%d, want %d", size, c, got, want)
			}
		}
	}
}
