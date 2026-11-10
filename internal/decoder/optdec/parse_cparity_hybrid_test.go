//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// A read-only lexer can inspect tokens ahead of the grammar, but accepting a
// token is still the parser's decision. Wrong-context tokens consume only the
// first nonspace byte; rejected or ignored escaped strings must not be decoded.
// This digest is captured from the retained C parser, not from the candidate.
func TestCParityHybridLookaheadOracle(t *testing.T) {
	corpus := []string{
		`[1 234]`, `{"a" "long"}`, `{12345:0}`, `[1 true]`,
		`[1 "\u00xx"]`, `{"a" "\u0041"}`, `{12345:"\n"}`,
		`{"a":0 "\u0041"}`, `0 "\u0041"`, `true "\n"`,
		`[] "\u0041"`, `"root" ["\u0041"]`,
		"[\"a\\n\",\"\x01\"]", "[\"a\",\"\x01\\n\"]",
		"[\"a\\n\",\"\x01\\u00xx\"]", "[\"a\\n\",1e2,\"\x01\"]",
		"[\"a\\n\",[],\"\x01\"]", `[7,-x]`, `[1.25,1e+]`,
	}
	// The array opener plus n (number, comma) pairs positions the next token
	// on either side of a 32-token refill. An initial empty array shifts its
	// parity so the next token can also land exactly at the refill boundary.
	for _, n := range []int{14, 15, 16, 30, 31, 32} {
		prefix := "[" + strings.Repeat("0,", n)
		corpus = append(corpus, prefix+`"plain"]`, prefix+`"x\n"]`,
			prefix+`0 "x\u0041"]`, prefix+`1.25]`, prefix+`truX]`)
	}
	for _, n := range []int{13, 14, 15} {
		prefix := "[[]," + strings.Repeat("0,", n)
		corpus = append(corpus, prefix+`"x\n"]`, prefix+`0 234]`,
			prefix+`"unterminated`, prefix+"\"\x01\\n\"]")
	}
	type record struct {
		Case         int
		Source       []byte
		Options      uint64
		Eface        bool
		Capacity     int
		Code         ErrorCode
		Pos          int
		Depth        uint64
		Parent       int64
		Stat         [7]uint32
		Tape         [][2]uint64
		Decoded      []byte
		OptionsAfter uint64
	}
	hash := sha256.New()
	var writer io.Writer = hash
	if path := os.Getenv("SONIC_CPARITY_HYBRID_DUMP"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		writer = io.MultiWriter(hash, f)
	}
	enc := json.NewEncoder(writer)
	records := 0
	for i, src := range corpus {
		for _, capacity := range []int{1, 2, 8, len(src) + 8} {
			for _, options := range []uint64{0, uint64(OptionUseNumber | OptionCopyString), uint64(OptionValidateString | OptionUseUnicodeErrors)} {
				for _, eface := range []bool{false, true} {
					p := newParser(src, 0, options)
					original := p.nodes
					p.nodes = make([]node, capacity)
					p.nbuf.init(p.nodes)
					p.isEface = eface
					code := p.parse()
					used := p.nbuf.nlen
					if used < 0 || used > len(p.nodes) {
						t.Fatalf("case=%d capacity=%d invalid tape size %d", i, capacity, used)
					}
					tape := make([][2]uint64, used)
					for j, n := range p.nodes[:used] {
						if kind := n.typ & TypeMask; kind == KNull || kind == KTrue || kind == KFalse {
							n.val = 0
						}
						tape[j] = [2]uint64{n.typ, n.val}
					}
					stat := p.nbuf.stat
					r := record{i, []byte(src), options, eface, capacity, code, p.Pos(), p.nbuf.depth, p.nbuf.parent,
						[7]uint32{stat.object, stat.array, stat.str, stat.number, stat.array_elems, stat.object_keys, stat.max_depth},
						tape, p.JsonBytes()[:len(p.Json)], p.options}
					if err := enc.Encode(r); err != nil {
						t.Fatal(err)
					}
					records++
					p.backup = nil
					p.nodes = original
					p.free()
				}
			}
		}
	}
	got := fmt.Sprintf("%x", hash.Sum(nil))
	t.Logf("hybrid lookahead oracle: inputs=%d records=%d sha256=%s", len(corpus), records, got)
	const want = "ed8388bc61447786e59c10496eb4c947cd0a27c8133b2d4bc0d6930856d8c247"
	if want != "" && got != want {
		t.Fatalf("C lookahead oracle mismatch: got %s want %s (set SONIC_CPARITY_HYBRID_DUMP for records)", got, want)
	}
}
