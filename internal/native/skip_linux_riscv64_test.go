//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVStrictSkipProtectedTail(t *testing.T) {
	page := os.Getpagesize()
	memory, err := syscall.Mmap(-1, 0, 2*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(memory)
	if err := syscall.Mprotect(memory[page:], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	for size := 0; size <= 130; size++ {
		for _, raw := range []string{
			`"` + strings.Repeat("a", size) + `"`,
			`{"k":"` + strings.Repeat("a", size) + `"}`,
			strings.Repeat(" ", size) + `["a",0,true]`,
			// Long keys exercise the fused key consumer across two cache windows.
			`{"` + strings.Repeat("a", size) + `":0}`,
			`{"` + strings.Repeat("a", size) + `\"b":0}`,
			`{"` + strings.Repeat("a", size) + `\u0041":0}`,
			// Relaxed scanning accepts these escapes while strict scanning
			// rejects them; both must stay within the protected input tail.
			`{"` + strings.Repeat("a", size) + `\q":0}`,
			`{"k":"` + strings.Repeat("a", size) + `\u12"}`,
			// Place the unclosed key or incomplete escape directly at the protected
			// page. A preceding field also exercises an already populated cache.
			`{"` + strings.Repeat("a", size),
			`{"` + strings.Repeat("a", size) + `\`,
			`{"` + strings.Repeat("a", size) + `\u12`,
			`{"seed":0,"` + strings.Repeat("a", size) + `\u12`,
		} {
			for cut := 0; cut < 3 && cut <= len(raw); cut++ {
				input := raw[:len(raw)-cut]
				start := page - len(input)
				copy(memory[start:page], input)
				src := unsafe.String(&memory[start], len(input))
				for _, flags := range []uint64{0, types.F_VALIDATE_STRING} {
					pos := 0
					var state types.StateMachine
					ret := ValidateOne(&src, &pos, &state, flags)
					if flags != 0 {
						if (ret >= 0) != json.Valid([]byte(input)) || pos < 0 || pos > len(src) {
							t.Fatalf("size=%d cut=%d input=%q: ret=%d pos=%d", size, cut, input, ret, pos)
						}
						continue
					}
					// Use the unguarded source for the FSM oracle. Relaxed
					// strings need not satisfy encoding/json's validation.
					wantPos := 0
					var wantState types.StateMachine
					want := validateOneState(&input, &wantPos, &wantState, flags)
					if ret != want || pos != wantPos || state.Sp != wantState.Sp ||
						!slices.Equal(state.Vt[:state.Sp], wantState.Vt[:wantState.Sp]) {
						t.Fatalf("size=%d cut=%d input=%q: token=(%d,%d,%d), state=(%d,%d,%d)", size, cut, input, ret, pos, state.Sp, want, wantPos, wantState.Sp)
					}
				}
			}
		}
	}
}
