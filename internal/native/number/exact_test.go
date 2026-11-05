package number

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestExactFloatSuffixDomain(t *testing.T) {
	for _, tc := range []struct {
		s                  string
		i                  int
		mantissa           uint64
		negative, accepted bool
	}{
		{"1.23456789", 1, 1, false, true},
		{"-2.718281828e+10", 2, 2, true, true},
		{"0.035,0", 1, 0, false, true},
		{"-0.0", 2, 0, true, true},
		{"-0e-22", 2, 0, true, true},
		{"1e22", 1, 1, false, true},
		{"0.0000000000000000000001e43", 1, 0, false, true},
		{"0.0000000000000000000001e44", 1, 0, false, true},
		{"0.0000000000000000000001e45", 1, 0, false, false},
		{"0.1000000000000001e16", 1, 0, false, true},
		{"0.1000000000000001e17", 1, 0, false, false},
		{"0.4503599627370495", 1, 0, false, true},
		{"0.4503599627370496", 1, 0, false, false},
		{"1e-22", 1, 1, false, true},
		{"1e23", 1, 1, false, false},
		{"1e-23", 1, 1, false, false},
		{"1e000", 1, 1, false, true},
		{"1.", 1, 1, false, false},
		{"1.2e+", 1, 1, false, false},
		{"4503599627370495e0", 16, 4503599627370495, false, true},
		{"4503599627370496e0", 16, 4503599627370496, false, false},
		{"4503599627370495.0", 16, 4503599627370495, false, false},
		{"450359962737049.5", 15, 450359962737049, false, true},
		{"450359962737049.6", 15, 450359962737049, false, false},
		{"1000000000000000e1", 16, 1000000000000000, false, true},
		{"1000000000000001e1", 16, 1000000000000001, false, false},
		{"0." + strings.Repeat("0", 22), 1, 0, false, true},
		{"0." + strings.Repeat("0", 23), 1, 0, false, false},
	} {
		got, end, ok := exactFloatSuffix(tc.s, tc.i, tc.mantissa, tc.negative)
		if ok != tc.accepted {
			t.Fatalf("%q accepted=%v, want %v", tc.s, ok, tc.accepted)
		}
		if !ok {
			continue
		}
		wantEnd, err := Scan(tc.s, 0)
		if err != 0 || end != wantEnd {
			t.Fatalf("%q end=%d, want %d (scan error %v)", tc.s, end, wantEnd, err)
		}
		want, convErr := strconv.ParseFloat(tc.s[:wantEnd], 64)
		if convErr != nil || math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("%q bits=%x, want %x (conversion error %v)", tc.s, math.Float64bits(got), math.Float64bits(want), convErr)
		}
	}
}

func TestExactFloatSuffixValidatedEnd(t *testing.T) {
	// Conversion needs strconv, but syntax validation has already established
	// the prefix boundary. Trailing non-delimiters belong to the JSON caller.
	for _, token := range []string{"1e23", "1e-23", "1e120", "1e-120", "1e999"} {
		for _, suffix := range []string{"", ",0]", "x", ".", "e+", "\x00", "\xff"} {
			src := "[ " + token + suffix
			_, end, ok := exactFloatSuffix(src, 3, 1, false)
			wantEnd, err := Scan(src, 2)
			if ok || err != 0 || end != 2+len(token) || end != wantEnd {
				t.Fatalf("%q: exact=(%d,%v), scan=(%d,%v), token end=%d", src, end, ok, wantEnd, err, 2+len(token))
			}
		}
	}
	// An unfinished exponent must request the scanner, which owns the exact
	// error byte and the distinction between EOF and an invalid character.
	for _, token := range []string{"1e", "1e+", "1e-"} {
		for _, suffix := range []string{"", "x", "\x00"} {
			src := "[ " + token + suffix
			_, end, ok := exactFloatSuffix(src, 3, 1, false)
			if ok || end != 0 {
				t.Fatalf("%q: unfinished suffix reported validated end=(%d,%v)", src, end, ok)
			}
			_, gotEnd, gotErr := ParsePrefix(src, 2)
			wantEnd, wantErr := Scan(src, 2)
			if wantErr == 0 || gotEnd != wantEnd || gotErr != wantErr {
				t.Fatalf("%q: parse=(%d,%v), scan=(%d,%v)", src, gotEnd, gotErr, wantEnd, wantErr)
			}
		}
	}
}
