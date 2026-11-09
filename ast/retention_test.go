package ast

import (
	"runtime"
	"strings"
	"testing"
)

func parseSiblingAndKeepSmallFirst(doc string) *Node {
	root, err := NewParser(doc).Parse()
	if err != 0 {
		panic(err)
	}
	// Trigger full sibling materialization as in public Interface() / Get() usage
	if _, ierr := root.Interface(); ierr != nil {
		panic(ierr)
	}
	out := new(Node)
	*out = *root.Index(0)
	return out
}

func TestASTMutableNodeDoesNotRetainLargeSibling(t *testing.T) {
	// [[true], ["<2 MiB escaped string>"]]
	bigEscaped := strings.Repeat(`a\nb`, 512<<10) // ~2 MiB unescaped per tree
	docTemplate := `[[true],["` + bigEscaped + `"]]`

	const rounds = 16 // 16 * 2 MiB = 32 MiB if sibling payload leaked
	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	kept := make([]*Node, rounds)
	for i := 0; i < rounds; i++ {
		doc := string([]byte(docTemplate))
		kept[i] = parseSiblingAndKeepSmallFirst(doc)
	}

	// Also verify post-parse mutation on a transient sibling does not retain data in kept[0]
	{
		p := Parser{s: `[[true],[false]]`, noLazy: true}
		r, err := p.Parse()
		if err != 0 {
			t.Fatal(err)
		}
		_ = r.Index(1).Add(NewString(strings.Repeat("Z", 4<<20)))
		c := new(Node)
		*c = *r.Index(0)
		kept = append(kept, c)
	}

	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	var retained uint64
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	// 17 tiny [true] nodes (~560B each) must retain << 512 KiB total (vs ~36 MiB if siblings leaked)
	if retained > 512<<10 {
		t.Fatalf("kept small AST nodes retained %d bytes of sibling payloads", retained)
	}
	for i, n := range kept {
		b, err := n.Index(0).Bool()
		if err != nil || !b {
			t.Fatalf("idx %d corrupted: %v %v", i, b, err)
		}
	}
	runtime.KeepAlive(kept)
}
