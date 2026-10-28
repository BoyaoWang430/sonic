//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestScalarUTF8BulkTransitions(t *testing.T) {
	storage := make([]byte, 136)
	for align := 0; align < 8; align++ {
		for _, size := range []int{31, 32, 33, 63, 64, 65, 127, 128, 129} {
			input := storage[align : align+size]
			for i := range input {
				input[i] = 'a'
			}
			src := unsafe.String(unsafe.SliceData(input), len(input))
			checkUTF8AgainstStdlib(t, src, 0)
			// The first non-ASCII byte can occur in any word of a bulk
			// load. Include valid runes spanning word and bulk boundaries,
			// then require scanning to resume through the ASCII suffix.
			for pos := range input {
				for _, marker := range []string{"\xff", "α", "中", "😊"} {
					if pos+len(marker) > size {
						continue
					}
					copy(input[pos:], marker)
					checkUTF8AgainstStdlib(t, src, 0)
					if marker == "\xff" {
						const base = 113
						next, count, full := utf8Scan(unsafe.SliceData(input), size, base, nil, 0)
						if next != base+pos || count != 0 || !full {
							t.Fatalf("align=%d size=%d invalid=%d: (%d, %d, %v), want (%d, 0, true)", align, size, pos, next, count, full, base+pos)
						}
					}
					for i := 0; i < len(marker); i++ {
						input[pos+i] = 'a'
					}
				}
			}
		}
	}
	runtime.KeepAlive(storage)
}

func TestScalarUTF8BulkCapacityAndResume(t *testing.T) {
	const size, base = 323, 113
	invalid := []int{31, 64, 127, 192, 257}
	storage := make([]byte, size+7)
	for align := 0; align < 8; align++ {
		input := storage[align : align+size]
		for i := range input {
			input[i] = 'a'
		}
		copy(input[95:], "中")
		copy(input[159:], "😊")
		copy(input[223:], "α")
		for _, pos := range invalid {
			input[pos] = 0xff
		}
		for _, slots := range []int{0, 1, 2, 4, 5} {
			out := make([]int, slots+2)
			start, consumed := 0, 0
			for {
				for i := range out {
					out[i] = -77
				}
				tail := input[start:]
				next, count, full := utf8Scan(unsafe.SliceData(tail), len(tail), base+start, &out[1], slots)
				wantCount := min(slots, len(invalid)-consumed)
				wantFull := consumed+wantCount < len(invalid)
				wantNext := base + size
				if wantFull {
					wantNext = base + invalid[consumed+wantCount]
				}
				if next != wantNext || count != wantCount || full != wantFull {
					t.Fatalf("align=%d slots=%d start=%d: (%d, %d, %v), want (%d, %d, %v)", align, slots, start, next, count, full, wantNext, wantCount, wantFull)
				}
				for i := 0; i < count; i++ {
					if got, want := out[1+i], base+invalid[consumed+i]; got != want {
						t.Fatalf("align=%d slots=%d start=%d output[%d]=%d, want %d", align, slots, start, i, got, want)
					}
				}
				for i, value := range out {
					if (i == 0 || i > count) && value != -77 {
						t.Fatalf("output outside recorded positions modified: %v", out)
					}
				}
				if !full || slots == 0 {
					break
				}
				start, consumed = next-base, consumed+count
			}
		}
	}
	runtime.KeepAlive(storage)
}
