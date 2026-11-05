//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"strings"
	"testing"
	"unsafe"
)

// In native/parse_with_padding.c, parse_true/false/null consume the first
// mismatching byte, then node_on_bool/null write only typ. check_error precedes
// check_visit and value_inc: even a full tape must retain that partial node and
// report the literal error without growing or counting it as a completed child.
func TestCParityRunsLiteralErrorBoundary(t *testing.T) {
	for _, context := range []struct {
		name, prefix, suffix string
		nodes                int
	}{
		{"root", " \t", " ", 1},
		{"array", " [0, ", "]", 3},
		{"object", ` {"ok":0,"bad": `, "}", 5},
	} {
		for _, literal := range []struct {
			name, text string
			kind       uint64
			consumed   int
		}{
			{"true_first", "tXXX", KTrue, 2},
			{"true_last", "truX", KTrue, 4},
			{"false_first", "fXXXX", KFalse, 2},
			{"false_last", "falsX", KFalse, 5},
			{"null_first", "nXXX", KNull, 2},
			{"null_last", "nulX", KNull, 4},
		} {
			t.Run(context.name+"/"+literal.name, func(t *testing.T) {
				src := context.prefix + literal.text + context.suffix
				p := cParityRunsTuneParser(src, 0, context.nodes, 0)
				start, end := p.nbuf.nstart, p.nbuf.nend
				const sentinel = uint64(0x8badf00dcafebabe)
				p.nodes[context.nodes-1] = node{typ: ^uint64(0), val: sentinel}

				code := p.parse()
				if code != SONIC_INVALID_LITERAL || p.Pos() != len(context.prefix)+literal.consumed {
					t.Fatalf("code=%d pos=%d, want literal error at %d", code, p.Pos(), len(context.prefix)+literal.consumed)
				}
				if p.nbuf.nstart != start || p.nbuf.nend != end || p.backup != nil || cap(p.nodes) != context.nodes {
					t.Fatal("literal error grew or replaced the full tape")
				}
				used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
				if used != context.nodes || p.nbuf.ncur != end {
					t.Fatalf("used=%d, want exactly full tape of %d nodes", used, context.nodes)
				}
				want := node{typ: literal.kind | uint64(len(context.prefix))<<PosBits, val: sentinel}
				if got := p.nodes[context.nodes-1]; got != want {
					t.Fatalf("partial node=%+v, want %+v", got, want)
				}
				if context.name == "root" {
					if p.nbuf.parent != -1 || p.nbuf.depth != 0 {
						t.Fatalf("root error changed parent=%d depth=%d", p.nbuf.parent, p.nbuf.depth)
					}
				} else if count := uint32(p.nodes[0].typ) >> TypeBits; p.nbuf.parent != 0 || p.nbuf.depth != 1 || count != 1 {
					t.Fatalf("parent=%d depth=%d completed children=%d, want 0/1/1", p.nbuf.parent, p.nbuf.depth, count)
				}
			})
		}
	}
}

// Exercise the Go-owned numeric fallback with decimals, exponents and integers
// outside uint64. The shared stage harness validates the tape against json.
func BenchmarkCParityRunsColdNumbers(b *testing.B) {
	const group = "0.125,-1.75,3.141592653589793,6.022e23,-2.5e-9,1e100," +
		"18446744073709551616,-18446744073709551617"
	src := "[" + strings.Repeat(group+",", 31) + group + "]"
	armAnalysisParseStages[interface{}](b, src, true)
}
