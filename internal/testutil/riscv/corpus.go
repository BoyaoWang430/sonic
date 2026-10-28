package riscv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

type Corpus struct {
	Schema string `json:"schema"`
	Cases  []Case `json:"cases"`
}

type Case struct {
	ID        string            `json:"id"`
	Operation string            `json:"operation"`
	Input     string            `json:"input,omitempty"`
	InputHex  string            `json:"input_hex,omitempty"`
	Target    string            `json:"target,omitempty"`
	Config    string            `json:"config,omitempty"`
	Path      []json.RawMessage `json:"path,omitempty"`
	Policy    string            `json:"policy"`
	WantError bool              `json:"want_error,omitempty"`
	WantBool  *bool             `json:"want_bool,omitempty"`
}

func (c Case) Bytes() ([]byte, error) {
	if c.InputHex != "" {
		if c.Input != "" {
			return nil, fmt.Errorf("case %q sets both input and input_hex", c.ID)
		}
		return hex.DecodeString(c.InputHex)
	}
	return []byte(c.Input), nil
}

func LoadCorpus(path string) (*Corpus, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var corpus Corpus
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&corpus); err != nil {
		return nil, "", err
	}
	if dec.InputOffset() < int64(len(bytes.TrimRight(data, " \t\r\n"))) {
		return nil, "", fmt.Errorf("trailing data in corpus")
	}
	if corpus.Schema != "sonic-riscv-cases-v1" || len(corpus.Cases) == 0 {
		return nil, "", fmt.Errorf("unsupported corpus schema or zero cases")
	}
	ids := make(map[string]bool)
	for _, c := range corpus.Cases {
		if c.ID == "" || ids[c.ID] {
			return nil, "", fmt.Errorf("empty or duplicate corpus ID %q", c.ID)
		}
		if c.Policy != "exact" && c.Policy != "error-presence" && c.Policy != "json-equivalent" {
			return nil, "", fmt.Errorf("case %q needs an explicit comparison policy", c.ID)
		}
		if c.Policy == "json-equivalent" && (c.Operation != "marshal" || c.WantError) {
			return nil, "", fmt.Errorf("case %q: json-equivalent requires successful marshal output", c.ID)
		}
		ids[c.ID] = true
	}
	sum := sha256.Sum256(data)
	return &corpus, hex.EncodeToString(sum[:]), nil
}
