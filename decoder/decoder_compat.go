//go:build sonic_compat || (!(amd64 && go1.20) && !(arm64 && go1.20) && !(riscv64 && go1.26 && riscv64.rva22u64) && !(sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
* Copyright 2023 ByteDance Inc.
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

package decoder

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"github.com/bytedance/sonic/internal/compat"
	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/option"
)

func init() {
	compat.Warn("sonic/decoder")
}

const (
	_F_use_int64        = consts.F_use_int64
	_F_disable_urc      = consts.F_disable_urc
	_F_disable_unknown  = consts.F_disable_unknown
	_F_copy_string      = consts.F_copy_string
	_F_use_number       = consts.F_use_number
	_F_validate_string  = consts.F_validate_string
	_F_allow_control    = consts.F_allow_control
	_F_no_validate_json = consts.F_no_validate_json
	_F_case_sensitive   = consts.F_case_sensitive
)

// Options shares the canonical option bitmask definitions from internal/decoder/consts.
// Under sonic_compat (encoding/json adapter), OptionUseNumber and OptionDisableUnknown
// map directly to json.Decoder; Sonic-specific parser flags remain distinct bits and fall
// back to standard encoding/json behavior rather than aliasing unrelated options.
type Options = consts.Options

const (
	OptionUseInt64         Options = consts.OptionUseInt64
	OptionUseNumber        Options = consts.OptionUseNumber
	OptionUseUnicodeErrors Options = consts.OptionUseUnicodeErrors
	OptionDisableUnknown   Options = consts.OptionDisableUnknown
	OptionCopyString       Options = consts.OptionCopyString
	OptionValidateString   Options = consts.OptionValidateString
	OptionNoValidateJSON   Options = consts.OptionNoValidateJSON
	OptionCaseSensitive    Options = consts.OptionCaseSensitive
)

func (self *Decoder) SetOptions(opts Options) {
	if (opts&OptionUseNumber != 0) && (opts&OptionUseInt64 != 0) {
		panic("can't set OptionUseInt64 and OptionUseNumber both!")
	}
	self.f = uint64(opts)
}

// Decoder is the decoder context object
type Decoder struct {
	i int
	f uint64
	s string
}

// NewDecoder creates a new decoder instance.
func NewDecoder(s string) *Decoder {
	return &Decoder{s: s}
}

// Pos returns the current decoding position.
func (self *Decoder) Pos() int {
	return self.i
}

func (self *Decoder) Reset(s string) {
	self.s = s
	self.i = 0
	// self.f = 0
}

// CheckTrailings reports an error if non-whitespace bytes remain after the
// last decoded JSON value.
func (self *Decoder) CheckTrailings() error {
	pos, hasTrailing := compat.FindTrailingNonSpace(self.s, self.i)
	if !hasTrailing {
		return nil
	}
	return SyntaxError(*compat.NewTrailingSyntaxError(self.s[pos], pos))
}

// Decode parses the JSON-encoded data from current position and stores the result
// in the value pointed to by val.
func (self *Decoder) Decode(val interface{}) error {
	if self.i < 0 {
		self.i = 0
	} else if self.i > len(self.s) {
		self.i = len(self.s)
	}
	r := strings.NewReader(self.s[self.i:])
	dec := json.NewDecoder(r)
	if (self.f & uint64(OptionUseNumber)) != 0 {
		dec.UseNumber()
	}
	if (self.f & uint64(OptionDisableUnknown)) != 0 {
		dec.DisallowUnknownFields()
	}
	err := dec.Decode(val)
	self.i += int(dec.InputOffset())
	return err
}

// UseInt64 indicates the Decoder to unmarshal an integer into an interface{} as an
// int64 instead of as a float64.
func (self *Decoder) UseInt64() {
	self.f |= 1 << _F_use_int64
	self.f &^= 1 << _F_use_number
}

// UseNumber indicates the Decoder to unmarshal a number into an interface{} as a
// json.Number instead of as a float64.
func (self *Decoder) UseNumber() {
	self.f &^= 1 << _F_use_int64
	self.f |= 1 << _F_use_number
}

// UseUnicodeErrors indicates the Decoder to return an error when encounter invalid
// UTF-8 escape sequences.
func (self *Decoder) UseUnicodeErrors() {
	self.f |= 1 << _F_disable_urc
}

// DisallowUnknownFields indicates the Decoder to return an error when the destination
// is a struct and the input contains object keys which do not match any
// non-ignored, exported fields in the destination.
func (self *Decoder) DisallowUnknownFields() {
	self.f |= 1 << _F_disable_unknown
}

// CopyString indicates the Decoder to decode string values by copying instead of referring.
func (self *Decoder) CopyString() {
	self.f |= 1 << _F_copy_string
}

// ValidateString causes the Decoder to validate string values when decoding string value
// in JSON. Validation is that, returning error when unescaped control chars(0x00-0x1f) or
// invalid UTF-8 chars in the string value of JSON.
func (self *Decoder) ValidateString() {
	self.f |= 1 << _F_validate_string
}

// Pretouch is a no-op on the encoding/json compatibility adapter backend.
//
// Opts are the compile options, for example, "option.WithCompileRecursiveDepth" is
// a compile option to set the depth of recursive compile for the nested struct type.
func Pretouch(vt reflect.Type, opts ...option.CompileOption) error {
	return nil
}

// PretouchMany is a no-op on the encoding/json compatibility adapter backend.
func PretouchMany(vts []reflect.Type, opts ...option.CompileOption) error {
	return nil
}

type StreamDecoder = json.Decoder

// NewStreamDecoder adapts to encoding/json.NewDecoder API.
//
// NewStreamDecoder returns a new decoder that reads from r.
func NewStreamDecoder(r io.Reader) *StreamDecoder {
	return json.NewDecoder(r)
}

// SyntaxError represents json syntax error
type SyntaxError json.SyntaxError

// Description
func (s SyntaxError) Description() string {
	return (*json.SyntaxError)(&s).Error()
}

// Error
func (s SyntaxError) Error() string {
	return (*json.SyntaxError)(&s).Error()
}

// MismatchTypeError represents mismatching between json and object
type MismatchTypeError json.UnmarshalTypeError

// Description returns the error description.
func (m MismatchTypeError) Description() string {
	return (*json.UnmarshalTypeError)(&m).Error()
}

// Error implements the error interface.
func (m MismatchTypeError) Error() string {
	return (*json.UnmarshalTypeError)(&m).Error()
}
