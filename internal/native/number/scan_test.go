package number_test

import (
	"testing"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

func TestScan(t *testing.T) {
	tests := []struct {
		s     string
		start int
		end   int
		err   types.ParsingError
	}{
		{"0", 0, 1, 0},
		{"-0", 0, 2, 0},
		{"1234567890", 0, 10, 0},
		{"-12.345E-67", 0, 11, 0},
		{"0e+0", 0, 4, 0},
		{"0.0e-0", 0, 6, 0},
		{"[ -12.5e+2,0]", 2, 10, 0},
		{"123xyz", 0, 3, 0},
		{"123\x00", 0, 3, 0},
		{"1 2", 0, 1, 0},
		{"01", 0, 1, 0},
		{"-01", 0, 2, 0},
		{"00.1", 0, 1, 0},
		{"0+1", 0, 1, 0},
		{"1+2", 0, 1, 0},
		{"1-2", 0, 1, 0},
		{"1.2.3", 0, 3, 0},
		{"1e2e3", 0, 3, 0},
		{"", 0, 0, types.ERR_EOF},
		{"1", 1, 1, types.ERR_EOF},
		{"-", 0, 1, types.ERR_EOF},
		{"1.", 0, 2, types.ERR_EOF},
		{"1e", 0, 2, types.ERR_EOF},
		{"1e+", 0, 3, types.ERR_EOF},
		{"1e-", 0, 3, types.ERR_EOF},
		{"[1e-", 1, 4, types.ERR_EOF},
		{" 1", 0, 0, types.ERR_INVALID_CHAR},
		{"+1", 0, 0, types.ERR_INVALID_CHAR},
		{".1", 0, 0, types.ERR_INVALID_CHAR},
		{"--1", 0, 1, types.ERR_INVALID_CHAR},
		{"-x", 0, 1, types.ERR_INVALID_CHAR},
		{"1.x", 0, 2, types.ERR_INVALID_CHAR},
		{"1.e1", 0, 2, types.ERR_INVALID_CHAR},
		{"1e+x", 0, 3, types.ERR_INVALID_CHAR},
		{"[1e-x", 1, 4, types.ERR_INVALID_CHAR},
		{"1", -1, -1, types.ERR_INVALID_CHAR},
		{"1", 2, 2, types.ERR_INVALID_CHAR},
	}
	for _, tt := range tests {
		end, err := number.Scan(tt.s, tt.start)
		if end != tt.end || err != tt.err {
			t.Errorf("Scan(%q, %d) = (%d, %d), want (%d, %d)", tt.s, tt.start, end, err, tt.end, tt.err)
		}
	}
}
