//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import "testing"

func TestNumberWordFirstNonDigit(t *testing.T) {
	// Exhaust each byte in each lane, with low/high digits before it and
	// non-ASCII bytes after it to exercise carry and borrow boundaries. Put the
	// run after an exponent: the production prefix scanner must stop at every
	// non-digit, including bytes that are markers in earlier number phases.
	for _, digit := range []byte{'0', '9'} {
		for offset := 0; offset < 16; offset++ {
			for size := 0; size <= 32; size++ {
				for lane := 0; lane < size; lane++ {
					for value := 0; value < 256; value++ {
						data := make([]byte, offset+3+size)
						copy(data[offset:], "1e1")
						start := offset + 3
						for i := start; i < len(data); i++ {
							data[i] = digit
						}
						data[start+lane] = byte(value)
						if lane+1 < size {
							data[start+lane+1] = 0xff
						}
						want := start
						for want < len(data) && data[want] >= '0' && data[want] <= '9' {
							want++
						}
						if got, _ := ScanNumber(string(data), offset); got != want {
							t.Fatalf("digit=%q offset=%d size=%d lane=%d value=%#x: got %d, want %d", digit, offset, size, lane, value, got, want)
						}
					}
				}
			}
		}
	}
}
