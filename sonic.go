//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

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
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"unsafe"

	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/internal/decoder/api"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/option"
)

const apiKind = UseSonicJSON

type frozenConfig struct {
	Config
	encoderOpts encoder.Options
	decoderOpts decoder.Options
}

// Froze convert the Config to API
func (cfg Config) Froze() API {
	api := &frozenConfig{Config: cfg}

	// configure encoder options:
	if cfg.EscapeHTML {
		api.encoderOpts |= encoder.EscapeHTML
	}
	if cfg.SortMapKeys {
		api.encoderOpts |= encoder.SortMapKeys
	}
	if cfg.CompactMarshaler {
		api.encoderOpts |= encoder.CompactMarshaler
	}
	if cfg.NoQuoteTextMarshaler {
		api.encoderOpts |= encoder.NoQuoteTextMarshaler
	}
	if cfg.NoNullSliceOrMap {
		api.encoderOpts |= encoder.NoNullSliceOrMap
	}
	if cfg.ValidateString {
		api.encoderOpts |= encoder.ValidateString
	}
	if cfg.NoValidateJSONMarshaler {
		api.encoderOpts |= encoder.NoValidateJSONMarshaler
	}
	if cfg.NoEncoderNewline {
		api.encoderOpts |= encoder.NoEncoderNewline
	}
	if cfg.EncodeNullForInfOrNan {
		api.encoderOpts |= encoder.EncodeNullForInfOrNan
	}

	// configure decoder options:
	if cfg.NoValidateJSONSkip {
		api.decoderOpts |= decoder.OptionNoValidateJSON
	}
	if cfg.UseInt64 {
		api.decoderOpts |= decoder.OptionUseInt64
	}
	if cfg.UseNumber {
		api.decoderOpts |= decoder.OptionUseNumber
	}
	if cfg.UseUnicodeErrors {
		api.decoderOpts |= decoder.OptionUseUnicodeErrors
	}
	if cfg.DisallowUnknownFields {
		api.decoderOpts |= decoder.OptionDisableUnknown
	}
	if cfg.CopyString {
		api.decoderOpts |= decoder.OptionCopyString
	}
	if cfg.ValidateString {
		api.decoderOpts |= decoder.OptionValidateString
	}
	if cfg.CaseSensitive {
		api.decoderOpts |= decoder.OptionCaseSensitive
	}
	return api
}

// Marshal is implemented by sonic
func (cfg frozenConfig) Marshal(val interface{}) ([]byte, error) {
	return encoder.Encode(val, cfg.encoderOpts)
}

// MarshalToString is implemented by sonic
func (cfg frozenConfig) MarshalToString(val interface{}) (string, error) {
	buf, err := encoder.Encode(val, cfg.encoderOpts)
	return rt.Mem2Str(buf), err
}

// MarshalIndent is implemented by sonic
func (cfg frozenConfig) MarshalIndent(val interface{}, prefix, indent string) ([]byte, error) {
	return encoder.EncodeIndented(val, prefix, indent, cfg.encoderOpts)
}

// UnmarshalFromString is implemented by sonic
func (cfg frozenConfig) UnmarshalFromString(buf string, val interface{}) error {
	return api.UnmarshalString(buf, uint64(cfg.decoderOpts), val)
}

func isUntypedDOMTarget(val interface{}) bool {
	switch val.(type) {
	case *interface{}, *map[string]interface{}, *[]interface{}:
		return true
	default:
		return false
	}
}

// Unmarshal is implemented by sonic
func (cfg frozenConfig) Unmarshal(buf []byte, val interface{}) error {
	if !cfg.CopyString && isUntypedDOMTarget(val) {
		return cfg.UnmarshalFromString(string(buf), val)
	}
	err := api.UnmarshalString(rt.Mem2Str(buf), uint64(cfg.decoderOpts)|uint64(decoder.OptionCopyString), val)
	if err != nil {
		return copyUnmarshalErrorSource(err, buf)
	}
	return nil
}

func cloneIfAliasesBuf(s string, b0, bEnd uintptr, buf []byte) (string, bool) {
	if len(s) == 0 {
		return s, false
	}
	sp := uintptr(rt.IndexChar(s, 0))
	if sp < b0 || sp >= bEnd {
		return s, false
	}
	if sp == b0 && len(s) == len(buf) {
		return string(buf), true
	}
	return string([]byte(s)), true
}

func copyUnmarshalErrorSource(err error, buf []byte) error {
	if len(buf) == 0 {
		return err
	}
	b0 := uintptr(unsafe.Pointer(&buf[0]))
	bEnd := b0 + uintptr(len(buf))
	switch e := err.(type) {
	case decoder.SyntaxError:
		if cloned, ok := cloneIfAliasesBuf(e.Src, b0, bEnd, buf); ok {
			e.Src = cloned
			return e
		}
	case *decoder.SyntaxError:
		if e != nil {
			if cloned, ok := cloneIfAliasesBuf(e.Src, b0, bEnd, buf); ok {
				cp := *e
				cp.Src = cloned
				return &cp
			}
		}
	case decoder.MismatchTypeError:
		if cloned, ok := cloneIfAliasesBuf(e.Src, b0, bEnd, buf); ok {
			e.Src = cloned
			return e
		}
	case *decoder.MismatchTypeError:
		if e != nil {
			if cloned, ok := cloneIfAliasesBuf(e.Src, b0, bEnd, buf); ok {
				cp := *e
				cp.Src = cloned
				return &cp
			}
		}
	case *json.UnmarshalTypeError:
		if e != nil {
			valCloned, okV := cloneIfAliasesBuf(e.Value, b0, bEnd, buf)
			fldCloned, okF := cloneIfAliasesBuf(e.Field, b0, bEnd, buf)
			if okV || okF {
				cp := *e
				if okV {
					cp.Value = valCloned
				}
				if okF {
					cp.Field = fldCloned
				}
				return &cp
			}
		}
	case *strconv.NumError:
		if e != nil {
			if cloned, ok := cloneIfAliasesBuf(e.Num, b0, bEnd, buf); ok {
				cp := *e
				cp.Num = cloned
				return &cp
			}
		}
	}
	return err
}

// NewEncoder is implemented by sonic
func (cfg frozenConfig) NewEncoder(writer io.Writer) Encoder {
	enc := encoder.NewStreamEncoder(writer)
	enc.Opts = cfg.encoderOpts
	return enc
}

// NewDecoder is implemented by sonic
func (cfg frozenConfig) NewDecoder(reader io.Reader) Decoder {
	dec := decoder.NewStreamDecoder(reader)
	dec.SetOptions(cfg.decoderOpts)
	return dec
}

// Valid is implemented by sonic
func (cfg frozenConfig) Valid(data []byte) bool {
	ok, _ := encoder.Valid(data)
	return ok
}

// Pretouch compiles vt ahead-of-time to avoid JIT compilation on-the-fly, in
// order to reduce the first-hit latency.
//
// Opts are the compile options, for example, "option.WithCompileRecursiveDepth" is
// a compile option to set the depth of recursive compile for the nested struct type.
func Pretouch(vt reflect.Type, opts ...option.CompileOption) error {
	return PretouchMany([]reflect.Type{vt}, opts...)
}

// PretouchMany compiles all JIT types in single moduledata, to reduce the overhead of `findmoduledata`
//
// Opts are the compile options, for example, "option.WithCompileRecursiveDepth" is
// a compile option to set the depth of recursive compile for the nested struct type.
func PretouchMany(vts []reflect.Type, opts ...option.CompileOption) error {
	if len(vts) == 0 {
		return nil
	}

	allVts := make([]reflect.Type, 0, len(vts)*2)
	for _, vt := range vts {
		allVts = append(allVts, vt)
		if vt.Kind() == reflect.Ptr {
			allVts = append(allVts, vt.Elem())
		} else {
			allVts = append(allVts, reflect.PtrTo(vt))
		}
	}

	if err := encoder.PretouchMany(allVts, opts...); err != nil {
		return err
	}
	return decoder.PretouchMany(allVts, opts...)
}

func init() {
	decoder.RegisterNoCopyRawMessageType(reflect.TypeOf(NoCopyRawMessage(nil)))
}
