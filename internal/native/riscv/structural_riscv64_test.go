//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func checkContainer(t *testing.T, src string, start, wantEnd int, wantErr types.ParsingError) {
	t.Helper()
	end, err := SkipContainer(src, start)
	if end != wantEnd || err != wantErr {
		t.Fatalf("SkipContainer(%q, %d) = (%d, %v), want (%d, %v)", src, start, end, err, wantEnd, wantErr)
	}
}

func TestSkipContainer(t *testing.T) {
	t.Logf("scan width=%d bits", stringScanVectorBytes()*8)
	for _, tt := range []struct {
		src        string
		start, end int
		err        types.ParsingError
	}{
		{`[] trailing`, 0, 2, 0},
		{`{} trailing`, 0, 2, 0},
		{`prefix [1,[2],3] suffix`, 7, 16, 0},
		{`{"a":{"b":1},"c":[2,3]}`, 0, 23, 0},
		{`[}`, 0, 2, types.ERR_EOF},
		{`{]`, 0, 2, types.ERR_EOF},
		{`[}] suffix`, 0, 3, 0},
		{`{]} suffix`, 0, 3, 0},
		{`[{] ignored }`, 0, 3, 0},
		{`{[} ignored ]`, 0, 3, 0},
		{`["[]{}"]`, 0, 8, 0},
		{`["\x\uQQQQ"]`, 0, 12, 0},
		{"[\"\x00\xff\"]", 0, 6, 0},
		{`[\"] trailing`, 0, 4, 0},
		{`[\\"]`, 0, 5, types.ERR_EOF},
		{`[\[]]`, 0, 5, 0}, // a backslash does not escape a bracket
		{`[`, 0, 1, types.ERR_EOF},
		{`["`, 0, 2, types.ERR_EOF},
		{`["\`, 0, 3, types.ERR_EOF},
		{`[[]`, 0, 3, types.ERR_EOF},
	} {
		checkContainer(t, tt.src, tt.start, tt.end, tt.err)
	}
	end, err := skipContainer(nil, 0, '[')
	if end != 0 || err != types.ERR_EOF {
		t.Fatalf("empty private scan = (%d, %v)", end, err)
	}
}

func TestSkipContainerVectorBoundaries(t *testing.T) {
	const vl = 64 // the scalar-mask width and maximum requested VL
	for pad := 0; pad <= 3*vl+1; pad++ {
		prefix := strings.Repeat("x", pad)
		for run := 0; run <= 2*vl+1; run++ {
			slashes := strings.Repeat(`\`, run)
			// Outside a string, an odd run escapes the first quote and lets
			// the immediately following bracket finish the container.
			src := `[` + prefix + slashes + `"]"]`
			end := len(src)
			if run%2 != 0 {
				end -= 2
			}
			checkContainer(t, src, 0, end, 0)
			// Inside a string, the parity has the opposite effect.
			src = `[` + prefix + `"` + slashes + `"]"]`
			end = len(src)
			if run%2 == 0 {
				end -= 2
			}
			checkContainer(t, src, 0, end, 0)
		}
		for _, brackets := range [][2]string{{"[", "]"}, {"{", "}"}} {
			open, close := brackets[0], brackets[1]
			for depth := 1; depth <= 2*vl+1; depth++ {
				src := strings.Repeat(open, depth) + prefix + `"[}\\\"{]"` + strings.Repeat(close, depth)
				checkContainer(t, src+open, 0, len(src), 0)
				checkContainer(t, src[:len(src)-1], 0, len(src)-1, types.ERR_EOF)
			}
		}
	}
	// Fixed backing storage exercises every source alignment independently
	// of the payload length and checks a nonzero opening-bracket offset.
	storage := make([]byte, 6*vl+32)
	for align := 0; align < vl; align++ {
		for n := 0; n <= 3*vl+1; n++ {
			input := `[{"x":"` + strings.Repeat("a", n) + `\\\"[]"}]`
			copy(storage[align:], input)
			src := unsafe.String(unsafe.SliceData(storage), align+len(input))
			checkContainer(t, src, align, align+len(input), 0)
		}
	}
	// The relaxed contract deliberately has no JSON nesting-depth limit.
	src := strings.Repeat("[", 10000) + strings.Repeat("]", 10000)
	checkContainer(t, src, 0, len(src), 0)
}
