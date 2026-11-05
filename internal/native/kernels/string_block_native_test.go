//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"runtime"
	"strings"
	"testing"
)

func TestRelaxedStringBlock(t *testing.T) {
	// Compare the new entry directly with the byte scanner: the ordinary and
	// block assembly entries share a body and cannot serve as separate oracles.
	// Raw tail events include escaped quotes and ignore controls after the end.
	tail := "#\"#\\]\x00\x1f\\\"\\\\"
	for _, prefix := range []int{0, 15, 16, 31, 32, 63, 64, 127, 128} {
		for _, suffix := range []string{
			`"` + tail, `\"a"` + tail, `\\"` + tail, `\\\"a"` + tail,
			`\x"` + tail, `\u12"` + tail, "\x00\x1f\"" + tail,
			"", `\`, `\"`, strings.Repeat(`\`, 65),
		} {
			content := strings.Repeat("a", prefix) + suffix
			want := stringBlockReference(`"`+content, 0, false)
			want.end--
			if want.escape >= 0 {
				want.escape--
			}
			if want.errorPos >= 0 {
				want.errorPos--
			}
			for align := 0; align < 16; align++ {
				storage, src := alignedStringBlockInput(content, align)
				end, escape, errorPos, err, events, width := scanStringRelaxedBlock(src, len(content))
				if got := (stringBlockResult{end, escape, errorPos, err}); got != want {
					t.Fatalf("align=%d content=%q: result=%+v, want %+v", align, content, got, want)
				}
				if width < 0 || width > minInt(63, len(content)-end) || err != 0 && width != 0 {
					t.Fatalf("align=%d content=%q: end=%d err=%v tail width=%d", align, content, end, err, width)
				}
				var raw uint64
				for i, c := range []byte(content[end : end+width]) {
					if c == '"' || c == '\\' {
						raw |= uint64(1) << uint(i)
					}
				}
				if events != raw {
					t.Fatalf("align=%d content=%q: tail events=%x, want %x", align, content, events, raw)
				}
				runtime.KeepAlive(storage)
			}
		}
	}
}

func TestRelaxedStringPackedChunks(t *testing.T) {
	// Each rotation places raw quote/slash events in different lanes of the
	// packed 32/64-byte masks, including events beyond the closing quote.
	for _, prefix := range []int{0, 32, 96} {
		for shift := 0; shift < 16; shift++ {
			content := strings.Repeat("a", prefix) + `"` +
				strings.Repeat("b", shift) + strings.Repeat(`"#\]`, 32)
			for align := 0; align < 16; align++ {
				storage, pointer := alignedStringBlockInput(content, align)
				checkScanStringBlock(t, pointer, len(content), 0)
				runtime.KeepAlive(storage)
			}
		}
	}
	// An escape at a chunk boundary consumes the next ordinary byte. A
	// following plain chunk must clear that carry before the final quote.
	for _, prefix := range []int{31, 63, 95, 127, 159} {
		content := strings.Repeat("a", prefix) + `\` + strings.Repeat("b", 64) + `"#\]`
		for align := 0; align < 16; align++ {
			storage, pointer := alignedStringBlockInput(content, align)
			checkScanStringBlock(t, pointer, len(content), 0)
			end, escape, errorPos, err := scanStringRelaxed(pointer, len(content))
			if end != prefix+66 || escape != prefix || errorPos != -1 || err != 0 {
				t.Fatalf("prefix=%d align=%d: result=(%d,%d,%d,%v)", prefix, align, end, escape, errorPos, err)
			}
			runtime.KeepAlive(storage)
		}
	}
}
