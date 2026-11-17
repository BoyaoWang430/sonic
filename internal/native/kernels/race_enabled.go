//go:build race && !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"runtime"
	"unsafe"
)

// raceRead and raceWrite perform ordinary Go memory loads/stores across bounded
// kernel buffers in -race builds so Go's compiler emits TSAN instrumentation
// without private runtime symbols or re-encoding via encoding/json.
func raceRead(ptr *byte, size int) {
	if ptr == nil || size <= 0 {
		return
	}
	b := unsafe.Slice(ptr, size)
	var acc byte
	for i := 0; i < len(b); i++ {
		acc ^= b[i]
	}
	runtime.KeepAlive(acc)
}

func raceWrite(ptr *byte, size int) {
	if ptr == nil || size <= 0 {
		return
	}
	b := unsafe.Slice(ptr, size)
	for i := 0; i < len(b); i++ {
		b[i] = b[i]
	}
}
