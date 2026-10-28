//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package optdec

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func rvvBooleanArray(count int) string {
	return "[" + strings.Repeat("true,", count-1) + "false]"
}

func TestRVVParserLargeNodeGrowth(t *testing.T) {
	// Cross the resident node capacity and multiple subsequent growth points.
	// Check every produced node: unused allocation contents must never leak
	// into a node's type, source offset, value, or the root's child count.
	for _, count := range []int{65535, 65536, 131073} {
		src := rvvBooleanArray(count)
		p := newParser(src, 0, 0)
		if code := p.parse(); code != SONIC_OK || p.Pos() != len(src) {
			t.Fatalf("count=%d code=%d pos=%d", count, code, p.Pos())
		}
		used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
		if used != count+1 || p.nodes[0].val != uint64(count)|uint64(count+1)<<ConLenBits {
			t.Fatalf("count=%d used=%d root=%#x", count, used, p.nodes[0].val)
		}
		for i := 1; i <= count; i++ {
			kind := uint64(KTrue)
			if i == count {
				kind = KFalse
			}
			want := node{typ: kind | uint64(1+(i-1)*5)<<PosBits}
			if p.nodes[i] != want {
				t.Fatalf("count=%d node=%d got=%+v want=%+v", count, i, p.nodes[i], want)
			}
		}
		p.free()
	}
}

func TestRVVParserFrameReuseAfterError(t *testing.T) {
	p := newParser("", 0, 0)
	defer p.free()
	for _, tc := range []struct {
		src  string
		code ErrorCode
	}{
		{strings.Repeat("[", 128), SONIC_EOF},
		{"[false]", SONIC_OK},
		{strings.Repeat("[", 128) + "true" + strings.Repeat("]", 128), SONIC_OK},
		{`{"a":[`, SONIC_EOF},
		{"[false]", SONIC_OK},
		{strings.Repeat("[", types.MAX_RECURSE+1), SONIC_STACK_OVERFLOW},
		{"[false]", SONIC_OK},
	} {
		src := tc.src
		p.reset()
		p.Json = src
		p.padded = append(p.padded, src...)
		p.start = uintptr(unsafe.Pointer(unsafe.SliceData(p.padded)))
		p.cur, p.end = p.start, p.start+uintptr(len(src))
		p.nbuf.init(p.nodes)
		code := p.parse()
		if code != tc.code || code == SONIC_OK && p.Pos() != len(src) {
			t.Fatalf("code=%d want=%d pos=%d input=%q", code, tc.code, p.Pos(), src)
		}
		if len(p.rvFrames) != 0 {
			t.Fatalf("scratch length=%d after parse", len(p.rvFrames))
		}
		if src == "[false]" && (p.nbuf.stat.max_depth != 1 || p.nodes[1] != (node{typ: KFalse | 1<<PosBits})) {
			t.Fatalf("stale frame state: stats=%+v leaf=%+v", p.nbuf.stat, p.nodes[1])
		}
	}
}

func BenchmarkRVVNodeGrowth(b *testing.B) {
	for _, tc := range []struct {
		name  string
		count int
	}{{"Resident32K", 32768}, {"Grow128K", 131072}, {"Grow512K", 524288}} {
		b.Run(tc.name, func(b *testing.B) {
			src := rvvBooleanArray(tc.count)
			p := newParser(src, 0, 0)
			if code := p.parse(); code != SONIC_OK || p.Pos() != len(src) {
				b.Fatal("invalid benchmark fixture")
			}
			p.free()
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := newParser(src, 0, 0)
				code, pos := p.parse(), p.Pos()
				p.free()
				if code != SONIC_OK || pos != len(src) {
					b.Fatalf("parse code=%d pos=%d", code, pos)
				}
			}
		})
	}
}
