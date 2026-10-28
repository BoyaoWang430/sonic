//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVStrictNumberIntegration(t *testing.T) {
	for _, tc := range []struct {
		token string
		end   int
		err   types.ParsingError
	}{
		{"-", 1, types.ERR_EOF}, {"-x", 1, types.ERR_INVALID_CHAR},
		{"0", 1, 0}, {"-0", 2, 0}, {"01", 1, 0}, {"-01", 2, 0},
		{"123", 3, 0}, {"12345", 5, 0}, {"12345678", 8, 0}, {"123456789", 9, 0},
		{"12345.25e-9", 11, 0}, {"0.0e-0", 6, 0},
		{"1.2.3", 3, types.ERR_INVALID_CHAR}, {"1+2+3", 3, types.ERR_INVALID_CHAR},
		{"1e+", 2, types.ERR_INVALID_CHAR}, {"1e+2.3e4", 6, types.ERR_INVALID_CHAR},
	} {
		for _, whitespace := range []string{"", " ", "\t\r\n"} {
			src := "xx" + whitespace + tc.token
			start, end := 2+len(whitespace), 2+len(whitespace)+tc.end
			var value types.JsonState
			gotEnd := Value(unsafe.Pointer(unsafe.StringData(src)), len(src), 2, &value, types.F_USE_NUMBER)
			wantKind := int64(types.V_DOUBLE)
			if tc.err != 0 {
				wantKind = -int64(tc.err)
			}
			if gotEnd != end || value.Vt != wantKind || value.Ep != start {
				t.Fatalf("Value(%q)=(end=%d kind=%d start=%d), want (%d,%d,%d)", src, gotEnd, value.Vt, value.Ep, end, wantKind, start)
			}
			pos := 2
			var state types.StateMachine
			got := ValidateOne(&src, &pos, &state, 0)
			want := start
			if tc.err != 0 {
				wantErr := tc.err
				if wantErr == types.ERR_EOF {
					wantErr = types.ERR_INVALID_CHAR
				}
				want = -int(wantErr)
			}
			if got != want || pos != end || state.Sp != 0 {
				t.Fatalf("ValidateOne(%q)=(%d,%d,sp=%d), want (%d,%d,0)", src, got, pos, state.Sp, want, end)
			}
		}
	}
}
