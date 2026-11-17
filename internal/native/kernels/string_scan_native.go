//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// ScanString scans the JSON string whose opening quote is at start. On success,
// end follows the closing quote, escape is the first backslash offset or -1,
// and errorPos is -1. On error, end equals errorPos. EOF is reported at len(src);
// invalid escapes are reported at their backslash, and raw controls at the byte.
//
// With validate disabled, escaped bytes are skipped without syntax checks.
// With validate enabled, JSON controls and escape syntax are checked uniformly
// at every input length. Lone UTF-16 surrogates are allowed, and UTF-8 is not
// validated. The input needs no padding and is never modified. The caller must
// provide an opening quote at start.
func ScanString(src string, start int, validate bool) (end, escape, errorPos int, err types.ParsingError) {
	base := start + 1
	tail := src[base:]
	if cpu.UseScalar {
		end, escape, errorPos, err = scanStringScalar(rt.StringData(tail), len(tail), validate)
	} else if validate {
		end, escape, errorPos, err = scanStringStrict(rt.StringData(tail), len(tail))
	} else {
		end, escape, errorPos, err = scanStringRelaxed(rt.StringData(tail), len(tail))
	}
	if !cpu.UseScalar {
		readN := end
		if err != 0 && readN < len(tail) {
			readN++
		}
		raceRead(rt.StringData(tail), readN)
	}
	end += base
	if escape >= 0 {
		escape += base
	}
	if errorPos >= 0 {
		errorPos += base
	}
	return
}
