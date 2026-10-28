//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

type stringScanResult struct {
	end, escape, errorPos int
	err                   types.ParsingError
}

func checkStringScan(t *testing.T, src string, start int, validate bool, want stringScanResult) {
	t.Helper()
	end, escape, errorPos, err := ScanString(src, start, validate)
	got := stringScanResult{end, escape, errorPos, err}
	if got != want {
		t.Fatalf("ScanString(%q, %d, %v) = %+v, want %+v", src, start, validate, got, want)
	}
}

func TestScanString(t *testing.T) {
	t.Logf("toolchain=%s scan width=%d bits", runtime.Version(), stringScanVectorBytes()*8)
	for _, tt := range []struct {
		name, src string
		start     int
		want      stringScanResult
	}{
		{"empty", `""`, 0, stringScanResult{2, -1, -1, 0}},
		{"plain", `"hello"`, 0, stringScanResult{7, -1, -1, 0}},
		{"offset", `x ["hello"]`, 3, stringScanResult{10, -1, -1, 0}},
		{"trailing-backslash", `"hello"\x`, 0, stringScanResult{7, -1, -1, 0}},
		{"utf8", `"你好"`, 0, stringScanResult{8, -1, -1, 0}},
		{"invalid-utf8", "\"\xff\xc0\x80\"", 0, stringScanResult{5, -1, -1, 0}},
		{"only-opening", `"`, 0, stringScanResult{1, -1, 1, types.ERR_EOF}},
		{"unterminated", `"hello`, 0, stringScanResult{6, -1, 6, types.ERR_EOF}},
		{"dangling-backslash", `"hello\`, 0, stringScanResult{7, 6, 7, types.ERR_EOF}},
		{"escaped-final-quote", `"\"`, 0, stringScanResult{3, 1, 3, types.ERR_EOF}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkStringScan(t, tt.src, tt.start, false, tt.want)
			checkStringScan(t, tt.src, tt.start, true, tt.want)
		})
	}
	end, escape, errorPos, err := scanString(nil, 0, 1)
	if got, want := (stringScanResult{end, escape, errorPos, err}), (stringScanResult{0, -1, 0, types.ERR_EOF}); got != want {
		t.Fatalf("scanString(nil, 0, 1) = %+v, want %+v", got, want)
	}
}

func TestScanStringEscapes(t *testing.T) {
	for _, body := range []string{
		`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`,
		`\u0123`, `\u4567`, `\u89ab`, `\ucdef`, `\uAbCd`, `\uEF01`, `\uFFFF`, `\ud800`, `\udc00`,
		`\uD800\uDC00`, `\uD800a`, `\uD800\u0041`, `\uD800\uD800`,
		`a\nb\tc`, `a\"b\\c`,
	} {
		src := `"` + body + `"`
		first := 1
		if body[0] == 'a' {
			first = 2
		}
		for _, validate := range []bool{false, true} {
			checkStringScan(t, src, 0, validate, stringScanResult{len(src), first, -1, 0})
		}
	}
	for _, body := range []string{`\a`, `\x`, `\U`, `\v`, `\0`, `\z`, "\\\n", `\u123x`, `\ux123`, `\u12+4`, `\uDE-G`, "\\u\xff123"} {
		src := `"` + body + `"`
		checkStringScan(t, src, 0, false, stringScanResult{len(src), 1, -1, 0})
		checkStringScan(t, src, 0, true, stringScanResult{1, 1, 1, types.ERR_INVALID_CHAR})
	}
	for _, src := range []string{`"\u`, `"\u0`, `"\u01`, `"\u012`, `"\u0123`} {
		for _, validate := range []bool{false, true} {
			checkStringScan(t, src, 0, validate, stringScanResult{len(src), 1, len(src), types.ERR_EOF})
		}
	}
	// The helper checks that all four hex bytes exist before classifying them.
	checkStringScan(t, `"\u12"`, 0, true, stringScanResult{6, 1, 6, types.ERR_EOF})
	checkStringScan(t, `"\u12"`, 0, false, stringScanResult{6, 1, -1, 0})
	checkStringScan(t, `"\uZZ"`, 0, true, stringScanResult{6, 1, 6, types.ERR_EOF})
	checkStringScan(t, `"\u12"x`, 0, true, stringScanResult{1, 1, 1, types.ERR_INVALID_CHAR})
	// Keep the first escape separately from the later offending backslash.
	checkStringScan(t, `"a\nb\x"`, 0, true, stringScanResult{5, 2, 5, types.ERR_INVALID_CHAR})
	checkStringScan(t, `"a\nb\uQQQQ"`, 0, true, stringScanResult{5, 2, 5, types.ERR_INVALID_CHAR})
	checkStringScan(t, `junk "a\x"`, 5, true, stringScanResult{7, 7, 7, types.ERR_INVALID_CHAR})
	checkStringScan(t, `junk "\`, 5, true, stringScanResult{7, 6, 7, types.ERR_EOF})
}

func TestScanStringControls(t *testing.T) {
	for c := byte(0); c < 32; c++ {
		src := "\"" + string([]byte{c}) + "\""
		checkStringScan(t, src, 0, false, stringScanResult{3, -1, -1, 0})
		checkStringScan(t, src, 0, true, stringScanResult{1, -1, 1, types.ERR_INVALID_CHAR})
	}
	checkStringScan(t, "\"\\n\x01\"", 0, true, stringScanResult{3, 1, 3, types.ERR_INVALID_CHAR})
	checkStringScan(t, "\"x\"\x00", 0, true, stringScanResult{3, -1, -1, 0})
}

func TestScanStringVectorBoundaries(t *testing.T) {
	const vl = 64 // the scalar-mask width and maximum requested VL
	for pad := 0; pad <= 3*vl+1; pad++ {
		prefix := strings.Repeat("a", pad)
		for _, body := range []string{`\"`, `\\`, `\n`, `\u1234`, `\uD800\uDC00`} {
			src := `"` + prefix + body + strings.Repeat("b", vl+1) + `"`
			for _, validate := range []bool{false, true} {
				checkStringScan(t, src, 0, validate, stringScanResult{len(src), 1 + pad, -1, 0})
			}
		}
		// Bad escape syntax must be rejected both in short and long strings;
		// the old C SIMD path did not validate all escapes in complete blocks.
		for _, body := range []string{`\x`, `\uZZZZ`} {
			src := `"` + prefix + body + strings.Repeat("b", 3*vl) + `"`
			checkStringScan(t, src, 0, true, stringScanResult{1 + pad, 1 + pad, 1 + pad, types.ERR_INVALID_CHAR})
			checkStringScan(t, src, 0, false, stringScanResult{len(src), 1 + pad, -1, 0})
		}
		src := `"` + prefix + "\x00" + strings.Repeat("b", vl) + `"`
		checkStringScan(t, src, 0, true, stringScanResult{1 + pad, -1, 1 + pad, types.ERR_INVALID_CHAR})
		checkStringScan(t, src, 0, false, stringScanResult{len(src), -1, -1, 0})
		// Even backslash runs end at the first quote; odd runs escape it.
		for run := 1; run <= 2*vl+1; run++ {
			src := `"` + prefix + strings.Repeat(`\`, run) + `""`
			end := 1 + pad + run + 1 + run%2
			for _, validate := range []bool{false, true} {
				checkStringScan(t, src, 0, validate, stringScanResult{end, 1 + pad, -1, 0})
			}
		}
	}
	// A single allocation fixes the base address while every alignment is
	// visited. Expected positions come from the constructed input lengths.
	storage := make([]byte, 5*vl+16)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			input := `"` + strings.Repeat("a", n) + `\n"`
			copy(storage[align:], input)
			for _, validate := range []bool{false, true} {
				checkStringScan(t, unsafe.String(unsafe.SliceData(storage), align+len(input)), align, validate, stringScanResult{align + len(input), align + 1 + n, -1, 0})
			}
		}
	}
}

func TestScanStringMaskEvents(t *testing.T) {
	// Every escape is a separate event, but the vector block is loaded once.
	// Keep exact errors and first-escape offsets when many events share a block.
	for pad := 0; pad <= 2*64+1; pad++ {
		prefix := strings.Repeat("a", pad)
		body := strings.Repeat(`\\\"\n\t\/\u0123`, 12)
		input := `"` + prefix + body + `"`
		for _, validate := range []bool{false, true} {
			checkStringScan(t, input, 0, validate, stringScanResult{len(input), 1 + pad, -1, 0})
		}
		for _, bad := range []string{`\x`, `\uQQQQ`, "\x01"} {
			input = `"` + prefix + body + bad + `"ignored\n`
			pos := 1 + pad + len(body)
			checkStringScan(t, input, 0, true, stringScanResult{pos, 1 + pad, pos, types.ERR_INVALID_CHAR})
		}
		// Stop at the earliest event: a later backslash must not affect the
		// first-escape result after an error or after the closing quote.
		input = `"` + prefix + "\x00" + `\n"`
		checkStringScan(t, input, 0, true, stringScanResult{1 + pad, -1, 1 + pad, types.ERR_INVALID_CHAR})
		input = `"` + prefix + `"\n`
		for _, validate := range []bool{false, true} {
			checkStringScan(t, input, 0, validate, stringScanResult{2 + pad, -1, -1, 0})
		}
	}
}
