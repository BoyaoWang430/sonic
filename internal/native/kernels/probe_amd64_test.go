//go:build amd64

package kernels

func quoteVectorBytes() int {
	return 32
}

func spaceVectorBytes() int {
	return 32
}

func stringDecodeVectorBytes() int {
	return 32
}

func stringScanVectorBytes() int {
	return 32
}

func utf8VectorBytes() int {
	return 32
}
