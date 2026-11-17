//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/rt"
)

// ValidUTF8 reports whether src contains only valid UTF-8 encodings. It does not
// require padding. The selected assembly kernel validates ASCII and multibyte
// sequences within the logical input bounds.
func ValidUTF8(src string) bool {
	if len(src) == 0 {
		return true
	}
	if cpu.UseScalar {
		_, _, full := utf8ScanScalar(rt.StringData(src), len(src), 0, nil, 0)
		return !full
	}
	raceRead(rt.StringData(src), len(src))
	_, _, full := utf8Scan(rt.StringData(src), len(src), 0, nil, 0)
	return !full
}

// InvalidUTF8 records the absolute offsets of invalid bytes at or after start
// in positions, whose length limits the number of offsets written. Each invalid
// byte is consumed separately; valid multibyte sequences are consumed intact.
// Start must be in [0, len(src)] and identify the next unread byte.
//
// Next is the next unread offset and count is the number of offsets written.
// Full is true only when an invalid byte cannot be recorded: resume at next with
// a fresh output slice. Even when positions is empty, a valid prefix is consumed.
// Neither input nor output requires padding.
func InvalidUTF8(src string, start int, positions []int) (next, count int, full bool) {
	tail := src[start:]
	if cpu.UseScalar {
		return utf8ScanScalar(rt.StringData(tail), len(tail), start, rt.SliceData(positions), len(positions))
	}
	next, count, full = utf8Scan(rt.StringData(tail), len(tail), start, rt.SliceData(positions), len(positions))
	scanned := next - start
	if full && scanned < len(tail) {
		scanned++
	}
	raceRead(rt.StringData(tail), scanned)
	return next, count, full
}
