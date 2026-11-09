//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"
	"unsafe"
)

// This fixture is also compiled unchanged with the retained generated-C parser.
// Its oracle covers C's partial tapes and cursor semantics, which intentionally
// differ from the RISC-V Go parser. Bool/null payloads are unspecified by C
// (including after unzeroed buffer growth), so only their type/position is
// compared. The digest is of JSON records, never of addresses or padding bytes.
func TestCParityOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(571315))
	leaves := []interface{}{nil, true, false, 0, -1, 1.25, "", "ascii", "a\nb\t\"\\c", "\x00\x1f", "中文", "🚀"}
	var value func(int) interface{}
	value = func(depth int) interface{} {
		if depth == 0 || rng.Intn(3) == 0 {
			return leaves[rng.Intn(len(leaves))]
		}
		if rng.Intn(2) == 0 {
			a := make([]interface{}, rng.Intn(5))
			for i := range a {
				a[i] = value(depth - 1)
			}
			return a
		}
		m := make(map[string]interface{})
		keys := []string{"", "a", "b\nc", "unicode中", "q\"\\"}
		for i, n := 0, rng.Intn(5); i < n; i++ {
			m[keys[i]] = value(depth - 1)
		}
		return m
	}
	corpus := []string{"", " ", "\t\r\n", "null", "true", "false", "truX", "nul", "falsex", "[true,]", `{"a" `, `{"a":1,}`, "1e+", "1e999", "18446744073709551616", `"\ud800"`, `"\ud800\udc00"`, `"\ud800\u0041"`, `"\u00xx"`, `"\q"`, "\"\xff\"", "[1,\x00]", TwitterJson}
	for _, n := range []string{"0", "-0", "01", "-01", "0.", "0e", "-", "-x", "0.035", "1.5", "1e-10000", "-1e999", "18446744073709551615", "9223372036854775808", "-9223372036854775808", "-9223372036854775809", "0.000000000000000000000001", "1.00000000000000011102230246251565404236316680908203125", "2.2250738585072014e-308", "2.2250738585072012e-308", "4.9406564584124654e-324", "2.4703282292062327e-324", "1.7976931348623157e308", "1.7976931348623159e308", strings.Repeat("9", 800), "0." + strings.Repeat("0", 800) + "1"} {
		corpus = append(corpus, n, "["+n+"]", `{"n":`+n+`}`)
	}
	for _, n := range []int{0, 1, 2, 3, 15, 16, 17, 31, 32, 33, 63, 64, 65, 128, 4096, 4097} {
		corpus = append(corpus, strings.Repeat("[", n), strings.Repeat("[", n)+`"a\nb"`+strings.Repeat("]", n))
		for _, suffix := range []string{`"`, `\n"`, `\u4e2d"`, `\ud800"`, `\q"`, "\x01\\n\"", "\\n\x01\""} {
			corpus = append(corpus, `"`+strings.Repeat("a", n)+suffix)
		}
		corpus = append(corpus, strings.Repeat(" ", n)+`[1,2]`)
	}
	for i := 0; i < 120; i++ {
		data, err := json.Marshal(value(5))
		if err != nil {
			t.Fatal(err)
		}
		src := strings.Repeat(" \t\r\n", rng.Intn(18)) + string(data)
		corpus = append(corpus, src, src+" tail")
		for j := 0; j < 4; j++ {
			at := rng.Intn(len(src) + 1)
			corpus = append(corpus, src[:at])
			if at < len(src) {
				bad := []byte(src)
				bad[at] = []byte{'X', 0, '\\', '"'}[j]
				corpus = append(corpus, string(bad))
			}
		}
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
		Used         int
		Tape         string
		Decoded      []byte
		Corrected    []byte
		InvalidUTF8  bool
		OptionsAfter uint64
	}
	hash := sha256.New()
	var writer io.Writer = hash
	if path := os.Getenv("SONIC_CPARITY_DUMP"); path != "" {
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
		capacities := []int{len(src) + 8}
		// The valid fixtures also exercise the C wrapper's growth and resume.
		if i < 23 && json.Valid([]byte(src)) {
			capacities = append(capacities, 1, 2, 8)
		}
		for _, capacity := range capacities {
			for _, options := range []uint64{0, uint64(OptionUseNumber | OptionCopyString), uint64(OptionValidateString | OptionUseUnicodeErrors)} {
				for _, eface := range []bool{false, true} {
					p := newParser(src, 0, options)
					original := p.nodes
					p.nodes = make([]node, capacity)
					p.nbuf.init(p.nodes)
					p.isEface = eface
					code := p.parse()
					stat := p.nbuf.stat
					used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
					if used < 0 || used > len(p.nodes) {
						t.Fatalf("case %d invalid tape size %d", i, used)
					}
					tape := sha256.New()
					var buf [16]byte
					for _, n := range p.nodes[:used] {
						if kind := n.typ & TypeMask; kind == KNull || kind == KTrue || kind == KFalse {
							n.val = 0
						}
						binary.LittleEndian.PutUint64(buf[:8], n.typ)
						binary.LittleEndian.PutUint64(buf[8:], n.val)
						tape.Write(buf[:])
					}
					corrected := []byte(p.Json)
					if p.Utf8Inv {
						// C aliases its corrected source with the mutable decode
						// buffer. Compare that representation to keep the original
						// oracle; ARM's immutable source is checked independently
						// by TestARMParserCorrectedSourceOwnership.
						corrected = p.JsonBytes()[:len(p.Json)]
					}
					r := record{i, []byte(src), options, eface, capacity, code, p.Pos(), p.nbuf.depth, p.nbuf.parent,
						[7]uint32{stat.object, stat.array, stat.str, stat.number, stat.array_elems, stat.object_keys, stat.max_depth},
						used, hex.EncodeToString(tape.Sum(nil)), p.JsonBytes()[:len(p.Json)], corrected, p.Utf8Inv, p.options}
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
	t.Logf("C parity records=%d inputs=%d sha256=%s", records, len(corpus), got)
	// Filled from the generated-C run, not from the candidate.
	const want = "d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004"
	if want != "" && got != want {
		t.Fatalf("C oracle digest mismatch: got %s want %s (set SONIC_CPARITY_DUMP for records)", got, want)
	}
}
