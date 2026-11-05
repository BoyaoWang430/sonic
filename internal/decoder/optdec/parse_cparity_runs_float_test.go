//go:build arm64 && go1.20 && !go1.28

package optdec

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

// runsNumberCorpus mixes boundary tokens with random decimals around every
// limit of the assembly fast paths: 19 integer digits, 17 significant digits,
// 2^52 significands, 1e15 for positive exponents, |exponent| <= 22, and at
// most three exponent digits. Malformed tokens must keep the Go behavior.
func runsNumberCorpus() []string {
	corpus := []string{
		"0", "-0", "0.0", "-0.0", "0.000", "0e5", "-0e-5", "0.000e7", "0.0001", "0.000123e3",
		"1", "1.5", "-2.718281828e+10", "1e22", "1e23", "1e-22", "1e-23", "-1e22",
		"4503599627370495", "4503599627370495.5", "4503599627370496.5", "450359962737049.55",
		"1000000000000000e1", "1000000000000001e1", "999999999999999.9e7", "1000000000000000e22",
		"9007199254740993", "123456789012345678.9", "12345678901234567.89", "1234567890123456.789",
		"0.12345678901234567890123", "0.00000000000000000000001", "1.00000000000000011102230246251565404236316680908203125",
		"1e0", "1e00", "1e000", "1e0000", "1e+022", "1e-022", "1e400", "1e-400", "2.5E3", "2.5E-3",
		"1.", "1.e5", "1e", "1e+", "1e-", "-", "-x", "1.x", "0.", "0.e1", "01", "01.5", "1ee5", "1.5.5",
		"18446744073709551615", "18446744073709551616", "-9223372036854775808", "-9223372036854775809",
		"99999999999999999999.5", "0.99999999999999999999",
	}
	rng := rand.New(rand.NewSource(20261105))
	digits := func(n int, leading bool) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			d := byte('0' + rng.Intn(10))
			if i == 0 && leading && d == '0' {
				d = '1' + byte(rng.Intn(9))
			}
			b.WriteByte(d)
		}
		return b.String()
	}
	for i := 0; i < 40000; i++ {
		var b strings.Builder
		if rng.Intn(3) == 0 {
			b.WriteByte('-')
		}
		if rng.Intn(6) == 0 {
			b.WriteByte('0')
		} else {
			b.WriteString(digits(1+rng.Intn(20), true))
		}
		if rng.Intn(4) != 0 {
			b.WriteByte('.')
			if rng.Intn(5) == 0 {
				b.WriteString(strings.Repeat("0", rng.Intn(25)))
			}
			b.WriteString(digits(rng.Intn(26), false))
		}
		if rng.Intn(3) == 0 {
			b.WriteByte("eE"[rng.Intn(2)])
			switch rng.Intn(3) {
			case 0:
				b.WriteByte('-')
			case 1:
				b.WriteByte('+')
			}
			b.WriteString(digits(rng.Intn(5), false))
		}
		corpus = append(corpus, b.String())
	}
	for e := -30; e <= 30; e++ {
		for _, m := range []string{"1", "45", "4503599627370495", "1000000000000000", "1000000000000001", "123.456"} {
			corpus = append(corpus, m+"e"+strconv.Itoa(e), "-"+m+"e"+strconv.Itoa(e))
		}
	}
	return corpus
}

// The Runs assembly converts common numbers itself and yields the rest to
// cParityParseNumberValue. Every token must produce the reference node bits,
// or the reference error code and cursor, whichever path handles it.
func TestRunsNumberMatchesGoReference(t *testing.T) {
	for _, s := range runsNumberCorpus() {
		input := append([]byte(s), padding...)
		start := unsafe.Pointer(&input[0])
		cur := start
		negative := s[0] == '-'
		if negative {
			cur = unsafe.Add(cur, 1)
		}
		want := cParityNum{typ: 0xabc, val: 0xdef}
		scratch := make([]byte, 0, 800)
		code := cParityParseNumber(&cur, &want, negative, &scratch)
		wantPos := int(uintptr(cur)-uintptr(start)) + 1

		p := newParser("["+s+"]", 0, 0)
		got := p.parse()
		if code != SONIC_OK {
			if got != code || p.Pos() != wantPos {
				t.Fatalf("%q: parse=(%d,%d), reference=(%d,%d)", s, got, p.Pos(), code, wantPos)
			}
		} else if len(p.nodes) < 2 || p.nodes[1].typ&TypeMask != want.typ || p.nodes[1].val != want.val {
			var node node
			if len(p.nodes) > 1 {
				node = p.nodes[1]
			}
			t.Fatalf("%q: parse=%d node=(%#x,%#x), reference=(%#x,%#x)", s, got, node.typ&TypeMask, node.val, want.typ, want.val)
		}
		p.free()
	}
}

// Whole-word digit skipping must stop at every non-digit byte value in every
// lane, including 0xfa..0xff whose +6 carries into the next byte.
func TestCParitySkipDigitsAllBytes(t *testing.T) {
	for run := 0; run <= 24; run++ {
		for value := 0; value < 256; value++ {
			if value >= '0' && value <= '9' {
				continue
			}
			for _, next := range []byte{'0', '9', 0xff} {
				input := make([]byte, 0, run+2+len(padding))
				for i := 0; i < run; i++ {
					input = append(input, '0'+byte((i*7)%10))
				}
				input = append(input, byte(value), next)
				input = append(input, padding...)
				end, skipped := cParitySkipDigits(unsafe.Pointer(&input[0]))
				if got := int(uintptr(end) - uintptr(unsafe.Pointer(&input[0]))); got != run || int(skipped) != run {
					t.Fatalf("run=%d value=%#x next=%#x: end=%d skipped=%d", run, value, next, got, skipped)
				}
			}
		}
	}
}

func TestSkipNumberFastAllBytes(t *testing.T) {
	isNumByte := func(b byte) bool {
		return b >= '0' && b <= '9' || b == '.' || b == '-' || b == '+' || b == 'e' || b == 'E'
	}
	for start := 0; start <= 7; start++ {
		for run := 0; run <= 24; run++ {
			for value := 0; value < 256; value++ {
				if isNumByte(byte(value)) {
					continue
				}
				var b strings.Builder
				b.WriteString(strings.Repeat(" ", start))
				for i := 0; i < run; i++ {
					b.WriteByte('0' + byte((i*7)%10))
				}
				b.WriteByte(byte(value))
				b.WriteString("99999999")
				end, ok := SkipNumberFast(b.String(), start)
				if wantEnd, wantOK := start+run, run > 0; end != wantEnd || ok != wantOK {
					t.Fatalf("start=%d run=%d value=%#x: got (%d,%v), want (%d,%v)", start, run, value, end, ok, wantEnd, wantOK)
				}
			}
		}
	}
}
