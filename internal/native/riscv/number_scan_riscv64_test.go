//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestNumberScan(t *testing.T) {
	const invalid, eof = types.ERR_INVALID_CHAR, types.ERR_EOF
	for _, tc := range []struct {
		src       string
		prefixEnd int
		prefixErr types.ParsingError
		strictEnd int
		strictErr types.ParsingError
	}{
		{"", 0, eof, 0, eof},
		{"-", 1, eof, 1, eof},
		{"0", 1, 0, 1, 0},
		{"-0", 2, 0, 2, 0},
		{"01", 1, 0, 1, 0},
		{"-01", 2, 0, 2, 0},
		{"00.1", 1, 0, 1, 0},
		{"0+1", 1, 0, 1, 0},
		{"-0-1", 2, 0, 2, 0},
		{"0e+0", 4, 0, 4, 0},
		{"0.0e-0", 6, 0, 6, 0},
		{"-12.345E-67", 11, 0, 11, 0},
		{"123xyz", 3, 0, 3, 0},
		{"123\x00", 3, 0, 3, 0},
		{"1 2", 1, 0, 1, 0},
		{"1+2", 1, 0, 1, invalid},
		{"1-2", 1, 0, 1, invalid},
		{"1.2.3", 3, 0, 3, invalid},
		{"1e2E3", 3, 0, 3, invalid},
		{"1e+-2", 3, invalid, 3, invalid},
		{"1.", 2, eof, 1, invalid},
		{"1e", 2, eof, 1, invalid},
		{"1e+", 3, eof, 2, invalid},
		{"1e-", 3, eof, 2, invalid},
		{"1.,", 2, invalid, 1, invalid},
		{"1e+,", 3, invalid, 2, invalid},
		{" 1", 0, invalid, 0, invalid},
		{"+1", 0, invalid, 0, invalid},
		{".1", 0, invalid, 0, invalid},
		{"--1", 1, invalid, 1, invalid},
		{"-x", 1, invalid, 1, invalid},
		{"1.x", 2, invalid, 1, invalid},
		{"1.e1", 2, invalid, 2, invalid},
		{"1e+x", 3, invalid, 2, invalid},
		{"1e2.3", 3, 0, 3, invalid},
		// Duplicates take precedence over earlier misplaced markers; among
		// duplicates the first byte in source order wins, across categories.
		{"1+2+3", 1, 0, 3, invalid},
		{"1+2.3.4", 1, 0, 5, invalid},
		{"1e.2e3", 2, invalid, 4, invalid},
		{"1e+2.3e4", 4, 0, 6, invalid},
		{"1.2e3.+", 5, 0, 5, invalid},
		{"1+2e3.", 1, 0, 5, invalid},
		// Bytes after a delimiter must not contribute marker positions.
		{"1,++..ee", 1, 0, 1, 0},
		{"1.2 e+.e+", 3, 0, 3, 0},
		{"1e+2\x00.++ee", 4, 0, 4, 0},
	} {
		for _, padding := range []string{"", "xx["} {
			src, start := padding+tc.src, len(padding)
			checkNumberScan(t, "prefix", src, start, tc.prefixEnd+start, tc.prefixErr, ScanNumber)
			checkNumberScan(t, "strict", src, start, tc.strictEnd+start, tc.strictErr, ScanNumberStrict)
		}
	}
	for _, scan := range []func(string, int) (int, types.ParsingError){ScanNumber, ScanNumberStrict} {
		for _, start := range []int{-1, 2} {
			if end, err := scan("1", start); end != start || err != invalid {
				t.Fatalf("out-of-range start %d: (%d,%v)", start, end, err)
			}
		}
		if end, err := scan("1", 1); end != 1 || err != eof {
			t.Fatalf("at EOF: (%d,%v)", end, err)
		}
	}
}

func checkNumberScan(t *testing.T, mode, src string, start, wantEnd int, wantErr types.ParsingError, scan func(string, int) (int, types.ParsingError)) {
	t.Helper()
	if end, err := scan(src, start); end != wantEnd || err != wantErr {
		t.Fatalf("%s(%q,%d)=(%d,%v), want (%d,%v)", mode, src, start, end, err, wantEnd, wantErr)
	}
}

func TestNumberScanEveryByte(t *testing.T) {
	for b := 0; b < 256; b++ {
		src := "1" + string([]byte{byte(b)}) + "2,"
		prefixEnd, strictEnd, skipEnd := 1, 1, 3
		var strictErr types.ParsingError
		switch byte(b) {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.', 'e', 'E':
			prefixEnd, strictEnd = 3, 3
		case '+', '-':
			strictErr = types.ERR_INVALID_CHAR
		}
		switch byte(b) {
		case '}', ']', ',', ' ', '\t', '\r', '\n':
			skipEnd = 1
		}
		checkNumberScan(t, "prefix-byte", src, 0, prefixEnd, 0, ScanNumber)
		checkNumberScan(t, "strict-byte", src, 0, strictEnd, strictErr, ScanNumberStrict)
		if end := SkipNumber(src, 0); end != skipEnd {
			t.Fatalf("SkipNumber byte %#02x: %d, want %d", b, end, skipEnd)
		}
	}
}

func TestNumberScanStrictShortIntegers(t *testing.T) {
	// Exercise every following byte immediately below, at, and above the
	// scalar threshold. Appended digits make fraction/exponent markers valid;
	// signs remain misplaced. Every other byte terminates the candidate.
	for _, digits := range []int{1, 2, 7, 8, 9, 10} {
		for _, sign := range []string{"", "-"} {
			integer := sign + strings.Repeat("7", digits)
			for _, padding := range []string{"", "xx["} {
				start, boundary := len(padding), len(padding)+len(integer)
				checkNumberScan(t, "strict-short-eof", padding+integer, start, boundary, 0, ScanNumberStrict)
				for b := 0; b < 256; b++ {
					src := padding + integer + string([]byte{byte(b)}) + "2,"
					end := boundary
					var err types.ParsingError
					switch byte(b) {
					case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.', 'e', 'E':
						end += 2
					case '+', '-':
						err = types.ERR_INVALID_CHAR
					}
					checkNumberScan(t, "strict-short-byte", src, start, end, err, ScanNumberStrict)
				}
				// Falling back after the lookahead must preserve marker positions
				// and the priority of a later duplicate over an earlier bad sign.
				for _, tc := range []struct {
					suffix string
					at     int
				}{
					{".", 0}, {"e+", 1}, {".e2", 1}, {"e2.3", 2},
					{"+2+3", 2}, {"+2.3.4", 4}, {"e+2.3e4", 5},
				} {
					checkNumberScan(t, "strict-short-marker", padding+integer+tc.suffix, start, boundary+tc.at, types.ERR_INVALID_CHAR, ScanNumberStrict)
				}
			}
		}
	}
}

func TestNumberScanVectorBoundaries(t *testing.T) {
	const invalid, eof = types.ERR_INVALID_CHAR, types.ERR_EOF
	vl := spaceVectorBytes()
	storage := make([]byte, 4*vl+64)
	for align := 0; align < vl; align++ {
		for n := 1; n <= 3*vl+3; n++ {
			for _, tc := range []struct {
				suffix    string
				prefixEnd int // offset after the run of n digits
				prefixErr types.ParsingError
				strictEnd int
				strictErr types.ParsingError
			}{
				{"", 0, 0, 0, 0},
				{",e+..", 0, 0, 0, 0},
				{".25e-9,", 6, 0, 6, 0},
				{".,", 1, invalid, 0, invalid},
				{".", 1, eof, 0, invalid},
				{"e+,", 2, invalid, 1, invalid},
				{"e+", 2, eof, 1, invalid},
				{"+2,", 0, 0, 0, invalid},
				{".2.3,", 2, 0, 2, invalid},
				{"e2E3,", 2, 0, 2, invalid},
				{".e2,", 1, invalid, 1, invalid},
				{"e2.3,", 2, 0, 2, invalid},
				{"+.2+3,", 0, 0, 3, invalid},
				{"e+2.3e4,", 3, 0, 5, invalid},
			} {
				copy(storage[align:], strings.Repeat("7", n))
				copy(storage[align+n:], tc.suffix)
				src := unsafe.String(unsafe.SliceData(storage), align+n+len(tc.suffix))
				checkNumberScan(t, "prefix-lane", src, align, align+n+tc.prefixEnd, tc.prefixErr, ScanNumber)
				checkNumberScan(t, "strict-lane", src, align, align+n+tc.strictEnd, tc.strictErr, ScanNumberStrict)
			}
		}
	}
	// The two markers of each category can straddle several vector blocks.
	for first := 1; first <= 2*vl; first++ {
		for second := first + 1; second <= 3*vl+1; second++ {
			for _, markers := range []string{"..", "eE", "Ee", "+-", "-+"} {
				body := []byte(strings.Repeat("1", second+2))
				body[first], body[second] = markers[0], markers[1]
				checkNumberScan(t, "strict-cross-block-duplicate", string(body), 0, second, invalid, ScanNumberStrict)
			}
		}
	}
}

func TestNumberScanMagnitudeIsNotConversion(t *testing.T) {
	for _, src := range []string{
		"18446744073709551616", "-9223372036854775809", "1e9999999999999999999", "-1e-9999999999999999999",
		strings.Repeat("9", 8192), "0." + strings.Repeat("0", 8192) + "1e-99999",
	} {
		for _, suffix := range []string{"", ",", "\t", "\xff"} {
			checkNumberScan(t, "prefix-magnitude", src+suffix, 0, len(src), 0, ScanNumber)
			checkNumberScan(t, "strict-magnitude", src+suffix, 0, len(src), 0, ScanNumberStrict)
		}
	}
}

func TestSkipNumberVectorBoundaries(t *testing.T) {
	vl := spaceVectorBytes()
	storage := make([]byte, 4*vl+4)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			for _, delimiter := range []string{"", "}", "]", ",", " ", "\t", "\n", "\r"} {
				// All these bytes are accepted without grammar validation.
				const unit = "bogus\"[{:+-e..\v\f\x00\xff"
				body := strings.Repeat(unit, (n+len(unit)-1)/len(unit))
				copy(storage[align:], body[:n])
				copy(storage[align+n:], delimiter)
				src := unsafe.String(unsafe.SliceData(storage), align+n+len(delimiter))
				if end := SkipNumber(src, align); end != align+n {
					t.Fatalf("SkipNumber(%q,%d)=%d, want %d", src, align, end, align+n)
				}
			}
		}
	}
}

func TestNumberScanJSONGrammar(t *testing.T) {
	// encoding/json independently checks complete JSON grammar. These inputs
	// start as numeric candidates, so a whole successful scan must be a number.
	// Exact error locations and the native candidate precedence are tested above.
	rng := rand.New(rand.NewSource(61026))
	alphabet := "0123456789.eE+-x, \x00\xff"
	for i := 0; i < 30000; i++ {
		buf := make([]byte, 1+rng.Intn(130))
		buf[0] = "01-"[rng.Intn(3)]
		for j := 1; j < len(buf); j++ {
			buf[j] = alphabet[rng.Intn(len(alphabet))]
		}
		src := string(buf)
		// A trailing JSON space would be valid for json.Valid, but deliberately
		// belongs to the caller of a prefix operation.
		want := json.Valid(buf) && strings.TrimSpace(src) == src
		for _, scan := range []func(string, int) (int, types.ParsingError){ScanNumber, ScanNumberStrict} {
			end, err := scan(src, 0)
			if got := err == 0 && end == len(src); got != want {
				t.Fatalf("number %q: end=%d err=%v, whole-valid=%v", src, end, err, want)
			}
		}
	}
}
