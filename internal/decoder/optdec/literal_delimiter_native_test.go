//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import "testing"

func TestNativeParserLiteralContainerDelimiter(t *testing.T) {
	// A complete literal followed by another token fails at the enclosing
	// container's delimiter check, not inside the literal parser.
	for _, word := range []string{"null", "true", "false"} {
		input := "[" + word + "x]"
		p := newParser(input, 0, 0)
		code, pos, used := p.parse(), p.Pos(), nativeParserUsed(p)
		p.free()
		if code != SONIC_EXPECT_ARR_COMMA_OR_END || pos != len(word)+2 || used != 2 {
			t.Fatalf("parse(%q)=(%v,%d,%d), want delimiter error at %d after two nodes", input, code, pos, used, len(word)+2)
		}
	}
}
