//go:build sonic_compat || (!(amd64 && go1.20) && !(arm64 && go1.20) && !(riscv64 && go1.26 && riscv64.rva22u64) && !(sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package sonic

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bytedance/sonic/decoder"
	"github.com/stretchr/testify/require"
)

func assertDecoderTrailingSyntaxError(t *testing.T, err error, fullSrc string, wantZeroBasedPos int) {
	t.Helper()
	require.Error(t, err)
	var se decoder.SyntaxError
	require.True(t, errors.As(err, &se), "expected decoder.SyntaxError, got %T: %v", err, err)
	require.NotEmpty(t, se.Error())
	require.NotEmpty(t, se.Description())

	// Reconstruct single-value reference at the same offset to compare stdlib Offset convention.
	ref := make([]byte, wantZeroBasedPos+len(fullSrc[wantZeroBasedPos:]))
	for i := 0; i < wantZeroBasedPos; i++ {
		ref[i] = ' '
	}
	if wantZeroBasedPos > 0 {
		ref[0] = '0'
	}
	copy(ref[wantZeroBasedPos:], fullSrc[wantZeroBasedPos:])
	var sink interface{}
	stdErr := json.Unmarshal(ref, &sink)
	var stdSE *json.SyntaxError
	require.ErrorAs(t, stdErr, &stdSE)
	require.Equal(t, stdSE.Offset, se.Offset)
}
