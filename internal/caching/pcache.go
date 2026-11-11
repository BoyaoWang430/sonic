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

package caching

import (
	"reflect"
	"sync"
	"sync/atomic"
)

type _ProgramMap struct {
	m map[reflect.Type]interface{}
}

func newProgramMap() *_ProgramMap {
	return &_ProgramMap{
		m: make(map[reflect.Type]interface{}, 64),
	}
}

func (self *_ProgramMap) get(vt reflect.Type) interface{} {
	return self.m[vt]
}

func (self *_ProgramMap) add(vt reflect.Type, fn interface{}) *_ProgramMap {
	fork := &_ProgramMap{
		m: make(map[reflect.Type]interface{}, len(self.m)+1),
	}
	for k, v := range self.m {
		fork.m[k] = v
	}
	fork.m[vt] = fn
	return fork
}

type ProgramBatchItem struct {
	Vt reflect.Type
	Fn interface{}
}

func (self *_ProgramMap) addMany(items []ProgramBatchItem) *_ProgramMap {
	missing := 0
	for i := range items {
		if items[i].Vt != nil && self.m[items[i].Vt] == nil {
			missing++
		}
	}
	if missing == 0 {
		return nil
	}
	fork := &_ProgramMap{
		m: make(map[reflect.Type]interface{}, len(self.m)+missing),
	}
	for k, v := range self.m {
		fork.m[k] = v
	}
	for i := range items {
		vt := items[i].Vt
		if vt != nil && fork.m[vt] == nil {
			fork.m[vt] = items[i].Fn
		}
	}
	return fork
}

/** RCU Program Cache with Staged Compaction and Per-Type Singleflight **/

type inflightProgram struct {
	wg   sync.WaitGroup
	val  interface{}
	err  error
	done bool
}

type ProgramCache struct {
	p           atomic.Pointer[_ProgramMap]
	hasStaging  atomic.Bool
	m           sync.Mutex
	epoch       uint64
	staging     map[reflect.Type]interface{}
	stagingHits uint32
	inflight    map[reflect.Type]*inflightProgram
}

func CreateProgramCache() *ProgramCache {
	pc := &ProgramCache{}
	pc.p.Store(newProgramMap())
	return pc
}

func compactSizeThreshold(fastLen int) int {
	t := fastLen >> 2
	if t < 8 {
		return 8
	}
	return t
}

func compactHitThreshold(fastLen int) uint32 {
	t := fastLen >> 1
	if t < 8 {
		return 8
	}
	return uint32(t)
}

func (self *ProgramCache) Reset() {
	self.m.Lock()
	self.epoch++
	self.inflight = nil
	self.staging = nil
	self.stagingHits = 0
	self.p.Store(newProgramMap())
	self.hasStaging.Store(false)
	self.m.Unlock()
}

func (self *ProgramCache) Get(vt reflect.Type) (val interface{}) {
	if val = self.p.Load().m[vt]; val == nil {
		val = self.getSlow(vt)
	}
	return
}

func (self *ProgramCache) getSlow(vt reflect.Type) interface{} {
	if !self.hasStaging.Load() {
		return self.p.Load().m[vt]
	}
	return self.getFromStaging(vt)
}

func (self *ProgramCache) getFromStaging(vt reflect.Type) interface{} {
	self.m.Lock()
	if val := self.p.Load().get(vt); val != nil {
		self.m.Unlock()
		return val
	}
	val := self.staging[vt]
	if val != nil {
		self.stagingHits++
		if self.stagingHits >= compactHitThreshold(len(self.p.Load().m)) {
			self.promoteStagingLocked()
		}
	}
	self.m.Unlock()
	return val
}

func (self *ProgramCache) getLocked(vt reflect.Type) interface{} {
	if val := self.p.Load().get(vt); val != nil {
		return val
	}
	if self.staging != nil {
		return self.staging[vt]
	}
	return nil
}

func (self *ProgramCache) promoteStagingLocked() {
	if len(self.staging) == 0 {
		self.stagingHits = 0
		self.hasStaging.Store(false)
		return
	}
	cur := self.p.Load()
	merged := &_ProgramMap{
		m: make(map[reflect.Type]interface{}, len(cur.m)+len(self.staging)),
	}
	for k, v := range cur.m {
		merged.m[k] = v
	}
	for k, v := range self.staging {
		if merged.m[k] == nil {
			merged.m[k] = v
		}
	}
	self.p.Store(merged)
	self.staging = nil
	self.stagingHits = 0
	self.hasStaging.Store(false)
}

func (self *ProgramCache) insertLocked(vt reflect.Type, val interface{}) {
	cur := self.p.Load()
	fastLen := len(cur.m)
	if fastLen < 8 && len(self.staging) == 0 {
		self.p.Store(cur.add(vt, val))
		return
	}
	if self.staging == nil {
		self.staging = make(map[reflect.Type]interface{}, 16)
	}
	self.staging[vt] = val
	self.hasStaging.Store(true)
	if len(self.staging) >= compactSizeThreshold(fastLen) {
		self.promoteStagingLocked()
	}
}

func (self *ProgramCache) Compute(vt reflect.Type, compute func(reflect.Type, ...interface{}) (interface{}, error), ex ...interface{}) (interface{}, error) {
	if val := self.Get(vt); val != nil {
		return val, nil
	}

	for {
		self.m.Lock()
		if val := self.getLocked(vt); val != nil {
			self.m.Unlock()
			return val, nil
		}
		if call, ok := self.inflight[vt]; ok {
			self.m.Unlock()
			call.wg.Wait()
			if call.done {
				return call.val, call.err
			}
			continue
		}
		if self.inflight == nil {
			self.inflight = make(map[reflect.Type]*inflightProgram)
		}
		call := &inflightProgram{}
		call.wg.Add(1)
		self.inflight[vt] = call
		startEpoch := self.epoch
		self.m.Unlock()

		finished := false
		defer func() {
			if !finished {
				self.m.Lock()
				if self.inflight[vt] == call {
					delete(self.inflight, vt)
				}
				call.wg.Done()
				self.m.Unlock()
			}
		}()

		// Execute compilation outside self.m so distinct types compile concurrently.
		val, err := compute(vt, ex...)

		self.m.Lock()
		finished = true
		if self.inflight[vt] == call {
			delete(self.inflight, vt)
		}
		if err == nil && self.epoch == startEpoch {
			if existing := self.getLocked(vt); existing != nil {
				val = existing
			} else {
				self.insertLocked(vt, val)
			}
		}
		call.val = val
		call.err = err
		call.done = true
		call.wg.Done()
		self.m.Unlock()
		return val, err
	}
}

func (self *ProgramCache) PutMany(items []ProgramBatchItem) {
	if len(items) == 0 {
		return
	}
	self.m.Lock()
	cur := self.p.Load()
	if len(self.staging) == 0 {
		if next := cur.addMany(items); next != nil {
			self.p.Store(next)
		}
		self.m.Unlock()
		return
	}
	missing := 0
	for i := range items {
		vt := items[i].Vt
		if vt != nil && cur.m[vt] == nil && self.staging[vt] == nil {
			missing++
		}
	}
	fork := &_ProgramMap{
		m: make(map[reflect.Type]interface{}, len(cur.m)+len(self.staging)+missing),
	}
	for k, v := range cur.m {
		fork.m[k] = v
	}
	for k, v := range self.staging {
		if fork.m[k] == nil {
			fork.m[k] = v
		}
	}
	for i := range items {
		vt := items[i].Vt
		if vt != nil && fork.m[vt] == nil {
			fork.m[vt] = items[i].Fn
		}
	}
	self.p.Store(fork)
	self.staging = nil
	self.stagingHits = 0
	self.hasStaging.Store(false)
	self.m.Unlock()
}
