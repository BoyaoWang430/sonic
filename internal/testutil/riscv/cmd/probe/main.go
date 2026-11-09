package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/bytedance/sonic/internal/testutil/riscv"
	"github.com/bytedance/sonic/unquote"
	sonicutf8 "github.com/bytedance/sonic/utf8"
)

type sample struct {
	Text  string `json:"text"`
	Count int64  `json:"count"`
	Items []int  `json:"items"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func run() error {
	corpusPath := flag.String("corpus", "internal/testutil/riscv/testdata/common.json", "case corpus")
	metadataPath := flag.String("metadata", "", "metadata JSON from the runner")
	flag.Parse()
	if *metadataPath == "" || flag.NArg() != 0 {
		return fmt.Errorf("-metadata is required; positional arguments are unsupported")
	}
	data, err := os.ReadFile(*metadataPath)
	if err != nil {
		return err
	}
	var metadata riscv.Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return err
	}
	metadata.GoVersion = runtime.Version()
	metadata.APIKind = sonic.APIKind
	if metadata.Mode == "baseline" {
		if sonic.APIKind != sonic.UseStdJSON {
			return fmt.Errorf("baseline requires the community fallback (APIKind=%d)", sonic.APIKind)
		}
	} else if metadata.Mode == "candidate" {
		if sonic.APIKind != sonic.UseSonicJSON {
			return fmt.Errorf("candidate requires a native backend; GORISCV64=%q selects fallback (APIKind=%d); use an RVA22 scalar or RVA23 RVV profile", metadata.GoEnv["GORISCV64"], sonic.APIKind)
		}
	} else {
		return fmt.Errorf("unsupported mode %q", metadata.Mode)
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "riscv64" {
		return fmt.Errorf("this probe qualifies linux/riscv64 only, got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	corpus, hash, err := riscv.LoadCorpus(*corpusPath)
	if err != nil {
		return err
	}
	report := riscv.Report{Schema: riscv.Schema, Metadata: metadata, CorpusSHA256: hash}
	for _, c := range corpus.Cases {
		result, err := observe(c)
		if err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
		report.Results = append(report.Results, result)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "observed %d cases; APIKind=%d, mode=%s, suite=%s\n", len(report.Results), metadata.APIKind, metadata.Mode, metadata.Suite)
	return nil
}

func config(name string) (sonic.API, error) {
	switch name {
	case "", "std":
		return sonic.ConfigStd, nil
	case "std-number":
		return sonic.Config{EscapeHTML: true, SortMapKeys: true, CompactMarshaler: true, CopyString: true, ValidateString: true, UseNumber: true}.Froze(), nil
	case "std-unknown":
		return sonic.Config{EscapeHTML: true, SortMapKeys: true, CompactMarshaler: true, CopyString: true, ValidateString: true, DisallowUnknownFields: true}.Froze(), nil
	default:
		return nil, fmt.Errorf("unsupported config %q", name)
	}
}

func destination(target string) (interface{}, func() interface{}, error) {
	switch target {
	case "", "interface":
		var value interface{}
		return &value, func() interface{} { return value }, nil
	case "sample":
		var value sample
		return &value, func() interface{} { return value }, nil
	case "float64":
		var value float64
		return &value, func() interface{} { return value }, nil
	case "ints":
		var value []int
		return &value, func() interface{} { return value }, nil
	default:
		return nil, nil, fmt.Errorf("unsupported target %q", target)
	}
}

func observe(c riscv.Case) (riscv.Result, error) {
	input, err := c.Bytes()
	if err != nil {
		return riscv.Result{}, err
	}
	api, err := config(c.Config)
	if err != nil {
		return riscv.Result{}, err
	}
	var value interface{}
	var operationErr error
	switch c.Operation {
	case "marshal":
		if c.Target == "string" {
			value = string(input)
		} else {
			dst, get, err := destination(c.Target)
			if err != nil {
				return riscv.Result{}, err
			}
			// Decode marshal fixtures with encoding/json.
			if err := json.Unmarshal(input, dst); err != nil {
				return riscv.Result{}, fmt.Errorf("invalid marshal fixture: %w", err)
			}
			value = get()
		}
		value, operationErr = api.Marshal(value)
	case "unmarshal":
		dst, get, err := destination(c.Target)
		if err != nil {
			return riscv.Result{}, err
		}
		operationErr = api.Unmarshal(input, dst)
		value = get()
	case "valid":
		value = api.Valid(input)
	case "ast-get":
		path := make([]interface{}, len(c.Path))
		for i, component := range c.Path {
			var key string
			if err := json.Unmarshal(component, &key); err == nil {
				path[i] = key
				continue
			}
			var index int
			if err := json.Unmarshal(component, &index); err != nil || index < 0 {
				return riscv.Result{}, fmt.Errorf("invalid AST path component %s", component)
			}
			path[i] = index
		}
		node, err := ast.NewSearcher(string(input)).GetByPath(path...)
		operationErr = err
		if err == nil {
			value, operationErr = node.InterfaceUseNumber()
		}
	case "unquote":
		text, code := unquote.String(string(input))
		value = text
		if code != 0 {
			operationErr = code
		}
	case "utf8":
		bytesValid := sonicutf8.Validate(input)
		stringValid := sonicutf8.ValidateString(string(input))
		if bytesValid != stringValid {
			return riscv.Result{}, fmt.Errorf("UTF-8 byte/string APIs disagree")
		}
		value = bytesValid
	default:
		return riscv.Result{}, fmt.Errorf("unsupported operation %q", c.Operation)
	}
	if (operationErr != nil) != c.WantError {
		return riscv.Result{}, fmt.Errorf("expected error=%v, observed %v", c.WantError, operationErr)
	}
	if c.WantBool != nil && value != *c.WantBool {
		return riscv.Result{}, fmt.Errorf("expected boolean %v, observed %v", *c.WantBool, value)
	}
	observed, err := riscv.Capture(value)
	if err != nil {
		return riscv.Result{}, err
	}
	return riscv.Result{ID: c.ID, Operation: c.Operation, Policy: c.Policy, Value: observed, Error: riscv.ObserveError(operationErr)}, nil
}
