//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"bytes"
	"strings"
	"testing"
	"unsafe"
)

// Independent input/output alignments exercise bounded word loads and stores,
// including when an escape changes their relative alignment.
// Existing semantic tests provide the oracle; these tests require every
// alignment and capacity to produce the same bytes and cursor results.
func TestEscapeIndependentAlignmentAndCapacity(t *testing.T) {
	inputs := []string{
		"", "a", "abcdefg", "abcdefgh", "abcdefghi",
		strings.Repeat("0123456789abcdef", 3),
		"abcdefgh\"ijklmnop\\qrstuvwx\nyz",
		"\x00\x01\b\t\n\f\r\x1f\"\\\x7f\x80\xff",
		"abcdefgh<ijklmnop>qrstuvwx&yz",
		"abcdefgh\u2028ijklmnop\u2029qrstuvwx",
		"\xe2\x80\xa8\xe2\x80\xa9\xe2\x80\xa7\xe2\x80\xe2",
	}
	type kernel struct {
		name  string
		flags []uint64
		call  func(*byte, int, *byte, int, uint64) (int, int)
	}
	kernels := []kernel{
		{"quote", []uint64{0, 1}, quote},
		{"html", []uint64{0}, func(src *byte, n int, dst *byte, capacity int, _ uint64) (int, int) {
			return htmlEscape(src, n, dst, capacity)
		}},
	}
	for _, kernel := range kernels {
		t.Run(kernel.name, func(t *testing.T) {
			for _, input := range inputs {
				for _, flags := range kernel.flags {
					_, src, sp := decodeAlignedRegion(len(input), 0)
					copy(src, input)
					_, _, dp := decodeAlignedRegion(12*len(input)+16, 0)
					_, full := kernel.call(sp, len(input), dp, 12*len(input)+16, flags)
					for capacity := 0; capacity <= full+1; capacity++ {
						_, want, wp := decodeAlignedRegion(capacity, 0)
						wantConsumed, wantWritten := kernel.call(sp, len(input), wp, capacity, flags)
						for srcAlign := 0; srcAlign < 8; srcAlign++ {
							for dstAlign := 0; dstAlign < 8; dstAlign++ {
								srcStorage, src, sp := decodeAlignedRegion(len(input), srcAlign)
								copy(src, input)
								dstStorage, got, dp := decodeAlignedRegion(capacity, dstAlign)
								consumed, written := kernel.call(sp, len(input), dp, capacity, flags)
								if consumed != wantConsumed || written != wantWritten || !bytes.Equal(got, want) {
									t.Fatalf("input=%q flags=%d capacity=%d srcAlign=%d dstAlign=%d: got (%d,%d,%x), want (%d,%d,%x)", input, flags, capacity, srcAlign, dstAlign, consumed, written, got, wantConsumed, wantWritten, want)
								}
								if !bytes.Equal(src, []byte(input)) {
									t.Fatalf("input was modified: %q", input)
								}
								decodeCheckCanaries(t, srcStorage, sp, len(src))
								decodeCheckCanaries(t, dstStorage, dp, capacity)
							}
						}
					}
				}
			}
		})
	}
}

func TestStringDecodeIndependentAlignment(t *testing.T) {
	inputs := []string{
		"", "a", "abcdefg", "abcdefgh", "abcdefghi",
		strings.Repeat("0123456789abcdef", 3),
		strings.Repeat("0123456789abcdef", 3) + `"tail`,
		`abcdefgh\nijklmnop\tqrstuvwx\"yz"tail`,
		`abcdefgh\u4e2dijklmnop\ud83d\ude00qrstuvwx"tail`,
		`abcdefgh\ud800ijklmnop\udc00qrstuvwx"tail`,
		`abcdefgh\u0000ijklmnop\uffffqrstuvwx"tail`,
		`abcdefgh\\\"ijklmnop\\nqrstuvwx"tail`,
		`abcdefgh\xijklmnop`, `abcdefgh\u12`,
		"abcdefgh\x00ijklmnop\x1fqrstuvwx\xff\"tail",
	}
	modes := []uint64{0, 1, 2, 3, 4, 5, 6, 7, 12, 13, 14, 15}
	call := func(src *byte, n int, dst *byte, mode uint64) [5]int {
		end, written, escaped, errorPos, code := stringDecode(src, n, dst, mode)
		return [5]int{end, written, escaped, errorPos, code}
	}
	for _, input := range inputs {
		for _, mode := range modes {
			_, source, sp := decodeAlignedRegion(len(input), 0)
			copy(source, input)
			_, want, wp := decodeAlignedRegion(len(input), 0)
			wantResult := call(sp, len(input), wp, mode)
			for srcAlign := 0; srcAlign < 8; srcAlign++ {
				for dstAlign := 0; dstAlign < 8; dstAlign++ {
					srcStorage, src, sp := decodeAlignedRegion(len(input), srcAlign)
					copy(src, input)
					dstStorage, got, dp := decodeAlignedRegion(len(input), dstAlign)
					result := call(sp, len(input), dp, mode)
					if result != wantResult || !bytes.Equal(got, want) {
						t.Fatalf("input=%q mode=%d srcAlign=%d dstAlign=%d: got (%v,%x), want (%v,%x)", input, mode, srcAlign, dstAlign, result, got, wantResult, want)
					}
					if !bytes.Equal(src, []byte(input)) {
						t.Fatalf("input was modified: %q", input)
					}
					decodeCheckCanaries(t, srcStorage, sp, len(src))
					decodeCheckCanaries(t, dstStorage, dp, len(got))
				}
			}

			// In-place contraction must preserve unread input and leave the
			// bytes after the produced prefix identical across alignments.
			_, inPlaceWant, wp := decodeAlignedRegion(len(input), 0)
			copy(inPlaceWant, input)
			wantResult = call(wp, len(input), wp, mode)
			for align := 0; align < 8; align++ {
				storage, got, dp := decodeAlignedRegion(len(input), align)
				copy(got, input)
				result := call(dp, len(input), dp, mode)
				if result != wantResult || !bytes.Equal(got, inPlaceWant) {
					t.Fatalf("in-place input=%q mode=%d align=%d: got (%v,%x), want (%v,%x)", input, mode, align, result, got, wantResult, inPlaceWant)
				}
				decodeCheckCanaries(t, storage, dp, len(got))
			}
		}
	}
}

func decodeAlignedRegion(size, alignment int) (storage, data []byte, ptr *byte) {
	storage = bytes.Repeat([]byte{0xa5}, size+32)
	base := uintptr(unsafe.Pointer(&storage[8]))
	start := 8 + int((uintptr(alignment)-base)&7)
	return storage, storage[start : start+size], &storage[start]
}

func decodeCheckCanaries(t *testing.T, storage []byte, ptr *byte, size int) {
	t.Helper()
	start := int(uintptr(unsafe.Pointer(ptr)) - uintptr(unsafe.Pointer(&storage[0])))
	for _, b := range storage[:start] {
		if b != 0xa5 {
			t.Fatal("write before the logical buffer")
		}
	}
	for _, b := range storage[start+size:] {
		if b != 0xa5 {
			t.Fatal("write after the logical buffer")
		}
	}
}
