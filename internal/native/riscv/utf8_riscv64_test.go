//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestUTF8NativeCases(t *testing.T) {
	t.Logf("toolchain=%s scan width=%d bits", runtime.Version(), utf8VectorBytes()*8)
	// Include the one-invalid-byte-at-a-time cases from utf8_native_test.go.
	tests := []struct {
		name string
		src  string
		want []int
	}{
		{"empty", "", nil},
		{"ascii", "abc\x00\x7f", nil},
		{"mixed", strings.Repeat("helloα，景😊", 1000), nil},
		{"replacement-rune", "\ufffd", nil},
		{"continuation", "\xb0", []int{0}},
		{"two-header", "\xc0", []int{0}},
		{"three-header", "\xe0", []int{0}},
		{"four-header", "\xf0", []int{0}},
		{"overlong-two", "\xc0\xb0", []int{0, 1}},
		{"short-four", "\xf0\xb0xx", []int{0, 1}},
		{"short-four-prefix", "xx\xf0\xb0\xb0", []int{2, 3, 4}},
		{"surrogate-low", "\xed\xa0\x80", []int{0, 1, 2}},
		{"surrogate-high", "\xed\xb0\x80", []int{0, 1, 2}},
		{"surrogate-pair", "\xed\xa0\x80\xed\xb0\x80", []int{0, 1, 2, 3, 4, 5}},
		{"continuations", "\x80\x80\x80\x80", []int{0, 1, 2, 3}},
		{"mixed-errors", "x\xff中\xe2(\xa1y", []int{1, 5, 7}},
		{"max-rune", "\xf4\x8f\xbf\xbf", nil},
		{"above-max", "\xf4\x90\x80\x80", []int{0, 1, 2, 3}},
		{"overlong-three", "\xe0\x9f\xbf", []int{0, 1, 2}},
		{"overlong-four", "\xf0\x8f\xbf\xbf", []int{0, 1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidUTF8(tt.src); got != (len(tt.want) == 0) {
				t.Fatalf("ValidUTF8(%q) = %v", tt.src, got)
			}
			positions := make([]int, len(tt.src))
			next, count, full := InvalidUTF8(tt.src, 0, positions)
			if next != len(tt.src) || count != len(tt.want) || full || !equalUTF8Positions(positions[:count], tt.want) {
				t.Fatalf("InvalidUTF8(%q) = (%d, %d, %v), positions %v; want %v", tt.src, next, count, full, positions[:count], tt.want)
			}
			checkUTF8AgainstStdlib(t, tt.src, 0)
		})
	}
	if next, count, full := utf8Scan(nil, 0, 19, nil, 0); next != 19 || count != 0 || full {
		t.Fatalf("empty direct assembly call = (%d, %d, %v)", next, count, full)
	}
	input := "abc\xffα"
	positions := [1]int{-1}
	if next, count, full := utf8Scan(unsafe.StringData(input), len(input), 11, &positions[0], 1); next != 11+len(input) || count != 1 || full || positions[0] != 14 {
		t.Fatalf("direct assembly with base = (%d, %d, %v), positions %v", next, count, full, positions)
	}
}

func TestInvalidUTF8CapacityAndResume(t *testing.T) {
	for _, tt := range []struct {
		src          string
		start, slots int
		next         int
		want         []int
		full         bool
	}{
		{"", 0, 0, 0, nil, false},
		{"αz", 0, 0, 3, nil, false},
		{"abc\xffz", 0, 0, 3, nil, true},
		{"\xffα", 0, 1, 3, []int{0}, false},
		{"\xffα\xffz", 0, 1, 3, []int{0}, true},
		{"\xffα\xffz", 3, 1, 5, []int{3}, false},
		{"\xffα\xffz", 4, 0, 5, nil, false},
		{"\xffα\xffz", 5, 0, 5, nil, false},
		{"中", 1, 2, 3, []int{1, 2}, false},
	} {
		// Backing capacity exceeds len: no output outside the supplied slice
		// length is allowed, even though that memory is accessible.
		storage := make([]int, tt.slots+2)
		for i := range storage {
			storage[i] = -77
		}
		next, count, full := InvalidUTF8(tt.src, tt.start, storage[1:1+tt.slots])
		if next != tt.next || full != tt.full || count != len(tt.want) || !equalUTF8Positions(storage[1:1+count], tt.want) {
			t.Errorf("src=%q start=%d slots=%d: (%d, %d, %v), positions %v; want (%d, %d, %v), %v", tt.src, tt.start, tt.slots, next, count, full, storage[1:1+count], tt.next, len(tt.want), tt.full, tt.want)
		}
		if storage[0] != -77 || storage[len(storage)-1] != -77 {
			t.Fatalf("output canary modified: %v", storage)
		}
	}
	for _, src := range []string{
		"abc\xff中\xed\xa0\x80xyz\xf0\x90\x80\x80\xc2",
		strings.Repeat("\xffabα", 5000), // exceeds native StateMachine capacity
	} {
		all := make([]int, len(src))
		end, total, full := InvalidUTF8(src, 0, all)
		if end != len(src) || full {
			t.Fatal("unbounded collection failed")
		}
		for _, slots := range []int{1, 2, 7, 4096} {
			var got []int
			buf := make([]int, slots)
			next := 0
			for {
				start := next
				var count int
				next, count, full = InvalidUTF8(src, start, buf)
				got = append(got, buf[:count]...)
				if !full {
					break
				}
				if next <= start {
					t.Fatalf("no progress: start=%d next=%d slots=%d", start, next, slots)
				}
			}
			if next != len(src) || !equalUTF8Positions(got, all[:total]) {
				t.Fatalf("batch size %d differs from whole-input collection", slots)
			}
		}
		// Reconstruction follows the public CorrectWith consumer protocol.
		var corrected strings.Builder
		start := 0
		for _, pos := range all[:total] {
			corrected.WriteString(src[start:pos])
			corrected.WriteRune(utf8.RuneError)
			start = pos + 1
		}
		corrected.WriteString(src[start:])
		if got, want := corrected.String(), string([]rune(src)); got != want {
			t.Fatal("recorded invalid bytes do not reproduce per-byte Unicode replacement")
		}
	}
}

func TestUTF8LengthsAndAlignments(t *testing.T) {
	vl := utf8VectorBytes()
	storage := make([]byte, 5*vl+16)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			for _, suffix := range []string{"", "\x7f", "α", "中", "😊", "\xff", "\xe2(\xa1", "\xf0\x90\x80"} {
				input := storage[align : align+n+len(suffix)]
				for i := 0; i < n; i++ {
					input[i] = 'a'
				}
				copy(input[n:], suffix)
				src := unsafe.String(unsafe.SliceData(input), len(input))
				checkUTF8AgainstStdlib(t, src, 0)
			}
		}
	}
}

func TestUTF8ByteAndRuneRanges(t *testing.T) {
	// Every one- and two-byte input, including all possible continuation and
	// illegal leading bytes, is checked against the existing standard library.
	var pair [2]byte
	for a := 0; a < 256; a++ {
		pair[0] = byte(a)
		checkUTF8AgainstStdlib(t, string(pair[:1]), 0)
		for b := 0; b < 256; b++ {
			pair[1] = byte(b)
			checkUTF8AgainstStdlib(t, string(pair[:]), 0)
		}
	}
	// Exercise all legal code points in a single input, not a second decoder.
	var valid strings.Builder
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		valid.WriteRune(r)
	}
	src := valid.String()
	if !ValidUTF8(src) {
		t.Fatal("rejected an input containing every legal Unicode code point")
	}
	if next, count, full := InvalidUTF8(src, 0, nil); next != len(src) || count != 0 || full {
		t.Fatalf("valid Unicode range = (%d, %d, %v)", next, count, full)
	}
	// Check second-byte restrictions and later continuation ranges, including
	// overlong encodings, surrogates, and the upper Unicode boundary.
	for lead := 0xe0; lead <= 0xf5; lead++ {
		for second := 0; second < 256; second++ {
			for _, tail := range []string{"", "\x7f", "\x80", "\xbf", "\xc0", "\x80\x80", "\xbf\xbf", "\x80\x7f"} {
				src := string([]byte{byte(lead), byte(second)}) + tail
				checkUTF8AgainstStdlib(t, src, 0)
			}
		}
	}
}

func TestUTF8RandomAndRestart(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 1000; i++ {
		buf := make([]byte, rng.Intn(1024))
		_, _ = rng.Read(buf)
		src := string(buf)
		checkUTF8AgainstStdlib(t, src, 0)
		checkUTF8AgainstStdlib(t, src, rng.Intn(len(src)+1))
	}
}

func checkUTF8AgainstStdlib(t *testing.T, src string, start int) {
	t.Helper()
	// The compatibility implementation also uses unicode/utf8. Range and
	// DecodeRuneInString supply offsets; this is not a separate UTF-8 algorithm.
	if got, want := ValidUTF8(src[start:]), utf8.ValidString(src[start:]); got != want {
		t.Fatalf("ValidUTF8(%x) = %v, want %v", src[start:], got, want)
	}
	var want []int
	for offset, r := range src[start:] {
		if r == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(src[start+offset:])
			if size == 1 {
				want = append(want, start+offset)
			}
		}
	}
	positions := make([]int, len(src)-start)
	next, count, full := InvalidUTF8(src, start, positions)
	if next != len(src) || full || count != len(want) || !equalUTF8Positions(positions[:count], want) {
		t.Fatalf("InvalidUTF8(%x, %d) = (%d, %d, %v), positions %v; want %v", src, start, next, count, full, positions[:count], want)
	}
}

func FuzzUTF8Operations(f *testing.F) {
	for _, src := range []string{
		"", "ascii", "helloα，景😊", "\xff", "\xed\xa0\x80", "\xf0\x90\x80", "\ufffd\x80",
		strings.Repeat("α中😊", 20),
		strings.Repeat("中", 21) + "\xf4\x90\x80\x80" + strings.Repeat("α😊", 20),
		strings.Repeat("😊", 20) + "\xe0\x9f\xbf",
	} {
		f.Add(src, uint16(0))
	}
	f.Fuzz(func(t *testing.T, src string, start uint16) {
		if len(src) > 8192 {
			return
		}
		checkUTF8AgainstStdlib(t, src, int(start)%(len(src)+1))
	})
}
