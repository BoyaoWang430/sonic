//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import "testing"

func TestNativeParserLiteralPrefix(t *testing.T) {
	for _, word := range []string{"null", "true", "false"} {
		for _, suffix := range []string{"", "x", "0", " ", "\x00"} {
			input := word + suffix
			p := newParser(input, 0, 0)
			code := p.parse()
			got := p.Pos()
			p.free()
			if code != SONIC_OK || got != len(word) {
				t.Fatalf("parse(%q)=(%d,%d), want success at %d", input, code, got, len(word))
			}
		}
	}
}
