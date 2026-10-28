//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestMasksBounded(t *testing.T) {
	rng := rand.New(rand.NewSource(9123))
	for length := 0; length <= 129; length++ {
		for align := 0; align < 8; align++ {
			data := make([]byte, align+length)
			rng.Read(data)
			for i := align; i < len(data); i += 3 {
				data[i] = " \t\r\n\"\\,}]"[rng.Intn(9)]
			}
			src := string(data)
			width := min(length, 64)
			var events, controls, nonspace, boundaries uint64
			for i := 0; i < width; i++ {
				c := src[align+i]
				bit := uint64(1) << i
				if c == '"' || c == '\\' {
					events |= bit
				}
				if c < 32 {
					controls |= bit
				}
				space := c == ' ' || c == '\t' || c == '\r' || c == '\n'
				if !space {
					nonspace |= bit
				}
				if space || c == ',' || c == '}' || c == ']' {
					boundaries |= bit
				}
			}
			if mask, n := NonSpaceMask(src, align); mask != nonspace || n != width {
				t.Fatalf("NonSpaceMask(%x,%d)=(%x,%d), want (%x,%d)", src, align, mask, n, nonspace, width)
			}
			for _, strict := range []bool{false, true} {
				wantEvents := events
				if strict {
					wantEvents |= controls
				}
				if e, ns, n := TraversalMasks(src, align, strict); e != wantEvents || ns != nonspace || n != width {
					t.Fatalf("TraversalMasks(%x,%d,%v)=(%x,%x,%d), want (%x,%x,%d)", src, align, strict, e, ns, n, wantEvents, nonspace, width)
				}
			}
			if e, ns, b, n := PathMasks(src, align); e != events || ns != nonspace || b != boundaries || n != width {
				t.Fatalf("PathMasks(%x,%d)=(%x,%x,%x,%d), want (%x,%x,%x,%d)", src, align, e, ns, b, n, events, nonspace, boundaries, width)
			}
		}
	}
	for n := 0; n <= 129; n++ {
		prefix := strings.Repeat(" \t\n\r", (n+3)/4)[:n]
		for c := 0; c < 256; c++ {
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				continue
			}
			src := "xx" + prefix + string([]byte{byte(c)})
			if got := SkipSpace(src, 2); got != 2+n {
				t.Fatalf("SkipSpace(%q,2)=%d, want %d", src, got, 2+n)
			}
		}
		if got := SkipSpace(prefix, 0); got != n {
			t.Fatalf("whitespace length %d: got %d", n, got)
		}
	}
}

func TestScanStringContracts(t *testing.T) {
	for pad := 0; pad <= 129; pad++ {
		prefix := "xx\"" + strings.Repeat("a", pad)
		for _, tc := range []struct {
			tail                  string
			strict                bool
			end, escape, errorPos int
			err                   types.ParsingError
		}{
			{`"`, true, 1, -1, -1, 0},
			{`\n"`, true, 3, 0, -1, 0},
			{`\uD800"`, true, 7, 0, -1, 0},
			{`\x"`, false, 3, 0, -1, 0},
			{`\x"`, true, 0, 0, 0, types.ERR_INVALID_CHAR},
			{`\u12"`, true, 5, 0, 5, types.ERR_EOF},
			{`\u12"`, false, 5, 0, -1, 0},
			{`\uZZZZ"`, true, 0, 0, 0, types.ERR_INVALID_CHAR},
			{`\`, true, 1, 0, 1, types.ERR_EOF},
			{"\x00\"", true, 0, -1, 0, types.ERR_INVALID_CHAR},
			{"\x00\"", false, 2, -1, -1, 0},
			{"", true, 0, -1, 0, types.ERR_EOF},
		} {
			src := prefix + tc.tail
			wantEnd, wantEscape, wantPos := len(prefix)+tc.end, tc.escape, tc.errorPos
			if wantEscape >= 0 {
				wantEscape += len(prefix)
			}
			if wantPos >= 0 {
				wantPos += len(prefix)
			}
			end, escape, pos, err := ScanString(src, 2, tc.strict)
			if end != wantEnd || escape != wantEscape || pos != wantPos || err != tc.err {
				t.Fatalf("ScanString(%q,2,%v)=(%d,%d,%d,%v), want (%d,%d,%d,%v)", src, tc.strict, end, escape, pos, err, wantEnd, wantEscape, wantPos, tc.err)
			}
			if end, err := SkipString(src[3:], tc.strict); end != wantEnd-3 || err != tc.err {
				t.Fatalf("SkipString(%q,%v)=(%d,%v)", src[3:], tc.strict, end, err)
			}
		}
		for run := 0; run <= 33; run++ {
			src := prefix + strings.Repeat(`\`, run) + `""`
			end, _, _, err := ScanString(src, 2, true)
			if want := len(prefix) + run + 1 + run%2; end != want || err != 0 {
				t.Fatalf("backslash parity: %q got (%d,%v), want %d", src, end, err, want)
			}
		}
	}
}

func TestSkipContainerContracts(t *testing.T) {
	for _, tc := range []struct {
		src string
		end int
		err types.ParsingError
	}{
		{`[] trailing`, 2, 0}, {`{}`, 2, 0},
		{`[}] suffix`, 3, 0}, {`{]} suffix`, 3, 0},
		{`[{] ignored }`, 3, 0}, {`["[]{}"]`, 8, 0},
		{`[\"] trailing`, 4, 0}, {`[\\"]`, 5, types.ERR_EOF},
		{`[\[]]`, 5, 0}, {`[`, 1, types.ERR_EOF},
		{`["\`, 3, types.ERR_EOF},
	} {
		if end, err := SkipContainer(tc.src, 0); end != tc.end || err != tc.err {
			t.Fatalf("SkipContainer(%q)=(%d,%v), want (%d,%v)", tc.src, end, err, tc.end, tc.err)
		}
	}
	for pad := 0; pad <= 129; pad++ {
		for run := 0; run <= 33; run++ {
			prefix := "[" + strings.Repeat("x", pad)
			src := prefix + strings.Repeat(`\`, run) + `"]"]`
			want := len(src)
			if run%2 != 0 {
				want -= 2
			}
			if end, err := SkipContainer(src, 0); end != want || err != 0 {
				t.Fatalf("container parity %q: (%d,%v), want %d", src, end, err, want)
			}
		}
	}
	deep := strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000)
	if end, err := SkipContainer(deep, 0); end != len(deep) || err != 0 {
		t.Fatalf("deep container: (%d,%v)", end, err)
	}
}

func TestNumberContracts(t *testing.T) {
	const invalid, eof = types.ERR_INVALID_CHAR, types.ERR_EOF
	for _, tc := range []struct {
		src       string
		prefixEnd int
		prefixErr types.ParsingError
		strictEnd int
		strictErr types.ParsingError
	}{
		{"", 0, eof, 0, eof}, {"-", 1, eof, 1, eof},
		{"01", 1, 0, 1, 0}, {"-01", 2, 0, 2, 0},
		{"0+1", 1, 0, 1, 0}, {"-0-1", 2, 0, 2, 0},
		{"0.0e-0", 6, 0, 6, 0}, {"-12.345E-67", 11, 0, 11, 0},
		{"1+2", 1, 0, 1, invalid}, {"1.2.3", 3, 0, 3, invalid},
		{"1e2E3", 3, 0, 3, invalid}, {"1e+-2", 3, invalid, 3, invalid},
		{"1.", 2, eof, 1, invalid}, {"1e+", 3, eof, 2, invalid},
		{"1.,", 2, invalid, 1, invalid}, {"--1", 1, invalid, 1, invalid},
		{"1.e1", 2, invalid, 2, invalid}, {"1e2.3", 3, 0, 3, invalid},
		{"1+2+3", 1, 0, 3, invalid}, {"1+2.3.4", 1, 0, 5, invalid},
		{"1e.2e3", 2, invalid, 4, invalid}, {"1e+2.3e4", 4, 0, 6, invalid},
		{"1.2e3.+", 5, 0, 5, invalid}, {"1+2e3.", 1, 0, 5, invalid},
		{"1,++..ee", 1, 0, 1, 0}, {"1e+2\x00.++ee", 4, 0, 4, 0},
	} {
		for _, padding := range []string{"", "xx["} {
			src, start := padding+tc.src, len(padding)
			if end, err := ScanNumber(src, start); end != start+tc.prefixEnd || err != tc.prefixErr {
				t.Fatalf("prefix %q: (%d,%v), want (%d,%v)", src, end, err, start+tc.prefixEnd, tc.prefixErr)
			}
			if end, err := ScanNumberStrict(src, start); end != start+tc.strictEnd || err != tc.strictErr {
				t.Fatalf("strict %q: (%d,%v), want (%d,%v)", src, end, err, start+tc.strictEnd, tc.strictErr)
			}
		}
	}
	for n := 1; n <= 129; n++ {
		prefix := strings.Repeat("7", n)
		for c := 0; c < 256; c++ {
			src := prefix + string([]byte{byte(c)}) + "2,"
			wantEnd, wantStrict, wantSkip := n, n, n+2
			var strictErr types.ParsingError
			switch byte(c) {
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.', 'e', 'E':
				wantEnd += 2
				wantStrict += 2
			case '+', '-':
				strictErr = invalid
			case ' ', '\t', '\n', '\r', '}', ']', ',':
				wantSkip = n
			}
			if end, err := ScanNumber(src, 0); end != wantEnd || err != 0 {
				t.Fatalf("prefix %q: (%d,%v), want %d", src, end, err, wantEnd)
			}
			if end, err := ScanNumberStrict(src, 0); end != wantStrict || err != strictErr {
				t.Fatalf("strict %q: (%d,%v), want (%d,%v)", src, end, err, wantStrict, strictErr)
			}
			if end := SkipNumber(src, 0); end != wantSkip {
				t.Fatalf("skip %q: %d, want %d", src, end, wantSkip)
			}
		}
	}
	for _, scan := range []func(string, int) (int, types.ParsingError){ScanNumber, ScanNumberStrict} {
		for _, start := range []int{-1, 2} {
			if end, err := scan("1", start); end != start || err != invalid {
				t.Fatalf("invalid start: (%d,%v)", end, err)
			}
		}
	}
}
