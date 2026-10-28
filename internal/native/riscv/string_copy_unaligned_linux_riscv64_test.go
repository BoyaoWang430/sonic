//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"bytes"
	"strings"
	"testing"
	"unsafe"
)

// Check independent alignments, output capacities, and write boundaries.
func TestMisalignedStringCopies(t *testing.T) {
	for n := 0; n <= 32; n++ {
		for _, tail := range []string{"", "\n\"\\tail", "\x00tail", "<>&tail", "\u2028\u2029tail", "\xe2\x80"} {
			src := strings.Repeat("p", n) + tail + strings.Repeat("z", n%9)
			for _, op := range []struct {
				name string
				call func(string, []byte) (int, int)
			}{
				{"quote", func(s string, d []byte) (int, int) { return Quote(s, d, 0) }},
				{"double", func(s string, d []byte) (int, int) { return Quote(s, d, 1) }},
				{"html", HTMLEscape},
			} {
				_, full := scalarCopyTableReference(src, op.name, 7*len(src)+1)
				fullLen := len(full)
				for _, capacity := range []int{0, len(src) / 2, len(src), max(0, fullLen-1), fullLen, fullLen + 7} {
					wantN, wantText := scalarCopyTableReference(src, op.name, capacity)
					wantW := len(wantText)
					want := bytes.Repeat([]byte{0xa5}, capacity)
					copy(want, wantText)
					for srcOffset := 0; srcOffset < 8; srcOffset++ {
						input := bytes.Repeat([]byte{0x91}, len(src)+16)
						copy(input[srcOffset:], src)
						before := bytes.Clone(input)
						s := unsafe.String(&input[srcOffset], len(src))
						for dstOffset := 0; dstOffset < 8; dstOffset++ {
							storage := bytes.Repeat([]byte{0xa5}, capacity+16)
							dst := storage[dstOffset : dstOffset+capacity]
							gotN, gotW := op.call(s, dst)
							if gotN != wantN || gotW != wantW || !bytes.Equal(dst, want) || !bytes.Equal(input, before) {
								t.Fatalf("%s n=%d tail=%q capacity=%d src=%d dst=%d: got (%d,%d,%x), want (%d,%d,%x)", op.name, n, tail, capacity, srcOffset, dstOffset, gotN, gotW, dst, wantN, wantW, want)
							}
							if !bytes.Equal(storage[:dstOffset], bytes.Repeat([]byte{0xa5}, dstOffset)) || !bytes.Equal(storage[dstOffset+capacity:], bytes.Repeat([]byte{0xa5}, len(storage)-dstOffset-capacity)) {
								t.Fatal("copy wrote outside the destination")
							}
						}
					}
				}
			}
		}
	}
}

func TestMisalignedStringDecode(t *testing.T) {
	// Compare all offsets and exact aliasing with aligned output.
	for n := 0; n <= 32; n++ {
		for _, tail := range []string{"", `\nplain\t`, `\u4e2dplain\u0041`, `\ud83d\ude00plain`, `\q`, `\u12x4`, `\`, "\x01"} {
			body := strings.Repeat("p", n) + tail + strings.Repeat("z", n%9)
			for _, mode := range []uint64{0, 1, 2, 3, 4, 6, 12, 14} {
				src := body
				if mode&4 != 0 {
					src += `",trailing`
				}
				want, wantOutput, wantInPlaceResult, wantInPlace := stringDecodeAlignedBaseline(src, mode)
				for srcOffset := 0; srcOffset < 8; srcOffset++ {
					storage := bytes.Repeat([]byte{0x91}, len(src)+16)
					input := storage[srcOffset : srcOffset+len(src)]
					copy(input, src)
					before := bytes.Clone(storage)
					for dstOffset := 0; dstOffset < 8; dstOffset++ {
						outStorage := bytes.Repeat([]byte{0xa5}, len(src)+16)
						output := outStorage[dstOffset : dstOffset+len(src)]
						got := callStringDecodeTail(input, output, mode)
						if got != want || !bytes.Equal(output, wantOutput) || !bytes.Equal(storage, before) {
							t.Fatalf("decode n=%d tail=%q mode=%d src=%d dst=%d: got %+v want %+v", n, tail, mode, srcOffset, dstOffset, got, want)
						}
						if !bytes.Equal(outStorage[:dstOffset], bytes.Repeat([]byte{0xa5}, dstOffset)) || !bytes.Equal(outStorage[dstOffset+len(src):], bytes.Repeat([]byte{0xa5}, len(outStorage)-dstOffset-len(src))) {
							t.Fatal("decode wrote outside the destination")
						}
					}
					got := callStringDecodeTail(input, input, mode)
					if got != wantInPlaceResult || !bytes.Equal(input, wantInPlace) || !bytes.Equal(storage[:srcOffset], before[:srcOffset]) || !bytes.Equal(storage[srcOffset+len(src):], before[srcOffset+len(src):]) {
						t.Fatalf("in-place decode n=%d tail=%q mode=%d src=%d: got %+v want %+v", n, tail, mode, srcOffset, got, wantInPlaceResult)
					}
				}
			}
		}
	}
}

// Derive output and consumed length from fixed escape spellings.
func scalarCopyTableReference(src, operation string, capacity int) (int, string) {
	var parts []scalarEscapingPart
	for i := 0; i < len(src); {
		part := scalarEscapingPart{src[i : i+1], src[i : i+1]}
		if operation == "html" {
			for _, fixture := range htmlEscapeCases {
				if strings.HasPrefix(src[i:], fixture.src) {
					part = scalarEscapingPart{fixture.src, fixture.output}
					break
				}
			}
		} else {
			for _, fixture := range quoteEscapes {
				if part.src == fixture.src {
					part.dst = fixture.single
					if operation == "double" {
						part.dst = fixture.double
					}
					break
				}
			}
		}
		parts = append(parts, part)
		i += len(part.src)
	}
	_, consumed, output := scalarEscapingTailResult(parts, capacity)
	return consumed, output
}
