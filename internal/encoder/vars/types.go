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

package vars

import (
	"encoding"
	"encoding/json"
	"reflect"
)

var (
	ByteType                 = reflect.TypeOf(byte(0))
	ByteSliceType            = reflect.TypeOf([]byte(nil))
	BoolSliceType            = reflect.TypeOf([]bool(nil))
	Float32SliceType         = reflect.TypeOf([]float32(nil))
	Float64SliceType         = reflect.TypeOf([]float64(nil))
	StringSliceType          = reflect.TypeOf([]string(nil))
	IntSliceType             = reflect.TypeOf([]int(nil))
	Int32SliceType           = reflect.TypeOf([]int32(nil))
	Int64SliceType           = reflect.TypeOf([]int64(nil))
	UintSliceType            = reflect.TypeOf([]uint(nil))
	Uint32SliceType          = reflect.TypeOf([]uint32(nil))
	Uint64SliceType          = reflect.TypeOf([]uint64(nil))
	AnySliceType             = reflect.TypeOf([]any(nil))
	EmptyInterfaceType       = reflect.TypeOf((*any)(nil)).Elem()
	StringAnyMapType         = reflect.TypeOf(map[string]any(nil))
	StringStringMapType      = reflect.TypeOf(map[string]string(nil))
	StringIntMapType         = reflect.TypeOf(map[string]int(nil))
	StringInt64MapType       = reflect.TypeOf(map[string]int64(nil))
	StringUint64MapType      = reflect.TypeOf(map[string]uint64(nil))
	StringFloat64MapType     = reflect.TypeOf(map[string]float64(nil))
	StringBoolMapType        = reflect.TypeOf(map[string]bool(nil))
	IntStringMapType         = reflect.TypeOf(map[int]string(nil))
	Int64StringMapType       = reflect.TypeOf(map[int64]string(nil))
	JsonNumberType           = reflect.TypeOf(json.Number(""))
	JsonUnsupportedValueType = reflect.TypeOf(new(json.UnsupportedValueError))
)

const (
	MapBuiltinStrAny = iota
	MapBuiltinStrStr
	MapBuiltinStrInt
	MapBuiltinStrI64
	MapBuiltinStrU64
	MapBuiltinStrF64
	MapBuiltinStrBool
	MapBuiltinIntStr
	MapBuiltinI64Str
)

func BuiltinMapKind(vt reflect.Type) (int, bool) {
	switch vt {
	case StringAnyMapType:
		return MapBuiltinStrAny, true
	case StringStringMapType:
		return MapBuiltinStrStr, true
	case StringIntMapType:
		return MapBuiltinStrInt, true
	case StringInt64MapType:
		return MapBuiltinStrI64, true
	case StringUint64MapType:
		return MapBuiltinStrU64, true
	case StringFloat64MapType:
		return MapBuiltinStrF64, true
	case StringBoolMapType:
		return MapBuiltinStrBool, true
	case IntStringMapType:
		return MapBuiltinIntStr, true
	case Int64StringMapType:
		return MapBuiltinI64Str, true
	default:
		return 0, false
	}
}

var (
	ErrorType                 = reflect.TypeOf((*error)(nil)).Elem()
	JsonMarshalerType         = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	EncodingTextMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	jsonAppenderMethodType    = reflect.TypeOf((*interface {
		AppendJSON([]byte) ([]byte, error)
	})(nil)).Elem()
	isZeroerMethodType = reflect.TypeOf((*interface {
		IsZero() bool
	})(nil)).Elem()
)

func IsSimpleByte(vt reflect.Type) bool {
	if vt.Kind() != ByteType.Kind() {
		return false
	} else {
		return !isEitherMarshaler(vt) && !isEitherMarshaler(reflect.PtrTo(vt))
	}
}

func isEitherMarshaler(vt reflect.Type) bool {
	return vt.Implements(JsonMarshalerType) || vt.Implements(EncodingTextMarshalerType) || vt.Implements(jsonAppenderMethodType)
}

func TypeMayEscapeValueSlot(vt reflect.Type) bool {
	return typeMayEscapeValueSlotSeen(vt, nil)
}

func typeMayEscapeValueSlotSeen(vt reflect.Type, seen map[reflect.Type]bool) bool {
	if vt == nil {
		return false
	}
	if vt.Kind() == reflect.Interface {
		return true
	}
	if isEitherMarshaler(vt) || vt.Implements(isZeroerMethodType) {
		return true
	}
	if vt.Kind() != reflect.Ptr {
		pt := reflect.PtrTo(vt)
		if isEitherMarshaler(pt) || pt.Implements(isZeroerMethodType) {
			return true
		}
	}
	switch vt.Kind() {
	case reflect.Struct:
		if seen == nil {
			seen = make(map[reflect.Type]bool)
		}
		if seen[vt] {
			return false
		}
		seen[vt] = true
		for i := 0; i < vt.NumField(); i++ {
			sf := vt.Field(i)
			if !sf.IsExported() && !sf.Anonymous {
				continue
			}
			if sf.Tag.Get("json") == "-" {
				continue
			}
			if typeMayEscapeValueSlotSeen(sf.Type, seen) {
				return true
			}
		}
	case reflect.Array:
		return typeMayEscapeValueSlotSeen(vt.Elem(), seen)
	}
	return false
}
