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

package alg

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/rt"
)

type _MapPair struct {
	k string
	v unsafe.Pointer
	m [32]byte
}

type MapIterState struct {
	K unsafe.Pointer
	V unsafe.Pointer
}

type mapIterPooledSlots struct {
	keySlot reflect.Value
	valSlot [2]reflect.Value
}

type mapIterTypeInfo struct {
	sliceType reflect.Type
	zeroKey   reflect.Value
	zeroVal   reflect.Value
	canPool   bool
	slotPool  sync.Pool
}

var mapIterTypeCache sync.Map // map[reflect.Type]*mapIterTypeInfo

func init() {
	vars.RegisterCacheReset(func() {
		mapIterTypeCache.Range(func(k, _ any) bool {
			mapIterTypeCache.Delete(k)
			return true
		})
	})
}

func getMapIterTypeInfo(vt reflect.Type) *mapIterTypeInfo {
	if v, ok := mapIterTypeCache.Load(vt); ok {
		return v.(*mapIterTypeInfo)
	}
	kt := vt.Key()
	et := vt.Elem()
	var st reflect.Type
	if !(et.Kind() == reflect.Interface && et.NumMethod() == 0) {
		st = reflect.SliceOf(et)
	}
	info := &mapIterTypeInfo{
		sliceType: st,
		zeroKey:   reflect.Zero(kt),
		zeroVal:   reflect.Zero(et),
		canPool:   !vars.TypeMayEscapeValueSlot(kt) && !vars.TypeMayEscapeValueSlot(et),
	}
	actual, _ := mapIterTypeCache.LoadOrStore(vt, info)
	return actual.(*mapIterTypeInfo)
}

type MapIterator struct {
	It          MapIterState
	ki          int
	kvs         []_MapPair
	anyVals     []any
	vals        reflect.Value
	iter        reflect.MapIter
	keySlot     reflect.Value
	valSlot     [2]reflect.Value
	pooledSlots *mapIterPooledSlots
	typeInfo    *mapIterTypeInfo
	slotIdx     uint8
	sorted      bool
	platformMapOrder
}

var (
	iteratorPool = sync.Pool{}
)

func newIterator() *MapIterator {
	if v := iteratorPool.Get(); v == nil {
		return new(MapIterator)
	} else {
		return resetIterator(v.(*MapIterator))
	}
}

func resetIterator(p *MapIterator) *MapIterator {
	p.It = MapIterState{}
	p.ki = 0
	p.slotIdx = 0
	p.sorted = false
	p.iter.Reset(reflect.Value{})
	if p.pooledSlots != nil && p.typeInfo != nil {
		s := p.pooledSlots
		s.keySlot.Set(p.typeInfo.zeroKey)
		if s.valSlot[0].IsValid() {
			s.valSlot[0].Set(p.typeInfo.zeroVal)
			s.valSlot[1].Set(p.typeInfo.zeroVal)
		}
		p.typeInfo.slotPool.Put(s)
	}
	p.pooledSlots = nil
	p.typeInfo = nil
	p.keySlot = reflect.Value{}
	p.valSlot[0] = reflect.Value{}
	p.valSlot[1] = reflect.Value{}
	p.vals = reflect.Value{}
	for i := range p.anyVals {
		p.anyVals[i] = nil
	}
	p.anyVals = p.anyVals[:0]
	for i := range p.kvs {
		p.kvs[i].k = ""
		p.kvs[i].v = nil
	}
	p.kvs = p.kvs[:0]
	return p
}

func (self *MapIterator) at(i int) *_MapPair {
	return &self.kvs[i]
}

func (self *MapIterator) data() []_MapPair {
	return self.kvs
}

func ensurePairs(kvs []_MapPair, n int) []_MapPair {
	if cap(kvs) < n {
		return make([]_MapPair, n)
	}
	return kvs[:n]
}

func ensureAnyVals(vals []any, n int) []any {
	if cap(vals) < n {
		return make([]any, n)
	}
	return vals[:n]
}

func appendKey(p *_MapPair, kt reflect.Type, keySlot reflect.Value) error {
	kind := kt.Kind()
	if kind == reflect.String {
		p.k = keySlot.String()
		return nil
	}
	if kt.Implements(vars.EncodingTextMarshalerType) {
		if kind == reflect.Ptr && keySlot.IsNil() {
			p.k = ""
			return nil
		}
		if kind == reflect.Interface && keySlot.IsNil() {
			return &json.UnsupportedValueError{Value: keySlot, Str: "nil interface map key"}
		}
		out, err := keySlot.Interface().(encoding.TextMarshaler).MarshalText()
		if err != nil {
			return err
		}
		p.k = rt.Mem2Str(out)
		return nil
	}
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		p.k = rt.Mem2Str(I64toa(p.m[:0], keySlot.Int()))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		p.k = rt.Mem2Str(U64toa(p.m[:0], keySlot.Uint()))
		return nil
	case reflect.Bool:
		if keySlot.Bool() {
			p.k = "true"
		} else {
			p.k = "false"
		}
		return nil
	case reflect.Interface:
		if keySlot.IsNil() {
			return &json.UnsupportedValueError{Value: keySlot, Str: "nil interface map key"}
		}
		elem := keySlot.Elem()
		return appendKey(p, elem.Type(), elem)
	default:
		return vars.Error_type(kt)
	}
}

// IteratorStartStringAnySorted initializes a sorted MapIterator directly from a map[string]any value without taking its address.
func IteratorStartStringAnySorted(m map[string]any) (*MapIterator, error) {
	return startStringAnyMapSorted(m)
}

func startStringAnyMapSorted(m map[string]any) (*MapIterator, error) {
	it := newIterator()
	n := len(m)
	if n == 0 {
		return it, nil
	}
	it.kvs = ensurePairs(it.kvs, n)
	it.anyVals = ensureAnyVals(it.anyVals, n)
	i := 0
	for k, v := range m {
		if i >= len(it.kvs) {
			it.kvs = append(it.kvs, _MapPair{})
			it.anyVals = append(it.anyVals, nil)
			// Rebind v pointers if anyVals reallocated.
			for j := 0; j < i; j++ {
				it.kvs[j].v = unsafe.Pointer(&it.anyVals[j])
			}
		}
		it.anyVals[i] = v
		it.kvs[i].k = k
		it.kvs[i].v = unsafe.Pointer(&it.anyVals[i])
		i++
	}
	it.kvs = it.kvs[:i]
	it.anyVals = it.anyVals[:i]
	if i == 0 {
		return it, nil
	}
	it.sorted = true
	it.sortMapPairs()
	pair := it.orderedMapPair(0)
	it.ki = 1
	it.It.K = unsafe.Pointer(&pair.k)
	it.It.V = pair.v
	return it, nil
}

// IteratorStart initializes a MapIterator from a pointer `mp` to a map variable of type `vt`.
func IteratorStart(vt reflect.Type, mp unsafe.Pointer, fv uint64) (*MapIterator, error) {
	if mp == nil {
		return newIterator(), nil
	}
	if vt == vars.StringAnyMapType {
		m := *(*map[string]any)(mp)
		if m == nil {
			return newIterator(), nil
		}
		if (fv & (1 << BitSortMapKeys)) != 0 {
			return startStringAnyMapSorted(m)
		}
	}
	return IteratorStartValue(reflect.NewAt(vt, mp).Elem(), fv)
}

// IteratorStartValue initializes a MapIterator from a reflect.Value of Kind Map.
func IteratorStartValue(mapVal reflect.Value, fv uint64) (*MapIterator, error) {
	if !mapVal.IsValid() || mapVal.IsNil() {
		return newIterator(), nil
	}
	vt := mapVal.Type()
	if vt == vars.StringAnyMapType && (fv&(1<<BitSortMapKeys)) != 0 {
		return startStringAnyMapSorted(mapVal.Interface().(map[string]any))
	}

	it := newIterator()
	info := getMapIterTypeInfo(vt)
	if (fv & (1 << BitSortMapKeys)) == 0 {
		it.iter.Reset(mapVal)
		if !it.iter.Next() {
			it.iter.Reset(reflect.Value{})
			return it, nil
		}
		kt := vt.Key()
		et := vt.Elem()
		if info.canPool {
			var slots *mapIterPooledSlots
			if v := info.slotPool.Get(); v != nil {
				slots = v.(*mapIterPooledSlots)
			} else {
				slots = &mapIterPooledSlots{
					keySlot: reflect.New(kt).Elem(),
					valSlot: [2]reflect.Value{reflect.New(et).Elem(), reflect.New(et).Elem()},
				}
			}
			it.pooledSlots = slots
			it.typeInfo = info
			it.keySlot = slots.keySlot
			it.valSlot = slots.valSlot
		} else {
			it.keySlot = reflect.New(kt).Elem()
			it.valSlot[0] = reflect.New(et).Elem()
			it.valSlot[1] = reflect.New(et).Elem()
		}
		it.slotIdx = 0
		it.sorted = false
		it.keySlot.SetIterKey(&it.iter)
		it.valSlot[0].SetIterValue(&it.iter)
		it.It.K = it.keySlot.Addr().UnsafePointer()
		it.It.V = it.valSlot[0].Addr().UnsafePointer()
		return it, nil
	}

	count := mapVal.Len()
	if count == 0 {
		return it, nil
	}
	it.iter.Reset(mapVal)
	kt := vt.Key()
	et := vt.Elem()
	var keySlot reflect.Value
	if info.canPool {
		var slots *mapIterPooledSlots
		if v := info.slotPool.Get(); v != nil {
			slots = v.(*mapIterPooledSlots)
		} else {
			slots = &mapIterPooledSlots{
				keySlot: reflect.New(kt).Elem(),
				valSlot: [2]reflect.Value{reflect.New(et).Elem(), reflect.New(et).Elem()},
			}
		}
		it.pooledSlots = slots
		it.typeInfo = info
		keySlot = slots.keySlot
	} else {
		keySlot = reflect.New(kt).Elem()
	}
	it.kvs = ensurePairs(it.kvs, count)

	useAnyVals := et.Kind() == reflect.Interface && et.NumMethod() == 0
	if useAnyVals {
		it.anyVals = ensureAnyVals(it.anyVals, count)
	} else {
		it.vals = reflect.MakeSlice(info.sliceType, count, count)
	}

	i := 0
	for it.iter.Next() {
		if i >= len(it.kvs) {
			newCap := len(it.kvs) * 2
			if newCap < i+1 {
				newCap = i + 1
			}
			newKvs := make([]_MapPair, i+1, newCap)
			copy(newKvs, it.kvs)
			it.kvs = newKvs
			if useAnyVals {
				newAny := make([]any, i+1, newCap)
				copy(newAny, it.anyVals)
				it.anyVals = newAny
				for j := 0; j < i; j++ {
					it.kvs[j].v = unsafe.Pointer(&it.anyVals[j])
				}
			} else {
				newVals := reflect.MakeSlice(info.sliceType, i+1, newCap)
				reflect.Copy(newVals, it.vals)
				it.vals = newVals
				for j := 0; j < i; j++ {
					it.kvs[j].v = it.vals.Index(j).Addr().UnsafePointer()
				}
			}
		}
		keySlot.SetIterKey(&it.iter)
		if err := appendKey(&it.kvs[i], kt, keySlot); err != nil {
			IteratorStop(it)
			return nil, err
		}
		if useAnyVals {
			valSlot := reflect.New(et).Elem()
			valSlot.SetIterValue(&it.iter)
			it.anyVals[i] = valSlot.Interface()
			it.kvs[i].v = unsafe.Pointer(&it.anyVals[i])
		} else {
			ev := it.vals.Index(i)
			ev.SetIterValue(&it.iter)
			it.kvs[i].v = ev.Addr().UnsafePointer()
		}
		i++
	}
	it.iter.Reset(reflect.Value{})
	it.kvs = it.kvs[:i]
	if useAnyVals {
		it.anyVals = it.anyVals[:i]
	}
	if i == 0 {
		return it, nil
	}
	it.sorted = true
	it.sortMapPairs()
	pair := it.orderedMapPair(0)
	it.ki = 1
	it.It.K = unsafe.Pointer(&pair.k)
	it.It.V = pair.v
	return it, nil
}

func IteratorNext(p *MapIterator) {
	if !p.sorted {
		if !p.iter.Next() {
			p.iter.Reset(reflect.Value{})
			p.It.K = nil
			p.It.V = nil
			return
		}
		nextSlot := p.slotIdx ^ 1
		p.slotIdx = nextSlot
		p.keySlot.SetIterKey(&p.iter)
		p.valSlot[nextSlot].SetIterValue(&p.iter)
		p.It.K = p.keySlot.Addr().UnsafePointer()
		p.It.V = p.valSlot[nextSlot].Addr().UnsafePointer()
		return
	}
	if p.ki >= len(p.kvs) {
		p.It.K = nil
		p.It.V = nil
	} else {
		pair := p.orderedMapPair(p.ki)
		p.ki++
		p.It.K = unsafe.Pointer(&pair.k)
		p.It.V = pair.v
	}
}

func IteratorStop(p *MapIterator) {
	iteratorPool.Put(resetIterator(p))
}

func IsValidNumber(s string) bool {
	// This function implements the JSON numbers grammar.
	// See https://tools.ietf.org/html/rfc7159#section-6
	// and https://www.json.org/img/number.png

	if s == "" {
		return false
	}

	// Optional -
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}

	// Digits
	switch {
	default:
		return false

	case s[0] == '0':
		s = s[1:]

	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}

	// . followed by 1 or more digits.
	if len(s) >= 2 && s[0] == '.' && '0' <= s[1] && s[1] <= '9' {
		s = s[2:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}

	// e or E followed by an optional - or + and
	// 1 or more digits.
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			if s == "" {
				return false
			}
		}
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}

	// Make sure we are at the end.
	return s == ""
}
