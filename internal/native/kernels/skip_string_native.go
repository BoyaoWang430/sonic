//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// SkipString scans content immediately after a JSON string's opening quote.
// It follows ScanString's syntax contract and returns a relative end offset,
// including the closing quote. On error end identifies the offending byte,
// or len(content) for EOF. All reads stay within content.
// Callers that know the mode should use SkipStringRelaxed or SkipStringStrict:
// each makes one assembly call and stays within the inlining budget.
func SkipString(content string, validate bool) (end int, err types.ParsingError) {
	if validate {
		return SkipStringStrict(content)
	}
	return SkipStringRelaxed(content)
}

// SkipStringRelaxed is SkipString without control or escape validation.
func SkipStringRelaxed(content string) (end int, err types.ParsingError) {
	end, _, _, err = scanStringRelaxed(rt.StringData(content), len(content))
	return
}

// SkipStringStrict is SkipString with JSON control and escape validation.
func SkipStringStrict(content string) (end int, err types.ParsingError) {
	end, _, _, err = scanStringStrict(rt.StringData(content), len(content))
	return
}
