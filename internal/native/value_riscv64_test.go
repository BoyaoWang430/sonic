//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"math"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVValue(t *testing.T) {
	for _, tt := range []struct {
		src   string
		flags uint64
		end   int
		kind  int64
		iv    int64
		ep    int
	}{
		{"   -12345", 0, 9, types.V_INTEGER, -12345, 3},
		{"0", 0, 1, types.V_INTEGER, 0, 0},
		{"-0", 0, 2, types.V_INTEGER, 0, 0},
		{"01", 0, 1, types.V_INTEGER, 0, 0},
		{"9223372036854775808", 0, 19, types.V_DOUBLE, 0, 0},
		{"1e999", 0, 5, -int64(types.ERR_FLOAT_INFINITY), 0, 0},
		{"1e999", types.F_USE_NUMBER, 5, types.V_DOUBLE, 0, 0},
		{"1+2", types.F_USE_NUMBER, 1, -int64(types.ERR_INVALID_CHAR), 0, 0},
		{"-", 0, 1, -int64(types.ERR_EOF), 0, 0},
		{"true", 0, 4, types.V_TRUE, 0, 0},
		{"fals", 0, 4, -int64(types.ERR_EOF), 0, 0},
		{"trux", 0, 3, -int64(types.ERR_INVALID_CHAR), 0, 0},
		{"[", 0, 1, types.V_ARRAY, 0, 0},
		{"{", 0, 1, types.V_OBJECT, 0, 0},
		{"}", 0, 0, -int64(types.ERR_INVALID_CHAR), 0, 0},
		{"}", types.F_ALLOW_CONTROL, 1, 13, 0, 0},
		{"", 0, 0, types.V_EOF, 0, 0},
		{" \t", 0, 2, types.V_EOF, 0, 0},
		{`"`, 0, 1, -int64(types.ERR_EOF), 0, 0},
		{`"abc"`, 0, 5, types.V_STRING, 1, -1},
		{` "a\nb"`, 0, 7, types.V_STRING, 2, 3},
		{`"\q"`, 0, 4, types.V_STRING, 1, 1},
		{`"\q"`, types.F_VALIDATE_STRING, 4, -int64(types.ERR_INVALID_CHAR), 0, 0},
	} {
		var state types.JsonState
		end := Value(unsafe.Pointer(unsafe.StringData(tt.src)), len(tt.src), 0, &state, tt.flags)
		if end != tt.end || state.Vt != tt.kind || state.Iv != tt.iv || state.Ep != tt.ep {
			t.Errorf("Value(%q, flags=%d) = end %d, state %+v; want end=%d kind=%d iv=%d ep=%d", tt.src, tt.flags, end, state, tt.end, tt.kind, tt.iv, tt.ep)
		}
	}
	for _, src := range []string{"-0.0", "-1e-999"} {
		var state types.JsonState
		Value(unsafe.Pointer(unsafe.StringData(src)), len(src), 0, &state, 0)
		if state.Vt != types.V_DOUBLE || !math.Signbit(state.Dv) || state.Dv != 0 {
			t.Errorf("negative floating zero %q lost: %+v", src, state)
		}
	}
}
