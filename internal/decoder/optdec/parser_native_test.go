//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRVVParserSmallNodeBuffer(t *testing.T) {
	input := `{"escaped\u006bey":[1,{"text":"a\nb","empty":[]},null],"ok":true}`
	for _, capacity := range []int{1, 2, 3, 4, 5, 8, 16} {
		p := newParser(input, 0, 0)
		original := p.nodes
		p.nodes = make([]node, capacity)
		p.nbuf.init(p.nodes)
		code := p.parse()
		used := int((p.nbuf.ncur - p.nbuf.nstart) / unsafe.Sizeof(node{}))
		if code != 0 || p.Pos() != len(input) {
			t.Errorf("capacity %d: code=%d pos=%d", capacity, code, p.Pos())
		}
		if used != 12 || p.nodes[0].val != uint64(12)<<32|2 {
			t.Errorf("capacity %d: nodes=%d root=%#x", capacity, used, p.nodes[0].val)
		}
		wantStat := jsonStat{object: 2, array: 2, str: 1, number: 1, array_elems: 3, object_keys: 4, max_depth: 4}
		if p.nbuf.stat != wantStat {
			t.Errorf("capacity %d: stats=%+v want=%+v", capacity, p.nbuf.stat, wantStat)
		}
		if p.nodes[1].typ&TypeMask != KStringEscaped {
			t.Errorf("capacity %d: escaped key type=%d", capacity, p.nodes[1].typ&TypeMask)
		}
		keyStart := int(p.nodes[1].typ >> PosBits)
		keyLen := int(p.nodes[1].val)
		if got := string(p.JsonBytes()[keyStart : keyStart+keyLen]); got != "escapedkey" {
			t.Errorf("capacity %d: in-place key=%q", capacity, got)
		}
		p.backup = nil
		p.nodes = original
		p.free()
	}
}

func TestRVVParserDecodeAtOffset(t *testing.T) {
	for _, suffix := range []string{
		`{"x":"plain","y":["a\nb",true,null]}`,
		`[{},[],0,-0,1.5,18446744073709551615]`,
		`"\ud834\udd1e"`,
	} {
		input := "prefix " + suffix + " rest"
		pos := len("prefix ")
		var got, want interface{}
		if err := Decode(&input, &pos, 0, &got); err != nil {
			t.Fatalf("Decode(%q): %v", suffix, err)
		}
		if err := json.Unmarshal([]byte(suffix), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || pos != len("prefix ")+len(suffix) {
			t.Errorf("Decode(%q)=%#v at %d, want %#v", suffix, got, pos, want)
		}
	}
}

func TestRVVParserCorrectedSourceAtOffset(t *testing.T) {
	input := "prefix " + `{"a":"x` + string([]byte{0xff}) + `\ny"}` + ` {"next":true}`
	pos := len("prefix ")
	var got map[string]string
	if err := Decode(&input, &pos, uint64(OptionValidateString), &got); err != nil {
		t.Fatal(err)
	}
	if got["a"] != "x\ufffd\ny" {
		t.Fatalf("corrected string=%q", got["a"])
	}
	wantInput := "prefix " + `{"a":"x` + "\ufffd" + `\ny"} {"next":true}`
	if input != wantInput || input[pos:] != ` {"next":true}` {
		t.Fatalf("corrected source=%q at %d", input, pos)
	}
	var next map[string]bool
	if err := Decode(&input, &pos, uint64(OptionValidateString), &next); err != nil || !next["next"] || pos != len(input) {
		t.Fatalf("next decode=%v at %d: %v", next, pos, err)
	}
}

func TestRVVParserRawNumbersAndUnicode(t *testing.T) {
	input := `[18446744073709551616,1e999,-0]`
	pos := 0
	var got interface{}
	if err := Decode(&input, &pos, uint64(OptionUseNumber), &got); err != nil {
		t.Fatal(err)
	}
	want := []interface{}{json.Number("18446744073709551616"), json.Number("1e999"), json.Number("-0")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw numbers=%#v", got)
	}
	input, pos = `"\ud800"`, 0
	if err := Decode(&input, &pos, 0, &got); err != nil || got != "\ufffd" {
		t.Errorf("unicode replacement: result=%#v error=%v", got, err)
	}
}

func TestRVVParserDepthAndTruncation(t *testing.T) {
	for _, depth := range []int{types.MAX_RECURSE, types.MAX_RECURSE + 1} {
		input := strings.Repeat("[", depth) + strings.Repeat("]", depth)
		p := newParser(input, 0, 0)
		code := p.parse()
		if depth == types.MAX_RECURSE && code != 0 || depth > types.MAX_RECURSE && code != SONIC_STACK_OVERFLOW {
			t.Errorf("depth %d: code=%d", depth, code)
		}
		p.free()
	}
	for _, input := range []string{"", "[", "{", `{"a":`, `[1,`, `"abc`, `{"a"`, `1e+`} {
		p := newParser(input, 0, 0)
		if code := p.parse(); code == 0 {
			t.Errorf("accepted truncated input %q", input)
		}
		p.free()
	}
}

func TestRVVParserContainerValueTransitions(t *testing.T) {
	for _, space := range []string{"", " ", "\t\r\n", strings.Repeat(" ", 130)} {
		for _, token := range []string{`null`, `true`, `false`, `-0`, `12345`, `1.5e-2`, `"a\nb"`, `[]`, `{}`} {
			for _, input := range []string{
				"[" + space + token + "," + space + token + "]",
				`{"a":` + space + token + `,"b":` + space + token + "}",
			} {
				pos := 0
				var got, want interface{}
				if err := Decode(&input, &pos, 0, &got); err != nil {
					t.Fatalf("Decode(%q): %v", input, err)
				}
				if err := json.Unmarshal([]byte(input), &want); err != nil {
					t.Fatal(err)
				}
				if pos != len(input) || !reflect.DeepEqual(got, want) {
					t.Fatalf("Decode(%q)=%#v at %d, want %#v", input, got, pos, want)
				}
			}
		}
		for _, prefix := range []string{"[", "[0,", `{"a":`, `{"a":0,"b":`} {
			input := prefix + space
			pos := 0
			var got interface{}
			if err := Decode(&input, &pos, 0, &got); err == nil {
				t.Fatalf("accepted truncated input %q", input)
			}
		}
	}
}

func TestParserPoolCapacityGovernance(t *testing.T) {
	small := `{"a":1,"b":"hello"}`
	p := newParser(small, 0, 0)
	if code := p.parse(); code != 0 {
		t.Fatalf("small parse failed: %v", code)
	}
	p.reset()
	if uintptr(cap(p.padded)) > maxPooledJsonPaddedCap || uintptr(cap(p.nodes)) > maxPooledNodesCap {
		t.Fatalf("unexpected small cap: padded=%d nodes=%d", cap(p.padded), cap(p.nodes))
	}
	p.free()

	hugeStr := strings.Repeat("a", (1<<20)+1024)
	hugeJSON := `{"big":"` + hugeStr + `"}`
	p2 := newParser(hugeJSON, 0, 0)
	if uintptr(cap(p2.padded)) <= maxPooledJsonPaddedCap {
		t.Fatalf("expected oversized padded buffer during parse, got %d", cap(p2.padded))
	}
	if code := p2.parse(); code != 0 {
		t.Fatalf("huge parse failed: %v", code)
	}
	p2.reset()
	if uintptr(cap(p2.padded)) > maxPooledJsonPaddedCap {
		t.Fatalf("oversized padded buffer leaked into pool after reset: cap=%d > %d", cap(p2.padded), maxPooledJsonPaddedCap)
	}
	if uintptr(cap(p2.nodes)) > maxPooledNodesCap {
		t.Fatalf("oversized nodes buffer leaked into pool after reset: cap=%d > %d", cap(p2.nodes), maxPooledNodesCap)
	}
	p2.free()
}

func TestBoundedContainerCloseYield(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 5000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":1,"v":[2,3]}`)
	}
	b.WriteByte(']')
	input := b.String()
	type item struct {
		ID int   `json:"id"`
		V  []int `json:"v"`
	}
	var out []item
	pos := 0
	if err := Decode(&input, &pos, 0, &out); err != nil {
		t.Fatalf("Decode >4096 containers failed: %v", err)
	}
	if len(out) != 5000 || out[4999].ID != 1 || len(out[4999].V) != 2 || out[4999].V[1] != 3 {
		t.Fatalf("unexpected decoded slice: len=%d last=%+v", len(out), out[len(out)-1])
	}
}

func TestSingleLongStringOnDemandTape(t *testing.T) {
	// A 128 KiB single JSON string needs only 1 Tape node and must NOT pre-allocate 1 MiB of nodes.
	longStr := `"` + strings.Repeat("x", 128<<10) + `"`
	p := newParser(longStr, 0, 0)
	if code := p.parse(); code != 0 {
		t.Fatalf("long string parse failed: %v", code)
	}
	if uintptr(cap(p.nodes)) > maxPooledNodesCap {
		t.Fatalf("long single string over-allocated node tape: cap=%d > %d", cap(p.nodes), maxPooledNodesCap)
	}
	p.free()
}
