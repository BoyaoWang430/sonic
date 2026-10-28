//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// Compare complete path behavior, including ignored malformed fields, exact
// failure cursors and the selected value's grammar state, to the frozen search.
func TestRISCVPathCacheMatchesFrozen(t *testing.T) {
	var oldState, newState types.StateMachine
	cases := 0
	check := func(src string, start int, path []interface{}) {
		t.Helper()
		for _, strict := range []bool{false, true} {
			oldPos, newPos := start, start
			var om, nm *types.StateMachine
			if strict {
				om, nm = &oldState, &newState
				om.Sp, nm.Sp = 0, 0
			}
			want := frozenGetByPath(&src, &oldPos, &path, om)
			got := GetByPath(&src, &newPos, &path, nm)
			cases++
			if got != want || newPos != oldPos {
				t.Fatalf("src=%q start=%d path=%#v strict=%v: (%d,%d), want (%d,%d)", src, start, path, strict, got, newPos, want, oldPos)
			}
			if strict {
				if om.Sp != nm.Sp {
					t.Fatalf("state depth %q: %d != %d", src, nm.Sp, om.Sp)
				}
				for i := 0; i < int(om.Sp); i++ {
					if om.Vt[i] != nm.Vt[i] {
						t.Fatalf("state %q at %d: %d != %d", src, i, nm.Vt[i], om.Vt[i])
					}
				}
			}
		}
	}
	paths := [][]interface{}{{}, {""}, {"target"}, {"missing"}, {0}, {1}, {-1}, {"target", 0}, {"target", "x"}, {"x"}, {"\\"}, {"\x00"}, {nil}}
	keys := []string{`"target"`, `"missing"`, `""`, `"targ\u0065t"`, `"targ\xet"`, `"targ\u00x5t"`, `"x\x"`, `"\ud800"`, `"\ud800\udc00"`, `"\\"`, "\"\x00\"", `"x\"y"`}
	values := []string{"0", "123", "1x\x00.", "1e+", "-", "[1,{\"x\":2}]", "{\"x\":null}", `"plain"`, `"escape\"next"`, `"\x"`, "true", "falsX", "nulX", "[}", "", ":"}
	spaces := []string{"", " ", "\n\t ", strings.Repeat(" ", 63), strings.Repeat("\n", 65)}
	for _, space := range spaces {
		for _, key := range keys {
			for _, value := range values {
				src := "{" + space + key + space + ":" + space + value + space + ",\"target\":{\"x\":[1,2]},\"tail\":0}"
				for _, path := range paths {
					check(src, 0, path)
				}
				for i := 0; i < len(src); i++ {
					if i < 5 || i%7 == 0 || i > len(src)-5 {
						check(src[:i], 0, []interface{}{"target"})
					}
				}
			}
		}
	}
	// Move every kind of byte across all mask lanes, including block edges.
	for pad := 0; pad < 130; pad++ {
		for x := 0; x < 256; x++ {
			src := `{"noise":` + strings.Repeat("7", pad) + string([]byte{byte(x)}) + `,"target":1}`
			check(src, 0, []interface{}{"target"})
			src = `{"` + strings.Repeat("k", pad) + string([]byte{byte(x)}) + `":0,"target":1}`
			check(src, 0, []interface{}{"target"})
		}
	}
	// Cache offsets can start anywhere in a source, not only at byte zero.
	for pad := 0; pad < 130; pad++ {
		for _, suffix := range []string{`"a\\\"b"`, `"\u0030"`, `"\x"`, `"\ud800\udc00"`, `"\`, `123x`, `null`, `[1,2]`} {
			src := strings.Repeat(" ", pad) + `{"noise":` + suffix + `,"target":7}`
			check(src, pad, []interface{}{"target"})
		}
	}
	// Mutations exercise malformed delimiters and escaped-key mismatch error
	// precedence without assuming that unselected fields are valid JSON.
	r := rand.New(rand.NewSource(20261024))
	seeds := []string{`{"target":1}`, `{"a":1,"target":[1,2],"b":3}`, `{"x\x":0,"target":1}`, `[{"target":1},2]`}
	for n := 0; n < 20000; n++ {
		buf := []byte(seeds[r.Intn(len(seeds))])
		for k := 0; k < 1+r.Intn(4); k++ {
			buf[r.Intn(len(buf))] = byte(r.Intn(256))
		}
		if r.Intn(4) == 0 {
			buf = buf[:r.Intn(len(buf)+1)]
		}
		for _, path := range paths {
			check(string(buf), 0, path)
		}
	}
	t.Logf("path equivalence cases=%d", cases)
}

func TestRISCVPathCacheLongKeyAndString(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 127, 128, 129, 4096} {
		key := strings.Repeat("k", n)
		for _, escape := range []string{"", `\\`, `\u006b`, `\"`} {
			src := fmt.Sprintf(`{"noise":"%s%s","%s":17}`, strings.Repeat("v", n), escape, key)
			path := []interface{}{key}
			p, q := 0, 0
			got := GetByPath(&src, &p, &path, nil)
			want := frozenGetByPath(&src, &q, &path, nil)
			if got != want || p != q {
				t.Fatalf("n=%d escape=%q: (%d,%d) != (%d,%d)", n, escape, got, p, want, q)
			}
		}
	}
}
