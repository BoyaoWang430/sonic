//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package sonic

import (
	"errors"
	"testing"

	"github.com/bytedance/sonic/decoder"
	"github.com/stretchr/testify/require"
)

func assertDecoderTrailingSyntaxError(t *testing.T, err error, _ string, wantZeroBasedPos int) {
	t.Helper()
	require.Error(t, err)
	var se decoder.SyntaxError
	require.True(t, errors.As(err, &se), "expected decoder.SyntaxError, got %T: %v", err, err)
	require.NotEmpty(t, se.Error())
	require.NotEmpty(t, se.Description())
	require.Equal(t, wantZeroBasedPos, se.Pos)
}
