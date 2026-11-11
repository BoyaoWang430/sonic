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

package optdec

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"reflect"
)

var (
	boolType                = reflect.TypeOf(bool(false))
	byteType                = reflect.TypeOf(byte(0))
	intType                 = reflect.TypeOf(int(0))
	int8Type                = reflect.TypeOf(int8(0))
	int16Type               = reflect.TypeOf(int16(0))
	int32Type               = reflect.TypeOf(int32(0))
	int64Type               = reflect.TypeOf(int64(0))
	uintType                = reflect.TypeOf(uint(0))
	uint8Type               = reflect.TypeOf(uint8(0))
	uint16Type              = reflect.TypeOf(uint16(0))
	uint32Type              = reflect.TypeOf(uint32(0))
	uint64Type              = reflect.TypeOf(uint64(0))
	uintptrType             = reflect.TypeOf(uintptr(0))
	float32Type             = reflect.TypeOf(float32(0))
	float64Type             = reflect.TypeOf(float64(0))
	stringType              = reflect.TypeOf("")
	bytesType               = reflect.TypeOf([]byte(nil))
	jsonNumberType          = reflect.TypeOf(json.Number(""))
	base64CorruptInputError = reflect.TypeOf(base64.CorruptInputError(0))
	anyType                 = reflect.TypeOf((*interface{})(nil)).Elem()
	sliceEfaceType          = reflect.TypeOf([]interface{}(nil))
	mapEfaceType            = reflect.TypeOf(map[string]interface{}(nil))
	mapStringType           = reflect.TypeOf(map[string]string(nil))
	mapStrIntType           = reflect.TypeOf(map[string]int(nil))
	mapStrI64Type           = reflect.TypeOf(map[string]int64(nil))
	mapStrU64Type           = reflect.TypeOf(map[string]uint64(nil))
	mapStrF64Type           = reflect.TypeOf(map[string]float64(nil))
	mapStrBoolType          = reflect.TypeOf(map[string]bool(nil))
	mapIntStrType           = reflect.TypeOf(map[int]string(nil))
	mapI64StrType           = reflect.TypeOf(map[int64]string(nil))
	sliceI32Type            = reflect.TypeOf([]int32(nil))
	sliceI64Type            = reflect.TypeOf([]int64(nil))
	sliceU32Type            = reflect.TypeOf([]uint32(nil))
	sliceU64Type            = reflect.TypeOf([]uint64(nil))
	sliceF32Type            = reflect.TypeOf([]float32(nil))
	sliceF64Type            = reflect.TypeOf([]float64(nil))
	sliceBoolType           = reflect.TypeOf([]bool(nil))
	sliceStringType         = reflect.TypeOf([]string(nil))
	ptrBoolType             = reflect.TypeOf((*bool)(nil))
	ptrIntType              = reflect.TypeOf((*int)(nil))
	ptrInt32Type            = reflect.TypeOf((*int32)(nil))
	ptrInt64Type            = reflect.TypeOf((*int64)(nil))
	ptrUintType             = reflect.TypeOf((*uint)(nil))
	ptrUint32Type           = reflect.TypeOf((*uint32)(nil))
	ptrUint64Type           = reflect.TypeOf((*uint64)(nil))
	ptrFloat32Type          = reflect.TypeOf((*float32)(nil))
	ptrFloat64Type          = reflect.TypeOf((*float64)(nil))
	ptrStringType           = reflect.TypeOf((*string)(nil))
)

var (
	errorType                   = reflect.TypeOf((*error)(nil)).Elem()
	jsonUnmarshalerType         = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	encodingTextUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)
