//go:build linux && riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVPathProtectedTail(t *testing.T) {
	page := os.Getpagesize()
	memory, err := syscall.Mmap(-1, 0, 2*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(memory)
	if err := syscall.Mprotect(memory[page:], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	var gotState, wantState types.StateMachine
	for _, size := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129} {
		key := strings.Repeat("k", size)
		for _, tc := range []struct {
			src  string
			path []interface{}
		}{
			{`{"` + key + `":0,"target":"a\nb\\c\"d"}`, []interface{}{"target"}},
			{`{"noise":0,"` + key + `\t":"value"}`, []interface{}{key + "\t"}},
			{`{"noise":0,"target":"` + strings.Repeat("v", size) + `\"x"}`, []interface{}{"target"}},
			{`{"noise":0,"` + key, []interface{}{"target"}},
			{`{"noise":0,"` + key + `\`, []interface{}{"target"}},
			{`{"noise":0,"target":` + strings.Repeat(" ", size), []interface{}{"target"}},
			// A long string returns an event-only tail. Continue into keys,
			// numbers and EOF without reading beyond the protected source.
			{`{"p":"` + strings.Repeat("v", size+33) + `","#":"a\nb"}`, []interface{}{"#"}},
			{`{"p":"` + strings.Repeat("v", size+33) + `","\\]":0,"target":"tail\"x"}`, []interface{}{"target"}},
			{`{"p":"` + strings.Repeat("v", size+33) + `","":1234567,"target":` + strings.Repeat(" ", size), []interface{}{"target"}},
			{`{"p":"` + strings.Repeat("v", size+33) + `","":1e+,"target":"` + strings.Repeat("a", size) + `\`, []interface{}{"target"}},
		} {
			for cut := 0; cut <= 3 && cut <= len(tc.src); cut++ {
				input := tc.src[:len(tc.src)-cut]
				begin := page - len(input)
				copy(memory[begin:page], input)
				src := unsafe.String(&memory[begin], len(input))
				for _, strict := range []bool{false, true} {
					var gotMachine, wantMachine *types.StateMachine
					if strict {
						gotMachine, wantMachine = &gotState, &wantState
					}
					gotPos, wantPos := 0, 0
					want := frozenGetByPath(&input, &wantPos, &tc.path, wantMachine)
					got := GetByPath(&src, &gotPos, &tc.path, gotMachine)
					if got != want || gotPos != wantPos {
						t.Fatalf("size=%d cut=%d strict=%v src=%q path=%#v: (%d,%d), want (%d,%d)", size, cut, strict, input, tc.path, got, gotPos, want, wantPos)
					}
					if strict && (gotState.Sp != wantState.Sp || !slices.Equal(gotState.Vt[:gotState.Sp], wantState.Vt[:wantState.Sp])) {
						t.Fatalf("size=%d cut=%d src=%q: grammar stack differs", size, cut, input)
					}
				}
			}
		}
	}
}
