//go:build !sonic_compat && !sonic_scalar && amd64 && go1.20 && !go1.28

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
