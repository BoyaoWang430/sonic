//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import "testing"

// Both parsers must preserve every decoded value across tape growth. Literal
// node payloads are not part of that contract: C-compatible nodes encode the
// bool value in their type and may leave the separate payload unspecified.
func TestNativeParserLargeNodeGrowthValues(t *testing.T) {
	for _, count := range []int{65535, 65536, 131073} {
		input := rvvBooleanArray(count)
		pos := 0
		var got []bool
		if err := Decode(&input, &pos, 0, &got); err != nil {
			t.Fatalf("count=%d: %v", count, err)
		}
		if pos != len(input) || len(got) != count {
			t.Fatalf("count=%d: position=%d length=%d", count, pos, len(got))
		}
		for i, value := range got {
			if value != (i != count-1) {
				t.Fatalf("count=%d: value[%d]=%v", count, i, value)
			}
		}
	}
}
