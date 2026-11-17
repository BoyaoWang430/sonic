//go:build !amd64 || sonic_scalar || go1.28

package cpu

func detectCPU() (hasAVX2, hasSSE, hasSSSE3, hasPOPCNT, hasCLMUL bool) {
	return false, false, false, false, false
}
