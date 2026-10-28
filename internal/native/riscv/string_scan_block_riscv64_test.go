//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// The block API preserves the complete scan result and exposes only raw
// quote/backslash events in bytes already loaded after the closing quote.
func checkScanStringBlock(t *testing.T, src *byte, size int, validate uint64) int {
	t.Helper()
	wantEnd, wantEscape, wantErrorPos, wantErr := scanString(src, size, validate)
	end, escape, errorPos, err, events, width := scanStringBlock(src, size, validate)
	if got, want := (stringScanResult{end, escape, errorPos, err}), (stringScanResult{wantEnd, wantEscape, wantErrorPos, wantErr}); got != want {
		t.Fatalf("size=%d validate=%d: result=%+v, want %+v", size, validate, got, want)
	}
	if err != 0 || end == size {
		if events != 0 || width != 0 {
			t.Fatalf("size=%d end=%d err=%v: unexpected tail (%x,%d)", size, end, err, events, width)
		}
		return width
	}
	if width < 0 || width > min(63, size-end) {
		t.Fatalf("size=%d end=%d: tail width=%d", size, end, width)
	}
	var want uint64
	for i, c := range unsafe.Slice(src, size)[end : end+width] {
		if c == '"' || c == '\\' {
			want |= uint64(1) << uint(i)
		}
	}
	if events != want {
		t.Fatalf("validate=%d end=%d width=%d tail=%q: events=%x, want %x", validate, end, width,
			unsafe.Slice(src, size)[end:end+width], events, want)
	}
	return width
}

func alignedStringBlockInput(content string, align int) ([]byte, *byte) {
	storage := make([]byte, len(content)+32)
	base := unsafe.SliceData(storage)
	offset := (align - int(uintptr(unsafe.Pointer(base))&15) + 16) & 15
	copy(storage[offset:], content)
	return storage, &storage[offset]
}

func TestScanStringBlockTailEvents(t *testing.T) {
	// Quote/# and backslash/] differ by the low bit. Borrow-based equality
	// approximations must not turn the second byte into an additional event.
	// Controls after the closing quote are never part of the exported mask,
	// even when the current string was scanned in strict mode. Escaped quotes
	// in the tail are raw events: their future token has not been scanned yet.
	tails := []string{"", "#", `#"\]`, "\x00\x1f#\"#\\]\\\"\\\\", strings.Repeat(`"#\]`, 18)}
	for align := 0; align < 16; align++ {
		for quote := 0; quote <= 129; quote++ {
			for _, tail := range tails {
				content := strings.Repeat("a", quote) + `"` + tail
				storage, pointer := alignedStringBlockInput(content, align)
				for _, validate := range []uint64{0, 1} {
					width := checkScanStringBlock(t, pointer, len(content), validate)
					// Both backends load at least eight bytes at an aligned
					// start. This checks useful metadata, not just an empty
					// but otherwise valid cache result.
					if align&7 == 0 && quote < 7 && len(content) >= 8 && width == 0 {
						t.Fatalf("align=%d quote=%d len=%d validate=%d: missing loaded tail", align, quote, len(content), validate)
					}
					// A 64-byte boundary is also an eight-byte boundary.
					if align == 0 && quote == 63 && width != 0 {
						t.Fatalf("block-final quote: width=%d, want 0", width)
					}
				}
				runtime.KeepAlive(storage)
			}
		}
	}
}

func TestScanStringBlockEscapesAndErrors(t *testing.T) {
	// Vary the prefix across scalar word and RVV 64-byte boundaries. Odd/even
	// slash runs and Unicode escapes can move the final loaded block's origin.
	for align := 0; align < 16; align++ {
		for prefix := 0; prefix <= 129; prefix++ {
			for _, body := range []string{`\"`, `\\`, `\\\"`, `\n`, `\u1234`, `\uD800\uDC00`, `\x`, `\u12+4`, "\x00", "\x1f"} {
				content := strings.Repeat("a", prefix) + body + `"` + "#\"#\\]\x00\x1f\\\"\\\\"
				storage, pointer := alignedStringBlockInput(content, align)
				for _, validate := range []uint64{0, 1} {
					checkScanStringBlock(t, pointer, len(content), validate)
				}
				runtime.KeepAlive(storage)
			}
		}
		for _, content := range []string{"", "unterminated", `\`, `\"`, `\u`, `\u0`, `\u01`, `\u012`, `\u1234`, `\u12"`, `\uZZ"`, `a\nb\x"`} {
			storage, pointer := alignedStringBlockInput(content, align)
			for _, validate := range []uint64{0, 1} {
				checkScanStringBlock(t, pointer, len(content), validate)
			}
			runtime.KeepAlive(storage)
		}
	}
	for _, validate := range []uint64{0, 1} {
		checkScanStringBlock(t, nil, 0, validate)
	}
}

func FuzzScanStringBlock(f *testing.F) {
	tail := "#\"#\\]\x00\x1f\\\"\\\\"
	for _, content := range []string{
		"", `"`, `"` + tail, `a\"b"` + tail, `a\\b"` + tail,
		`\u12"`, `\x"`, "\x00\"",
		strings.Repeat("a", 63) + `"` + tail,
		strings.Repeat("a", 64) + `"` + tail,
		strings.Repeat("a", 127) + `"` + tail,
		strings.Repeat(`\`, 63) + `"a"` + tail,
		strings.Repeat(`\`, 64) + `"` + tail,
		strings.Repeat("a", 61) + `\u1234"` + tail,
	} {
		for _, strict := range []bool{false, true} {
			f.Add([]byte(content), uint8(len(content)&15), strict)
		}
	}
	f.Fuzz(func(t *testing.T, content []byte, alignment uint8, strict bool) {
		if len(content) > 65536 {
			t.Skip()
		}
		storage, pointer := alignedStringBlockInput(string(content), int(alignment&15))
		var validate uint64
		if strict {
			validate = 1
		}
		checkScanStringBlock(t, pointer, len(content), validate)
		runtime.KeepAlive(storage)
	})
}
