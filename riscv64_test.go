//go:build go1.26 && !go1.28 && riscv64.rva22u64

package sonic

import (
	"math"
	"testing"
)

// These expectations come from the community native implementation, not the
// encoding/json fallback. Keep them separate from the common-contract corpus.
func TestRVVNativeContracts(t *testing.T) {
	if APIKind != UseSonicJSON {
		t.Fatal("RISC-V native target selected the compatibility backend")
	}
	// native/parse_with_padding.c represents the integer token -0 as SINT 0.
	var zero float64
	if err := ConfigStd.UnmarshalFromString("-0", &zero); err != nil || math.Float64bits(zero) != 0 {
		t.Fatalf("integer negative zero: %v, %v", zero, err)
	}
	// internal/encoder/alg/spec.go normalizes both floating zero signs.
	encoded, err := ConfigStd.Marshal(math.Copysign(0, -1))
	if err != nil || string(encoded) != "0" {
		t.Fatalf("floating negative zero: %s, %v", encoded, err)
	}
	// Valid's existing native adapter passes flags=0, which only scans string
	// boundaries. It does not enable F_VALIDATE_STRING escape validation.
	if !Valid([]byte(`"\x"`)) {
		t.Fatal("native Valid string-boundary policy changed")
	}
	// This backend implements the public Unicode-error option in its Go
	// parser, including when optdec is enabled by the architecture dispatch.
	var value string
	if err := (Config{UseUnicodeErrors: true}).Froze().UnmarshalFromString(`"\ud800"`, &value); err == nil {
		t.Fatal("UseUnicodeErrors accepted an unpaired surrogate")
	}
}
