//go:build !riscv64 || !go1.26 || go1.28 || !riscv64.rva22u64

package number

import "github.com/bytedance/sonic/internal/native/types"

// Scan scans one JSON number prefix starting at start, without skipping space.
// The caller must validate the following delimiter: "01" ends after "0" and
// "1+2" ends before '+'. A decimal point or exponent requires following digits.
// On error, end is the offending byte, or len(s) for ERR_EOF. An out-of-range
// start returns ERR_INVALID_CHAR and the unchanged start.
func Scan(s string, start int) (end int, err types.ParsingError) {
	return scanGeneric(s, start)
}
