/**
 * Copyright 2024 ByteDance Inc.
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

package prim

import (
	"encoding"
	"encoding/json"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/resolver"
	"github.com/bytedance/sonic/internal/rt"
)

func Compact(p *[]byte, v []byte) error {
	out, err := alg.AppendCompactedJSON(*p, v, 0)
	if err != nil {
		return err
	}
	*p = out
	return nil
}

func EncodeNil(rb *[]byte) error {
	*rb = append(*rb, 'n', 'u', 'l', 'l')
	return nil
}

// JSONAppender is an opt-in interface for custom types that can append their
// JSON representation directly into the destination buffer's spare capacity.
//
// Dispatch & allocation contract:
//   - Priority: JSONAppender (AppendJSON) > json.Marshaler (MarshalJSON) >
//     encoding.TextMarshaler (MarshalText) across concrete fields, pointer fields,
//     slice elements, any / non-marshaler interfaces, and static json.Marshaler
//     interface fields (while static encoding.TextMarshaler interface fields still
//     call MarshalText to match encoding/json).
//     Types implementing both AppendJSON and MarshalJSON should produce semantically
//     equivalent JSON so callers and fallback paths observe identical payloads.
//   - AppendJSON is called with a zero-length slice `dst[origLen:origLen:cap(dst)]`
//     sharing the parent encoding session's spare capacity, and must return the
//     appended JSON value slice (`ret`) or a non-nil error.
//   - If `ret` stays within `dst`'s spare suffix and requires neither whitespace
//     compaction nor HTML/UTF-8 character expansion (<, >, &, U+2028, U+2029, or
//     invalid UTF-8), validation and post-processing complete in place with zero
//     extra allocations (including under ConfigStd).
//   - If whitespace compaction or character escaping/replacement must rewrite an
//     overlapping slice, DetachIfOverlappingSpare allocates a detached copy so
//     source bytes are never overwritten before reading.
//   - If AppendJSON returns an error or invalid JSON, the parent buffer is
//     restored to `buf[:origLen]`.
type JSONAppender interface {
	AppendJSON(dst []byte) ([]byte, error)
}

var JSONAppenderType = reflect.TypeOf((*JSONAppender)(nil)).Elem()

func EncodeJSONAppender(buf *[]byte, val JSONAppender, opt uint64) error {
	b := *buf
	origLen := len(b)
	spare := b[origLen:origLen:cap(b)]
	ret, err := val.AppendJSON(spare)
	if err != nil {
		*buf = b[:origLen]
		return err
	}
	if len(ret) == 0 {
		*buf = b[:origLen]
		return vars.Error_marshaler(ret, 0)
	}
	sanitize := opt & alg.SanitizeMask
	if opt&(1<<alg.BitCompactMarshaler) != 0 {
		out, err := alg.AppendCompactedJSON(b[:origLen], ret, sanitize)
		if err != nil {
			*buf = b[:origLen]
			return err
		}
		*buf = out
		return nil
	}
	if opt&(1<<alg.BitNoValidateJSONMarshaler) == 0 {
		if ok, s := alg.Valid(ret); !ok {
			*buf = b[:origLen]
			return vars.Error_marshaler(ret, s)
		}
	}
	*buf = alg.AppendSanitizedBytes(b[:origLen], ret, sanitize)
	return nil
}

func EncodeJsonMarshaler(buf *[]byte, val json.Marshaler, opt uint64) error {
	if app, ok := val.(JSONAppender); ok {
		return EncodeJSONAppender(buf, app, opt)
	}
	ret, err := val.MarshalJSON()
	if err != nil {
		return err
	}
	sanitize := opt & alg.SanitizeMask
	if opt&(1<<alg.BitCompactMarshaler) != 0 {
		out, err := alg.AppendCompactedJSON(*buf, ret, sanitize)
		if err != nil {
			return err
		}
		*buf = out
		return nil
	}
	if opt&(1<<alg.BitNoValidateJSONMarshaler) == 0 {
		if ok, s := alg.Valid(ret); !ok {
			return vars.Error_marshaler(ret, s)
		}
	}
	*buf = alg.AppendSanitizedBytes(*buf, ret, sanitize)
	return nil
}

func EncodeTextMarshaler(buf *[]byte, val encoding.TextMarshaler, opt uint64) error {
	ret, err := val.MarshalText()
	if err != nil {
		return err
	}
	sanitize := opt & alg.SanitizeMask
	if opt&(1<<alg.BitNoQuoteTextMarshaler) != 0 {
		*buf = alg.AppendSanitizedBytes(*buf, ret, sanitize)
		return nil
	}
	ret = alg.DetachIfOverlappingSpare(*buf, ret)
	*buf = alg.QuoteFlags(*buf, rt.Mem2Str(ret), false, sanitize)
	return nil
}

func IsZero(val unsafe.Pointer, fv *resolver.FieldMeta) bool {
	rv := reflect.NewAt(fv.Type, val).Elem()
	b1 := fv.IsZero == nil && rv.IsZero()
	b2 := fv.IsZero != nil && fv.IsZero(rv)
	return b1 || b2
}
