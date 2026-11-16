//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"runtime"
	"strings"
	"testing"
)

// TestGrowNodeTapeEscalatesToHardBound simulates a scanner that produces one
// node per input byte (an unclosed run of '['), which is denser than any valid
// JSON. After the growth clamped to the valid-JSON bound runs out, the tape
// must jump to the absolute bound instead of re-halving the remaining gap.
func TestGrowNodeTapeEscalatesToHardBound(t *testing.T) {
	src := strings.Repeat("[", 1<<16)
	p := &Parser{Json: src}
	fill := func(n int) {
		p.nbuf.nlen = n
		p.pos = n
	}
	p.nodes = make([]node, 4096)
	fill(len(p.nodes))

	stage := growGeometric
	rounds := 0
	for p.growNodeTape(&stage) {
		rounds++
		if rounds > 8 {
			t.Fatalf("node tape kept growing: %d rounds, cap=%d", rounds, len(p.nodes))
		}
		if len(p.nodes) >= len(src) {
			break
		}
		fill(len(p.nodes))
	}
	if len(p.nodes) < len(src) {
		t.Fatalf("final capacity %d cannot hold %d single-byte nodes", len(p.nodes), len(src))
	}
	if max := len(src) + 2; len(p.nodes) > max {
		t.Fatalf("final capacity %d exceeds the one-node-per-byte bound %d", len(p.nodes), max)
	}
	if stage != growExhausted {
		t.Fatalf("expected the hard bound to be reached, stage=%d", stage)
	}
	fill(len(p.nodes))
	if p.growNodeTape(&stage) {
		t.Fatal("growth past the hard bound must be refused")
	}
}

// TestParseUnclosedArrayRunBoundedGrowth checks the end-to-end cost of parsing
// a long unclosed run of '['. Before the growth was bounded, a 1 MiB input
// regrew and copied the whole tape about 20 times (hundreds of MiB).
func TestParseUnclosedArrayRunBoundedGrowth(t *testing.T) {
	src := strings.Repeat("[", 1<<20)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	p := newParserImpl(src, 0, 0)
	err := parseImpl(p)
	runtime.ReadMemStats(&after)
	p.free()

	if err == SONIC_OK {
		t.Fatal("expected a syntax error for an unclosed array run")
	}
	if classifyScanStop(err) == scanStopNodeCapExhausted {
		t.Fatalf("node tape growth gave up: %v", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 96<<20 {
		t.Fatalf("parsing a 1 MiB unclosed array run allocated %d MiB", got>>20)
	}

	// A 16 MiB unclosed '[' run must stop at MAX_RECURSE (SONIC_STACK_OVERFLOW)
	// without allocating a 16M-entry (256 MiB) node tape.
	huge := strings.Repeat("[", 16<<20)
	runtime.GC()
	runtime.ReadMemStats(&before)
	p2 := newParserImpl(huge, 0, 0)
	p2.presizeNodesOne()
	err2 := parseImpl(p2)
	runtime.ReadMemStats(&after)
	p2.free()
	if err2 != SONIC_STACK_OVERFLOW {
		t.Fatalf("expected SONIC_STACK_OVERFLOW for 16 MiB unclosed '[', got %v", err2)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 48<<20 {
		t.Fatalf("parsing a 16 MiB unclosed array run allocated %d MiB (want <= 48 MiB)", got>>20)
	}
}

func TestPublicDecodeOneGiantPresizeBounded(t *testing.T) {
	var before, after runtime.MemStats

	// 1. Public DecodeOne (>4 MiB -> Giant route) on 16 MiB unclosed '[' must stop
	// at MAX_RECURSE without allocating an uncapped ~21 MiB+ initial tape.
	hugeUnclosed := strings.Repeat("[", 16<<20)
	var dstAny any
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, err := DecodeOne(hugeUnclosed, 0, &dstAny)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("expected syntax error for 16 MiB unclosed '[' via public DecodeOne")
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 12<<20 {
		t.Fatalf("public DecodeOne on 16 MiB unclosed '[' allocated %d MiB (want <= 12 MiB)", got>>20)
	}

	// 2. Public DecodeOne on empty root object + 16 MiB trailing spaces must not
	// allocate a proportional multi-MiB node tape.
	emptyPlusSpaces := "{}" + strings.Repeat(" ", 16<<20)
	var dstStruct struct {
		A int `json:"a"`
	}
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, err = DecodeOne(emptyPlusSpaces, 0, &dstStruct)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("unexpected error on '{}' + 16 MiB spaces: %v", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 512<<10 {
		t.Fatalf("public DecodeOne on '{}' + 16 MiB spaces allocated %d KiB (want <= 512 KiB)", got>>10)
	}

	// 3. Public DecodeOne on small non-empty object + 16 MiB trailing spaces is
	// capped by maxPresizeNodesOne (4 MiB).
	smallPlusSpaces := `{"a":42}` + strings.Repeat(" ", 16<<20)
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, err = DecodeOne(smallPlusSpaces, 0, &dstStruct)
	runtime.ReadMemStats(&after)
	if err != nil || dstStruct.A != 42 {
		t.Fatalf("unexpected result on small object + 16 MiB spaces: err=%v dst=%+v", err, dstStruct)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 8<<20 {
		t.Fatalf("public DecodeOne on small object + 16 MiB spaces allocated %d MiB (want <= 8 MiB)", got>>20)
	}
}
