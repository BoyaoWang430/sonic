//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"encoding/json"
	"reflect"
	"testing"
	"unsafe"
)

type wholeNumericErrorState struct {
	Code    ErrorCode
	Pos     int
	Depth   uint64
	Parent  int64
	Stat    [7]uint32
	Tape    [][2]uint64
	Decoded string
}

// These expected states were captured from the retained generated-C parser,
// including its partial tape on malformed input. A numeric cold return must
// preserve the last successful number; a node-buffer growth must reset it.
// In particular, raw-number mode accepts the minus of "-x", then reports the
// enclosing container error, while ordinary mode reports an invalid number.
func TestCParityWholeNumericErrorResume(t *testing.T) {
	const golden = `[
  [{"Code":3,"Pos":4,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[263,18446744073709551615],[4294967299,7],[12884901891,7]],"Decoded":"[7,-x]"},{"Code":10,"Pos":5,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[519,18446744073709551615],[4294967323,1],[12884901915,1]],"Decoded":"[7,-x]"}],
  [{"Code":3,"Pos":7,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[263,18446744073709551615],[4294967315,4608308318706860032],[25769803795,4608308318706860032]],"Decoded":"[1.25,-x]"},{"Code":10,"Pos":8,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[519,18446744073709551615],[4294967323,4],[25769803803,1]],"Decoded":"[1.25,-x]"}],
  [{"Code":3,"Pos":6,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[263,18446744073709551615],[4294967299,7],[12884901891,7]],"Decoded":"[7,1e+]"},{"Code":3,"Pos":5,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[263,18446744073709551615],[4294967323,1],[12884901915,18446744073709551613]],"Decoded":"[7,1e+]"}],
  [{"Code":4,"Pos":11,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[263,18446744073709551615],[4294967315,4608308318706860032],[25769803795,4608308318706860032]],"Decoded":"[1.25,1e309]"},{"Code":0,"Pos":12,"Depth":0,"Parent":-1,"Stat":[0,1,0,2,2,0,1],"Tape":[[7,12884901890],[4294967323,4],[25769803803,5]],"Decoded":"[1.25,1e309]"}],
  [{"Code":3,"Pos":21,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[262,18446744073709551615],[8589934604,2],[47244640275,4608308318706860032],[73014444036,1],[85899345939,4608308318706860032]],"Decoded":"{\"kAu0041\":1.25,\"v\":-x}"},{"Code":9,"Pos":22,"Depth":1,"Parent":0,"Stat":[0,0,0,2,0,0,0],"Tape":[[518,18446744073709551615],[8589934604,2],[47244640283,4],[73014444036,1],[85899345947,1]],"Decoded":"{\"kAu0041\":1.25,\"v\":-x}"}]
]`
	var states [5][2]wholeNumericErrorState
	if err := json.Unmarshal([]byte(golden), &states); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		src      string
		resetCap []int
	}{
		{`[7,-x]`, []int{2}},
		{`[1.25,-x]`, []int{2}},
		{`[7,1e+]`, []int{2}},
		{`[1.25,1e309]`, []int{2}},
		{`{"k\u0041":1.25,"v":-x}`, []int{3, 4}},
	}
	for i, tc := range cases {
		for j, opt := range []uint64{uint64(OptionCopyString), uint64(OptionCopyString | OptionUseNumber)} {
			for _, capacity := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 128} {
				want := states[i][j]
				if j == 0 {
					for _, resetCap := range tc.resetCap {
						if capacity == resetCap {
							// C starts a fresh parse invocation after growth, so there is no
							// previous number to publish for this malformed token.
							want.Tape = want.Tape[:len(want.Tape)-1]
							want.Stat[3]--
						}
					}
				}
				p := newParser(tc.src, 0, opt)
				original := p.nodes
				p.nodes = make([]node, capacity)
				p.nbuf.init(p.nodes)
				p.isEface = true
				code := p.parse()
				stat := p.nbuf.stat
				used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
				if used < 0 || used > len(p.nodes) {
					t.Fatalf("case=%d options=%d capacity=%d bad tape size %d", i, opt, capacity, used)
				}
				tape := make([][2]uint64, used)
				for k, n := range p.nodes[:used] {
					if kind := n.typ & TypeMask; kind == KNull || kind == KTrue || kind == KFalse {
						n.val = 0
					}
					tape[k] = [2]uint64{n.typ, n.val}
				}
				got := wholeNumericErrorState{code, p.Pos(), p.nbuf.depth, p.nbuf.parent, [7]uint32{stat.object, stat.array, stat.str, stat.number, stat.array_elems, stat.object_keys, stat.max_depth}, tape, string(p.JsonBytes()[:len(p.Json)])}
				if !reflect.DeepEqual(got, want) || p.options != opt {
					t.Fatalf("case=%d options=%d capacity=%d\ngot  %+v (options=%d)\nwant %+v", i, opt, capacity, got, p.options, want)
				}
				p.backup = nil
				p.nodes = original
				p.free()
			}
		}
	}
}
