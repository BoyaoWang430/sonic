//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"strings"
	"testing"
	"unsafe"
)

// In the Runs kernel protocol, literal scanners (true/false/null) consume the
// first mismatching byte and report SONIC_INVALID_LITERAL without growing the
// tape or writing a partial node past the valid prefix.
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
				startPtr, endCap := unsafe.SliceData(p.nodes), p.nbuf.ncap
				wantUntouched := node{typ: ^uint64(0), val: 0x8badf00dcafebabe}
				p.nodes[context.nodes-1] = wantUntouched

				code := p.parse()
				if code != SONIC_INVALID_LITERAL || p.Pos() != len(context.prefix)+literal.consumed {
					t.Fatalf("code=%d pos=%d, want literal error at %d", code, p.Pos(), len(context.prefix)+literal.consumed)
				}
				if unsafe.SliceData(p.nodes) != startPtr || p.nbuf.ncap != endCap || p.backup != nil || cap(p.nodes) != context.nodes {
					t.Fatal("literal error grew or replaced the full tape")
				}
				used := p.nbuf.nlen
				if used != context.nodes-1 {
					t.Fatalf("used=%d, want valid prefix of %d nodes", used, context.nodes-1)
				}
				if got := p.nodes[context.nodes-1]; got != wantUntouched {
					t.Fatalf("slot past valid prefix modified=%+v, want untouched %+v", got, wantUntouched)
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

func TestZeroCapParserAndStreamSessionBeginReset(t *testing.T) {
	// 1. Bare Parser with zero-cap nodes slice must not underflow nodesLast in cParityParseRunsAsm.
	src := `{"a":1,"b":"ok"}`
	bare := &Parser{
		ScanContext: ScanContext{
			ScanKernelState: ScanKernelState{end: len(src)},
			padded:          append(append([]byte(nil), src...), padding...),
			dbuf:            make([]byte, 800),
		},
		Document: Document{
			Json: src,
		},
	}
	bare.nbuf.init(nil)
	if code := cParityParseWithPadding(bare); code != SONIC_OK {
		t.Fatalf("cParityParseWithPadding on zero-cap Parser failed: code=%v", code)
	}
	if bare.nbuf.nlen == 0 || cap(bare.nodes) == 0 {
		t.Fatalf("expected initialized nodes after zero-cap parse, nlen=%d cap=%d", bare.nbuf.nlen, cap(bare.nodes))
	}

	// 2. StreamSession.Begin must clear stale strSlab state in DecodeContext even if a prior run aborted without Reset.
	sess := getStreamSession()
	defer FreeStreamSession(sess)
	_ = sess.ensureParser()
	sess.ctx.strSlab = []byte("stale-slab")
	sess.ctx.strSlabOff = 5
	sess.ctx.strSlabNext = 2048
	sess.Begin(0, false)
	if sess.ctx.strSlab != nil || sess.ctx.strSlabOff != 0 || sess.ctx.strSlabNext != 0 {
		t.Fatalf("StreamSession.Begin left stale strSlab state: len=%d off=%d next=%d", len(sess.ctx.strSlab), sess.ctx.strSlabOff, sess.ctx.strSlabNext)
	}
}
