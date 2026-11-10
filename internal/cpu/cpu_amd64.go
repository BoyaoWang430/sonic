//go:build amd64

package cpu

func cpuid(leaf, subleaf uint32) (eax, ebx, ecx, edx uint32)
func xgetbv() uint64

func detectCPU() (hasAVX2, hasSSE, hasPOPCNT, hasCLMUL bool) {
	maxLeaf, _, _, _ := cpuid(0, 0)
	if maxLeaf < 1 {
		return false, false, false, false
	}
	_, _, ecx1, edx1 := cpuid(1, 0)
	hasSSE = (edx1 & (1 << 25)) != 0
	hasCLMUL = (ecx1 & (1 << 1)) != 0
	hasPOPCNT = (ecx1 & (1 << 23)) != 0
	osxsave := (ecx1 & (1 << 27)) != 0
	hasAVX := (ecx1 & (1 << 28)) != 0
	if maxLeaf >= 7 && osxsave && hasAVX {
		if (xgetbv() & 0x6) == 0x6 {
			_, ebx7, _, _ := cpuid(7, 0)
			hasAVX2 = (ebx7 & (1 << 5)) != 0
		}
	}
	return
}
