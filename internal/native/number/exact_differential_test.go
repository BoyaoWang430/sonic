package number_test

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestParsePrefixExactFloatBoundaries(t *testing.T) {
	mantissas := []string{"0", "1", "999999999999999", "1000000000000000", "1000000000000001", "4503599627370494", "4503599627370495", "4503599627370496", "4503599627370497"}
	var tokens []string
	for _, m := range mantissas {
		for _, e := range []string{"-23", "-22", "-1", "0", "1", "22", "23", "000", "+000", "-000", "9999999999999999999999"} {
			tokens = append(tokens, m+"e"+e, m+".0e"+e, "0."+m+"e"+e)
		}
	}
	tokens = append(tokens, "1.", "1.e2", "1e", "1e+", "1e-", "1.2e+", "1.2.3", "1.2e3e4", "450359962737049.5", "450359962737049.6", "0."+strings.Repeat("0", 21)+"1", "0."+strings.Repeat("0", 22)+"1", "1.00000000000000011102230246251565404236316680908203125", "1.00000000000000033306690738754696212708950042724609375")
	tokens = append(tokens, "0.0000000000000000000001e43", "0.0000000000000000000001e44", "0.0000000000000000000001e45", "0.1000000000000001e16", "0.1000000000000001e17", "0.4503599627370495", "0.4503599627370496")
	for _, token := range tokens {
		for _, sign := range []string{"", "-"} {
			for _, suffix := range []string{"", ",null]", "e+", ".", "\x00", "\xff"} {
				checkPrefix(t, sign+token+suffix, 0)
				checkPrefix(t, "[ "+sign+token+suffix, 2)
			}
		}
	}
}

func TestParsePrefixExactFloatLongDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(240521))
	for i := 0; i < 5000; i++ {
		digits := make([]byte, rng.Intn(512)+1)
		for j := range digits {
			digits[j] = byte(rng.Intn(10)) + '0'
		}
		prefix := strconv.FormatUint(rng.Uint64()%100000000000000000, 10)
		if i%3 == 0 {
			prefix = "0"
		}
		token := prefix + "." + string(digits)
		if i%2 != 0 {
			token = "-" + token
		}
		if i%4 != 0 {
			token += "e" + strconv.Itoa(rng.Intn(1001)-500)
		}
		for _, suffix := range []string{"", ",0]", "e+"} {
			checkPrefix(t, token+suffix, 0)
			checkPrefix(t, "[ "+token+suffix, 2)
		}
	}
}
