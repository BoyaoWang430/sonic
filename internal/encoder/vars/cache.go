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
	"reflect"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
)

type Encoder func(
	rb *[]byte,
	vp unsafe.Pointer,
	sb *Stack,
	fv uint64,
) error

var legacyOmitNullMarker = new(byte)
var hasAnyLegacyOmitNull atomic.Bool

func init() {
	RegisterCacheReset(func() {
		hasAnyLegacyOmitNull.Store(false)
	})
}

func cacheForMode(pv, omitNull bool) *caching.ProgramCache {
	if omitNull {
		if pv {
			return programCachePVOmitNull
		}
		return programCacheOmitNull
	}
	if pv {
		return programCachePV
	}
	return programCache
}

func rootCacheForMode(omitNull bool) *caching.ProgramCache {
	if omitNull {
		return rootEncCacheOmitNull
	}
	return rootEncCache
}

func FindOrCompile(vt reflect.Type, pv bool, compiler func(reflect.Type, ...interface{}) (interface{}, error)) (interface{}, error) {
	return FindOrCompileMode(vt, pv, false, compiler)
}

func FindOrCompileMode(vt reflect.Type, pv, omitNull bool, compiler func(reflect.Type, ...interface{}) (interface{}, error)) (interface{}, error) {
	pc := cacheForMode(pv, omitNull)
	if val := pc.Get(vt); val != nil {
		return val, nil
	} else if ret, err := pc.Compute(vt, compiler, pv, omitNull); err == nil {
		return ret, nil
	} else {
		return nil, err
	}
}

func GetProgram(vt reflect.Type, pv bool) interface{} {
	return GetProgramMode(vt, pv, false)
}

func GetProgramMode(vt reflect.Type, pv, omitNull bool) interface{} {
	return cacheForMode(pv, omitNull).Get(vt)
}

func ComputeProgram(vt reflect.Type, compute func(reflect.Type, ...interface{}) (interface{}, error), pv bool) (interface{}, error) {
	return ComputeProgramMode(vt, compute, pv, false)
}

func ComputeProgramMode(vt reflect.Type, compute func(reflect.Type, ...interface{}) (interface{}, error), pv, omitNull bool) (interface{}, error) {
	return cacheForMode(pv, omitNull).Compute(vt, compute, pv, omitNull)
}

func PutPrograms(items []caching.ProgramBatchItem, pv bool) {
	PutProgramsMode(items, pv, false)
}

func PutProgramsMode(items []caching.ProgramBatchItem, pv, omitNull bool) {
	cacheForMode(pv, omitNull).PutMany(items)
}

func GetRootPlan(vt reflect.Type) interface{} {
	return GetRootPlanMode(vt, false)
}

func GetRootPlanMode(vt reflect.Type, omitNull bool) interface{} {
	return rootCacheForMode(omitNull).Get(vt)
}

func ComputeRootPlan(vt reflect.Type, compute func(reflect.Type, ...interface{}) (interface{}, error)) (interface{}, error) {
	return ComputeRootPlanMode(vt, false, compute)
}

func ComputeRootPlanMode(vt reflect.Type, omitNull bool, compute func(reflect.Type, ...interface{}) (interface{}, error)) (interface{}, error) {
	return rootCacheForMode(omitNull).Compute(vt, compute, omitNull)
}

// RegisterLegacyOmitNullType records explicit root types passed to deprecated
// Pretouch(..., WithCompileEncOnlyOmitNull(true)) without polluting unrelated
// shared nested types or default-mode plans.
func RegisterLegacyOmitNullType(vt reflect.Type) {
	if vt == nil {
		return
	}
	items := []caching.ProgramBatchItem{
		{Vt: vt, Fn: legacyOmitNullMarker},
	}
	if vt.Kind() == reflect.Ptr {
		items = append(items, caching.ProgramBatchItem{Vt: vt.Elem(), Fn: legacyOmitNullMarker})
	} else {
		items = append(items, caching.ProgramBatchItem{Vt: reflect.PtrTo(vt), Fn: legacyOmitNullMarker})
	}
	legacyOmitNullTypes.PutMany(items)
	hasAnyLegacyOmitNull.Store(true)
}

func HasLegacyOmitNull(vt reflect.Type) bool {
	if !hasAnyLegacyOmitNull.Load() || vt == nil {
		return false
	}
	return legacyOmitNullTypes.Get(vt) != nil
}
