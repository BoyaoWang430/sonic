package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bytedance/sonic/internal/testutil/riscv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}
}

func read(path string) (*riscv.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return riscv.ReadReport(f)
}

func run() error {
	baselinePath := flag.String("baseline", "", "baseline report")
	candidatePath := flag.String("candidate", "", "candidate report")
	allowSame := flag.Bool("allow-same-backend", false, "allow a recorder self-check")
	flag.Parse()
	if *baselinePath == "" || *candidatePath == "" || flag.NArg() != 0 {
		return fmt.Errorf("-baseline and -candidate are required")
	}
	a, err := read(*baselinePath)
	if err != nil {
		return err
	}
	b, err := read(*candidatePath)
	if err != nil {
		return err
	}
	if !*allowSame && (a.Metadata.Mode != "baseline" || a.Metadata.APIKind != 0 || b.Metadata.Mode != "candidate" || b.Metadata.APIKind != 1) {
		return fmt.Errorf("requires fallback baseline and Sonic candidate; use -allow-same-backend only for recorder self-checks")
	}
	if err := compareEnvironment(a.Metadata, b.Metadata); err != nil {
		return err
	}
	if err := riscv.Compare(a, b); err != nil {
		return err
	}
	fmt.Printf("PASS: %d common-contract cases match\n", len(a.Results))
	fmt.Printf("GORISCV64: baseline=%q candidate=%q\n", a.Metadata.GoEnv["GORISCV64"], b.Metadata.GoEnv["GORISCV64"])
	if a.Metadata.GoEnv["GORISCV64"] != b.Metadata.GoEnv["GORISCV64"] {
		fmt.Println("The comparison includes compiler target differences as well as backend differences.")
	}
	if *allowSame {
		fmt.Println("Recorder self-check: backend selection was not checked.")
	}
	return nil
}

func compareEnvironment(a, b riscv.Metadata) error {
	if a.GoVersion == "" || a.GoVersion != b.GoVersion {
		return fmt.Errorf("Go versions are missing or differ")
	}
	// Backends use different GORISCV64 profiles; other settings must match.
	for _, key := range []string{"GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT"} {
		if a.GoEnv[key] != b.GoEnv[key] {
			return fmt.Errorf("%s differs: %q vs %q", key, a.GoEnv[key], b.GoEnv[key])
		}
	}
	return nil
}
