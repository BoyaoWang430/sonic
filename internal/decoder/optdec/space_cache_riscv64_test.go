//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRVVParserSpaceBitmapReuse(t *testing.T) {
	src := "   true   false   null" + strings.Repeat(" ", 80) + "["
	r := rvParser{src: src, data: []byte(src)}
	r.space()
	if r.pos != 3 || r.spaceBase != 2 || r.spaceEnd != 66 {
		t.Fatalf("first: pos=%d cache=[%d,%d)", r.pos, r.spaceBase, r.spaceEnd)
	}
	base, mask := r.spaceBase, r.spaceMask
	// Simulate token consumption and decoded strings overwriting the mutable
	// buffer. Whitespace classification must continue to use the source.
	for i := range r.data {
		r.data[i] = ' '
	}
	for _, tc := range []struct{ start, want int }{{7, 10}, {15, 18}, {22, len(src) - 1}} {
		r.pos = tc.start
		r.space()
		if r.pos != tc.want {
			t.Fatalf("start=%d: pos=%d want=%d", tc.start, r.pos, tc.want)
		}
		if tc.start < 20 && (r.spaceBase != base || r.spaceMask != mask) {
			t.Fatal("cached block was replaced")
		}
	}
	r.pos = len(src)
	r.space()
	if r.pos != len(src) {
		t.Fatal(r.pos)
	}
}

func TestRVVParserSpaceCacheBoundaries(t *testing.T) {
	for n := 0; n <= 160; n++ {
		white := strings.Repeat(" \t\r\n", (n+3)/4)[:n]
		for _, suffix := range []string{"", "x", "\x00", "\v", "\f"} {
			r := rvParser{src: white + suffix}
			r.space()
			if r.pos != n {
				t.Fatalf("n=%d suffix=%q: %d", n, suffix, r.pos)
			}
		}
		src := "[" + white + `{"a\u0062" :  [ true, "x\ny", null ]},` + white + `{"c":12}]`
		var got, want interface{}
		pos := 0
		if err := Decode(&src, &pos, 0, &got); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if err := json.Unmarshal([]byte(src), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || pos != len(src) {
			t.Fatalf("n=%d: got=%#v pos=%d", n, got, pos)
		}
	}
}
