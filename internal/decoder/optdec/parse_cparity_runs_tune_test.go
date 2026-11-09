//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

// Use owned, padded input at every byte alignment without relying on the pool's
// allocation alignment. Parser keeps both the input and movable tape alive.
func cParityRunsTuneParser(src string, offset, capacity int, options uint64) *Parser {
	storage := make([]byte, offset+len(src)+len(padding))
	copy(storage[offset:], src)
	copy(storage[offset+len(src):], padding)
	p := &Parser{Json: src, padded: storage[offset:], nodes: make([]node, capacity), options: options, isEface: true}
	p.start = uintptr(unsafe.Pointer(&p.padded[0]))
	p.cur, p.end = p.start, p.start+uintptr(len(src))
	p.nbuf.init(p.nodes)
	return p
}

// The existing block scanner retains the original 32-byte C priority. Compare
// cursor, escape status, partial node and all modified string bytes against it,
// including first-control/first-backslash cases that a first-event fast path
// must not decide itself. An earlier value supplies both possible stale flags.
func TestCParityRunsStringProbeBoundary(t *testing.T) {
	suffixes := []string{
		`"`, `中🚀"`, `\n"`, `\u4e2d\ud800\u0041"`, `\q"`, `\u00xx"`,
		"\x01\"", "\x01" + strings.Repeat("b", 15) + "\\n\"", "\\n\x01\"",
		`\n` + strings.Repeat("b", 97) + `\t"`,
		`\na\q` + "\x01\"", `\n\q` + "\x01\"",
		`\ud800\u00xx"`,
		`\u0022\u005c\u0000` + strings.Repeat("b", 97) + `\n"`,
	}
	lengths := make([]int, 34)
	for i := range lengths {
		lengths[i] = i
	}
	lengths = append(lengths, 63, 64, 65, 127, 128)
	for _, n := range lengths {
		for _, suffix := range suffixes {
			body := strings.Repeat("a", n) + suffix
			for _, options := range []uint64{0, uint64(OptionValidateString)} {
				for _, priorEscaped := range []bool{false, true} {
					prefix := `["seed","`
					if priorEscaped {
						prefix = `["s\n","`
					}
					src := prefix + body + "]"
					wantBytes := append([]byte(src), padding...)
					wantCur := unsafe.Pointer(&wantBytes[len(prefix)])
					wantEscaped := priorEscaped
					wantLength := cParityParseString(&wantCur, &wantEscaped, options)
					wantPos := int(uintptr(wantCur) - uintptr(unsafe.Pointer(&wantBytes[0])))
					wantCode := ErrorCode(SONIC_OK)
					if wantLength < 0 {
						wantCode = ErrorCode(-wantLength)
					} else {
						wantPos++ // The complete parser also consumes the array's ']'.
					}
					kind := uint64(KStringCommon)
					if wantEscaped {
						kind = KStringEscaped
					}
					wantNode := node{typ: kind | uint64(len(prefix))<<PosBits, val: uint64(wantLength)}
					for offset := 0; offset < 32; offset++ {
						p := cParityRunsTuneParser(src, offset, 8, options)
						code := p.parse()
						used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
						if code != wantCode || p.Pos() != wantPos || used != 3 || p.nodes[2] != wantNode ||
							!bytes.Equal(p.padded[len(prefix):len(src)], wantBytes[len(prefix):len(src)]) {
							t.Fatalf("prefix=%d offset=%d options=%d priorEscaped=%v suffix=%q: code=%d pos=%d used=%d node=%+v, want %d/%d/3/%+v",
								n, offset, options, priorEscaped, suffix, code, p.Pos(), used, p.nodes[2], wantCode, wantPos, wantNode)
						}
					}
				}
			}
		}
	}
}

func cParityRunsTuneStats(value interface{}, depth uint32, stat *jsonStat) {
	switch v := value.(type) {
	case map[string]interface{}:
		stat.object++
		stat.object_keys += uint32(len(v))
		depth++
		if depth > stat.max_depth {
			stat.max_depth = depth
		}
		for _, child := range v {
			cParityRunsTuneStats(child, depth, stat)
		}
	case []interface{}:
		stat.array++
		stat.array_elems += uint32(len(v))
		depth++
		if depth > stat.max_depth {
			stat.max_depth = depth
		}
		for _, child := range v {
			cParityRunsTuneStats(child, depth, stat)
		}
	case string:
		stat.str++
	case float64:
		stat.number++
	}
}

// Adjacent closers can share a Go continuation, but closing a child must still
// update the parent's element count and restore the right object/array phase.
// Semantic values and aggregate statistics come from encoding/json, not from
// a snapshot of this parser's output.
func TestCParityRunsCloseChainsGrowth(t *testing.T) {
	dec, err := findOrCompile(rt.UnpackType(reflect.TypeOf((*interface{})(nil)).Elem()))
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{
		`{"a":[{"b":[1,2]}],"c":[[],{}]}`,
		`[[[{"a":[],"b":{}}]],{"last":[true,false,null]}]`,
		`{"outer":{"child":{"leaf":1}},"tail":"x\u4e2d"}`,
		strings.Repeat("[", 12) + `{"u":"x\u4e2d","n":1.25}` + strings.Repeat("]", 12),
	} {
		for _, n := range []int{0, 1, 2, 63, 64, 65} {
			white := strings.Repeat(" \t\r\n", (n+3)/4)[:n]
			src := strings.NewReplacer("]", white+"]", "}", white+"}").Replace(base)
			var want interface{}
			if err := json.Unmarshal([]byte(src), &want); err != nil {
				t.Fatal(err)
			}
			var stat jsonStat
			cParityRunsTuneStats(want, 0, &stat)
			for capacity := 1; capacity <= 24; capacity++ {
				p := cParityRunsTuneParser(src, 0, capacity, uint64(OptionCopyString|OptionValidateString))
				code := p.parse()
				if code != SONIC_OK || p.Pos() != len(src) || p.nbuf.depth != 0 || p.nbuf.parent != -1 || p.nbuf.stat != stat {
					t.Fatalf("capacity=%d whitespace=%d input=%q: code=%d pos=%d depth=%d parent=%d stat=%+v want=%+v",
						capacity, n, base, code, p.Pos(), p.nbuf.depth, p.nbuf.parent, p.nbuf.stat, stat)
				}
				var got interface{}
				ctx := Context{Parser: p}
				if err := dec.FromDom(unsafe.Pointer(&got), ctx.Root(), &ctx); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("capacity=%d whitespace=%d input=%q: decoded=%#v want=%#v err=%v", capacity, n, base, got, want, err)
				}
			}
		}
	}
}

func TestCParityRunsCloseChainErrors(t *testing.T) {
	for _, tc := range []struct {
		prefix, bad, tail string
		code              ErrorCode
	}{
		{"[[0]", "}", "]", SONIC_EXPECT_ARR_COMMA_OR_END},
		{`{"k":[0]`, "]", "}", SONIC_EXPECT_OBJ_COMMA_OR_END},
		{`[{"k":0}`, "}", "]", SONIC_EXPECT_ARR_COMMA_OR_END},
		{`{"k":{"j":0}`, "]", "}", SONIC_EXPECT_OBJ_COMMA_OR_END},
		{"[[0]", "\x00", "]", SONIC_EXPECT_ARR_COMMA_OR_END},
	} {
		for _, n := range []int{0, 1, 2, 63, 64, 65} {
			white := strings.Repeat(" \t\r\n", (n+3)/4)[:n]
			src := tc.prefix + white + tc.bad + tc.tail
			for capacity := 1; capacity <= 24; capacity++ {
				p := cParityRunsTuneParser(src, 0, capacity, 0)
				code := p.parse()
				// C's enclosing-container check consumes the unexpected byte.
				if code != tc.code || p.Pos() != len(tc.prefix)+len(white)+1 {
					t.Fatalf("capacity=%d input=%q: code=%d pos=%d want=%d/%d", capacity, src, code, p.Pos(), tc.code, len(tc.prefix)+len(white)+1)
				}
			}
		}
	}
}

// The C oracle already covers these limits. Keep a focused check alongside
// closing-chain changes: overflow is observed at the first close, and that
// close records max_depth before returning without decrementing active depth.
func TestCParityRunsCloseChainDepth(t *testing.T) {
	for _, depth := range []int{4096, 4097} {
		src := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		for _, capacity := range []int{1, 2, 8, 24, depth + 2} {
			p := cParityRunsTuneParser(src, 0, capacity, 0)
			code := p.parse()
			if p.nbuf.stat.max_depth != uint32(depth) {
				t.Fatalf("depth=%d capacity=%d max_depth=%d", depth, capacity, p.nbuf.stat.max_depth)
			}
			if depth == 4096 {
				if code != SONIC_OK || p.Pos() != len(src) || p.nbuf.depth != 0 || p.nbuf.parent != -1 || p.nbuf.stat.array != uint32(depth) {
					t.Fatalf("depth=%d capacity=%d code=%d pos=%d state=%+v", depth, capacity, code, p.Pos(), p.nbuf)
				}
			} else if code != SONIC_STACK_OVERFLOW || p.Pos() != depth+2 || p.nbuf.depth != uint64(depth) {
				t.Fatalf("depth=%d capacity=%d code=%d pos=%d state=%+v", depth, capacity, code, p.Pos(), p.nbuf)
			}
		}
	}
}
