//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

func minNative[T ~int | ~uintptr](a, b T) T {
	if a < b {
		return a
	}
	return b
}
