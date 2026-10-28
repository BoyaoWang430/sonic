//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestScanStringUnalignedAndOverlap(t *testing.T) {
	bodies := []string{
		"", `"`, `"#"\]`, `\`, `\"`, `\\`, `\\"`, `\\\""`,
		`\n"`, `\n\t\\\""`, `\u1234"`, `\u12"`, `\uZZ"`, `\u12"x`,
		"\x00\"", "\x1f\"", `\x"`,
		strings.Repeat(`\n\t\\\"`, 3) + "abcdefghi" + `\n\t"#"\]`,
	}
	for align := 0; align < 16; align++ {
		for prefix := 0; prefix < 32; prefix++ {
			for _, body := range bodies {
				src := strings.Repeat("a", prefix) + body
				storage, ptr := alignedStringBlockInput(src, align)
				for strict := uint64(0); strict < 2; strict++ {
					reference := stringBlockReference(src, -1, strict != 0)
					want := stringScanResult{reference.end, reference.escape, reference.errorPos, reference.err}
					end, escape, errorPos, err := scanString(ptr, len(src), strict)
					if got := (stringScanResult{end, escape, errorPos, err}); got != want {
						t.Fatalf("align=%d strict=%d src=%q: got %+v, want %+v", align, strict, src, got, want)
					}
					checkScanStringBlock(t, ptr, len(src), strict)
				}
				runtime.KeepAlive(storage)
			}
		}
	}
	// Both an unaligned first word and a partial overlapping final word must
	// retain the loaded raw events after the closing quote.
	for _, src := range []string{`aa"#"\]x`, `aaaaaaaaaa"#"\]`} {
		for align := 0; align < 16; align++ {
			storage, ptr := alignedStringBlockInput(src, align)
			if width := checkScanStringBlock(t, ptr, len(src), 0); width == 0 {
				t.Fatalf("align=%d src=%q: missing loaded suffix", align, src)
			}
			runtime.KeepAlive(storage)
		}
	}
}

func TestUTF8UnalignedAndOverlap(t *testing.T) {
	for align := 0; align < 16; align++ {
		for prefix := 0; prefix < 16; prefix++ {
			for _, body := range []string{"", "α", "中", "😊", "α中😊", "\xff", "中\xff", "\xe0\x9f\xbf", "\xf0\x90", "\xffα\xff"} {
				for tail := 0; tail < 8; tail++ {
					content := strings.Repeat("a", prefix) + body + strings.Repeat("z", tail)
					storage, ptr := alignedStringBlockInput(content, align)
					src := unsafe.String(ptr, len(content))
					for _, start := range []int{0, len(src) / 2, len(src)} {
						checkUTF8AgainstStdlib(t, src, start)
						var invalid []int
						for offset, r := range src[start:] {
							if r == utf8.RuneError {
								_, size := utf8.DecodeRuneInString(src[start+offset:])
								if size == 1 {
									invalid = append(invalid, start+offset)
								}
							}
						}
						for slots := 0; slots < 4; slots++ {
							positions := make([]int, slots)
							wn, wc, wf := len(src), min(slots, len(invalid)), slots < len(invalid)
							if wf {
								wn = invalid[slots]
							}
							next, count, full := InvalidUTF8(src, start, positions)
							if next != wn || count != wc || full != wf || !equalUTF8Positions(positions[:count], invalid[:wc]) {
								t.Fatalf("align=%d src=%x start=%d slots=%d: (%d,%d,%v,%v), want (%d,%d,%v,%v)", align, src, start, slots, next, count, full, positions[:count], wn, wc, wf, invalid[:wc])
							}
						}
					}
					runtime.KeepAlive(storage)
				}
			}
		}
	}
}
