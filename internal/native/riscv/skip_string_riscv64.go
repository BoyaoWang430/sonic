//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// SkipString scans content immediately after a JSON string's opening quote.
// It follows ScanString's syntax contract and returns a relative end offset,
// including the closing quote. On error end identifies the offending byte,
// or len(content) for EOF. All reads stay within content.
func SkipString(content string, validate bool) (end int, err types.ParsingError) {
	var strict uint64
	if validate {
		strict = 1
	}
	end, _, _, err = scanString(unsafe.StringData(content), len(content), strict)
	return
}
