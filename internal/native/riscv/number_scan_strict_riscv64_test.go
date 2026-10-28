//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestNumberScanStrictMarkerPriority(t *testing.T) {
	// Check the complete public entry at scalar/vector boundaries, with signs
	// included in the input. Expected offsets also enforce duplicate-marker
	// precedence over an earlier misplaced sign or decimal point.
	vl := spaceVectorBytes()
	for _, digits := range []int{1, 2, 3, 4, 5, 7, 8, 9, vl - 1, vl, vl + 1, 2*vl + 1} {
		for _, padding := range []string{"", "xx["} {
			for _, sign := range []string{"", "-"} {
				start := len(padding)
				prefix := padding + sign + strings.Repeat("7", digits)
				for _, tc := range []struct {
					suffix string
					end    int
					err    types.ParsingError
				}{
					{"", 0, 0}, {",++..ee", 0, 0}, {".25e-9,", 6, 0},
					{".", 0, types.ERR_INVALID_CHAR}, {"e+", 1, types.ERR_INVALID_CHAR},
					{".e2", 1, types.ERR_INVALID_CHAR}, {"e2.3", 2, types.ERR_INVALID_CHAR},
					{"+2+3", 2, types.ERR_INVALID_CHAR}, {"+2.3.4", 4, types.ERR_INVALID_CHAR},
					{"e+2.3e4", 5, types.ERR_INVALID_CHAR},
				} {
					checkNumberScan(t, "strict-priority", prefix+tc.suffix, start, len(prefix)+tc.end, tc.err, ScanNumberStrict)
				}
				for c := 0; c < 256; c++ {
					src := prefix + string([]byte{byte(c)}) + "2,"
					end := len(prefix)
					var err types.ParsingError
					switch byte(c) {
					case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.', 'e', 'E':
						end += 2
					case '+', '-':
						err = types.ERR_INVALID_CHAR
					}
					checkNumberScan(t, "strict-boundary-byte", src, start, end, err, ScanNumberStrict)
				}
			}
		}
	}
}

func TestNumberScanStrictLeadingZero(t *testing.T) {
	for _, tc := range []struct {
		src string
		end int
		err types.ParsingError
	}{
		{"0", 1, 0}, {"00", 1, 0}, {"0+1", 1, 0},
		{"0.5,", 3, 0}, {"0e+0", 4, 0}, {"0.0e-0", 6, 0},
		{"0.", 1, types.ERR_INVALID_CHAR}, {"0e", 1, types.ERR_INVALID_CHAR},
		{"0e+", 2, types.ERR_INVALID_CHAR}, {"0e+2.3e4", 6, types.ERR_INVALID_CHAR},
	} {
		for _, padding := range []string{"", "xx["} {
			for _, sign := range []string{"", "-"} {
				checkNumberScan(t, "strict-zero", padding+sign+tc.src, len(padding), len(padding)+len(sign)+tc.end, tc.err, ScanNumberStrict)
			}
		}
	}
}
