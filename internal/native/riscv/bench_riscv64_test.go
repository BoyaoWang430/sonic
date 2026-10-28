//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

func equalUTF8Positions(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
