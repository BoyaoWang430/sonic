//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

package riscv

func quoteVectorBytes() (n int)
func spaceVectorBytes() (n int)
func stringDecodeVectorBytes() (n int)
func stringScanVectorBytes() (n int)
func utf8VectorBytes() (n int)
