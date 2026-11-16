//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

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
}
