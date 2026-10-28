//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64 && !riscv64.rva23u64

package riscv

// Use each scalar kernel's block width in the shared boundary tests.
func quoteVectorBytes() (n int)        { return 8 }
func spaceVectorBytes() (n int)        { return 8 }
func stringDecodeVectorBytes() (n int) { return 8 }
func stringScanVectorBytes() (n int)   { return 8 }
func utf8VectorBytes() (n int)         { return 32 }
