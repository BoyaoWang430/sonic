//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// numberScanReference uses only byte-by-byte grammar checks. In strict mode,
// collect marker positions before validating their order: duplicates have
// priority over misplaced markers, and trailing markers precede order errors.
func numberScanReference(src string, strict bool) (int, types.ParsingError) {
	const invalid, eof = types.ERR_INVALID_CHAR, types.ERR_EOF
	i := 0
	if len(src) != 0 && src[0] == '-' {
		i++
	}
	if i == len(src) {
		return i, eof
	}
	if src[i] < '0' || src[i] > '9' {
		return i, invalid
	}
	if strict {
		if src[i] == '0' && (i+1 == len(src) || !strings.ContainsRune(".eE", rune(src[i+1]))) {
			return i + 1, 0
		}
		// Markers cannot occupy byte zero, so zero means not encountered.
		positions := map[byte]int{}
		for ; i < len(src); i++ {
			c := src[i]
			if c >= '0' && c <= '9' {
				continue
			}
			if !strings.ContainsRune(".eE+-", rune(c)) {
				break
			}
			if c == 'E' {
				c = 'e'
			} else if c == '-' {
				c = '+'
			}
			if positions[c] != 0 {
				return i, invalid
			}
			positions[c] = i
		}
		if strings.ContainsRune(".eE+-", rune(src[i-1])) {
			return i - 1, invalid
		}
		if sign := positions['+']; sign != 0 && (positions['e'] == 0 || positions['e'] != sign-1) {
			return sign, invalid
		}
		if dot, exponent := positions['.'], positions['e']; dot != 0 && exponent != 0 {
			if dot > exponent {
				return dot, invalid
			}
			if dot+1 == exponent {
				return exponent, invalid
			}
		}
		return i, 0
	}
	if src[i] == '0' {
		i++
	} else {
		for i < len(src) && src[i] >= '0' && src[i] <= '9' {
			i++
		}
	}
	for _, part := range []string{".", "eE"} {
		if i == len(src) || !strings.ContainsRune(part, rune(src[i])) {
			continue
		}
		i++
		if part == "eE" && i < len(src) && (src[i] == '+' || src[i] == '-') {
			i++
		}
		if i == len(src) {
			return i, eof
		}
		if src[i] < '0' || src[i] > '9' {
			return i, invalid
		}
		for i < len(src) && src[i] >= '0' && src[i] <= '9' {
			i++
		}
	}
	return i, 0
}

func checkNumberScanReference(t *testing.T, src string) {
	t.Helper()
	for mode, scan := range []func(string, int) (int, types.ParsingError){ScanNumber, ScanNumberStrict} {
		wantEnd, wantErr := numberScanReference(src, mode == 1)
		if end, err := scan(src, 0); end != wantEnd || err != wantErr {
			t.Fatalf("mode %d src=%q: (%d,%v), want (%d,%v)", mode, src, end, err, wantEnd, wantErr)
		}
	}
}

func TestNumberScanReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261104))
	const alphabet = "0000011111799.eE+-x, \x00\xff"
	for i := 0; i < 12000; i++ {
		var src string
		if i%2 == 0 {
			// Long digit runs exercise transitions between bounded word, NEON,
			// and byte tails, with multiple independently placed markers.
			src = "7"
			if rng.Intn(2) != 0 {
				src = "-" + src
			}
			for j, n := 0, 1+rng.Intn(5); j < n; j++ {
				src += strings.Repeat("7", rng.Intn(130))
				src += string(alphabet[rng.Intn(len(alphabet))])
			}
		} else {
			buf := make([]byte, rng.Intn(130))
			for j := range buf {
				buf[j] = alphabet[rng.Intn(len(alphabet))]
			}
			src = string(buf)
		}
		checkNumberScanReference(t, src)
	}
}

func FuzzNumberScanReference(f *testing.F) {
	for _, src := range []string{"", "-", "0e+0", "-12.345E-67", "1+2.3.4", "1e+2.3e4", "1.e1", "1e+", "1,++..ee"} {
		f.Add(src)
	}
	for _, n := range []int{7, 8, 9, 15, 16, 17, 63, 64, 65} {
		f.Add(strings.Repeat("7", n) + ".25e-9,")
		f.Add("1." + strings.Repeat("7", n) + "e.4")
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 {
			t.Skip()
		}
		checkNumberScanReference(t, src)
	})
}
