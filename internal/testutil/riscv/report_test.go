package riscv

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func reportValue(t *testing.T, input interface{}) *Report {
	t.Helper()
	v, err := Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	r := &Report{Schema: Schema, CorpusSHA256: "fixture-hash", Results: []Result{{ID: "case", Operation: "unmarshal", Policy: "exact", Value: v}}}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	r, err = ReadReport(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestComparisonDetectsInformationLostByJSONValueEncoding(t *testing.T) {
	cases := []struct {
		name string
		a, b interface{}
	}{
		{"integer-type", int64(1), float64(1)},
		{"number-type", json.Number("1"), "1"},
		{"negative-zero", math.Copysign(0, -1), float64(0)},
		{"invalid-utf8", string([]byte{0xff}), string([]byte{0xfe})},
		{"nil-slice", []byte(nil), []byte{}},
		{"nested-type", map[string]interface{}{"n": int64(1)}, map[string]interface{}{"n": float64(1)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Compare(reportValue(t, c.a), reportValue(t, c.b)); err == nil {
				t.Fatal("comparison falsely accepted distinct observations")
			}
		})
	}
}

func TestComparisonCanonicalizesMapTraversal(t *testing.T) {
	a := reportValue(t, map[string]interface{}{"b": []byte{0xff}, "a": json.Number("1")})
	for i := 0; i < 20; i++ {
		b := reportValue(t, map[string]interface{}{"a": json.Number("1"), "b": []byte{0xff}})
		if err := Compare(a, b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComparisonRejectsMissingAndChangedCases(t *testing.T) {
	for _, mutate := range []func(*Report){
		func(r *Report) { r.Results = nil },
		func(r *Report) { r.Results = append(r.Results, r.Results[0]) },
		func(r *Report) { r.Results[0].ID = "other" },
		func(r *Report) { r.CorpusSHA256 = "other" },
		func(r *Report) { r.Results[0].Policy = "error-presence" },
		func(r *Report) { r.Results[0].Error = &ErrorRecord{Type: "error", Text: []byte("injected")} },
	} {
		a, b := reportValue(t, true), reportValue(t, true)
		mutate(b)
		if err := Compare(a, b); err == nil {
			t.Fatal("comparison accepted injected missing/changed evidence")
		}
	}
}

func TestErrorPresencePolicyIsExplicit(t *testing.T) {
	a, b := reportValue(t, nil), reportValue(t, nil)
	a.Results[0].Error = &ErrorRecord{Type: "A", Text: []byte("one")}
	b.Results[0].Error = &ErrorRecord{Type: "B", Text: []byte("two")}
	if err := Compare(a, b); err == nil {
		t.Fatal("exact policy accepted different errors")
	}
	a.Results[0].Policy, b.Results[0].Policy = "error-presence", "error-presence"
	if err := Compare(a, b); err != nil {
		t.Fatal(err)
	}
	b.Results[0].Error = nil
	if err := Compare(a, b); err == nil {
		t.Fatal("error-presence policy accepted success")
	}
}

func TestRawBytesRoundTrip(t *testing.T) {
	r := reportValue(t, string([]byte{'a', 0xff, 0, 'b'}))
	if !bytes.Equal(r.Results[0].Value.Bytes, []byte{'a', 0xff, 0, 'b'}) {
		t.Fatal("observation encoding changed raw bytes")
	}
}

func jsonOutputReport(t *testing.T, output []byte) *Report {
	t.Helper()
	r := reportValue(t, output)
	r.Results[0].Operation = "marshal"
	r.Results[0].Policy = "json-equivalent"
	return r
}

func TestJSONEquivalentPolicyIsExplicitAndPreservesRawBytes(t *testing.T) {
	a, b := reportValue(t, []byte(`"a\ufffdb"`)), reportValue(t, []byte("\"a\ufffdb\""))
	a.Results[0].Operation, b.Results[0].Operation = "marshal", "marshal"
	if err := Compare(a, b); err == nil {
		t.Fatal("exact policy accepted different JSON bytes")
	}
	a.Results[0].Policy = "json-equivalent"
	if err := Compare(a, b); err == nil {
		t.Fatal("comparison accepted mismatched policies")
	}
	b.Results[0].Policy = "json-equivalent"
	for _, r := range []*Report{a, b} {
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := ReadReport(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r.Results[0].Value.Bytes, roundTrip.Results[0].Value.Bytes) {
			t.Fatal("json-equivalent policy changed recorded bytes")
		}
	}
	if err := Compare(a, b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Results[0].Value.Bytes, []byte(`"a\ufffdb"`)) ||
		!bytes.Equal(b.Results[0].Value.Bytes, []byte("\"a\ufffdb\"")) {
		t.Fatal("comparison rewrote raw observations")
	}
}

func TestJSONEquivalentPolicyRetainsNumberTokens(t *testing.T) {
	a := jsonOutputReport(t, []byte(`{"text":"\u4e2d","n":[9007199254740993,1e0,-0]}`))
	b := jsonOutputReport(t, []byte(" \n {\"n\":[9007199254740993,1e0,-0],\"text\":\"中\"}\t"))
	if err := Compare(a, b); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"9007199254740992", "9007199254740993"},
		{"1", "1.0"},
		{"1", "1e0"},
		{"0", "-0"},
	} {
		t.Run(pair[0]+"-vs-"+pair[1], func(t *testing.T) {
			if err := Compare(jsonOutputReport(t, []byte(pair[0])), jsonOutputReport(t, []byte(pair[1]))); err == nil {
				t.Fatal("comparison lost number tokens")
			}
		})
	}
}

func TestJSONEquivalentPolicyRejectsInvalidOrChangedJSON(t *testing.T) {
	want := jsonOutputReport(t, []byte(`"a\ufffdb"`))
	for _, output := range []string{
		`"a?b"`,
		`"a\ufffdb" null`,
		`"a\ufffdb" x`,
		`{"x":`,
		``,
		"\"a\xffb\"",
	} {
		t.Run(output, func(t *testing.T) {
			got := jsonOutputReport(t, []byte(output))
			if err := Compare(want, got); err == nil {
				t.Fatal("comparison accepted invalid or changed candidate JSON")
			}
			if err := Compare(got, want); err == nil {
				t.Fatal("comparison accepted invalid or changed baseline JSON")
			}
		})
	}
	invalid := jsonOutputReport(t, []byte(`{"x":`))
	if err := Compare(invalid, invalid); err == nil {
		t.Fatal("identical invalid output must not pass")
	}
}

func TestJSONEquivalentPolicyRejectsWrongScopeOrType(t *testing.T) {
	for name, mutate := range map[string]func(*Result){
		"operation": func(r *Result) { r.Operation = "unmarshal" },
		"error":     func(r *Result) { r.Error = &ErrorRecord{Type: "error", Text: []byte("failed")} },
		"nil":       func(r *Result) { r.Value.Nil = true },
		"string":    func(r *Result) { r.Value.Type = "string" },
		"named":     func(r *Result) { r.Value.Type = "riscv.namedBytes" },
		"metadata":  func(r *Result) { r.Value.Data = "unexpected" },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := jsonOutputReport(t, []byte(`null`)), jsonOutputReport(t, []byte(`null`))
			mutate(&b.Results[0])
			if err := Compare(a, b); err == nil {
				t.Fatal("comparison accepted out-of-scope json-equivalent observation")
			}
			mutate(&a.Results[0])
			if err := Compare(a, b); err == nil {
				t.Fatal("matching invalid observations must not pass")
			}
		})
	}
}

func TestCorpusIsNonemptyAndInputsDecode(t *testing.T) {
	corpus, hash, err := LoadCorpus("testdata/common.json")
	if err != nil || hash == "" {
		t.Fatalf("load corpus: %v", err)
	}
	jsonEquivalent := 0
	for _, c := range corpus.Cases {
		if _, err := c.Bytes(); err != nil {
			t.Fatal(err)
		}
		if c.Policy == "json-equivalent" {
			jsonEquivalent++
			if c.ID != "marshal-invalid-utf8" {
				t.Fatalf("unexpected JSON-equivalence opt-in: %s", c.ID)
			}
		}
	}
	if jsonEquivalent != 1 {
		t.Fatalf("expected only the invalid-UTF-8 marshal opt-in, got %d", jsonEquivalent)
	}
}

func TestCorpusRejectsOutOfScopeJSONEquivalentPolicy(t *testing.T) {
	for _, c := range []Case{
		{ID: "case", Operation: "unmarshal", Policy: "json-equivalent"},
		{ID: "case", Operation: "marshal", Policy: "json-equivalent", WantError: true},
	} {
		data, err := json.Marshal(Corpus{Schema: "sonic-riscv-cases-v1", Cases: []Case{c}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "corpus.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadCorpus(path); err == nil {
			t.Fatal("corpus accepted out-of-scope json-equivalent policy")
		}
	}
}
