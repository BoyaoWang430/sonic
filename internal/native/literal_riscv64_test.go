//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"slices"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVLiteralTraversalContract(t *testing.T) {
	cases := []struct {
		src    string
		cursor int
		err    types.ParsingError
		stack  []int
	}{
		{"null", 4, 0, nil},
		{"trueX", 4, 0, nil}, // Traversal returns the first value, not the whole input.
		{"false", 5, 0, nil},
		{"tx", 2, types.ERR_EOF, nil}, // Truncation takes priority over a bad suffix.
		{"txxx", 1, types.ERR_INVALID_CHAR, nil},
		{"fal", 3, types.ERR_EOF, nil},
		{"falxe", 3, types.ERR_INVALID_CHAR, nil},
		{"falsX", 4, types.ERR_INVALID_CHAR, nil},
		{"n\x00ll", 1, types.ERR_INVALID_CHAR, nil},
		{"[true,false,null]", 17, 0, nil},
		{`{"x":true,"y":[false,null]}`, 27, 0, nil},
		{"[tx]", 4, types.ERR_EOF, []int{skipArrayState}},
		{"[txxx]", 2, types.ERR_INVALID_CHAR, []int{skipArrayState}},
		{`{"x":falXe}`, 8, types.ERR_INVALID_CHAR, []int{skipObjectState}},
	}
	for _, tc := range cases {
		for offset := 0; offset <= 64; offset++ {
			for _, flags := range []uint64{0, types.F_VALIDATE_STRING} {
				src := strings.Repeat(" ", offset) + tc.src
				var state types.StateMachine
				state.Sp, state.Vt[0] = 1, -1
				pos := 0
				ret := ValidateOne(&src, &pos, &state, flags)
				wantRet := offset
				if tc.err != 0 {
					wantRet = -int(tc.err)
				}
				if ret != wantRet || pos != offset+tc.cursor || !slices.Equal(state.Vt[:state.Sp], tc.stack) {
					t.Fatalf("src=%q offset=%d flags=%d: got (%d,%d,%v), want (%d,%d,%v)", tc.src, offset, flags, ret, pos, state.Vt[:state.Sp], wantRet, offset+tc.cursor, tc.stack)
				}
			}
		}
	}
}
