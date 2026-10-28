//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"
	"unsafe"
)

func TestUTF8VectorBlocks(t *testing.T) {
	vl := utf8VectorBytes()
	for _, tc := range []struct{ name, text string }{
		{"two-byte", "\u0080\u07ffα"},
		{"three-byte", "\u0800\ud7ff\ue000\uffff中"},
		{"four-byte", "\U00010000\U0010ffff😊"},
		{"chinese", "中文输入与向量边界"},
		{"emoji", "😊😀🌍🚀"},
		{"mixed", "aα中😊\x00\x7f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat(tc.text, (4*vl+3)/len(tc.text)+1)
			storage := make([]byte, 5*vl+3)
			for align := 0; align < vl; align++ {
				copy(storage[align:], text)
				for n := 0; n <= 4*vl+3; n++ {
					// Cutting the bytes exercises both complete code points and
					// every possible incomplete sequence at the logical end.
					src := unsafe.String(&storage[align], n)
					checkUTF8AgainstStdlib(t, src, 0)
				}
			}
			// A caller may resume at a continuation byte. That byte must be
			// reported independently rather than inheriting prior context.
			for start := 0; start <= 3*vl; start++ {
				checkUTF8AgainstStdlib(t, text, start)
			}
		})
	}
}

func TestUTF8VectorInvalidLanes(t *testing.T) {
	vl := utf8VectorBytes()
	for _, unit := range []string{"α", "中", "😊"} {
		for offset := 0; offset <= 3*vl+3; offset++ {
			// The malformed sequence starts at every byte offset while its
			// prefix and suffix still exercise valid multibyte vector blocks.
			prefix := strings.Repeat(unit, offset/len(unit)) + strings.Repeat("a", offset%len(unit))
			suffix := strings.Repeat(unit, 2*vl/len(unit)+1)
			for _, bad := range []string{
				"\x80", "\xbf", "\xc0\x80", "\xc1\xbf", "\xf5\x80\x80\x80", "\xff",
				"\xe0\x9f\xbf", "\xed\xa0\x80", "\xed\xbf\xbf",
				"\xf0\x8f\xbf\xbf", "\xf4\x90\x80\x80",
				"\xc2a", "\xe1a\x80", "\xe1\x80a",
				"\xf1a\x80\x80", "\xf1\x80a\x80", "\xf1\x80\x80a",
				"\xc2", "\xe1\x80", "\xf1\x80\x80",
			} {
				checkUTF8AgainstStdlib(t, prefix+bad+suffix, 0)
				checkUTF8AgainstStdlib(t, prefix+bad, 0)
			}
		}
	}
}

func TestUTF8VectorBytePairs(t *testing.T) {
	vl := utf8VectorBytes()
	// Unlike the short-input pair test, these pairs occur in a vector-sized
	// multibyte input. The three prefix lengths vary the block boundary.
	prefix := strings.Repeat("α中😊", 2*vl/len("α中😊")+1)
	suffix := "a" + strings.Repeat("中😊", 2*vl/len("中😊")+1)
	var pair [2]byte
	for padding := 0; padding < 3; padding++ {
		prefix += "a"
		for a := 0; a < 256; a++ {
			pair[0] = byte(a)
			for b := 0; b < 256; b++ {
				pair[1] = byte(b)
				checkUTF8AgainstStdlib(t, prefix+string(pair[:])+suffix, 0)
			}
		}
	}
}

func TestUTF8VectorCapacityAndResume(t *testing.T) {
	vl := utf8VectorBytes()
	prefix := strings.Repeat("α中😊", 3*vl)
	middle := strings.Repeat("中文", 2*vl)
	suffix := strings.Repeat("😊α", 2*vl)
	src := prefix + "\xed\xa0\x80" + middle + "\xff" + suffix + "\xf0\x90"
	second := len(prefix) + 3 + len(middle)
	want := []int{len(prefix), len(prefix) + 1, len(prefix) + 2, second, len(src) - 2, len(src) - 1}
	checkUTF8AgainstStdlib(t, src, 0)
	for slots := 0; slots <= len(want)+1; slots++ {
		storage := make([]int, slots+2)
		for i := range storage {
			storage[i] = -77
		}
		next, count, full := InvalidUTF8(src, 0, storage[1:1+slots])
		wantCount := min(slots, len(want))
		wantNext, wantFull := len(src), false
		if slots < len(want) {
			wantNext, wantFull = want[slots], true
		}
		if next != wantNext || count != wantCount || full != wantFull || !equalUTF8Positions(storage[1:1+count], want[:wantCount]) {
			t.Fatalf("slots=%d: (%d,%d,%v), positions=%v; want (%d,%d,%v), positions=%v", slots, next, count, full, storage[1:1+count], wantNext, wantCount, wantFull, want[:wantCount])
		}
		if storage[0] != -77 {
			t.Fatal("write before output positions")
		}
		for _, untouched := range storage[1+count:] {
			if untouched != -77 {
				t.Fatal("write outside returned positions")
			}
		}
	}
	for slots := 1; slots <= len(want); slots++ {
		var got []int
		out := make([]int, slots)
		for start := 0; ; {
			next, count, full := InvalidUTF8(src, start, out)
			got = append(got, out[:count]...)
			if !full {
				if next != len(src) {
					t.Fatalf("premature completion at %d", next)
				}
				break
			}
			if next <= start {
				t.Fatalf("resume made no progress at %d", start)
			}
			start = next
		}
		if !equalUTF8Positions(got, want) {
			t.Fatalf("slots=%d: positions=%v, want %v", slots, got, want)
		}
	}
	for _, valid := range []string{prefix, middle, suffix} {
		if next, count, full := InvalidUTF8(valid, 0, nil); next != len(valid) || count != 0 || full {
			t.Fatalf("zero-capacity valid multibyte input = (%d,%d,%v)", next, count, full)
		}
	}
}
