//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// Mix numeric cold returns with escaped keys, nested containers, and every
// small tape capacity. Resumption must retain the parent/count state through
// both assembly returns and a subsequent allocation/move of the tape.
func TestCParityWholeColdResumeGrowth(t *testing.T) {
	dec, err := findOrCompile(reflect.TypeOf((*interface{})(nil)).Elem())
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		`{"k\u0041":[1.25,{"v":"a\nb","f":2.2250738585072014e-308},-0.0,1e20],"last":7}`,
		`[18446744073709551615,-9223372036854775809,1.00000000000000011102230246251565404236316680908203125,"x\ud800\u0041",{},[]]`,
		`{"empty":[],"nested":{"n":-1.25,"s":"tail"},"n":1e-10000,"b":true,"z":null}`,
		`{"u\u0041":"a\n\u4e2d\t\ud800\u0041\\\"\ud83d\ude80\u0000-tail","next\u006b":1.25}`,
	} {
		for _, options := range []uint64{uint64(OptionCopyString), uint64(OptionCopyString | OptionUseNumber)} {
			var want interface{}
			jd := json.NewDecoder(strings.NewReader(src))
			if options&uint64(OptionUseNumber) != 0 {
				jd.UseNumber()
			}
			if err := jd.Decode(&want); err != nil {
				t.Fatal(err)
			}
			for capacity := 1; capacity <= 24; capacity++ {
				p := newParser(src, 0, options)
				original := p.nodes
				p.nodes = make([]node, capacity)
				p.nbuf.init(p.nodes)
				p.isEface = true
				code := p.parse()
				var got interface{}
				ctx := Context{Parser: p}
				if code != SONIC_OK || p.Pos() != len(src) {
					t.Fatalf("capacity=%d options=%d code=%d pos=%d input=%q", capacity, options, code, p.Pos(), src)
				}
				if err := dec.FromDom(unsafe.Pointer(&got), ctx.Root(), &ctx); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("capacity=%d options=%d decoded=%#v want=%#v err=%v", capacity, options, got, want, err)
				}
				if p.nbuf.depth != 0 || p.nbuf.parent != -1 || p.options != options {
					t.Fatalf("capacity=%d leaked parser state: %+v", capacity, p.nbuf)
				}
				p.backup = nil
				p.nodes = original
				p.free()
			}
		}
	}
}
