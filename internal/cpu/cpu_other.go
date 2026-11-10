//go:build !amd64

package cpu

func detectCPU() (hasAVX2, hasSSE, hasPOPCNT, hasCLMUL bool) {
	return false, false, false, false
}
