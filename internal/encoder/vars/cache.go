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
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
)

type Encoder func(
	rb *[]byte,
	vp unsafe.Pointer,
	sb *Stack,
	fv uint64,
) error

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
