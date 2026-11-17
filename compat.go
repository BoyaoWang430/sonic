//go:build sonic_compat || (!(amd64 && go1.20) && !(arm64 && go1.20) && !(riscv64 && go1.26 && riscv64.rva22u64) && !(sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2021 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package sonic

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"github.com/bytedance/sonic/internal/compat"
	"github.com/bytedance/sonic/option"
)

// apiKind marks the explicit encoding/json adapter backend (-tags=sonic_compat
// or uncertified 32-bit / big-endian / rva20u64 fallback targets). See
// COMPATIBILITY.md Section 4 for capability boundaries vs UseSonicJSON.
const apiKind = UseStdJSON

type frozenConfig struct {
	Config
}

// Froze convert the Config to API
func (cfg Config) Froze() API {
	api := &frozenConfig{Config: cfg}
	return api
}

func (cfg frozenConfig) marshalOptions(val interface{}, prefix, indent string) ([]byte, error) {
	w := bytes.NewBuffer([]byte{})
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(cfg.EscapeHTML)
	enc.SetIndent(prefix, indent)
	err := enc.Encode(val)
	if err != nil {
		return nil, err
	}
	out := w.Bytes()

	// json.Encoder always appends '\n' after encoding,
	// which is not same with json.Marshal()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}

// Marshal is implemented by sonic
func (cfg frozenConfig) Marshal(val interface{}) ([]byte, error) {
	if !cfg.EscapeHTML {
		return cfg.marshalOptions(val, "", "")
	}
	return json.Marshal(val)
}

// MarshalToString is implemented by sonic
func (cfg frozenConfig) MarshalToString(val interface{}) (string, error) {
	out, err := cfg.Marshal(val)
	return string(out), err
}

// MarshalIndent is implemented by sonic
func (cfg frozenConfig) MarshalIndent(val interface{}, prefix, indent string) ([]byte, error) {
	if !cfg.EscapeHTML {
		return cfg.marshalOptions(val, prefix, indent)
	}
	return json.MarshalIndent(val, prefix, indent)
}

// UnmarshalFromString is implemented by sonic
func (cfg frozenConfig) UnmarshalFromString(buf string, val interface{}) error {
	r := strings.NewReader(buf)
	dec := json.NewDecoder(r)
	if cfg.UseNumber {
		dec.UseNumber()
	}
	if cfg.DisallowUnknownFields {
		dec.DisallowUnknownFields()
	}
	err := dec.Decode(val)
	if err != nil {
		return err
	}

	// check the trailing chars without copying remaining buffer
	if pos, hasTrailing := compat.FindTrailingNonSpace(buf, int(dec.InputOffset())); hasTrailing {
		return compat.NewTrailingSyntaxError(buf[pos], pos)
	}
	return nil
}

// Unmarshal is implemented by sonic
func (cfg frozenConfig) Unmarshal(buf []byte, val interface{}) error {
	r := bytes.NewReader(buf)
	dec := json.NewDecoder(r)
	if cfg.UseNumber {
		dec.UseNumber()
	}
	if cfg.DisallowUnknownFields {
		dec.DisallowUnknownFields()
	}
	err := dec.Decode(val)
	if err != nil {
		return err
	}

	if pos, hasTrailing := compat.FindTrailingNonSpaceBytes(buf, int(dec.InputOffset())); hasTrailing {
		return compat.NewTrailingSyntaxError(buf[pos], pos)
	}
	return nil
}

// NewEncoder is implemented by sonic
func (cfg frozenConfig) NewEncoder(writer io.Writer) Encoder {
	enc := json.NewEncoder(writer)
	if !cfg.EscapeHTML {
		enc.SetEscapeHTML(cfg.EscapeHTML)
	}
	return enc
}

// NewDecoder is implemented by sonic
func (cfg frozenConfig) NewDecoder(reader io.Reader) Decoder {
	dec := json.NewDecoder(reader)
	if cfg.UseNumber {
		dec.UseNumber()
	}
	if cfg.DisallowUnknownFields {
		dec.DisallowUnknownFields()
	}
	return dec
}

// Valid is implemented by sonic
func (cfg frozenConfig) Valid(data []byte) bool {
	return json.Valid(data)
}

// Pretouch is a no-op on the encoding/json compatibility adapter backend.
func Pretouch(vt reflect.Type, opts ...option.CompileOption) error {
	return nil
}

// PretouchMany is a no-op on the encoding/json compatibility adapter backend.
func PretouchMany(vts []reflect.Type, opts ...option.CompileOption) error {
	return nil
}
