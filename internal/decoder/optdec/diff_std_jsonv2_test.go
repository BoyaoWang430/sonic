//go:build goexperiment.jsonv2 && !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"go/version"
	"runtime"
	"strings"
)

const diffStdUsesJSONV2 = true

var diffStdJSONV2Pre1273 = isDiffPreGo1273(runtime.Version())

func isDiffPreGo1273(v string) bool {
	if i := strings.Index(v, "go1."); i >= 0 {
		v = v[i:]
		if sp := strings.IndexByte(v, ' '); sp >= 0 {
			v = v[:sp]
		}
		if dash := strings.IndexByte(v, '-'); dash >= 0 {
			v = v[:dash]
		}
		return version.Compare(v, "go1.27.3") < 0
	}
	return false
}
