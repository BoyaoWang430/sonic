//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestNumberScanMisalignedTail(t *testing.T) {
	// The aligned baseline verifies cursor and error consistency at every
	// offset. Fixed ABI fixtures and TestNumberScanJSONGrammar independently
	// check number syntax; this is not an independent grammar oracle.
	check := func(src string, offset int) {
		storage, ptr := alignedStringBlockInput(src, offset)
		baseline, aligned := alignedStringBlockInput(src, 0)
		for mode := uint64(0); mode < 3; mode++ {
			wantEnd, wantErr := numberScan(aligned, len(src), mode)
			gotEnd, gotErr := numberScan(ptr, len(src), mode)
			if gotEnd != wantEnd || gotErr != wantErr {
				t.Fatalf("offset=%d mode=%d src=%q: (%d,%v), want (%d,%v)", offset, mode, src, gotEnd, gotErr, wantEnd, wantErr)
			}
		}
		runtime.KeepAlive(storage)
		runtime.KeepAlive(baseline)
	}
	// Markers from an already consumed prefix must not be replayed when a
	// final word overlaps that prefix; all final lengths and offsets matter.
	for n := 0; n <= 72; n++ {
		for offset := 0; offset < 8; offset++ {
			for _, prefix := range []string{"", "-", "0.", "123456789.", "123456789e+"} {
				for _, suffix := range []string{"", ",", ".", "e+", "e-2", "+4+5", "e.2", "\x00", "\xff"} {
					check(prefix+strings.Repeat("7", n)+suffix, offset)
				}
			}
		}
	}
	rng := rand.New(rand.NewSource(4721))
	alphabet := []byte("0123456789.eE+-,]} \t\r\n\x00\xff")
	for i := 0; i < 12000; i++ {
		input := make([]byte, rng.Intn(96))
		for j := range input {
			input[j] = alphabet[rng.Intn(len(alphabet))]
		}
		check(string(input), rng.Intn(8))
	}
	// Preserve the zero-length ABI even for a nil source.
	for mode := uint64(0); mode < 3; mode++ {
		we, wc := 0, types.ERR_EOF
		if mode == 2 {
			wc = 0
		}
		ge, gc := numberScan(nil, 0, mode)
		if ge != we || gc != wc {
			t.Fatal("empty mode", mode, ge, gc, we, wc)
		}
	}
}

// Short remainders must keep markers recorded before word preparation was
// deferred, including the strict scanner's duplicate-marker error precedence.
func TestNumberScanStrictBoundedContinuation(t *testing.T) {
	for _, tc := range []struct {
		src string
		end int
		err types.ParsingError
	}{
		{"123456789", 9, 0},
		{"-123456789", 10, 0},
		{"123456789.", 9, types.ERR_INVALID_CHAR},
		{"123456789e", 9, types.ERR_INVALID_CHAR},
		{"123456789e+", 10, types.ERR_INVALID_CHAR},
		{"123456789e+2", 12, 0},
		{"123456789e+2.3", 12, types.ERR_INVALID_CHAR},
		{"123456789+2e3", 9, types.ERR_INVALID_CHAR},
		{"1.23456789e+", 11, types.ERR_INVALID_CHAR},
		{"1.23456789e2e3", 12, types.ERR_INVALID_CHAR},
		{"1.23456789.2", 10, types.ERR_INVALID_CHAR},
		{"1e23456789.2e3", 12, types.ERR_INVALID_CHAR},
		{"123456789.2e+", 12, types.ERR_INVALID_CHAR},
	} {
		for offset := 0; offset < 8; offset++ {
			storage, ptr := alignedStringBlockInput(tc.src, offset)
			end, err := numberScan(ptr, len(tc.src), 1)
			if end != tc.end || err != tc.err {
				t.Fatalf("offset=%d src=%q: (%d,%v), want (%d,%v)", offset, tc.src, end, err, tc.end, tc.err)
			}
			runtime.KeepAlive(storage)
		}
	}
}
