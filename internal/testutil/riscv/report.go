// Package riscv records public API observations for RISC-V backend comparisons.
package riscv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"
)

const Schema = "sonic-riscv-observations-v1"

type Metadata struct {
	Revision      string            `json:"revision"`
	Dirty         bool              `json:"dirty"`
	WorkingDir    string            `json:"working_dir"`
	GoExecutable  string            `json:"go_executable"`
	GoVersion     string            `json:"go_version"`
	GoEnv         map[string]string `json:"go_env"`
	Mode          string            `json:"mode"`
	Suite         string            `json:"suite"`
	BuildTags     string            `json:"build_tags"`
	BuildFlags    []string          `json:"build_flags"`
	Toggles       map[string]string `json:"toggles"`
	APIKind       int               `json:"api_kind"`
	SelectedFiles []string          `json:"selected_root_files"`
}

type Report struct {
	Schema       string   `json:"schema"`
	Metadata     Metadata `json:"metadata"`
	CorpusSHA256 string   `json:"corpus_sha256"`
	Results      []Result `json:"results"`
}

type Result struct {
	ID        string       `json:"id"`
	Operation string       `json:"operation"`
	Policy    string       `json:"policy"`
	Value     Value        `json:"value"`
	Error     *ErrorRecord `json:"error,omitempty"`
}

// Bytes use JSON's base64 encoding, including string contents and error text.
// Data is a decimal integer, a boolean, or the hexadecimal IEEE float bits.
// Type retains named types such as encoding/json.Number.
type Value struct {
	Type     string  `json:"type"`
	Nil      bool    `json:"nil,omitempty"`
	Data     string  `json:"data,omitempty"`
	Bytes    []byte  `json:"bytes,omitempty"`
	Elements []Value `json:"elements,omitempty"`
	Fields   []Field `json:"fields,omitempty"`
	Entries  []Entry `json:"entries,omitempty"`
}

type Field struct {
	Name  string `json:"name"`
	Value Value  `json:"value"`
}

type Entry struct {
	Key   Value `json:"key"`
	Value Value `json:"value"`
}

type ErrorRecord struct {
	Type string `json:"type"`
	Text []byte `json:"text"`
}

func ObserveError(err error) *ErrorRecord {
	if err == nil {
		return nil
	}
	return &ErrorRecord{Type: fmt.Sprintf("%T", err), Text: []byte(err.Error())}
}

func Capture(value interface{}) (Value, error) {
	return capture(reflect.ValueOf(value), 0)
}

func capture(v reflect.Value, depth int) (Value, error) {
	if depth > 64 {
		return Value{}, fmt.Errorf("observation exceeds maximum depth (cycles are unsupported)")
	}
	if !v.IsValid() {
		return Value{Type: "<nil>", Nil: true}, nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return Value{Type: v.Type().String(), Nil: true}, nil
		}
		return capture(v.Elem(), depth+1)
	}
	out := Value{Type: v.Type().String()}
	switch v.Kind() {
	case reflect.Bool:
		out.Data = strconv.FormatBool(v.Bool())
	case reflect.String:
		out.Bytes = []byte(v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out.Data = strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		out.Data = strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32:
		out.Data = strconv.FormatUint(uint64(math.Float32bits(float32(v.Float()))), 16)
	case reflect.Float64:
		out.Data = strconv.FormatUint(math.Float64bits(v.Float()), 16)
	case reflect.Ptr:
		out.Nil = v.IsNil()
		if !out.Nil {
			child, err := capture(v.Elem(), depth+1)
			if err != nil {
				return Value{}, err
			}
			out.Elements = []Value{child}
		}
	case reflect.Slice, reflect.Array:
		out.Nil = v.Kind() == reflect.Slice && v.IsNil()
		if v.Type().Elem().Kind() == reflect.Uint8 {
			out.Bytes = make([]byte, v.Len())
			for i := 0; i < v.Len(); i++ {
				out.Bytes[i] = byte(v.Index(i).Uint())
			}
			break
		}
		for i := 0; i < v.Len(); i++ {
			child, err := capture(v.Index(i), depth+1)
			if err != nil {
				return Value{}, err
			}
			out.Elements = append(out.Elements, child)
		}
	case reflect.Map:
		out.Nil = v.IsNil()
		for _, key := range v.MapKeys() {
			k, err := capture(key, depth+1)
			if err != nil {
				return Value{}, err
			}
			child, err := capture(v.MapIndex(key), depth+1)
			if err != nil {
				return Value{}, err
			}
			out.Entries = append(out.Entries, Entry{Key: k, Value: child})
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			a, _ := json.Marshal(out.Entries[i].Key)
			b, _ := json.Marshal(out.Entries[j].Key)
			return bytes.Compare(a, b) < 0
		})
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			child, err := capture(v.Field(i), depth+1)
			if err != nil {
				return Value{}, err
			}
			out.Fields = append(out.Fields, Field{Name: field.Name, Value: child})
		}
	default:
		return Value{}, fmt.Errorf("unsupported observation type %s", v.Type())
	}
	return out, nil
}

func ReadReport(r io.Reader) (*Report, error) {
	var report Report
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(interface{})); err != io.EOF {
		return nil, fmt.Errorf("report contains trailing data: %v", err)
	}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	return &report, nil
}

func (r *Report) Validate() error {
	if r.Schema != Schema || r.CorpusSHA256 == "" || len(r.Results) == 0 {
		return fmt.Errorf("unsupported schema, missing corpus hash, or zero results")
	}
	ids := make(map[string]bool)
	for _, result := range r.Results {
		if result.ID == "" || ids[result.ID] || result.Operation == "" {
			return fmt.Errorf("empty/duplicate case ID or missing operation: %q", result.ID)
		}
		if result.Policy != "exact" && result.Policy != "error-presence" && result.Policy != "json-equivalent" {
			return fmt.Errorf("case %q has unknown comparison policy %q", result.ID, result.Policy)
		}
		if result.Policy == "json-equivalent" {
			v := result.Value
			if result.Operation != "marshal" || result.Error != nil || v.Type != "[]uint8" || v.Nil {
				return fmt.Errorf("case %q: json-equivalent requires successful marshal output of non-nil []byte", result.ID)
			}
			if v.Data != "" || len(v.Elements) != 0 || len(v.Fields) != 0 || len(v.Entries) != 0 {
				return fmt.Errorf("case %q: malformed byte-slice observation", result.ID)
			}
		}
		ids[result.ID] = true
	}
	return nil
}

// Compare uses exact typed observations by default. Explicit error-presence
// cases ignore error details; json-equivalent marshal cases compare one decoded
// JSON value while retaining number tokens and the report's original bytes.
func Compare(baseline, candidate *Report) error {
	for _, r := range []*Report{baseline, candidate} {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if baseline.CorpusSHA256 != candidate.CorpusSHA256 {
		return fmt.Errorf("corpus hashes differ")
	}
	if len(baseline.Results) != len(candidate.Results) {
		return fmt.Errorf("case counts differ: %d vs %d", len(baseline.Results), len(candidate.Results))
	}
	byID := make(map[string]Result)
	for _, result := range candidate.Results {
		byID[result.ID] = result
	}
	for _, want := range baseline.Results {
		got, exists := byID[want.ID]
		if !exists || want.Operation != got.Operation || want.Policy != got.Policy {
			return fmt.Errorf("case %q missing or operation/policy differs", want.ID)
		}
		if (want.Error == nil) != (got.Error == nil) {
			return fmt.Errorf("case %q: error presence differs", want.ID)
		}
		if want.Error != nil && want.Policy == "error-presence" {
			continue
		}
		if want.Policy == "json-equivalent" {
			a, err := decodeJSONOutput(want.Value.Bytes)
			if err != nil {
				return fmt.Errorf("case %q: invalid baseline JSON output: %w", want.ID, err)
			}
			b, err := decodeJSONOutput(got.Value.Bytes)
			if err != nil {
				return fmt.Errorf("case %q: invalid candidate JSON output: %w", want.ID, err)
			}
			if !reflect.DeepEqual(a, b) {
				return fmt.Errorf("case %q: decoded JSON values or number tokens differ", want.ID)
			}
			continue
		}
		// JSON encoding canonicalizes nil/empty omitempty fields after disk I/O;
		// it does not lose value types or bytes in this observation schema.
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("case %q: typed value, raw bytes, or error details differ", want.ID)
		}
	}
	return nil
}

func decodeJSONOutput(data []byte) (interface{}, error) {
	// encoding/json accepts malformed UTF-8 and substitutes U+FFFD. Reject it
	// here so a broken marshal result cannot compare equal to repaired output.
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value interface{}
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(interface{})); err != io.EOF {
		return nil, fmt.Errorf("expected EOF after one JSON value, got %v", err)
	}
	return value, nil
}
