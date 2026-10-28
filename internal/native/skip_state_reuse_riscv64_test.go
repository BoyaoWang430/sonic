//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"slices"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVValidateOneNestedStateReuse(t *testing.T) {
	// Closing a child must restore the parent's grammar phase, including when
	// another child follows it. Keep the same state buffer through success and
	// failure so stale frames cannot satisfy the expected active stack.
	cases := []struct {
		src   string
		err   types.ParsingError
		stack []int
	}{
		{`{"a":[{"x":[]},{}],"b":{"y":[0]}}`, 0, nil},
		{`{"a":[{"x":[]},{}],"b":{"y":[0]},"c"`, types.ERR_EOF, []int{skipObjectState, skipElementState}},
		{`{"a":[{"x":[]},{}],"b":{"y":[0],"z":`, types.ERR_EOF, []int{skipObjectState, skipObjectState, skipValueState}},
		{`{"a":[{"x":[]},{}],"b":[{},{"q"`, types.ERR_EOF, []int{skipObjectState, skipArrayState, skipObjectState, skipElementState}},
		{`{"a":[{"x":[]},{}],"b":[{},{"q":?`, types.ERR_INVALID_CHAR, []int{skipObjectState, skipArrayState, skipObjectState}},
		{"", types.ERR_EOF, []int{skipValueState}},
		{"true", 0, nil},
	}
	for _, flags := range []uint64{0, types.F_VALIDATE_STRING} {
		var m types.StateMachine
		m.Sp = len(m.Vt)
		for i := range m.Vt {
			m.Vt[i] = -1
		}
		for _, tc := range cases {
			p := 0
			ret := ValidateOne(&tc.src, &p, &m, flags)
			if ret != -int(tc.err) || p != len(tc.src) || m.Sp != len(tc.stack) || !slices.Equal(m.Vt[:m.Sp], tc.stack) {
				t.Fatalf("flags=%d src=%q: ret=%d cursor=%d states=%v; want %d/%d/%v", flags, tc.src, ret, p, m.Vt[:m.Sp], -int(tc.err), len(tc.src), tc.stack)
			}
		}
	}
}
