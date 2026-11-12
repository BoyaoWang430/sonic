//go:build linux && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package kernels

import (
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

func stringDecodePages(t *testing.T) ([]byte, []byte) {
	t.Helper()
	page := os.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Munmap(mapping); err != nil {
			t.Error(err)
		}
	})
	middle := mapping[page : 2*page]
	if err := syscall.Mprotect(middle, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	return mapping, middle
}

func TestStringDecodeGuardPages(t *testing.T) {
	inMap, inputPage := stringDecodePages(t)
	outMap, outputPage := stringDecodePages(t)
	page := len(inputPage)
	maxPrefix := 3*stringDecodeVectorBytes() + 1
	if maxPrefix > page/4 {
		maxPrefix = page / 4
	}
	for prefix := 0; prefix <= maxPrefix; prefix++ {
		for _, tail := range []string{`plain`, `\n`, `\nx`, `\nxy`, `\nx\t`, `\"\\`, `\u4e2d`, `\ud800\udc00`, `\ud800\u0041`, `\udfff`} {
			src := strings.Repeat("x", prefix) + tail
			want := stringDecodeOracle(t, src)
			for _, inputStart := range []int{0, page - len(src)} {
				input := inputPage[inputStart : inputStart+len(src)]
				copy(input, src)
				if err := syscall.Mprotect(inputPage, syscall.PROT_READ); err != nil {
					t.Fatal(err)
				}
				for _, outputStart := range []int{0, page - len(src)} {
					output := outputPage[outputStart : outputStart+len(src)]
					written, pos, code := Unquote(rt.String(rt.SliceData(input), len(input)), output, types.F_UNICODE_REPLACE)
					if code != 0 || pos != -1 || string(output[:written]) != want {
						t.Fatalf("guarded Unquote(%q) written=%d pos=%d code=%v", src, written, pos, code)
					}
				}
				if err := syscall.Mprotect(inputPage, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
					t.Fatal(err)
				}
			}
			// The doubled form exercises the extra escape/surrogate reads at
			// the same guard boundary, with a separate bounded output buffer.
			double, err := json.Marshal(src)
			if err != nil {
				t.Fatal(err)
			}
			body := double[1 : len(double)-1]
			input := inputPage[page-len(body):]
			copy(input, body)
			output := outputPage[page-len(body):]
			written, pos, code := Unquote(rt.String(rt.SliceData(input), len(input)), output, types.F_DOUBLE_UNQUOTE|types.F_UNICODE_REPLACE)
			if code != 0 || pos != -1 || string(output[:written]) != want {
				t.Fatalf("guarded double Unquote(%q) written=%d pos=%d code=%v", body, written, pos, code)
			}
			quoted := `"` + src + `"`
			for _, start := range []int{0, page - len(quoted)} {
				input := inputPage[start : start+len(quoted)]
				copy(input, quoted)
				end, written, _, pos, code := ParseString(input, 0, true, false)
				if end != len(quoted) || pos != -1 || code != 0 || string(input[1:1+written]) != want {
					t.Fatalf("guarded ParseString(%q) end=%d written=%d pos=%d code=%d", quoted, end, written, pos, code)
				}
			}
		}
		for _, tail := range []string{`\`, `\u`, `\u1`, `\u12`, `\u123`, `\u12x4`, `\ud800\u1`, `\ud800\u12x4`} {
			src := strings.Repeat("x", prefix) + tail
			input := inputPage[page-len(src):]
			copy(input, src)
			output := outputPage[page-len(src):]
			if _, _, code := Unquote(rt.String(rt.SliceData(input), len(input)), output, types.F_UNICODE_REPLACE); code == 0 {
				t.Fatalf("invalid guarded Unquote input accepted: %q", src)
			}
			quoted := `"` + src
			input = inputPage[page-len(quoted):]
			copy(input, quoted)
			if _, _, _, _, code := ParseString(input, 0, true, false); code == 0 {
				t.Fatalf("unterminated guarded ParseString accepted: %q", quoted)
			}
		}
		for _, tail := range []string{`\`, `\\`, `\\\`, `\\\x`, `\\u`, `\\u123`, `\\ud800\\u12x4`} {
			src := strings.Repeat("x", prefix) + tail
			input := inputPage[page-len(src):]
			copy(input, src)
			output := outputPage[page-len(src):]
			if _, _, code := Unquote(rt.String(rt.SliceData(input), len(input)), output, types.F_DOUBLE_UNQUOTE|types.F_UNICODE_REPLACE); code == 0 {
				t.Fatalf("invalid guarded double Unquote accepted: %q", src)
			}
		}
	}
	// A zero-length range may point into PROT_NONE without any access.
	for _, mode := range []uint64{0, 2, 4, 6, 12, 14} {
		_, written, _, pos, code := stringDecode(&inMap[2*page], 0, &outMap[2*page], mode)
		if written != 0 || (mode&4 == 0 && (pos != -1 || code != 0)) || (mode&4 != 0 && (pos != 0 || code != 5)) {
			t.Fatalf("zero-length guard mode=%d: written=%d pos=%d code=%d", mode, written, pos, code)
		}
	}
}

func TestParseStringNoEscapeNeedsNoStores(t *testing.T) {
	_, inputPage := stringDecodePages(t)
	for prefix := 0; prefix <= 2*stringDecodeVectorBytes(); prefix++ {
		for _, tc := range []struct {
			tail    string
			code    int
			escaped bool
		}{
			{`"`, 0, false},
			{"\x01\"", 1, false},
			{`\x"`, 2, true},
		} {
			// Trailing source keeps the event inside a full SIMD load even for
			// short keys. The prefix must not be copied before a closing quote,
			// raw control, or invalid escape that produces no decoded output.
			start := prefix % 16
			src := strings.Repeat("p", start) + `"` + strings.Repeat("a", prefix) + tc.tail + strings.Repeat("s", 32)
			input := inputPage[len(inputPage)-len(src):]
			if err := syscall.Mprotect(inputPage, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
				t.Fatal(err)
			}
			copy(input, src)
			if err := syscall.Mprotect(inputPage, syscall.PROT_READ); err != nil {
				t.Fatal(err)
			}
			end, written, escaped, pos, code := ParseString(input, start, true, true)
			wantEnd, wantPos := start+1+prefix, start+1+prefix
			if tc.code == 0 {
				wantEnd++
				wantPos = -1
			}
			if end != wantEnd || written != prefix || escaped != tc.escaped || pos != wantPos || code != tc.code || string(input) != src {
				t.Fatalf("read-only prefix=%d start=%d tail=%q: end=%d written=%d escaped=%v pos=%d code=%d",
					prefix, start, tc.tail, end, written, escaped, pos, code)
			}
		}
	}
}
