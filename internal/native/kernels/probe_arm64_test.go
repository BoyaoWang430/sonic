//go:build arm64 && go1.20 && !go1.28

package kernels

func quoteVectorBytes() int        { return 16 }
func spaceVectorBytes() int        { return 16 }
func stringDecodeVectorBytes() int { return 16 }
func stringScanVectorBytes() int   { return 16 }
func utf8VectorBytes() int         { return 16 }
