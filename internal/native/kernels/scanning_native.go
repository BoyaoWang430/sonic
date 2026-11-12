//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import "github.com/bytedance/sonic/internal/native/types"

// Every architecture supplies the same assembly entries: ARM64 NEON, RISC-V
// RVA22 scalar, or RISC-V RVA23 RVV. Mode selection stays in shared Go.

//go:noescape
func scanStringRelaxed(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)

//go:noescape
func scanStringStrict(src *byte, size int) (end, escape, errorPos int, err types.ParsingError)

// scanStringRelaxedBlock scans a complete string in one assembly call and
// exports raw quote/backslash events after its closing quote in the last loaded
// block. All offsets are relative to src; errors return an empty tail.
//
//go:noescape
func scanStringRelaxedBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)

//go:noescape
func scanStringStrictBlock(src *byte, size int) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int)

// scanString starts after the opening quote and scans a complete string in one
// assembly invocation. Strict mode checks controls and escape syntax inside
// that same scan. Both modes read only src[:size] and return relative offsets.
// Production callers select an entry directly because this dispatcher and
// scanStringBlock exceed the inlining budget; tests use them for both modes.
func scanString(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError) {
	if validate == 0 {
		return scanStringRelaxed(src, size)
	}
	return scanStringStrict(src, size)
}

func scanStringBlock(src *byte, size int, validate uint64) (end, escape, errorPos int, err types.ParsingError, tailEvents uint64, tailWidth int) {
	if validate == 0 {
		return scanStringRelaxedBlock(src, size)
	}
	return scanStringStrictBlock(src, size)
}

// skipContainer scans one complete relaxed container boundary. src starts
// after its known opening bracket; only src[:size] is readable. Escape parity,
// quote state and same-kind depth remain inside a single assembly invocation.
//
//go:noescape
func skipContainer(src *byte, size int, open uint64) (end int, err types.ParsingError)

// numberScan performs one complete operation: JSON number prefix (mode 0),
// strict native numeric candidate (mode 1), or relaxed boundary scan (mode 2).
// It reads only src[:size] and returns offsets relative to src, without
// converting the value or modifying the input. Grammar state and all digit
// runs remain within one assembly invocation.
//
//go:noescape
func numberScan(src *byte, size int, mode uint64) (end int, err types.ParsingError)
