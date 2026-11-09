package main

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/testutil/riscv"
)

func TestCompareEnvironment(t *testing.T) {
	metadata := func(profile string) riscv.Metadata {
		return riscv.Metadata{
			GoVersion: "go1.27",
			GoEnv: map[string]string{
				"GORISCV64": profile, "GOOS": "linux", "GOARCH": "riscv64",
				"CGO_ENABLED": "0", "GOEXPERIMENT": "",
			},
		}
	}
	baseline, candidate := metadata("rva22u64"), metadata("rva23u64,zacas")
	if err := compareEnvironment(baseline, candidate); err != nil {
		t.Fatalf("profile-selected backends must be comparable: %v", err)
	}
	for _, key := range []string{"GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT"} {
		t.Run(key, func(t *testing.T) {
			changed := metadata("rva23u64")
			changed.GoEnv[key] = "different"
			if err := compareEnvironment(baseline, changed); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("mismatched %s must be rejected: %v", key, err)
			}
		})
	}
	for _, version := range []string{"", "go1.28"} {
		changed := metadata("rva23u64")
		changed.GoVersion = version
		if err := compareEnvironment(baseline, changed); err == nil {
			t.Fatalf("mismatched Go version %q must be rejected", version)
		}
	}
}
