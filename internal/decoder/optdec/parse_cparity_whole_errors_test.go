//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"encoding/json"
	"reflect"
	"testing"
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

// Kernel protocol test for numeric cold returns and tape growth:
//   - Malformed numbers never inherit the preceding token's value/type or emit
//     half-formed nodes onto the tape.
//   - Raw-number mode rejects bare "-x" with SONIC_INVALID_NUM identically to
//     ordinary mode.
//   - Every initial node capacity (1..128) produces the exact same valid-prefix
//     tape, statistics, error code, and cursor position.
func TestCParityWholeNumericErrorResume(t *testing.T) {
	const golden = `[
  [{"Code":3,"Pos":4,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967299,7]],"Decoded":"[7,-x]"},{"Code":3,"Pos":4,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967323,1]],"Decoded":"[7,-x]"}],
  [{"Code":3,"Pos":7,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967315,4608308318706860032]],"Decoded":"[1.25,-x]"},{"Code":3,"Pos":7,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967323,4]],"Decoded":"[1.25,-x]"}],
  [{"Code":3,"Pos":6,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967299,7]],"Decoded":"[7,1e+]"},{"Code":3,"Pos":5,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967323,1]],"Decoded":"[7,1e+]"}],
  [{"Code":4,"Pos":11,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[263,18446744073709551615],[4294967315,4608308318706860032]],"Decoded":"[1.25,1e309]"},{"Code":0,"Pos":12,"Depth":0,"Parent":-1,"Stat":[0,1,0,2,2,0,1],"Tape":[[7,12884901890],[4294967323,4],[25769803803,5]],"Decoded":"[1.25,1e309]"}],
  [{"Code":3,"Pos":21,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[262,18446744073709551615],[8589934604,2],[47244640275,4608308318706860032],[73014444036,1]],"Decoded":"{\"kAu0041\":1.25,\"v\":-x}"},{"Code":3,"Pos":21,"Depth":1,"Parent":0,"Stat":[0,0,0,1,0,0,0],"Tape":[[262,18446744073709551615],[8589934604,2],[47244640283,4],[73014444036,1]],"Decoded":"{\"kAu0041\":1.25,\"v\":-x}"}]
]`
	var states [5][2]wholeNumericErrorState
	if err := json.Unmarshal([]byte(golden), &states); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`[7,-x]`,
		`[1.25,-x]`,
		`[7,1e+]`,
		`[1.25,1e309]`,
		`{"k\u0041":1.25,"v":-x}`,
	}
	for i, src := range cases {
		for j, opt := range []uint64{uint64(OptionCopyString), uint64(OptionCopyString | OptionUseNumber)} {
			want := states[i][j]
			for _, capacity := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 128} {
				p := newParser(src, 0, opt)
				original := p.nodes
				p.nodes = make([]node, capacity)
				p.nbuf.init(p.nodes)
				p.isEface = true
				code := p.parse()
				stat := p.nbuf.stat
				used := p.nbuf.nlen
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
